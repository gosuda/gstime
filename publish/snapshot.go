package publish

import (
	"errors"
	"fmt"

	"gosuda.org/gstime/assurance"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
)

var (
	ErrGuardScaleInvalid       = errors.New("publication guard: raw scale lower must be > 0 and <= upper")
	ErrGuardAnchorDisordered   = errors.New("publication guard: anchor lower exceeds upper")
	ErrGuardGenerationNotMono  = errors.New("publication guard: publication generation must increase monotonically")
	ErrGuardStatusDisagreement = errors.New("publication guard: assurance status contradicts anchor presence")
	ErrGuardNilSnapshot        = errors.New("next snapshot is nil")
	ErrGuardIdentityMismatch   = errors.New("publication guard: assurance identity mismatch")
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
		return ErrGuardNilSnapshot
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
		anchor := next.Assurance.Anchor
		sameDomain := anchor.ContinuityToken != 0 && anchor.ContinuityToken == next.ContinuityToken
		sameScale := anchor.RawScaleLower == next.RawScaleLower && anchor.RawScaleUpper == next.RawScaleUpper
		sameIdentity := anchor.LeapHistoryID == next.LeapHistoryID && anchor.ConfigID == next.ConfigID && next.Assurance.AssuranceEpochID == next.AssuranceEpochID
		if !sameDomain || !sameScale || !sameIdentity {
			return ErrGuardIdentityMismatch
		}
		if anchor.ValidUntilRaw < anchor.RawAnchor {
			return ErrGuardAnchorDisordered
		}
	case core.StatusUnanchored, core.StatusDesync:
		if next.Assurance.Anchor != nil {
			return fmt.Errorf("%w: status %s must not have active anchor", ErrGuardStatusDisagreement, status)
		}
	}

	return nil
}
