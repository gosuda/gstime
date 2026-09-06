package gstime

import (
	"gosuda.org/gstime/core"
	"testing"
)

func TestNTPLeapBoundaryPolicy(t *testing.T) {
	const transition int64 = 1483228800
	for _, delta := range []int8{-1, 1} {
		lh, _ := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: transition, Delta: delta}})
		q := &defaultSourceQuerier{leapHistory: lh}
		for _, tc := range []struct {
			name   string
			lo, hi int64
			reject bool
		}{
			{"before", transition - 3, transition - 2, false},
			{"repeated_or_deleted", transition - 1, transition - 1, true},
			{"transition", transition, transition, true},
			{"crossing", transition - 3, transition + 3, true},
			{"after", transition + 1, transition + 2, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := q.checkUnixInterval(tc.lo*1_000_000_000, tc.hi*1_000_000_000)
				if (err != nil) != tc.reject {
					t.Fatalf("delta=%d err=%v reject=%v", delta, err, tc.reject)
				}
			})
		}
	}
}
