package api

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

func loadOMPFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("testdata/omp_usage.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func TestParseOMPUsage_Fixture(t *testing.T) {
	usage, err := ParseOMPUsage(loadOMPFixture(t))
	if err != nil {
		t.Fatalf("ParseOMPUsage: %v", err)
	}
	if want := time.UnixMilli(1793744646004).UTC(); !usage.GeneratedAt.Equal(want) {
		t.Errorf("GeneratedAt = %v, want %v", usage.GeneratedAt, want)
	}
	if len(usage.Reports) != 4 {
		t.Fatalf("reports = %d, want 4", len(usage.Reports))
	}

	keys := make([]string, 0, len(usage.Reports))
	for _, r := range usage.Reports {
		keys = append(keys, r.AccountKey())
	}
	wantKeys := []string{"cursor:work@example.com", "cursor:team@example.com", "anthropic:me@example.com", "openai-codex:me@example.com"}
	if strings.Join(keys, ",") != strings.Join(wantKeys, ",") {
		t.Fatalf("account keys = %v, want %v", keys, wantKeys)
	}

	work := usage.Reports[0]
	if work.FetchedAt.UnixMilli() != 1793744489412 {
		t.Errorf("cursor fetchedAt = %d", work.FetchedAt.UnixMilli())
	}
	if len(work.Limits) != 1 {
		t.Fatalf("cursor work limits = %d, want 1", len(work.Limits))
	}
	l := work.Limits[0]
	if l.ID != "cursor:usd:individual-overall" || l.Unit != "usd" || l.WindowID != "monthly" || l.WindowLabel != "Monthly" {
		t.Errorf("cursor limit = %+v", l)
	}
	if l.Used == nil || *l.Used != 34.44 || l.Limit == nil || *l.Limit != 250 {
		t.Errorf("cursor amounts used=%v limit=%v", l.Used, l.Limit)
	}
	if l.Utilization == nil || math.Abs(*l.Utilization-13.776) > 1e-9 {
		t.Errorf("cursor utilization = %v, want 13.776", l.Utilization)
	}
	if l.WindowDuration != 0 {
		t.Errorf("cursor window duration = %v, want 0 (not reported)", l.WindowDuration)
	}

	team := usage.Reports[1]
	if len(team.Limits) != 0 {
		t.Errorf("team limits = %d, want 0", len(team.Limits))
	}
	if len(team.Notes) != 1 || !strings.Contains(team.Notes[0], "withheld") {
		t.Errorf("team notes = %v", team.Notes)
	}

	anthropic := usage.Reports[2]
	if anthropic.OrgName != "Example Org" || anthropic.AccountID != "acct-0000" || anthropic.OrgID != "org-0000" {
		t.Errorf("anthropic metadata = %+v", anthropic)
	}
	if len(anthropic.Limits) != 3 {
		t.Fatalf("anthropic limits = %d, want 3", len(anthropic.Limits))
	}
	fiveHour := anthropic.Limits[0]
	if fiveHour.WindowDuration != 5*time.Hour || fiveHour.ResetsAt == nil || fiveHour.ResetsAt.UnixMilli() != 1793746200030 {
		t.Errorf("5h window = %v resets %v", fiveHour.WindowDuration, fiveHour.ResetsAt)
	}
	if fiveHour.Utilization == nil || *fiveHour.Utilization != 13 {
		t.Errorf("5h utilization = %v, want 13", fiveHour.Utilization)
	}
	// No usedFraction: derived from used/limit.
	if sevenDay := anthropic.Limits[1]; sevenDay.Utilization == nil || *sevenDay.Utilization != 31 || sevenDay.Status != "warning" {
		t.Errorf("7d = %+v", sevenDay)
	}
	// Nothing to derive a fraction from: utilization stays unknown, not zero.
	fable := anthropic.Limits[2]
	if fable.Utilization != nil || fable.Used != nil || fable.Status != "" {
		t.Errorf("fable limit = %+v, want unknown utilization", fable)
	}
	if len(fable.Notes) != 1 {
		t.Errorf("fable notes = %v", fable.Notes)
	}

	codex := usage.Reports[3]
	if codex.PlanType != "plus" || len(codex.Limits) != 1 || codex.Limits[0].Utilization == nil || *codex.Limits[0].Utilization != 0 {
		t.Errorf("codex = %+v", codex)
	}
}

func TestParseOMPUsage_IdentityFallbacks(t *testing.T) {
	body := `{"generatedAt":1,"reports":[
		{"provider":"opencode-go","fetchedAt":1,"metadata":{"accountId":"acc-1"},"limits":[]},
		{"provider":"zai","fetchedAt":1,"limits":[]}
	]}`
	usage, err := ParseOMPUsage([]byte(body))
	if err != nil {
		t.Fatalf("ParseOMPUsage: %v", err)
	}
	if got := usage.Reports[0].AccountKey(); got != "opencode-go:acc-1" {
		t.Errorf("accountId fallback key = %q", got)
	}
	if got := usage.Reports[1].AccountKey(); got != "zai:default" {
		t.Errorf("default fallback key = %q", got)
	}
}

func TestParseOMPUsage_SameEmailTwoOrgs(t *testing.T) {
	body := `{"generatedAt":1,"reports":[
		{"provider":"anthropic","fetchedAt":1,"metadata":{"email":"me@example.com","orgId":"org-a"},"limits":[]},
		{"provider":"anthropic","fetchedAt":1,"metadata":{"email":"me@example.com","orgId":"org-b"},"limits":[]},
		{"provider":"cursor","fetchedAt":1,"metadata":{"email":"me@example.com"},"limits":[]}
	]}`
	usage, err := ParseOMPUsage([]byte(body))
	if err != nil {
		t.Fatalf("ParseOMPUsage: %v", err)
	}
	got := []string{usage.Reports[0].AccountKey(), usage.Reports[1].AccountKey(), usage.Reports[2].AccountKey()}
	want := []string{"anthropic:me@example.com#org-a", "anthropic:me@example.com#org-b", "cursor:me@example.com"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestParseOMPUsage_RejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"empty":                ``,
		"not json":             `<html>broker down</html>`,
		"array":                `[]`,
		"truncated":            `{"generatedAt":1,"reports":[`,
		"missing generatedAt":  `{"reports":[]}`,
		"missing reports":      `{"generatedAt":1}`,
		"null reports":         `{"generatedAt":1,"reports":null}`,
		"reports not array":    `{"generatedAt":1,"reports":{}}`,
		"missing provider":     `{"generatedAt":1,"reports":[{"fetchedAt":1,"limits":[]}]}`,
		"missing fetchedAt":    `{"generatedAt":1,"reports":[{"provider":"cursor","limits":[]}]}`,
		"zero fetchedAt":       `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":0,"limits":[]}]}`,
		"string fetchedAt":     `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":"now","limits":[]}]}`,
		"missing limits":       `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1}]}`,
		"limit missing id":     `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"limits":[{"label":"x","amount":{"unit":"usd"}}]}]}`,
		"limit missing amount": `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"limits":[{"id":"a","label":"x"}]}]}`,
		"limit missing unit":   `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"limits":[{"id":"a","amount":{"used":1}}]}]}`,
		"string amount":        `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"limits":[{"id":"a","amount":{"used":"1","unit":"usd"}}]}]}`,
		"bad resetsAt":         `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"limits":[{"id":"a","window":{"id":"m","label":"M","resetsAt":-5},"amount":{"unit":"usd"}}]}]}`,
		"duplicate limit id":   `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"limits":[{"id":"a","amount":{"unit":"usd"}},{"id":"a","amount":{"unit":"usd"}}]}]}`,
		"duplicate account":    `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"metadata":{"email":"a@example.com"},"limits":[]},{"provider":"cursor","fetchedAt":2,"metadata":{"email":"a@example.com"},"limits":[]}]}`,
		"notes not array":      `{"generatedAt":1,"reports":[{"provider":"cursor","fetchedAt":1,"notes":"x","limits":[]}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			usage, err := ParseOMPUsage([]byte(body))
			if err == nil {
				t.Fatalf("ParseOMPUsage accepted malformed body: %+v", usage)
			}
			if !errors.Is(err, ErrOMPInvalidResponse) {
				t.Errorf("error %v does not wrap ErrOMPInvalidResponse", err)
			}
		})
	}
}
