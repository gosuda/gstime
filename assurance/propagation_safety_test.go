package assurance_test

import (
	"gosuda.org/gstime/assurance"
	"gosuda.org/gstime/core"
	"math"
	"testing"
)

func TestPropagateAnchorRejectsOverflow(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lower, upper core.GstInstant
		delta        core.RawNanos
		bound        core.ErrorNs
	}{
		{"endpoint addition", math.MaxInt64 - 10, math.MaxInt64 - 5, 20, 0},
		{"raw delta exceeds signed range", 0, 100, math.MaxUint64 - 1, 0},
		{"raw read error addition", 0, 100, 1, math.MaxUint64},
		{"interval width", math.MinInt64, math.MaxInt64, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &assurance.AssuranceAnchor{RawAnchor: 0, LowerAtAnchor: tc.lower, UpperAtAnchor: tc.upper, RawScaleLower: core.RateScale(core.OneQ48), RawScaleUpper: core.RateScale(core.OneQ48), RawReadBound: tc.bound, ValidUntilRaw: math.MaxUint64, ContinuityToken: 1}
			inv, err := assurance.PropagateAnchor(a, tc.delta, tc.bound, 1, 0, 0, 0)
			if err == nil {
				t.Fatalf("overflow must fail closed, got interval=%+v", inv)
			}
		})
	}
}
