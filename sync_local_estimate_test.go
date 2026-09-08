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
		name     string
		label    core.UtcLabel
		leap     bool
		wantUnix int64
	}{
		{"ordinary", core.UtcLabel{Date: core.GregorianDate{Year: 2024, Month: 1, Day: 1}, LeapHistoryID: lh.ID}, false, 1_704_067_200_000_000_000},
		{"zero", core.UtcLabel{Date: core.GregorianDate{Year: 1970, Month: 1, Day: 1}, LeapHistoryID: lh.ID}, false, 0},
		{"negative", core.UtcLabel{Date: core.GregorianDate{Year: 1969, Month: 12, Day: 31}, SecondOfDay: 86399, LeapHistoryID: lh.ID}, false, -1_000_000_000},
		{"leap", core.UtcLabel{Date: core.GregorianDate{Year: 2016, Month: 12, Day: 31}, SecondOfDay: 86400, LeapHistoryID: lh.ID}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target, err := lh.UtcToGstInstant(tc.label)
			if err != nil {
				t.Fatal(err)
			}
			ec := clock.NewEstimateClock()
			if err := ec.InitializeAnchors(1000, target, 0); err != nil {
				t.Fatal(err)
			}
			q := &defaultSourceQuerier{estClock: ec, leapHistory: lh}
			got, _, err := q.localUnixEstimate(1000)
			if tc.leap {
				if err == nil {
					t.Fatal("ambiguous leap accepted")
				}
				return
			}
			if err != nil || got != tc.wantUnix {
				t.Fatalf("local Unix estimate = %d, %v; want %d", got, err, tc.wantUnix)
			}
		})
	}
	for _, ec := range []*clock.EstimateClock{nil, clock.NewEstimateClock()} {
		q := &defaultSourceQuerier{estClock: ec, leapHistory: lh}
		before := time.Now().UnixNano()
		got, _, err := q.localUnixEstimate(0)
		after := time.Now().UnixNano()
		if err != nil || got < before || got > after {
			t.Fatalf("fallback outside wall-clock bracket: %d %v", got, err)
		}
	}
}
