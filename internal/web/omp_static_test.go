package web

import (
	"strings"
	"testing"
)

// The omp tab is wired through the generic dashboard code paths; these checks
// catch a branch dropped by a later refactor of app.js.
func TestAppJS_OMPTabWiring(t *testing.T) {
	t.Parallel()
	appJS := readStaticAppJS(t)

	for _, want := range []string{
		"if (document.getElementById('quota-grid-omp')) return 'omp';",
		"param += ompAccountParam();",
		"if (provider === 'omp') return !isOMPAllAccounts();",
		"provider !== 'api-integrations' && provider !== 'omp' && !isAccountsOverviewMode(provider)",
		"} else if (provider === 'omp') {\n        renderOMPAccounts(data);",
		"return Array.isArray(data.accounts) ? '' : 'invalid body';",
		"State.chart.data.datasets = buildOMPHistoryDatasets(data, range);",
		"provider === 'omp' ? ompAccountParam() : ''",
		"entries.push(...ompBothEntries(current.omp));",
		"initOMPAccountPicker();",
	} {
		if !strings.Contains(appJS, want) {
			t.Errorf("app.js is missing omp wiring: %q", want)
		}
	}

	// Cards rebuild only when the shown accounts or their limits change, then
	// update in place.
	render := appJS[strings.Index(appJS, "function renderOMPAccounts(data) {"):]
	sigIdx := strings.Index(render, "if (container.dataset.signature !== signature) {")
	updateIdx := strings.Index(render, "shown.add(updateOMPCard(acc, l))")
	if sigIdx < 0 || updateIdx < 0 || sigIdx > updateIdx {
		t.Fatal("renderOMPAccounts must compare the account/limit signature before updating cards in place")
	}
}
