package web

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

// The OMP broker provider ("omp") is multi-account: every endpoint answers
// per broker account. See docs/OMP_SETUP.md for the JSON contract.

// ompStaleAfter marks an account whose newest report is this old as stale,
// e.g. an account the broker no longer reports.
const ompStaleAfter = 30 * time.Minute

// SetOMPTracker sets the OMP tracker for summary enrichment.
func (h *Handler) SetOMPTracker(t *tracker.OMPTracker) {
	h.ompTracker = t
}

// parseOMPAccountParam reads ?account=<id>. ok is false when the parameter is
// present but invalid; id is 0 when it is absent.
func parseOMPAccountParam(r *http.Request) (id int64, ok bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("account"))
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// ompAccounts returns the accounts selected by ?account (all when absent).
func (h *Handler) ompAccounts(r *http.Request) ([]store.OMPAccount, int, error) {
	accountID, ok := parseOMPAccountParam(r)
	if !ok {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid account")
	}
	accounts, err := h.store.QueryOMPAccounts()
	if err != nil {
		h.logger.Error("failed to query OMP accounts", "error", err)
		return nil, http.StatusInternalServerError, fmt.Errorf("failed to query accounts")
	}
	if accountID == 0 {
		return accounts, http.StatusOK, nil
	}
	for _, acc := range accounts {
		if acc.ID == accountID {
			return []store.OMPAccount{acc}, http.StatusOK, nil
		}
	}
	return nil, http.StatusNotFound, fmt.Errorf("unknown account")
}

func ompAccountMap(acc store.OMPAccount) map[string]interface{} {
	return map[string]interface{}{
		"accountId":         acc.ID,
		"key":               acc.Key,
		"sourceProvider":    acc.SourceProvider,
		"identity":          acc.Identity,
		"email":             acc.Email,
		"upstreamAccountId": acc.AccountID,
		"orgId":             acc.OrgID,
		"orgName":           acc.OrgName,
		"planType":          acc.PlanType,
	}
}

// ompLimitStatus maps a limit to the dashboard status vocabulary (healthy,
// warning, critical). The broker's own status can only raise it.
func ompLimitStatus(l api.OMPLimit) string {
	status := "healthy"
	if l.Utilization != nil {
		status = utilStatus(*l.Utilization)
	}
	switch strings.ToLower(l.Status) {
	case "exhausted":
		return "critical"
	case "warning":
		if status == "healthy" {
			return "warning"
		}
	}
	return status
}

func (h *Handler) currentOMP(w http.ResponseWriter, _ *http.Request) {
	respondJSON(w, http.StatusOK, h.buildOMPCurrent())
}

// buildOMPCurrent returns the newest snapshot of every account.
func (h *Handler) buildOMPCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"provider":   "omp",
		"capturedAt": now.Format(time.RFC3339),
		"accounts":   []interface{}{},
	}
	if h.store == nil {
		return response
	}
	accounts, err := h.store.QueryOMPAccounts()
	if err != nil {
		h.logger.Error("failed to query OMP accounts", "error", err)
		return response
	}
	latest, err := h.store.QueryLatestOMPSnapshots()
	if err != nil {
		h.logger.Error("failed to query latest OMP snapshots", "error", err)
		return response
	}
	byAccount := make(map[int64]*store.OMPSnapshot, len(latest))
	for _, snap := range latest {
		byAccount[snap.AccountID] = snap
	}

	out := make([]interface{}, 0, len(accounts))
	for _, acc := range accounts {
		snap := byAccount[acc.ID]
		if snap == nil {
			continue
		}
		entry := ompAccountMap(acc)
		age := now.Sub(snap.FetchedAt)
		entry["fetchedAt"] = snap.FetchedAt.Format(time.RFC3339)
		entry["capturedAt"] = snap.CapturedAt.Format(time.RFC3339)
		entry["ageSeconds"] = int64(age.Seconds())
		entry["isStale"] = age > ompStaleAfter
		entry["notes"] = snap.Notes

		limits := make([]interface{}, 0, len(snap.Limits))
		for _, l := range snap.Limits {
			limits = append(limits, h.ompLimitMap(acc.ID, l, now))
		}
		entry["limits"] = limits
		out = append(out, entry)
	}
	response["accounts"] = out
	return response
}

func (h *Handler) ompLimitMap(accountID int64, l api.OMPLimit, now time.Time) map[string]interface{} {
	m := map[string]interface{}{
		"id":               l.ID,
		"label":            l.Label,
		"windowId":         l.WindowID,
		"windowLabel":      l.WindowLabel,
		"windowDurationMs": nil,
		"used":             l.Used,
		"limit":            l.Limit,
		"remaining":        l.Remaining,
		"unit":             l.Unit,
		"utilization":      l.Utilization,
		"status":           ompLimitStatus(l),
		"upstreamStatus":   l.Status,
		"resetsAt":         nil,
		"notes":            l.Notes,
	}
	if l.WindowDuration > 0 {
		m["windowDurationMs"] = l.WindowDuration.Milliseconds()
	}
	if l.ResetsAt != nil {
		until := l.ResetsAt.Sub(now)
		m["resetsAt"] = l.ResetsAt.Format(time.RFC3339)
		m["timeUntilReset"] = formatDuration(until)
		m["timeUntilResetSeconds"] = int64(until.Seconds())
	}
	if h.ompTracker != nil && l.Utilization != nil {
		if summary, err := h.ompTracker.UsageSummary(accountID, l.ID); err == nil && summary != nil {
			m["currentRate"] = summary.CurrentRate
			m["projectedUtil"] = summary.ProjectedUtil
		}
	}
	return m
}

// historyOMP returns per-account utilization series:
// {"accounts":[{...account, "points":[{"fetchedAt", "values":{limitId: utilization|null}}]}]}
func (h *Handler) historyOMP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"accounts": []interface{}{}})
		return
	}
	duration, err := parseTimeRange(r.URL.Query().Get("range"))
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	accounts, status, err := h.ompAccounts(r)
	if err != nil {
		respondError(w, status, err.Error())
		return
	}
	end := time.Now().UTC()
	start := end.Add(-duration)

	out := make([]interface{}, 0, len(accounts))
	for _, acc := range accounts {
		snaps, err := h.store.QueryOMPRange(acc.ID, start, end)
		if err != nil {
			h.logger.Error("failed to query OMP history", "account_id", acc.ID, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to query history")
			return
		}
		step := downsampleStep(len(snaps), maxChartPoints)
		last := len(snaps) - 1
		points := make([]interface{}, 0, min(len(snaps), maxChartPoints))
		for i, snap := range snaps {
			if step > 1 && i != 0 && i != last && i%step != 0 {
				continue
			}
			values := make(map[string]interface{}, len(snap.Limits))
			for _, l := range snap.Limits {
				values[l.ID] = l.Utilization
			}
			points = append(points, map[string]interface{}{
				"fetchedAt": snap.FetchedAt.Format(time.RFC3339),
				"values":    values,
			})
		}
		entry := ompAccountMap(acc)
		entry["points"] = points
		out = append(out, entry)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"accounts": out})
}

// cyclesOMP returns the cycles of one limit of one account, active first:
// /api/cycles?provider=omp&account=<id>&type=<limitId>.
func (h *Handler) cyclesOMP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}
	accountID, ok := parseOMPAccountParam(r)
	if !ok || accountID == 0 {
		respondError(w, http.StatusBadRequest, "account is required")
		return
	}
	limitID := strings.TrimSpace(r.URL.Query().Get("type"))
	if limitID == "" {
		respondError(w, http.StatusBadRequest, "type (limit id) is required")
		return
	}

	response := []map[string]interface{}{}
	active, err := h.store.QueryActiveOMPCycle(accountID, limitID)
	if err != nil {
		h.logger.Error("failed to query active OMP cycle", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}
	if active != nil {
		response = append(response, ompCycleMap(active))
	}
	history, err := h.store.QueryOMPCycleHistory(accountID, limitID, 200)
	if err != nil {
		h.logger.Error("failed to query OMP cycle history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query cycles")
		return
	}
	for _, c := range history {
		response = append(response, ompCycleMap(c))
	}
	respondJSON(w, http.StatusOK, response)
}

func ompCycleMap(c *store.OMPResetCycle) map[string]interface{} {
	m := map[string]interface{}{
		"id":              c.ID,
		"accountId":       c.AccountID,
		"limitId":         c.LimitID,
		"cycleStart":      c.CycleStart.Format(time.RFC3339),
		"cycleEnd":        nil,
		"peakUtilization": c.PeakUtilization,
		"totalDelta":      c.TotalDelta,
		"isActive":        c.CycleEnd == nil,
	}
	if c.CycleEnd != nil {
		m["cycleEnd"] = c.CycleEnd.Format(time.RFC3339)
	}
	if c.ResetsAt != nil {
		m["resetsAt"] = c.ResetsAt.Format(time.RFC3339)
		if c.CycleEnd == nil {
			m["timeUntilReset"] = formatDuration(time.Until(*c.ResetsAt))
		}
	}
	return m
}

// summaryOMP returns per-account, per-limit cycle statistics:
// {"accounts":[{...account, "limits":{limitId: summary}}]}.
func (h *Handler) summaryOMP(w http.ResponseWriter, r *http.Request) {
	if h.store == nil || h.ompTracker == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"accounts": []interface{}{}})
		return
	}
	accounts, status, err := h.ompAccounts(r)
	if err != nil {
		respondError(w, status, err.Error())
		return
	}
	out := make([]interface{}, 0, len(accounts))
	for _, acc := range accounts {
		limitIDs, err := h.store.QueryOMPLimitIDs(acc.ID)
		if err != nil {
			h.logger.Error("failed to query OMP limit ids", "account_id", acc.ID, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to query summary")
			return
		}
		limits := make(map[string]interface{}, len(limitIDs))
		for _, limitID := range limitIDs {
			summary, err := h.ompTracker.UsageSummary(acc.ID, limitID)
			if err != nil || summary == nil {
				continue
			}
			entry := map[string]interface{}{
				"currentUtil":     summary.CurrentUtil,
				"currentRate":     summary.CurrentRate,
				"projectedUtil":   summary.ProjectedUtil,
				"completedCycles": summary.CompletedCycles,
				"peakCycle":       summary.PeakCycle,
				"avgPerCycle":     summary.AvgPerCycle,
				"totalTracked":    summary.TotalTracked,
				"trackingSince":   nil,
				"resetsAt":        nil,
			}
			if !summary.TrackingSince.IsZero() {
				entry["trackingSince"] = summary.TrackingSince.Format(time.RFC3339)
			}
			if summary.ResetsAt != nil {
				entry["resetsAt"] = summary.ResetsAt.Format(time.RFC3339)
				entry["timeUntilReset"] = formatDuration(summary.TimeUntilReset)
			}
			limits[limitID] = entry
		}
		entry := ompAccountMap(acc)
		entry["limits"] = limits
		out = append(out, entry)
	}
	respondJSON(w, http.StatusOK, map[string]interface{}{"accounts": out})
}
