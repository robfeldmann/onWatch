package web

import (
	"fmt"
	"strings"
	"time"
)

// ompMenubarAccount is one broker account's menubar card input.
type ompMenubarAccount struct {
	cardID  string // "omp:<provider_accounts id>"
	label   string // "Cursor · me@example.com"
	payload map[string]interface{}
	hasData bool // the newest report has at least one limit with a utilization
}

// ompMenubarAccounts builds the menubar payload of every broker account from
// its newest snapshot, in the shape normalizeProviderCard reads. USD limits
// carry format "currency" so the menubar shows "$used/$limit"; percent
// limits are percent-only so no fake "13/100" line is drawn. Limits without a
// utilization are left out, and an account without any (limits withheld) has
// no card. ok is false when the store could not be read.
func (h *Handler) ompMenubarAccounts() ([]ompMenubarAccount, bool) {
	if h.store == nil {
		return nil, false
	}
	accounts, err := h.store.QueryOMPAccounts()
	if err != nil {
		h.logger.Error("menubar: failed to query OMP accounts", "error", err)
		return nil, false
	}
	latest, err := h.store.QueryLatestOMPSnapshots()
	if err != nil {
		h.logger.Error("menubar: failed to query OMP snapshots", "error", err)
		return nil, false
	}
	bySnapshot := make(map[int64]int, len(latest))
	for i, snap := range latest {
		bySnapshot[snap.AccountID] = i
	}

	now := time.Now().UTC()
	out := make([]ompMenubarAccount, 0, len(accounts))
	for _, acc := range accounts {
		entry := ompMenubarAccount{cardID: fmt.Sprintf("omp:%d", acc.ID), label: acc.Label()}
		idx, ok := bySnapshot[acc.ID]
		if !ok {
			out = append(out, entry)
			continue
		}
		snap := latest[idx]
		quotas := make([]interface{}, 0, len(snap.Limits))
		for _, l := range snap.Limits {
			if l.Utilization == nil {
				continue
			}
			q := map[string]interface{}{
				"name":        l.ID,
				"label":       l.Label,
				"utilization": *l.Utilization,
			}
			switch l.Unit {
			case "usd":
				q["format"] = "currency"
			case "percent":
				q["percentOnly"] = true
			}
			if l.Used != nil {
				q["used"] = *l.Used
			}
			if l.Limit != nil {
				q["limit"] = *l.Limit
			}
			if l.ResetsAt != nil {
				q["resetsAt"] = l.ResetsAt.Format(time.RFC3339)
				q["timeUntilReset"] = formatDuration(l.ResetsAt.Sub(now))
			}
			// An exhausted window is critical whatever its percent.
			if strings.EqualFold(l.Status, "exhausted") {
				q["statusLabel"] = "exhausted"
			}
			quotas = append(quotas, q)
		}
		entry.payload = map[string]interface{}{
			"capturedAt": snap.FetchedAt.Format(time.RFC3339),
			"quotas":     quotas,
		}
		entry.hasData = len(quotas) > 0
		out = append(out, entry)
	}
	return out, true
}
