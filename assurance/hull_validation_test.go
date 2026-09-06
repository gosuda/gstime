package assurance

import (
	"gosuda.org/gstime/core"
	"math"
	"math/big"
	"reflect"
	"testing"
)

func TestValidateHullAgainstWideIntegerOracle(t *testing.T) {
	edges := []int64{math.MinInt64, math.MinInt64 + 1, -100, -1, 0, 1, 100, math.MaxInt64 - 1, math.MaxInt64}
	for _, lo := range edges {
		for _, hi := range edges {
			for _, cap := range []int64{0, 1, 100, math.MaxInt64} {
				span := new(big.Int).Sub(big.NewInt(hi), big.NewInt(lo))
				want := lo <= hi && cap > 0 && span.Cmp(big.NewInt(cap)) < 0
				err := ValidateHull(core.TimeInterval{Earliest: core.GstInstant(lo), Latest: core.GstInstant(hi)}, cap)
				if (err == nil) != want {
					t.Fatalf("[%d,%d] cap=%d: %v", lo, hi, cap, err)
				}
			}
		}
	}
}

func TestInvalidFullRoundLeavesStateUntouched(t *testing.T) {
	for _, anchored := range []bool{false, true} {
		ac := NewAssuranceClock(1000)
		if anchored {
			_, err := ac.ProcessFullRound(100, core.TimeInterval{Earliest: 10000, Latest: 10100}, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 1000, 1, 3, 2, 1, [32]byte{}, [32]byte{})
			if err != nil {
				t.Fatal(err)
			}
		}
		before := ac.Snapshot()
		for _, hull := range []core.TimeInterval{{Earliest: 10001, Latest: 20000}, {Earliest: 100, Latest: 0}, {Earliest: math.MinInt64, Latest: math.MaxInt64}} {
			_, err := ac.ProcessFullRound(100, hull, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 1000, 1, 3, 2, 1, [32]byte{}, [32]byte{})
			if err == nil {
				t.Fatal("invalid hull accepted")
			}
			if !reflect.DeepEqual(before, ac.Snapshot()) {
				t.Fatal("invalid input changed anchor, watermark or generation")
			}
		}
	}
}
