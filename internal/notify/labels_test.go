package notify

import (
	"strings"
	"testing"
)

func labelledOMPStatus(util float64) QuotaStatus {
	return QuotaStatus{
		Provider:       "omp",
		QuotaKey:       "cursor:usd:individual-overall",
		AccountID:      "cursor:work@example.com",
		Utilization:    util,
		Limit:          250,
		ProviderLabel:  "Cursor",
		AccountLabel:   "work@example.com",
		QuotaLabel:     "Personal Usage",
		SourceProvider: "cursor",
	}
}

func TestLabelledQuotaAlertText(t *testing.T) {
	e := &NotificationEngine{}
	status := labelledOMPStatus(85)

	if got, want := e.buildSubject(status, EventWarning), "[WARNING] Cursor (work@example.com) Personal Usage at 85.0%"; got != want {
		t.Errorf("warning subject = %q, want %q", got, want)
	}
	if got, want := e.buildSubject(status, EventReset), "[RESET] Cursor (work@example.com) Personal Usage has been reset"; got != want {
		t.Errorf("reset subject = %q, want %q", got, want)
	}
	body := e.buildBody(status, EventWarning)
	for _, want := range []string{"Account: Cursor · work@example.com\n", "Quota: Personal Usage (cursor:usd:individual-overall)\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}

	// Providers without labels keep the old text.
	plain := QuotaStatus{Provider: "anthropic", QuotaKey: "five_hour", Utilization: 97}
	if got, want := e.buildSubject(plain, EventCritical), "[CRITICAL] Anthropic quota five_hour at 97.0%"; got != want {
		t.Errorf("plain subject = %q, want %q", got, want)
	}
}

// A labelled threshold fires once per crossing and its webhook carries the
// account and source provider.
func TestEngineLabelledThresholdWebhook(t *testing.T) {
	engine, _, capture := setupWebhookEngine(t)

	engine.Check(labelledOMPStatus(85))
	engine.Check(labelledOMPStatus(86)) // same cycle, same threshold: no repeat
	payloads := capture.all()
	if len(payloads) != 1 {
		t.Fatalf("delivered %d payloads, want 1 warning", len(payloads))
	}
	p := payloads[0]
	if p.Event != EventWarning || p.Provider != "omp" || p.QuotaKey != "cursor:usd:individual-overall" ||
		p.AccountID != "cursor:work@example.com" || p.SourceProvider != "cursor" ||
		p.AccountLabel != "work@example.com" || p.QuotaLabel != "Personal Usage" {
		t.Errorf("payload = %+v", p)
	}
	if p.Title != "[WARNING] Cursor (work@example.com) Personal Usage at 85.0%" {
		t.Errorf("title = %q", p.Title)
	}

	// Another account's same limit is a separate crossing.
	other := labelledOMPStatus(85)
	other.AccountID = "cursor:team@example.com"
	other.AccountLabel = "team@example.com"
	engine.Check(other)
	if got := capture.count(); got != 2 {
		t.Fatalf("delivered %d payloads, want 2 after a second account crosses", got)
	}

	// A reset clears the cycle, so the next crossing alerts again.
	reset := labelledOMPStatus(0)
	reset.ResetOccurred = true
	engine.Check(reset)
	before := capture.count()
	engine.Check(labelledOMPStatus(85))
	if got := capture.count(); got != before+1 {
		t.Fatalf("delivered %d payloads after reset, want %d", got, before+1)
	}
}
