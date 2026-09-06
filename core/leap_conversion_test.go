package core_test

import (
	"gosuda.org/gstime/core"
	"math"
	"testing"
)

func TestUnixNanosToGstInstant(t *testing.T) {
	const transition int64 = 1483228800
	for _, delta := range []int8{-1, 1} {
		lh, err := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: transition, Delta: delta}})
		if err != nil {
			t.Fatal(err)
		}
		for _, sec := range []int64{transition - 2, transition, transition + 100} {
			unix := core.UnixNanos(sec*1_000_000_000 + 123)
			got, err := lh.UnixNanosToGstInstant(unix)
			if err != nil {
				t.Fatal(err)
			}
			want := core.GstInstant(unix)
			if sec >= transition {
				want += core.GstInstant(delta) * 1_000_000_000
			}
			if got != want {
				t.Fatalf("delta=%d sec=%d: got %d want %d", delta, sec, got, want)
			}
			if projected := lh.GstInstantToUnixProjection(got); projected.Nanos != unix || projected.IsLeapSecond {
				t.Fatalf("bad round trip %+v", projected)
			}
		}
		if _, err := lh.UnixNanosToGstInstant(core.UnixNanos((transition - 1) * 1_000_000_000)); err == nil {
			t.Fatal("ambiguous/deleted second accepted")
		}
	}
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: transition, Delta: 1}})
	if _, err := lh.UnixNanosToGstInstant(math.MaxInt64); err == nil {
		t.Fatal("conversion overflow accepted")
	}
	empty, _ := core.NewLeapHistory(10, nil)
	if got, err := empty.UnixNanosToGstInstant(-1); err != nil || got != -1 {
		t.Fatalf("negative Unix timestamp: %d %v", got, err)
	}
}
