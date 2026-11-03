package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// ompUsageFetcher is the OMPClient surface the agent needs.
type ompUsageFetcher interface {
	FetchUsage(ctx context.Context) (*api.OMPUsage, error)
	RefreshToken() bool
}

// OMPAgent polls the OMP auth broker and records every account it reports.
type OMPAgent struct {
	client       ompUsageFetcher
	store        *store.Store
	tracker      *tracker.OMPTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
	now          func() time.Time
}

// NewOMPAgent creates an OMPAgent.
func NewOMPAgent(client ompUsageFetcher, store *store.Store, tr *tracker.OMPTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *OMPAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &OMPAgent{
		client:   client,
		store:    store,
		tracker:  tr,
		interval: interval,
		logger:   logger,
		sm:       sm,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// SetPollingCheck sets the function that gates each poll.
func (a *OMPAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets the notification engine for quota alerts.
func (a *OMPAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// Run polls immediately, then on every interval until ctx is canceled.
func (a *OMPAgent) Run(ctx context.Context) error {
	a.logger.Info("OMP broker agent started", "interval", a.interval)
	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("OMP broker agent stopped")
	}()

	a.poll(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *OMPAgent) poll(ctx context.Context) {
	if a.client == nil {
		return
	}
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	// The token command runs before every poll so a rotated broker token is
	// picked up without waiting for a 401; a failure keeps the previous token.
	a.client.RefreshToken()

	usage, err := a.client.FetchUsage(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		a.logger.Error("Failed to fetch OMP broker usage", "error", err)
		pollFailed("omp", "", err)
		return
	}
	pollSucceeded("omp", "")

	capturedAt := a.now()
	stored := 0
	var sessionValues []float64
	for _, report := range usage.Reports {
		for _, l := range report.Limits {
			if l.Utilization != nil {
				sessionValues = append(sessionValues, *l.Utilization)
			}
		}

		account, err := a.store.UpsertOMPAccount(report)
		if err != nil {
			a.logger.Error("Failed to record OMP account", "provider", report.Provider, "error", err)
			continue
		}
		snapshotID, inserted, err := a.store.InsertOMPSnapshot(account.ID, capturedAt, report)
		if err != nil {
			a.logger.Error("Failed to insert OMP snapshot", "account_id", account.ID, "error", err)
			continue
		}
		if !inserted {
			// The broker served its cached report: nothing new to record.
			continue
		}
		stored++

		snapshot := &store.OMPSnapshot{
			ID:             snapshotID,
			AccountID:      account.ID,
			CapturedAt:     capturedAt,
			FetchedAt:      report.FetchedAt,
			SourceProvider: report.Provider,
			Notes:          report.Notes,
			Limits:         report.Limits,
		}
		if a.tracker != nil {
			if err := a.tracker.Process(snapshot); err != nil {
				a.logger.Error("OMP tracker processing failed", "account_id", account.ID, "error", err)
			}
		}
		a.notify(account, report)
	}

	if a.sm != nil {
		a.sm.ReportPoll(sessionValues)
	}

	a.logger.Info("OMP broker poll complete",
		"reports", len(usage.Reports),
		"new_snapshots", stored,
	)
}

// notify checks every limit of a freshly stored report against the alert
// thresholds. QuotaKey is the broker limit id; AccountID is the account key
// so alerts name the account and dedupe per account.
func (a *OMPAgent) notify(account *store.OMPAccount, report api.OMPReport) {
	if a.notifier == nil {
		return
	}
	for _, l := range report.Limits {
		if l.Utilization == nil {
			continue
		}
		status := notify.QuotaStatus{
			Provider:    "omp",
			QuotaKey:    l.ID,
			AccountID:   account.Key,
			Utilization: *l.Utilization,
			ResetAt:     derefTime(l.ResetsAt),
		}
		if l.Limit != nil {
			status.Limit = *l.Limit
		}
		a.notifier.Check(status)
	}
}
