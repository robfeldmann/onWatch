package store

import (
	"os"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

func loadOMPUsageFixture(t *testing.T) *api.OMPUsage {
	t.Helper()
	body, err := os.ReadFile("../api/testdata/omp_usage.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	usage, err := api.ParseOMPUsage(body)
	if err != nil {
		t.Fatalf("ParseOMPUsage: %v", err)
	}
	return usage
}

func newOMPTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func storeOMPUsage(t *testing.T, s *Store, usage *api.OMPUsage, capturedAt time.Time) int {
	t.Helper()
	inserted := 0
	for _, report := range usage.Reports {
		acc, err := s.UpsertOMPAccount(report)
		if err != nil {
			t.Fatalf("UpsertOMPAccount: %v", err)
		}
		_, ok, err := s.InsertOMPSnapshot(acc.ID, capturedAt, report)
		if err != nil {
			t.Fatalf("InsertOMPSnapshot: %v", err)
		}
		if ok {
			inserted++
		}
	}
	return inserted
}

func TestOMPStore_OneAccountPerReportWithValues(t *testing.T) {
	s := newOMPTestStore(t)
	usage := loadOMPUsageFixture(t)
	capturedAt := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	if n := storeOMPUsage(t, s, usage, capturedAt); n != 4 {
		t.Fatalf("inserted = %d, want 4", n)
	}

	accounts, err := s.QueryOMPAccounts()
	if err != nil {
		t.Fatalf("QueryOMPAccounts: %v", err)
	}
	if len(accounts) != 4 {
		t.Fatalf("accounts = %d, want 4", len(accounts))
	}
	for i, acc := range accounts {
		if acc.Key != usage.Reports[i].AccountKey() || acc.SourceProvider != usage.Reports[i].Provider {
			t.Errorf("account %d = %+v, want key %s", i, acc, usage.Reports[i].AccountKey())
		}
		if acc.LastFetchedAt == nil || !acc.LastFetchedAt.Equal(usage.Reports[i].FetchedAt) {
			t.Errorf("account %d LastFetchedAt = %v", i, acc.LastFetchedAt)
		}
	}
	if accounts[2].OrgName != "Example Org" || accounts[3].PlanType != "plus" {
		t.Errorf("metadata not persisted: %+v %+v", accounts[2], accounts[3])
	}

	latest, err := s.QueryLatestOMPSnapshots()
	if err != nil {
		t.Fatalf("QueryLatestOMPSnapshots: %v", err)
	}
	if len(latest) != 4 {
		t.Fatalf("latest = %d, want 4", len(latest))
	}
	team := latest[1]
	if len(team.Limits) != 0 || len(team.Notes) != 1 {
		t.Errorf("team snapshot limits=%d notes=%v", len(team.Limits), team.Notes)
	}
	anthropic := latest[2]
	if len(anthropic.Limits) != 3 {
		t.Fatalf("anthropic limits = %d", len(anthropic.Limits))
	}
	five := anthropic.Limits[0]
	if five.ID != "anthropic:5h" || five.Utilization == nil || *five.Utilization != 13 || five.WindowDuration != 5*time.Hour {
		t.Errorf("5h = %+v", five)
	}
	if five.ResetsAt == nil || five.ResetsAt.UnixMilli() != 1793746200030 {
		t.Errorf("5h resets = %v", five.ResetsAt)
	}
	if fable := anthropic.Limits[2]; fable.Utilization != nil || fable.Used != nil || len(fable.Notes) != 1 {
		t.Errorf("fable round-trip = %+v, want NULL amounts", fable)
	}
	cursor := latest[0].Limits[0]
	if cursor.Used == nil || *cursor.Used != 34.44 || cursor.Limit == nil || *cursor.Limit != 250 || cursor.Unit != "usd" {
		t.Errorf("cursor round-trip = %+v", cursor)
	}
}

func TestOMPStore_UnchangedFetchedAtIsNotStored(t *testing.T) {
	s := newOMPTestStore(t)
	usage := loadOMPUsageFixture(t)
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	storeOMPUsage(t, s, usage, t0)

	// The broker served its cache: same fetchedAt, later poll.
	if n := storeOMPUsage(t, s, usage, t0.Add(2*time.Minute)); n != 0 {
		t.Fatalf("second poll inserted %d snapshots, want 0", n)
	}

	// One account refreshed upstream: only it gets a new row.
	usage.Reports[2].FetchedAt = usage.Reports[2].FetchedAt.Add(5 * time.Minute)
	used := 20.0
	usage.Reports[2].Limits[0].Utilization = &used
	if n := storeOMPUsage(t, s, usage, t0.Add(4*time.Minute)); n != 1 {
		t.Fatalf("third poll inserted %d snapshots, want 1", n)
	}

	var snapshots, values int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM omp_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM omp_quota_values`).Scan(&values); err != nil {
		t.Fatal(err)
	}
	// 4 + 1 snapshots; 1+0+3+1 values, then 3 more for anthropic.
	if snapshots != 5 || values != 8 {
		t.Errorf("rows: snapshots=%d values=%d, want 5 and 8", snapshots, values)
	}

	accounts, _ := s.QueryOMPAccounts()
	series, err := s.QueryOMPLimitSeries(accounts[2].ID, "anthropic:5h", t0.Add(-24*time.Hour), t0.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("QueryOMPLimitSeries: %v", err)
	}
	if len(series) != 2 || *series[0].Utilization != 13 || *series[1].Utilization != 20 {
		t.Errorf("series = %+v", series)
	}
	latest, err := s.QueryLatestOMPSnapshot(accounts[2].ID)
	if err != nil || latest == nil || *latest.Limits[0].Utilization != 20 {
		t.Errorf("latest anthropic = %+v err=%v", latest, err)
	}
}

func TestOMPStore_NewAccountAppearsAutomatically(t *testing.T) {
	s := newOMPTestStore(t)
	usage := loadOMPUsageFixture(t)
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	storeOMPUsage(t, s, usage, t0)

	added := api.OMPReport{
		Provider:  "opencode-go",
		Identity:  "dev@example.com",
		Email:     "dev@example.com",
		FetchedAt: t0,
		Limits:    []api.OMPLimit{{ID: "opencode-go:5h", Label: "5 Hour", Unit: "percent"}},
	}
	usage.Reports = append(usage.Reports, added)
	if n := storeOMPUsage(t, s, usage, t0.Add(time.Minute)); n != 1 {
		t.Fatalf("inserted = %d, want 1 (only the new account)", n)
	}
	accounts, err := s.QueryOMPAccounts()
	if err != nil {
		t.Fatalf("QueryOMPAccounts: %v", err)
	}
	if len(accounts) != 5 || accounts[4].Key != "opencode-go:dev@example.com" || accounts[4].SourceProvider != "opencode-go" {
		t.Fatalf("accounts = %+v", accounts)
	}
	// Re-upserting an existing report reuses its row.
	again, err := s.UpsertOMPAccount(usage.Reports[0])
	if err != nil || again.ID != accounts[0].ID {
		t.Errorf("re-upsert id = %v err=%v, want %d", again, err, accounts[0].ID)
	}
}

func TestOMPStore_RangeAndCycles(t *testing.T) {
	s := newOMPTestStore(t)
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	report := api.OMPReport{Provider: "anthropic", Identity: "me@example.com", Email: "me@example.com"}
	acc, err := s.UpsertOMPAccount(report)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		u := float64(i * 10)
		report.FetchedAt = t0.Add(time.Duration(i) * 5 * time.Minute)
		report.Limits = []api.OMPLimit{{ID: "anthropic:5h", Unit: "percent", Utilization: &u}}
		if _, ok, err := s.InsertOMPSnapshot(acc.ID, report.FetchedAt, report); err != nil || !ok {
			t.Fatalf("insert %d: ok=%v err=%v", i, ok, err)
		}
	}
	snaps, err := s.QueryOMPRange(acc.ID, t0.Add(5*time.Minute), t0.Add(20*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 || !snaps[0].FetchedAt.Equal(t0.Add(5*time.Minute)) {
		t.Errorf("range = %d snaps, first %v", len(snaps), snaps[0].FetchedAt)
	}
	capped, _ := s.QueryOMPRange(acc.ID, t0, t0.Add(time.Hour), 2)
	if len(capped) != 2 || !capped[1].FetchedAt.Equal(t0.Add(20*time.Minute)) {
		t.Errorf("capped range should keep the newest rows oldest-first: %d", len(capped))
	}

	resets := t0.Add(5 * time.Hour)
	if _, err := s.CreateOMPCycle(acc.ID, "anthropic:5h", t0, &resets); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateOMPCycle(acc.ID, "anthropic:5h", 40, 40); err != nil {
		t.Fatal(err)
	}
	active, err := s.QueryActiveOMPCycle(acc.ID, "anthropic:5h")
	if err != nil || active == nil || active.PeakUtilization != 40 || !active.ResetsAt.Equal(resets) {
		t.Fatalf("active = %+v err=%v", active, err)
	}
	if err := s.CloseOMPCycle(acc.ID, "anthropic:5h", resets, 40, 40); err != nil {
		t.Fatal(err)
	}
	history, err := s.QueryOMPCycleHistory(acc.ID, "anthropic:5h")
	if err != nil || len(history) != 1 || history[0].CycleEnd == nil {
		t.Fatalf("history = %+v err=%v", history, err)
	}
	if ids, _ := s.QueryOMPLimitIDs(acc.ID); len(ids) != 1 || ids[0] != "anthropic:5h" {
		t.Errorf("limit ids = %v", ids)
	}
}
