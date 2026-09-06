package assurance

import (
	"gosuda.org/gstime/core"
	"reflect"
	"testing"
)

func TestExactWidthCapRejectedWithoutMutation(t *testing.T) {
	ac := NewAssuranceClock(1000)
	before := ac.Snapshot()
	_, err := ac.ProcessFullRound(1000, core.TimeInterval{Earliest: 0, Latest: 1000}, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 2000, 0, 1, 1, 1, [32]byte{}, [32]byte{})
	if err == nil || !reflect.DeepEqual(before, ac.Snapshot()) {
		t.Fatal("exact cap accepted or mutated assurance")
	}
	hull := core.TimeInterval{Earliest: 0, Latest: 999}
	_, err = ac.ProcessFullRound(1000, hull, 0, core.RateScale(core.OneQ48), core.RateScale(core.OneQ48), 1, 2000, 0, 1, 1, 1, [32]byte{}, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	propagated, err := PropagateAnchor(ac.Snapshot().Anchor, 1000, 0, 1, 0, 0, 1000)
	if err != nil || propagated != hull {
		t.Fatalf("below-cap round cannot be read: %v %v", propagated, err)
	}
}
