package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

type stubOMPFetcher struct {
	usage     *api.OMPUsage
	err       error
	refreshes int
	order     []string
}

func (s *stubOMPFetcher) FetchUsage(context.Context) (*api.OMPUsage, error) {
	s.order = append(s.order, "fetch")
	return s.usage, s.err
}

func (s *stubOMPFetcher) RefreshToken() bool {
	s.refreshes++
	s.order = append(s.order, "refresh")
	return true
}

func loadOMPAgentFixture(t *testing.T) []byte {
	t.Helper()
	body, err := os.ReadFile("../api/testdata/omp_usage.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func newOMPAgentForTest(t *testing.T, client ompUsageFetcher) (*OMPAgent, *store.Store) {
	t.Helper()
	st, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	ag := NewOMPAgent(client, st, tracker.NewOMPTracker(st, nil), time.Minute, nil, NewSessionManager(st, "omp", time.Minute, nil))
	return ag, st
}

func countOMPRows(t *testing.T, st *store.Store) (accounts, snapshots int) {
	t.Helper()
	accs, err := st.QueryOMPAccounts()
	if err != nil {
		t.Fatal(err)
	}
	for _, acc := range accs {
		snaps, err := st.QueryOMPRange(acc.ID, time.Unix(0, 0), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		snapshots += len(snaps)
	}
	return len(accs), snapshots
}

func TestOMPAgent_PollStoresEveryAccountOnce(t *testing.T) {
	usage, err := api.ParseOMPUsage(loadOMPAgentFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	client := &stubOMPFetcher{usage: usage}
	ag, st := newOMPAgentForTest(t, client)

	ag.poll(context.Background())
	if accounts, snapshots := countOMPRows(t, st); accounts != 4 || snapshots != 4 {
		t.Fatalf("after poll 1: accounts=%d snapshots=%d, want 4 and 4", accounts, snapshots)
	}
	if len(client.order) < 2 || client.order[0] != "refresh" || client.order[1] != "fetch" {
		t.Errorf("call order = %v, want the token refresh before the fetch", client.order)
	}

	// Cached broker response: nothing new.
	ag.poll(context.Background())
	if accounts, snapshots := countOMPRows(t, st); accounts != 4 || snapshots != 4 {
		t.Fatalf("after poll 2: accounts=%d snapshots=%d, want 4 and 4", accounts, snapshots)
	}
	if client.refreshes != 2 {
		t.Errorf("refreshes = %d, want one per poll", client.refreshes)
	}

	// The broker gains an account: it is recorded without configuration.
	usage.Reports = append(usage.Reports, api.OMPReport{
		Provider: "opencode-go", Identity: "dev@example.com", Email: "dev@example.com",
		FetchedAt: time.Now().UTC(), Limits: []api.OMPLimit{},
	})
	ag.poll(context.Background())
	if accounts, snapshots := countOMPRows(t, st); accounts != 5 || snapshots != 5 {
		t.Fatalf("after poll 3: accounts=%d snapshots=%d, want 5 and 5", accounts, snapshots)
	}

	// The tracker ran for limits with a utilization.
	accs, _ := st.QueryOMPAccounts()
	if cycle, err := st.QueryActiveOMPCycle(accs[2].ID, "anthropic:5h"); err != nil || cycle == nil {
		t.Errorf("anthropic 5h cycle = %v err=%v", cycle, err)
	}
}

func TestOMPAgent_FetchErrorStoresNothing(t *testing.T) {
	client := &stubOMPFetcher{err: errors.New("boom")}
	ag, st := newOMPAgentForTest(t, client)
	ag.poll(context.Background())
	if accounts, snapshots := countOMPRows(t, st); accounts != 0 || snapshots != 0 {
		t.Fatalf("accounts=%d snapshots=%d, want nothing stored", accounts, snapshots)
	}
}

// End to end through the real client: a malformed body fails the poll and
// stores nothing, and a 401 is retried with a token from the refresh hook.
func TestOMPAgent_RealClientMalformedAnd401(t *testing.T) {
	fixture := loadOMPAgentFixture(t)
	var malformed atomic.Bool
	malformed.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if malformed.Load() {
			w.Write([]byte(`{"generatedAt":1,"reports":[{"provider":"cursor","limits":[]}]}`))
			return
		}
		w.Write(fixture)
	}))
	defer srv.Close()

	client := api.NewOMPClient(srv.URL, "", nil)
	var hookCalls atomic.Int32
	client.SetTokenRefresh(func() string {
		// The first call (pre-poll) yields a stale token; later calls a good one.
		if hookCalls.Add(1) == 1 {
			return "stale"
		}
		return "good"
	})
	ag, st := newOMPAgentForTest(t, client)

	ag.poll(context.Background())
	if accounts, snapshots := countOMPRows(t, st); accounts != 0 || snapshots != 0 {
		t.Fatalf("malformed poll stored accounts=%d snapshots=%d", accounts, snapshots)
	}
	if hookCalls.Load() != 2 {
		t.Errorf("hook calls = %d, want pre-poll + 401 retry", hookCalls.Load())
	}

	malformed.Store(false)
	ag.poll(context.Background())
	if accounts, snapshots := countOMPRows(t, st); accounts != 4 || snapshots != 4 {
		t.Fatalf("good poll stored accounts=%d snapshots=%d, want 4 and 4", accounts, snapshots)
	}
}

func TestOMPQuotaStatusLabels(t *testing.T) {
	util, limit := 82.5, 250.0
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	account := &store.OMPAccount{ID: 4, Key: "cursor:work@example.com", SourceProvider: "cursor", Identity: "work@example.com"}
	status := OMPQuotaStatus(account, api.OMPLimit{ID: "cursor:usd:individual-overall", Label: "Personal Usage", Utilization: &util, Limit: &limit, ResetsAt: &reset})
	if status.Provider != "omp" || status.QuotaKey != "cursor:usd:individual-overall" || status.AccountID != "cursor:work@example.com" {
		t.Errorf("dedupe keys = %+v", status)
	}
	if status.ProviderLabel != "Cursor" || status.AccountLabel != "work@example.com" || status.QuotaLabel != "Personal Usage" || status.SourceProvider != "cursor" {
		t.Errorf("labels = %+v", status)
	}
	if status.Utilization != 82.5 || status.Limit != 250 || !status.ResetAt.Equal(reset) {
		t.Errorf("values = %+v", status)
	}
}
