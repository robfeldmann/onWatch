package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/onllm-dev/onwatch/v2/internal/menubar"
)

func TestOMPMenubarCardsPerAccount(t *testing.T) {
	h, s := newOMPTestHandler(t)
	accounts, err := s.QueryOMPAccounts()
	if err != nil {
		t.Fatal(err)
	}

	providers, _, _ := h.buildMenubarProviders(menubar.DefaultSettings(), true)
	cards := map[string]menubar.ProviderCard{}
	for _, card := range providers {
		if card.BaseProvider == "omp" {
			cards[card.ID] = card
		}
	}
	// Four accounts, but the Cursor team account has no limits: no card.
	if len(cards) != 3 {
		t.Fatalf("omp cards = %d (%v), want 3", len(cards), cards)
	}
	if _, ok := cards[fmt.Sprintf("omp:%d", accounts[1].ID)]; ok {
		t.Error("an account without limits got a menubar card")
	}

	cursor := cards[fmt.Sprintf("omp:%d", accounts[0].ID)]
	if cursor.Label != "OMP - Cursor · work@example.com" {
		t.Errorf("cursor label = %q", cursor.Label)
	}
	if len(cursor.Quotas) != 1 {
		t.Fatalf("cursor quotas = %+v", cursor.Quotas)
	}
	q := cursor.Quotas[0]
	if q.Label != "Personal Usage" || q.Format != "currency" || q.Used != 34.44 || q.Limit != 250 || q.ResetAt == "" {
		t.Errorf("cursor quota = %+v, want $34.44/$250 currency with a reset", q)
	}
	if q.Percent < 13.77 || q.Percent > 13.78 {
		t.Errorf("cursor percent = %v", q.Percent)
	}

	anthropic := cards[fmt.Sprintf("omp:%d", accounts[2].ID)]
	if len(anthropic.Quotas) != 2 {
		t.Fatalf("anthropic quotas = %+v, want 5h and 7d (fable has no utilization)", anthropic.Quotas)
	}
	for _, q := range anthropic.Quotas {
		if !q.PercentOnly {
			t.Errorf("percent limit %q should be percent-only", q.Label)
		}
	}
	if anthropic.UpdatedAtTime == "" {
		t.Error("card has no data time")
	}

	// Hiding the provider on the dashboard hides every account card; hiding one
	// account hides only that card.
	if err := s.SetSetting("provider_visibility", fmt.Sprintf(`{"omp:%d":{"dashboard":false}}`, accounts[0].ID)); err != nil {
		t.Fatal(err)
	}
	providers, _, _ = h.buildMenubarProviders(menubar.DefaultSettings(), true)
	ids := []string{}
	for _, card := range providers {
		ids = append(ids, card.ID)
	}
	joined := strings.Join(ids, ",")
	if strings.Contains(joined, fmt.Sprintf("omp:%d", accounts[0].ID)) || !strings.Contains(joined, fmt.Sprintf("omp:%d", accounts[2].ID)) {
		t.Errorf("cards after hiding one account = %v", ids)
	}
}

func TestOMPMenubarKeyBaseAndStaleWindow(t *testing.T) {
	if got := providerKeyBase("omp:7"); got != "omp" {
		t.Errorf("providerKeyBase(omp:7) = %q", got)
	}
	if !menubarProviderKeysMatch("omp:7", "omp") || menubarProviderKeysMatch("omp:7", "omp:8") {
		t.Error("menubar settings keys must match omp account cards like codex ones")
	}
	// The broker caches reports for 5 minutes: a 2-minute poll must not flap
	// the card stale between broker refreshes.
	if got := staleAfterFor(staleContext{Base: "omp", Polling: true, PollInterval: 120e9}); got.Minutes() != 15 {
		t.Errorf("omp stale window = %v, want 15m", got)
	}
}
