package tracker

import (
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func ompSnapshot(accountID int64, at time.Time, util float64, resetsAt time.Time) *store.OMPSnapshot {
	return &store.OMPSnapshot{
		AccountID: accountID,
		FetchedAt: at,
		Limits: []api.OMPLimit{
			{ID: "anthropic:5h", Unit: "percent", Utilization: &util, ResetsAt: &resetsAt},
			{ID: "anthropic:7d:fable", Unit: "percent"}, // no utilization: never tracked
		},
	}
}

func TestOMPTracker_CyclesPerAccountAndLimit(t *testing.T) {
	s, err := store.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	tr := NewOMPTracker(s, nil)
	var resets []string
	tr.SetOnReset(func(accountID int64, limit api.OMPLimit) {
		resets = append(resets, limit.ID)
	})

	t0 := time.Now().UTC().Add(-6 * time.Hour).Truncate(time.Minute)
	reset1 := t0.Add(5 * time.Hour)
	for i, util := range []float64{10, 25, 40} {
		if err := tr.Process(ompSnapshot(7, t0.Add(time.Duration(i)*10*time.Minute), util, reset1)); err != nil {
			t.Fatalf("Process: %v", err)
		}
	}
	// A second account with the same limit id has its own cycle.
	if err := tr.Process(ompSnapshot(8, t0, 90, reset1)); err != nil {
		t.Fatal(err)
	}

	active, err := s.QueryActiveOMPCycle(7, "anthropic:5h")
	if err != nil || active == nil {
		t.Fatalf("active = %v err=%v", active, err)
	}
	if active.PeakUtilization != 40 || active.TotalDelta != 30 {
		t.Errorf("active peak=%v delta=%v, want 40 and 30", active.PeakUtilization, active.TotalDelta)
	}
	if other, _ := s.QueryActiveOMPCycle(8, "anthropic:5h"); other == nil || other.PeakUtilization != 90 {
		t.Errorf("account 8 cycle = %+v", other)
	}
	if fable, _ := s.QueryActiveOMPCycle(7, "anthropic:7d:fable"); fable != nil {
		t.Errorf("limit without utilization opened a cycle: %+v", fable)
	}

	// Past the reset time with a new window: the cycle closes.
	reset2 := reset1.Add(5 * time.Hour)
	if err := tr.Process(ompSnapshot(7, reset1.Add(10*time.Minute), 5, reset2)); err != nil {
		t.Fatal(err)
	}
	history, err := s.QueryOMPCycleHistory(7, "anthropic:5h")
	if err != nil || len(history) != 1 {
		t.Fatalf("history = %v err=%v", history, err)
	}
	if history[0].PeakUtilization != 40 || !history[0].CycleEnd.Equal(reset1) {
		t.Errorf("closed cycle = %+v", history[0])
	}
	if len(resets) != 1 || resets[0] != "anthropic:5h" {
		t.Errorf("onReset calls = %v", resets)
	}

	summary, err := tr.UsageSummary(7, "anthropic:5h")
	if err != nil {
		t.Fatal(err)
	}
	if summary.CompletedCycles != 1 || summary.PeakCycle != 40 {
		t.Errorf("summary = %+v", summary)
	}
}
