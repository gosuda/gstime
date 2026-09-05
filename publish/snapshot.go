package publish

import (
	"errors"
	"fmt"

	"github.com/gosuda/gstime/core"
	"github.com/gosuda/gstime/assurance"
	"github.com/gosuda/gstime/clock"
)

var (
	ErrGuardScaleInvalid       = errors.New("publication guard: raw scale lower must be > 0 and <= upper")
	ErrGuardAnchorDisordered   = errors.New("publication guard: anchor lower exceeds upper")
	ErrGuardGenerationNotMono  = errors.New("publication guard: publication generation must increase monotonically")
	ErrGuardStatusDisagreement = errors.New("publication guard: assurance status contradicts anchor presence")
)

// Snapshot represents a complete coherent published state (Section 7.9).
type Snapshot struct {
	EstimateMapping  clock.EstimateMapping
	Assurance        assurance.AssuranceState
	RawScaleLower    core.RateScale
	RawScaleUpper    core.RateScale
	ContinuityToken  uint64
	LeapHistoryID    [32]byte
	ConfigID         [32]byte
	AssuranceEpochID [16]byte
	Generation       core.Generation
}

// ValidatePublication verifies all publication guard invariants before release (Section 7.14).
func ValidatePublication(curr, next *Snapshot) error {
	if next == nil {
		return errors.New("next snapshot is nil")
	}

	if next.RawScaleLower <= 0 || next.RawScaleLower > next.RawScaleUpper {
		return ErrGuardScaleInvalid
	}

	if curr != nil && next.Generation <= curr.Generation {
		return ErrGuardGenerationNotMono
	}

	status := next.Assurance.Status
	switch status {
	case core.StatusSynced, core.StatusHoldover:
		if next.Assurance.Anchor == nil {
			return fmt.Errorf("%w: status %s requires non-nil anchor", ErrGuardStatusDisagreement, status)
		}
		if next.Assurance.Anchor.LowerAtAnchor > next.Assurance.Anchor.UpperAtAnchor {
			return ErrGuardAnchorDisordered
		}
	case core.StatusUnanchored, core.StatusDesync:
		if next.Assurance.Anchor != nil {
			return fmt.Errorf("%w: status %s must not have active anchor", ErrGuardStatusDisagreement, status)
		}
	}

	return nil
}
