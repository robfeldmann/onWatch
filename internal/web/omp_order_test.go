package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/menubar"
)

func cardIDs(cards []menubar.ProviderCard) string {
	ids := make([]string, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

// The menubar ranks account cards by their exact omp:<id> key; unranked
// accounts follow, and a key for a removed account changes nothing.
func TestMenubarCardOrderHonorsOMPAccountKeys(t *testing.T) {
	cards := []menubar.ProviderCard{
		{ID: "anthropic", Label: "Anthropic"},
		{ID: "omp:3", BaseProvider: "omp", Label: "OMP - Codex"},
		{ID: "omp:4", BaseProvider: "omp", Label: "OMP - Cursor a"},
		{ID: "omp:5", BaseProvider: "omp", Label: "OMP - Cursor b"},
		{ID: "omp:6", BaseProvider: "omp", Label: "OMP - Anthropic"},
	}
	sortProviderCards(cards, []string{"omp:5", "omp:99", "anthropic", "omp:6", "omp:3"})
	if got, want := cardIDs(cards), "omp:5,anthropic,omp:6,omp:3,omp:4"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}

	visible := filterMenubarProviders(cards, []string{"omp:5", "anthropic", "omp:6", "omp:4"})
	if got, want := cardIDs(visible), "omp:5,anthropic,omp:6,omp:4"; got != want {
		t.Errorf("visible = %s, want %s (omp:3 hidden alone)", got, want)
	}
}

func TestMenubarHistoryDiscoversOMPAccounts(t *testing.T) {
	// Visible list holds some OMP accounts: a new account becomes visible.
	settings := &menubar.Settings{
		KnownProviders:   []string{"anthropic", "omp", "omp:3", "omp:4"},
		VisibleProviders: []string{"anthropic", "omp:4"},
	}
	if !updateMenubarProviderHistory(settings, []string{"anthropic", "omp", "omp:3", "omp:4", "omp:7"}) {
		t.Fatal("a new account should change the history")
	}
	if got := strings.Join(settings.VisibleProviders, ","); got != "anthropic,omp:4,omp:7" {
		t.Errorf("visible = %s, want the new account appended", got)
	}
	if got := strings.Join(settings.KnownProviders, ","); !strings.HasSuffix(got, ",omp:7") {
		t.Errorf("known = %s, want omp:7 recorded", got)
	}
	// Already known: nothing changes, even though omp:3 stays hidden.
	if updateMenubarProviderHistory(settings, []string{"anthropic", "omp", "omp:3", "omp:4", "omp:7"}) {
		t.Error("known accounts must not change the history")
	}

	// Every OMP account hidden: a new account stays hidden too.
	hidden := &menubar.Settings{
		KnownProviders:   []string{"anthropic", "omp", "omp:3"},
		VisibleProviders: []string{"anthropic"},
	}
	updateMenubarProviderHistory(hidden, []string{"anthropic", "omp", "omp:3", "omp:8"})
	if got := strings.Join(hidden.VisibleProviders, ","); got != "anthropic" {
		t.Errorf("visible = %s, want OMP kept hidden", got)
	}

	// A bare "omp" visible entry already covers new accounts.
	base := &menubar.Settings{KnownProviders: []string{"omp"}, VisibleProviders: []string{"omp"}}
	updateMenubarProviderHistory(base, []string{"omp", "omp:9"})
	if got := strings.Join(base.VisibleProviders, ","); got != "omp" {
		t.Errorf("visible = %s, want just the base key", got)
	}
}

func TestOMPDashboardFollowsMenubarOrder(t *testing.T) {
	h, s := newOMPTestHandler(t)
	accounts, err := s.QueryOMPAccounts()
	if err != nil {
		t.Fatal(err)
	}
	id := func(i int) string { return fmt.Sprintf("omp:%d", accounts[i].ID) }
	order := func() string {
		cur := h.buildOMPCurrent()
		keys := []string{}
		for _, raw := range cur["accounts"].([]interface{}) {
			keys = append(keys, fmt.Sprintf("omp:%d", raw.(map[string]interface{})["accountId"].(int64)))
		}
		return strings.Join(keys, ",")
	}

	if got, want := order(), strings.Join([]string{id(0), id(1), id(2), id(3)}, ","); got != want {
		t.Fatalf("default order = %s, want id order %s", got, want)
	}

	settings := menubar.DefaultSettings()
	// Account 2 first, then 0; a removed account (omp:999) and a non-OMP key
	// are ignored; accounts 1 and 3 keep their relative order after them.
	settings.ProvidersOrder = []string{"anthropic", id(2), "omp:999", id(0)}
	if err := s.SetMenubarSettings(settings); err != nil {
		t.Fatal(err)
	}
	if got, want := order(), strings.Join([]string{id(2), id(0), id(1), id(3)}, ","); got != want {
		t.Errorf("menubar order = %s, want %s", got, want)
	}

	// History and the other per-account endpoints share the order.
	var history struct {
		Accounts []struct {
			AccountID int64 `json:"accountId"`
		} `json:"accounts"`
	}
	if code := getOMPJSON(t, h.History, "/api/history?provider=omp&range=30d", &history); code != 200 {
		t.Fatalf("history status = %d", code)
	}
	if len(history.Accounts) != 4 || history.Accounts[0].AccountID != accounts[2].ID || history.Accounts[1].AccountID != accounts[0].ID {
		t.Errorf("history order = %+v", history.Accounts)
	}
}
