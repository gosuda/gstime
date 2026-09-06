package gstime

import (
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
	"testing"
	"time"
)

func TestLocalUnixEstimateConversion(t *testing.T) {
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: 1483228800, Delta: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		label core.UtcLabel
		leap  bool
	}{
		{"ordinary", core.UtcLabel{Date: core.GregorianDate{Year: 2024, Month: 1, Day: 1}, LeapHistoryID: lh.ID}, false},
		{"leap", core.UtcLabel{Date: core.GregorianDate{Year: 2016, Month: 12, Day: 31}, SecondOfDay: 86400, LeapHistoryID: lh.ID}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := lh.UtcToGstInstant(tc.label)
			if err != nil {
				t.Fatal(err)
			}
			ec := clock.NewEstimateClock()
			ec.InitializeAnchors(1000, target, 0)
			q := &defaultSourceQuerier{estClock: ec, leapHistory: lh}
			got, err := q.localUnixEstimate(1000)
			if tc.leap {
				if err == nil {
					t.Fatal("ambiguous leap accepted")
				}
				return
			}
			if err != nil || got != time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixNano() {
				t.Fatalf("got %d, %v", got, err)
			}
		})
	}
	for _, ec := range []*clock.EstimateClock{nil, clock.NewEstimateClock()} {
		q := &defaultSourceQuerier{estClock: ec, leapHistory: lh}
		before := time.Now().UnixNano()
		got, err := q.localUnixEstimate(0)
		after := time.Now().UnixNano()
		if err != nil || got < before || got > after {
			t.Fatalf("fallback outside wall-clock bracket: %d %v", got, err)
		}
	}
}
