package publish

import (
	"sync"
	"testing"

	"gosuda.org/gstime/assurance"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
)

func TestPublicationGuard(t *testing.T) {
	snapValid := &Snapshot{
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
		Generation:    1,
		Assurance: assurance.AssuranceState{
			Status: core.StatusUnanchored,
		},
	}

	if err := ValidatePublication(nil, snapValid); err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}

	// 1. Invalid raw scale (lower > upper)
	snapInvalidScale := &Snapshot{
		RawScaleLower: 200,
		RawScaleUpper: 100,
		Generation:    2,
	}
	if err := ValidatePublication(snapValid, snapInvalidScale); err != ErrGuardScaleInvalid {
		t.Fatalf("expected ErrGuardScaleInvalid, got %v", err)
	}

	// 2. Non-monotonic generation
	snapNonMono := &Snapshot{
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
		Generation:    1, // same as snapValid
		Assurance: assurance.AssuranceState{
			Status: core.StatusUnanchored,
		},
	}
	if err := ValidatePublication(snapValid, snapNonMono); err != ErrGuardGenerationNotMono {
		t.Fatalf("expected ErrGuardGenerationNotMono, got %v", err)
	}

	// 3. Status disagreement (SYNCED with nil anchor)
	snapSyncedNoAnchor := &Snapshot{
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
		Generation:    2,
		Assurance: assurance.AssuranceState{
			Status: core.StatusSynced,
			Anchor: nil,
		},
	}
	if err := ValidatePublication(snapValid, snapSyncedNoAnchor); err == nil {
		t.Fatalf("expected error for SYNCED status with nil anchor")
	}
}

func TestConcurrentPublicationLitmus(t *testing.T) {
	initial := &Snapshot{
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
		Generation:    1,
		Assurance: assurance.AssuranceState{
			Status: core.StatusUnanchored,
		},
	}

	pub := NewPublisher(initial)
	done := make(chan struct{})

	// 10 concurrent readers
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					s := pub.Acquire()
					if s == nil || s.Generation == 0 {
						t.Errorf("observed nil or corrupted snapshot")
						return
					}
				}
			}
		}()
	}

	// 1 writer publishing 1,000 snapshots
	for g := uint64(2); g <= 1000; g++ {
		next := &Snapshot{
			EstimateMapping: clock.EstimateMapping{
				MappingGeneration: core.Generation(g),
			},
			RawScaleLower: core.RateScale(core.OneQ48),
			RawScaleUpper: core.RateScale(core.OneQ48),
			Generation:    core.Generation(g),
			Assurance: assurance.AssuranceState{
				Status: core.StatusUnanchored,
			},
		}
		if err := pub.Publish(next); err != nil {
			t.Fatalf("publish failed at gen %d: %v", g, err)
		}
	}

	close(done)
	wg.Wait()
}
