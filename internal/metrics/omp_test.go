package metrics

import (
	"strconv"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestMetrics_ScrapeOMPAccounts(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Millisecond)
	reset := now.Add(3 * time.Hour).Truncate(time.Second)
	util, used, limit := 13.8, 34.5, 250.0
	cursor := api.OMPReport{
		Provider: "cursor", Identity: "work@example.com", Email: "work@example.com", FetchedAt: now,
		Limits: []api.OMPLimit{
			{ID: "cursor:usd:individual-overall", Label: "Personal Usage", Unit: "usd", Utilization: &util, Used: &used, Limit: &limit, ResetsAt: &reset},
			{ID: "cursor:requests", Label: "Requests", Unit: "unknown"}, // no utilization: not exported
		},
	}
	withheld := api.OMPReport{Provider: "cursor", Identity: "team@example.com", Email: "team@example.com", FetchedAt: now, Notes: []string{"withheld"}}
	ids := make([]string, 0, 2)
	for _, report := range []api.OMPReport{cursor, withheld} {
		acc, err := s.UpsertOMPAccount(report)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.InsertOMPSnapshot(acc.ID, now, report); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, strconv.FormatInt(acc.ID, 10))
	}

	m := New()
	m.Scrape(s, 2*time.Minute)
	families, err := m.Gather().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	labels := map[string]string{"provider": "omp", "quota_type": "cursor:usd:individual-overall", "account_id": ids[0]}
	if got := findMetric(t, families, "onwatch_quota_utilization_percent", labels).GetGauge().GetValue(); got != 13.8 {
		t.Errorf("utilization = %v, want 13.8", got)
	}
	if got := findMetric(t, families, "onwatch_quota_reset_timestamp_seconds", labels).GetGauge().GetValue(); got != float64(reset.Unix()) {
		t.Errorf("reset timestamp = %v, want %d", got, reset.Unix())
	}
	for i, name := range []string{"Cursor · work@example.com", "Cursor · team@example.com"} {
		info := map[string]string{"provider": "omp", "account_id": ids[i], "account_name": name}
		if got := findMetric(t, families, "onwatch_account_info", info).GetGauge().GetValue(); got != 1 {
			t.Errorf("account_info %s = %v", name, got)
		}
	}
	health := map[string]string{"provider": "omp", "account_id": ids[0]}
	if got := findMetric(t, families, "onwatch_agent_healthy", health).GetGauge().GetValue(); got != 1 {
		t.Errorf("agent_healthy = %v, want 1 for a fresh report", got)
	}

	for _, family := range families {
		if family.GetName() != "onwatch_quota_utilization_percent" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, l := range metric.GetLabel() {
				if l.GetName() == "quota_type" && l.GetValue() == "cursor:requests" {
					t.Error("a limit without utilization was exported")
				}
			}
		}
	}
}
