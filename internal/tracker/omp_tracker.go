package tracker

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

// ompResetShiftThreshold is how far a limit's reset time must move before a
// utilization drop counts as a reset (rolling windows shift it every poll).
const ompResetShiftThreshold = 60 * time.Minute

// ompLimitKey identifies one limit of one broker account.
type ompLimitKey struct {
	accountID int64
	limitID   string
}

// OMPTracker detects reset cycles per (account, limit) for the OMP broker
// provider. It follows CodexTracker: a cycle closes when its reset time has
// passed, or when the reset time jumps and utilization drops.
type OMPTracker struct {
	store  *store.Store
	logger *slog.Logger

	// mu guards the maps below. The tracker outlives agents: a restarted
	// agent reuses it, so Process calls may overlap briefly.
	mu         sync.Mutex
	lastValues map[ompLimitKey]float64
	lastResets map[ompLimitKey]time.Time
	onReset    func(accountID int64, limit api.OMPLimit)
}

// OMPSummary contains computed usage statistics for one limit of an account.
type OMPSummary struct {
	AccountID       int64
	LimitID         string
	CurrentUtil     float64
	ResetsAt        *time.Time
	TimeUntilReset  time.Duration
	CurrentRate     float64 // percentage points per hour in the active cycle
	ProjectedUtil   float64
	CompletedCycles int
	AvgPerCycle     float64
	PeakCycle       float64
	TotalTracked    float64
	TrackingSince   time.Time
}

// NewOMPTracker creates an OMPTracker.
func NewOMPTracker(store *store.Store, logger *slog.Logger) *OMPTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &OMPTracker{
		store:      store,
		logger:     logger,
		lastValues: make(map[ompLimitKey]float64),
		lastResets: make(map[ompLimitKey]time.Time),
	}
}

// SetOnReset registers a callback invoked when a limit reset is detected.
func (t *OMPTracker) SetOnReset(fn func(accountID int64, limit api.OMPLimit)) {
	t.mu.Lock()
	t.onReset = fn
	t.mu.Unlock()
}

// Process updates the cycles of every limit in a stored snapshot. Limits
// without a derivable utilization are skipped.
func (t *OMPTracker) Process(snapshot *store.OMPSnapshot) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, limit := range snapshot.Limits {
		if limit.Utilization == nil {
			continue
		}
		if err := t.processLimit(snapshot.AccountID, limit, snapshot.FetchedAt); err != nil {
			return fmt.Errorf("omp tracker: account %d %s: %w", snapshot.AccountID, limit.ID, err)
		}
	}
	return nil
}

func (t *OMPTracker) processLimit(accountID int64, limit api.OMPLimit, sampledAt time.Time) error {
	key := ompLimitKey{accountID: accountID, limitID: limit.ID}
	currentUtil := *limit.Utilization
	resetsAt := futureResetsAt(limit.ResetsAt, sampledAt)

	cycle, err := t.store.QueryActiveOMPCycle(accountID, limit.ID)
	if err != nil {
		return fmt.Errorf("failed to query active cycle: %w", err)
	}
	if cycle == nil {
		if _, err := t.store.CreateOMPCycle(accountID, limit.ID, sampledAt, resetsAt); err != nil {
			return fmt.Errorf("failed to create cycle: %w", err)
		}
		if err := t.store.UpdateOMPCycle(accountID, limit.ID, currentUtil, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastValues[key] = currentUtil
		if resetsAt != nil {
			t.lastResets[key] = *resetsAt
		}
		return nil
	}

	lastUtil, hasLast := t.lastValues[key]

	resetDetected := false
	updateResetsAt := false
	if cycle.ResetsAt != nil && sampledAt.After(cycle.ResetsAt.Add(2*time.Minute)) {
		resetDetected = true
	}
	if !resetDetected {
		switch {
		case resetsAt != nil && cycle.ResetsAt != nil:
			ref := *cycle.ResetsAt
			if last, ok := t.lastResets[key]; ok {
				ref = last
			}
			diff := resetsAt.Sub(ref)
			if diff < 0 {
				diff = -diff
			}
			if diff > ompResetShiftThreshold && hasLast && currentUtil+2 < lastUtil {
				resetDetected = true
			} else {
				updateResetsAt = true
			}
		case resetsAt != nil && cycle.ResetsAt == nil:
			updateResetsAt = true
		}
	}

	if resetDetected {
		cycleEnd := sampledAt
		if cycle.ResetsAt != nil && sampledAt.After(*cycle.ResetsAt) {
			cycleEnd = *cycle.ResetsAt
		}
		if hasLast {
			if delta := currentUtil - lastUtil; delta > 0 {
				cycle.TotalDelta += delta
			}
			if currentUtil > cycle.PeakUtilization {
				cycle.PeakUtilization = currentUtil
			}
		}
		if err := t.store.CloseOMPCycle(accountID, limit.ID, cycleEnd, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to close cycle: %w", err)
		}
		if _, err := t.store.CreateOMPCycle(accountID, limit.ID, sampledAt, resetsAt); err != nil {
			return fmt.Errorf("failed to create new cycle: %w", err)
		}
		if err := t.store.UpdateOMPCycle(accountID, limit.ID, currentUtil, 0); err != nil {
			return fmt.Errorf("failed to set initial peak: %w", err)
		}
		t.lastValues[key] = currentUtil
		if resetsAt != nil {
			t.lastResets[key] = *resetsAt
		} else {
			delete(t.lastResets, key)
		}
		t.logger.Info("OMP limit reset detected", "account_id", accountID, "limit", limit.ID)
		if t.onReset != nil {
			t.onReset(accountID, limit)
		}
		return nil
	}

	if updateResetsAt {
		if err := t.store.UpdateOMPCycleResetsAt(accountID, limit.ID, resetsAt); err != nil {
			return fmt.Errorf("failed to update cycle reset timestamp: %w", err)
		}
		t.lastResets[key] = *resetsAt
	}

	changed := false
	if hasLast {
		if delta := currentUtil - lastUtil; delta > 0 {
			cycle.TotalDelta += delta
			changed = true
		}
	}
	if currentUtil > cycle.PeakUtilization {
		cycle.PeakUtilization = currentUtil
		changed = true
	}
	if changed {
		if err := t.store.UpdateOMPCycle(accountID, limit.ID, cycle.PeakUtilization, cycle.TotalDelta); err != nil {
			return fmt.Errorf("failed to update cycle: %w", err)
		}
	}
	t.lastValues[key] = currentUtil
	return nil
}

// UsageSummary returns computed stats for one limit of an account.
func (t *OMPTracker) UsageSummary(accountID int64, limitID string) (*OMPSummary, error) {
	active, err := t.store.QueryActiveOMPCycle(accountID, limitID)
	if err != nil {
		return nil, fmt.Errorf("failed to query active cycle: %w", err)
	}
	history, err := t.store.QueryOMPCycleHistory(accountID, limitID)
	if err != nil {
		return nil, fmt.Errorf("failed to query cycle history: %w", err)
	}

	summary := &OMPSummary{AccountID: accountID, LimitID: limitID, CompletedCycles: len(history)}
	if len(history) > 0 {
		var total float64
		summary.TrackingSince = history[len(history)-1].CycleStart
		for _, c := range history {
			total += c.TotalDelta
			if c.PeakUtilization > summary.PeakCycle {
				summary.PeakCycle = c.PeakUtilization
			}
		}
		summary.AvgPerCycle = total / float64(len(history))
		summary.TotalTracked = total
	}
	if active == nil {
		return summary, nil
	}
	if summary.TrackingSince.IsZero() {
		summary.TrackingSince = active.CycleStart
	}
	summary.TotalTracked += active.TotalDelta
	if active.PeakUtilization > summary.PeakCycle {
		summary.PeakCycle = active.PeakUtilization
	}
	summary.ResetsAt = active.ResetsAt

	latest, err := t.store.QueryLatestOMPSnapshot(accountID)
	if err != nil {
		return nil, fmt.Errorf("failed to query latest: %w", err)
	}
	if latest != nil {
		for _, l := range latest.Limits {
			if l.ID != limitID {
				continue
			}
			if l.Utilization != nil {
				summary.CurrentUtil = *l.Utilization
			}
			if summary.ResetsAt == nil {
				summary.ResetsAt = futureResetsAt(l.ResetsAt, time.Now())
			}
			break
		}
	}
	if summary.ResetsAt != nil {
		summary.TimeUntilReset = time.Until(*summary.ResetsAt)
	}

	elapsed := time.Since(active.CycleStart)
	if elapsed >= 30*time.Minute && active.TotalDelta > 0 {
		summary.CurrentRate = active.TotalDelta / elapsed.Hours()
		if summary.ResetsAt != nil {
			if hoursLeft := time.Until(*summary.ResetsAt).Hours(); hoursLeft > 0 {
				summary.ProjectedUtil = min(summary.CurrentUtil+summary.CurrentRate*hoursLeft, 100)
			}
		}
	}
	return summary, nil
}
