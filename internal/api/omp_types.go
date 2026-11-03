package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// This file holds the wire types and parser for the OMP auth broker's
// GET /v1/usage response. It has no HTTP or storage dependencies so a generic
// "usage report" provider could reuse it later. The shape mirrors the broker's
// UsageResponse schema (oh-my-pi packages/ai/src/usage.ts):
//
//	{generatedAt, reports: [{provider, fetchedAt, limits: [...], resetCredits?, notes?, metadata?}]}
//
// Times on the wire are milliseconds since the Unix epoch.

// OMPUsage is a parsed /v1/usage response.
type OMPUsage struct {
	GeneratedAt time.Time
	Reports     []OMPReport
}

// OMPReport is one account's usage report. The broker returns one report per
// account it holds, so a provider can appear several times.
type OMPReport struct {
	// Provider is the upstream provider id, e.g. "anthropic", "cursor",
	// "openai-codex", "opencode-go".
	Provider string
	// Identity identifies the account within Provider: metadata.email, else
	// metadata.accountId, else "default". When one response holds several
	// accounts with the same identity, "#<orgId or accountId>" is appended.
	Identity string
	// FetchedAt is when the broker fetched this report upstream. The broker
	// caches reports, so an unchanged FetchedAt means an unchanged report.
	FetchedAt time.Time

	Email     string
	AccountID string
	OrgID     string
	OrgName   string
	PlanType  string

	// Notes are report-wide caveats, e.g. why limits are withheld.
	Notes  []string
	Limits []OMPLimit
}

// AccountKey is the stable per-account key "<provider>:<identity>".
func (r OMPReport) AccountKey() string {
	return r.Provider + ":" + r.Identity
}

// OMPLimit is one limit window or quota bucket of a report.
type OMPLimit struct {
	ID    string // stable id, e.g. "anthropic:5h"
	Label string // display label, e.g. "Claude 5 Hour"

	WindowID       string        // e.g. "5h", "7d", "monthly"; empty when the limit has no window
	WindowLabel    string        // e.g. "5 Hour"
	WindowDuration time.Duration // zero when unknown
	ResetsAt       *time.Time

	// Amounts are in Unit. Each is nil when the broker did not report it.
	Used      *float64
	Limit     *float64
	Remaining *float64
	Unit      string // "percent", "usd", "tokens", "requests", "credits", "minutes", "bytes", "unknown"

	// Utilization is the used share of the limit in percent (100 = exhausted,
	// above 100 = overage), or nil when the report carries nothing to derive it.
	Utilization *float64
	Status      string // "ok", "warning", "exhausted", "unknown", or "" when absent
	Notes       []string
}

type ompWireUsage struct {
	GeneratedAt *float64         `json:"generatedAt"`
	Reports     *[]ompWireReport `json:"reports"`
}

type ompWireReport struct {
	Provider  string                     `json:"provider"`
	FetchedAt *float64                   `json:"fetchedAt"`
	Limits    *[]ompWireLimit            `json:"limits"`
	Notes     []string                   `json:"notes"`
	Metadata  map[string]json.RawMessage `json:"metadata"`
}

type ompWireLimit struct {
	ID     string         `json:"id"`
	Label  string         `json:"label"`
	Window *ompWireWindow `json:"window"`
	Amount *ompWireAmount `json:"amount"`
	Status string         `json:"status"`
	Notes  []string       `json:"notes"`
}

type ompWireWindow struct {
	ID         string   `json:"id"`
	Label      string   `json:"label"`
	DurationMs *float64 `json:"durationMs"`
	ResetsAt   *float64 `json:"resetsAt"`
}

type ompWireAmount struct {
	Used              *float64 `json:"used"`
	Limit             *float64 `json:"limit"`
	Remaining         *float64 `json:"remaining"`
	UsedFraction      *float64 `json:"usedFraction"`
	RemainingFraction *float64 `json:"remainingFraction"`
	Unit              string   `json:"unit"`
}

// ParseOMPUsage decodes and validates a /v1/usage body. Any structural
// problem rejects the whole response: storing a partial or zero-filled report
// would be indistinguishable from real usage.
func ParseOMPUsage(body []byte) (*OMPUsage, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: body is not a JSON object", ErrOMPInvalidResponse)
	}
	var wire ompWireUsage
	if err := json.Unmarshal(trimmed, &wire); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrOMPInvalidResponse, err)
	}
	if wire.GeneratedAt == nil {
		return nil, fmt.Errorf("%w: missing generatedAt", ErrOMPInvalidResponse)
	}
	generatedAt, ok := ompMillis(*wire.GeneratedAt)
	if !ok {
		return nil, fmt.Errorf("%w: invalid generatedAt", ErrOMPInvalidResponse)
	}
	if wire.Reports == nil {
		return nil, fmt.Errorf("%w: missing reports", ErrOMPInvalidResponse)
	}

	usage := &OMPUsage{GeneratedAt: generatedAt, Reports: make([]OMPReport, 0, len(*wire.Reports))}
	for i, wr := range *wire.Reports {
		report, err := parseOMPReport(wr)
		if err != nil {
			return nil, fmt.Errorf("%w: report %d: %v", ErrOMPInvalidResponse, i, err)
		}
		usage.Reports = append(usage.Reports, report)
	}
	if err := disambiguateOMPIdentities(usage.Reports); err != nil {
		return nil, err
	}
	return usage, nil
}

// disambiguateOMPIdentities keeps account keys unique. One email can own
// several accounts of a provider (e.g. a personal and a team Anthropic org);
// those reports get the org or account id appended to their identity. Reports
// that still collide cannot be told apart and fail the response.
func disambiguateOMPIdentities(reports []OMPReport) error {
	counts := make(map[string]int, len(reports))
	for _, r := range reports {
		counts[r.AccountKey()]++
	}
	for i := range reports {
		r := &reports[i]
		if counts[r.AccountKey()] < 2 {
			continue
		}
		suffix := r.OrgID
		if suffix == "" {
			suffix = r.AccountID
		}
		if suffix == "" || suffix == r.Identity {
			continue
		}
		r.Identity += "#" + suffix
	}
	seen := make(map[string]int, len(reports))
	for i, r := range reports {
		key := r.AccountKey()
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("%w: reports %d and %d are the same %s account", ErrOMPInvalidResponse, prev, i, r.Provider)
		}
		seen[key] = i
	}
	return nil
}

func parseOMPReport(wr ompWireReport) (OMPReport, error) {
	provider := strings.TrimSpace(wr.Provider)
	if provider == "" {
		return OMPReport{}, fmt.Errorf("missing provider")
	}
	if wr.FetchedAt == nil {
		return OMPReport{}, fmt.Errorf("missing fetchedAt")
	}
	fetchedAt, ok := ompMillis(*wr.FetchedAt)
	if !ok {
		return OMPReport{}, fmt.Errorf("invalid fetchedAt")
	}
	if wr.Limits == nil {
		return OMPReport{}, fmt.Errorf("missing limits")
	}

	report := OMPReport{
		Provider:  provider,
		FetchedAt: fetchedAt,
		Email:     ompMetadataString(wr.Metadata, "email"),
		AccountID: ompMetadataString(wr.Metadata, "accountId"),
		OrgID:     ompMetadataString(wr.Metadata, "orgId"),
		OrgName:   ompMetadataString(wr.Metadata, "orgName"),
		PlanType:  ompMetadataString(wr.Metadata, "planType"),
		Notes:     ompNotes(wr.Notes),
		Limits:    make([]OMPLimit, 0, len(*wr.Limits)),
	}
	switch {
	case report.Email != "":
		report.Identity = report.Email
	case report.AccountID != "":
		report.Identity = report.AccountID
	default:
		report.Identity = "default"
	}

	seen := make(map[string]struct{}, len(*wr.Limits))
	for i, wl := range *wr.Limits {
		limit, err := parseOMPLimit(wl)
		if err != nil {
			return OMPReport{}, fmt.Errorf("limit %d: %v", i, err)
		}
		if _, dup := seen[limit.ID]; dup {
			return OMPReport{}, fmt.Errorf("duplicate limit id %q", limit.ID)
		}
		seen[limit.ID] = struct{}{}
		report.Limits = append(report.Limits, limit)
	}
	return report, nil
}

func parseOMPLimit(wl ompWireLimit) (OMPLimit, error) {
	id := strings.TrimSpace(wl.ID)
	if id == "" {
		return OMPLimit{}, fmt.Errorf("missing id")
	}
	if wl.Amount == nil {
		return OMPLimit{}, fmt.Errorf("%s: missing amount", id)
	}
	unit := strings.TrimSpace(wl.Amount.Unit)
	if unit == "" {
		return OMPLimit{}, fmt.Errorf("%s: missing amount.unit", id)
	}
	limit := OMPLimit{
		ID:        id,
		Label:     strings.TrimSpace(wl.Label),
		Used:      wl.Amount.Used,
		Limit:     wl.Amount.Limit,
		Remaining: wl.Amount.Remaining,
		Unit:      unit,
		Status:    strings.TrimSpace(wl.Status),
		Notes:     ompNotes(wl.Notes),
	}
	if limit.Label == "" {
		limit.Label = id
	}
	if wl.Window != nil {
		limit.WindowID = strings.TrimSpace(wl.Window.ID)
		limit.WindowLabel = strings.TrimSpace(wl.Window.Label)
		if wl.Window.DurationMs != nil {
			if *wl.Window.DurationMs < 0 {
				return OMPLimit{}, fmt.Errorf("%s: negative window.durationMs", id)
			}
			limit.WindowDuration = time.Duration(*wl.Window.DurationMs) * time.Millisecond
		}
		if wl.Window.ResetsAt != nil {
			resetsAt, ok := ompMillis(*wl.Window.ResetsAt)
			if !ok {
				return OMPLimit{}, fmt.Errorf("%s: invalid window.resetsAt", id)
			}
			limit.ResetsAt = &resetsAt
		}
	}
	if fraction, ok := ompUsedFraction(wl.Amount); ok {
		pct := fraction * 100
		limit.Utilization = &pct
	}
	return limit, nil
}

// ompUsedFraction mirrors the broker's resolveUsedFraction: explicit fraction,
// then used/limit, then a percent-unit used value, then inverted remaining.
func ompUsedFraction(a *ompWireAmount) (float64, bool) {
	switch {
	case a.UsedFraction != nil:
		return *a.UsedFraction, true
	case a.Used != nil && a.Limit != nil && *a.Limit > 0:
		return *a.Used / *a.Limit, true
	case a.Unit == "percent" && a.Used != nil:
		return *a.Used / 100, true
	case a.RemainingFraction != nil:
		return math.Max(0, 1-*a.RemainingFraction), true
	}
	return 0, false
}

// ompMillis converts epoch milliseconds to UTC, rejecting non-positive values.
func ompMillis(ms float64) (time.Time, bool) {
	if ms <= 0 || math.IsNaN(ms) || math.IsInf(ms, 0) {
		return time.Time{}, false
	}
	return time.UnixMilli(int64(ms)).UTC(), true
}

// ompMetadataString returns a string metadata value, or "" when the key is
// absent or not a string. Metadata is provider-specific and open-ended.
func ompMetadataString(meta map[string]json.RawMessage, key string) string {
	raw, ok := meta[key]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func ompNotes(notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, n := range notes {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ompProviderLabels names the broker's provider ids for display.
var ompProviderLabels = map[string]string{
	"anthropic":    "Anthropic",
	"cursor":       "Cursor",
	"openai-codex": "Codex",
	"opencode-go":  "OpenCode Go",
}

// OMPProviderLabel returns the display name of a broker provider id, title
// casing ids it does not know ("kimi-code" -> "Kimi Code").
func OMPProviderLabel(id string) string {
	if label, ok := ompProviderLabels[id]; ok {
		return label
	}
	parts := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' })
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}
