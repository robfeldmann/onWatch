package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/config"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

func newOMPTestHandler(t *testing.T) (*Handler, *store.Store) {
	t.Helper()
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	body, err := os.ReadFile("../api/testdata/omp_usage.json")
	if err != nil {
		t.Fatal(err)
	}
	usage, err := api.ParseOMPUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	tr := tracker.NewOMPTracker(s, nil)
	// Rebase fetchedAt onto the clock so range queries and staleness hold
	// whenever the test runs.
	now := time.Now().UTC()
	for i, report := range usage.Reports {
		report.FetchedAt = now.Add(-time.Duration(i+1) * time.Minute)
		acc, err := s.UpsertOMPAccount(report)
		if err != nil {
			t.Fatal(err)
		}
		id, _, err := s.InsertOMPSnapshot(acc.ID, report.FetchedAt, report)
		if err != nil {
			t.Fatal(err)
		}
		if err := tr.Process(&store.OMPSnapshot{ID: id, AccountID: acc.ID, FetchedAt: report.FetchedAt, Limits: report.Limits}); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		AnthropicToken: "tok",
		OMPBrokerURL:   "http://127.0.0.1:1",
		PollInterval:   60 * time.Second,
		Port:           9211,
		AdminUser:      "admin",
		AdminPass:      "test",
	}
	h := NewHandler(s, nil, nil, nil, cfg)
	h.SetOMPTracker(tr)
	return h, s
}

func getOMPJSON(t *testing.T, handler http.HandlerFunc, url string, out interface{}) int {
	t.Helper()
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v (%s)", url, err, rec.Body.String())
		}
	}
	return rec.Code
}

func TestOMPCurrent_PerAccountShape(t *testing.T) {
	h, _ := newOMPTestHandler(t)
	var resp struct {
		Provider string `json:"provider"`
		Accounts []struct {
			AccountID      int64    `json:"accountId"`
			Key            string   `json:"key"`
			SourceProvider string   `json:"sourceProvider"`
			Identity       string   `json:"identity"`
			OrgName        string   `json:"orgName"`
			FetchedAt      string   `json:"fetchedAt"`
			IsStale        bool     `json:"isStale"`
			Notes          []string `json:"notes"`
			Limits         []struct {
				ID               string   `json:"id"`
				Label            string   `json:"label"`
				WindowLabel      string   `json:"windowLabel"`
				WindowDurationMs *int64   `json:"windowDurationMs"`
				Used             *float64 `json:"used"`
				Limit            *float64 `json:"limit"`
				Unit             string   `json:"unit"`
				Utilization      *float64 `json:"utilization"`
				Status           string   `json:"status"`
				UpstreamStatus   string   `json:"upstreamStatus"`
				ResetsAt         *string  `json:"resetsAt"`
			} `json:"limits"`
		} `json:"accounts"`
	}
	if code := getOMPJSON(t, h.Current, "/api/current?provider=omp", &resp); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if resp.Provider != "omp" || len(resp.Accounts) != 4 {
		t.Fatalf("provider=%q accounts=%d", resp.Provider, len(resp.Accounts))
	}
	team := resp.Accounts[1]
	if team.Key != "cursor:team@example.com" || len(team.Limits) != 0 || len(team.Notes) != 1 {
		t.Errorf("team account = %+v", team)
	}
	anthropic := resp.Accounts[2]
	if anthropic.SourceProvider != "anthropic" || anthropic.OrgName != "Example Org" || len(anthropic.Limits) != 3 {
		t.Fatalf("anthropic account = %+v", anthropic)
	}
	five := anthropic.Limits[0]
	if five.ID != "anthropic:5h" || five.Utilization == nil || *five.Utilization != 13 || five.Status != "healthy" ||
		five.WindowDurationMs == nil || *five.WindowDurationMs != 18000000 || five.ResetsAt == nil {
		t.Errorf("5h limit = %+v", five)
	}
	// The broker's "warning" raises a low percentage.
	if seven := anthropic.Limits[1]; seven.Status != "warning" || seven.UpstreamStatus != "warning" {
		t.Errorf("7d limit = %+v", seven)
	}
	if fable := anthropic.Limits[2]; fable.Utilization != nil || fable.Used != nil {
		t.Errorf("fable limit should report null amounts: %+v", fable)
	}
	cursor := resp.Accounts[0].Limits[0]
	if cursor.Unit != "usd" || cursor.Used == nil || *cursor.Used != 34.44 || cursor.Limit == nil || *cursor.Limit != 250 {
		t.Errorf("cursor limit = %+v", cursor)
	}
	if team.IsStale {
		t.Errorf("account fetched a minute ago is stale")
	}
}

func TestOMPHistoryCyclesSummary(t *testing.T) {
	h, s := newOMPTestHandler(t)
	accounts, err := s.QueryOMPAccounts()
	if err != nil {
		t.Fatal(err)
	}
	anthropicID := accounts[2].ID

	var cycles []map[string]interface{}
	url := "/api/cycles?provider=omp&account=" + strconv.FormatInt(anthropicID, 10) + "&type=anthropic:5h"
	if code := getOMPJSON(t, h.Cycles, url, &cycles); code != http.StatusOK {
		t.Fatalf("cycles status = %d", code)
	}
	if len(cycles) != 1 || cycles[0]["isActive"] != true || cycles[0]["limitId"] != "anthropic:5h" {
		t.Errorf("cycles = %v", cycles)
	}
	if code := getOMPJSON(t, h.Cycles, "/api/cycles?provider=omp&type=anthropic:5h", nil); code != http.StatusBadRequest {
		t.Errorf("cycles without account = %d, want 400", code)
	}

	var summary struct {
		Accounts []struct {
			AccountID int64                             `json:"accountId"`
			Limits    map[string]map[string]interface{} `json:"limits"`
		} `json:"accounts"`
	}
	if code := getOMPJSON(t, h.Summary, "/api/summary?provider=omp&account="+strconv.FormatInt(anthropicID, 10), &summary); code != http.StatusOK {
		t.Fatalf("summary status = %d", code)
	}
	if len(summary.Accounts) != 1 || summary.Accounts[0].AccountID != anthropicID {
		t.Fatalf("summary = %+v", summary)
	}
	if got := summary.Accounts[0].Limits["anthropic:5h"]["peakCycle"]; got != float64(13) {
		t.Errorf("5h peakCycle = %v, want 13", got)
	}

	var history struct {
		Accounts []struct {
			Key    string                   `json:"key"`
			Points []map[string]interface{} `json:"points"`
		} `json:"accounts"`
	}
	if code := getOMPJSON(t, h.History, "/api/history?provider=omp&range=30d", &history); code != http.StatusOK {
		t.Fatalf("history status = %d", code)
	}
	if len(history.Accounts) != 4 {
		t.Errorf("history accounts = %d, want 4", len(history.Accounts))
	}
	if code := getOMPJSON(t, h.History, "/api/history?provider=omp&account=999", nil); code != http.StatusNotFound {
		t.Errorf("history unknown account = %d, want 404", code)
	}
	if code := getOMPJSON(t, h.History, "/api/history?provider=omp&account=abc", nil); code != http.StatusBadRequest {
		t.Errorf("history bad account = %d, want 400", code)
	}
}

func TestOMPHiddenFromDashboardTabs(t *testing.T) {
	h, _ := newOMPTestHandler(t)
	for _, p := range h.buildDashboardProviderTabs() {
		if p == "omp" {
			t.Fatalf("omp tab rendered before its dashboard view exists: %v", h.buildDashboardProviderTabs())
		}
	}
	if !h.config.HasProvider("omp") {
		t.Fatal("omp provider should still be available to the API")
	}
}
