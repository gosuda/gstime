package assurance

import (
	"crypto/rand"
	"errors"
	"math"
	"sync"

	"gosuda.org/gstime/core"
)

var (
	ErrRawEarlierThanAnchor    = errors.New("raw instant earlier than anchor raw")
	ErrAnchorExpired           = errors.New("assurance anchor validity horizon expired")
	ErrContinuityTokenMismatch = errors.New("raw continuity token mismatch without bridge")
	ErrAssuranceConflict       = errors.New("empty intersection between old propagated bound and new hull")
	ErrBoundTooWide            = errors.New("assurance interval width exceeds maximum cap")
)

// AssuranceAnchor represents an immutable published absolute anchor (Section 4.15).
type AssuranceAnchor struct {
	RawAnchor         core.RawNanos
	LowerAtAnchor     core.GstInstant
	UpperAtAnchor     core.GstInstant
	RawScaleLower     core.RateScale
	RawScaleUpper     core.RateScale
	RawReadBound      core.ErrorNs
	SourceFaultBudget uint32
	EligibleDomains   uint32
	CoverageThreshold uint32
	ComponentCount    uint32
	LeapHistoryID     [32]byte
	ConfigID          [32]byte
	Generation        core.Generation
	CreatedRaw        core.RawNanos
	ValidUntilRaw     core.RawNanos
	ContinuityToken   uint64
}

// AssuranceState holds active assurance clock and status machine state (Section 6.1).
type AssuranceState struct {
	Anchor                 *AssuranceAnchor
	LowerDebt              core.DurationNs // <= 0
	UpperDebt              core.DurationNs // >= 0
	Status                 core.SyncStatus
	Reason                 core.StatusReason
	LastSuccessfulRoundRaw core.RawNanos
	PastWatermark          core.GstInstant
	Generation             core.Generation
	AssuranceEpochID       [16]byte
	MaxAssuranceWidthNs    int64
}

// PropagateAnchor evaluates the absolute assurance interval at raw instant r (Section 6.2).
func PropagateAnchor(
	anchor *AssuranceAnchor,
	r core.RawNanos,
	currentRawReadBound core.ErrorNs,
	continuityToken uint64,
	lowerDebt core.DurationNs,
	upperDebt core.DurationNs,
	maxAssuranceWidthNs int64,
) (core.TimeInterval, error) {
	if anchor == nil {
		return core.TimeInterval{}, errors.New("no anchor available")
	}
	if r < anchor.RawAnchor {
		return core.TimeInterval{}, ErrRawEarlierThanAnchor
	}
	if r > anchor.ValidUntilRaw {
		return core.TimeInterval{}, ErrAnchorExpired
	}
	if continuityToken != anchor.ContinuityToken {
		return core.TimeInterval{}, ErrContinuityTokenMismatch
	}

	if anchor.RawScaleLower <= 0 || anchor.RawScaleUpper < anchor.RawScaleLower {
		return core.TimeInterval{}, core.ErrInvalidRange
	}
	dr := r - anchor.RawAnchor
	if uint64(anchor.RawReadBound) > math.MaxUint64-uint64(currentRawReadBound) {
		return core.TimeInterval{}, core.ErrOverflow
	}
	rawDeltaError := uint64(anchor.RawReadBound) + uint64(currentRawReadBound)
	if uint64(dr) > math.MaxInt64 || rawDeltaError > uint64(math.MaxInt64)-uint64(dr) {
		return core.TimeInterval{}, core.ErrOverflow
	}

	var drLo uint64
	if uint64(dr) > rawDeltaError {
		drLo = uint64(dr) - rawDeltaError
	}
	drHi := uint64(dr) + rawDeltaError

	advanceLo, err := core.MulScaleDurationFloor(anchor.RawScaleLower, core.RawNanos(drLo))
	if err != nil {
		return core.TimeInterval{}, err
	}
	advanceHi, err := core.MulScaleDurationCeil(anchor.RawScaleUpper, core.RawNanos(drHi))
	if err != nil {
		return core.TimeInterval{}, err
	}

	L, err := checkedInstantAdd(anchor.LowerAtAnchor, advanceLo, int64(lowerDebt))
	if err != nil {
		return core.TimeInterval{}, err
	}
	U, err := checkedInstantAdd(anchor.UpperAtAnchor, advanceHi, int64(upperDebt))
	if err != nil {
		return core.TimeInterval{}, err
	}

	if L > U {
		return core.TimeInterval{}, core.ErrInvalidRange
	}

	if L < 0 && U > core.GstInstant(math.MaxInt64)+L {
		return core.TimeInterval{}, core.ErrOverflow
	}
	width := int64(U - L)
	if maxAssuranceWidthNs > 0 && width >= maxAssuranceWidthNs {
		return core.TimeInterval{}, ErrBoundTooWide
	}

	return core.TimeInterval{
		Earliest: L,
		Latest:   U,
	}, nil
}

func checkedInstantAdd(base core.GstInstant, terms ...int64) (core.GstInstant, error) {
	v := int64(base)
	for _, d := range terms {
		if willTermOverflow(v, d) {
			return 0, core.ErrOverflow
		}
		v += d
	}
	return core.GstInstant(v), nil
}

func willTermOverflow(v, d int64) bool {
	if d > 0 {
		return v > math.MaxInt64-d
	}
	if d < 0 {
		return v < math.MinInt64-d
	}
	return false
}

// AssuranceClock manages the synchronization state machine and certified bounds.
type AssuranceClock struct {
	mu    sync.RWMutex
	state AssuranceState
}

// NewAssuranceClock creates an unanchored AssuranceClock.
func NewAssuranceClock(maxAssuranceWidthNs int64) *AssuranceClock {
	if maxAssuranceWidthNs <= 0 {
		maxAssuranceWidthNs = core.MaxAssuranceWidthDefault
	}
	var epochID [16]byte
	_, _ = rand.Read(epochID[:])

	return &AssuranceClock{
		state: AssuranceState{
			Status:              core.StatusUnanchored,
			Reason:              core.ReasonNone,
			Generation:          1,
			AssuranceEpochID:    epochID,
			MaxAssuranceWidthNs: maxAssuranceWidthNs,
			PastWatermark:       -1 << 62,
		},
	}
}

// Snapshot returns a copy of the current assurance state.
func (ac *AssuranceClock) Snapshot() AssuranceState {
	ac.mu.RLock()
	defer ac.mu.RUnlock()
	return ac.state
}

// ValidateHull checks ordering and the full width without signed subtraction.
// Call before changing any assurance, estimate, or publication state.
func ValidateHull(hull core.TimeInterval, maxWidthNs int64) error {
	if hull.Earliest > hull.Latest || maxWidthNs <= 0 {
		return core.ErrInvalidRange
	}
	if uint64(hull.Latest)-uint64(hull.Earliest) >= uint64(maxWidthNs) {
		return ErrBoundTooWide
	}
	return nil
}

// ProcessFullRound integrates a newly computed consensus hull with existing state (Sections 6.4, 6.7).
func (ac *AssuranceClock) ProcessFullRound(
	rSel core.RawNanos,
	hull core.TimeInterval,
	rawReadBound core.ErrorNs,
	scaleLower core.RateScale,
	scaleUpper core.RateScale,
	continuityToken uint64,
	validUntilRaw core.RawNanos,
	faultBudget uint32,
	eligibleDomains uint32,
	coverageThreshold uint32,
	componentCount uint32,
	leapHistoryID [32]byte,
	configID [32]byte,
) (*core.TimeInterval, error) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	if err := ValidateHull(hull, ac.state.MaxAssuranceWidthNs); err != nil {
		return nil, err
	}
	publishedInterval := hull

	// If an existing valid anchor exists on matching domains, compute intersection
	if ac.state.Status == core.StatusSynced || ac.state.Status == core.StatusHoldover {
		if isAnchorMatching(ac.state.Anchor, leapHistoryID, configID, continuityToken, scaleLower, scaleUpper) {

			oldPropagated, err := PropagateAnchor(
				ac.state.Anchor,
				rSel,
				rawReadBound,
				continuityToken,
				ac.state.LowerDebt,
				ac.state.UpperDebt,
				ac.state.MaxAssuranceWidthNs,
			)

			if err == nil {
				// Intersect propagated old bound with new hull (Section 6.4)
				intEarliest := publishedInterval.Earliest
				if oldPropagated.Earliest > intEarliest {
					intEarliest = oldPropagated.Earliest
				}
				intLatest := publishedInterval.Latest
				if oldPropagated.Latest < intLatest {
					intLatest = oldPropagated.Latest
				}

				if intEarliest > intLatest {
					// ASSURANCE_CONFLICT: empty intersection
					ac.state.Status = core.StatusDesync
					ac.state.Reason = core.ReasonAssuranceConflict
					ac.state.Anchor = nil
					ac.state.Generation++
					return nil, ErrAssuranceConflict
				}

				publishedInterval = core.TimeInterval{
					Earliest: intEarliest,
					Latest:   intLatest,
				}
			}
		}
	}

	// Update certified past watermark: max(previousWatermark, L) (Section 6.9)
	if publishedInterval.Earliest > ac.state.PastWatermark {
		ac.state.PastWatermark = publishedInterval.Earliest
	}

	newAnchor := &AssuranceAnchor{
		RawAnchor:         rSel,
		LowerAtAnchor:     publishedInterval.Earliest,
		UpperAtAnchor:     publishedInterval.Latest,
		RawScaleLower:     scaleLower,
		RawScaleUpper:     scaleUpper,
		RawReadBound:      rawReadBound,
		SourceFaultBudget: faultBudget,
		EligibleDomains:   eligibleDomains,
		CoverageThreshold: coverageThreshold,
		ComponentCount:    componentCount,
		LeapHistoryID:     leapHistoryID,
		ConfigID:          configID,
		Generation:        ac.state.Generation + 1,
		CreatedRaw:        rSel,
		ValidUntilRaw:     validUntilRaw,
		ContinuityToken:   continuityToken,
	}

	ac.state.Anchor = newAnchor
	ac.state.LowerDebt = 0
	ac.state.UpperDebt = 0
	ac.state.Status = core.StatusSynced
	ac.state.Reason = core.ReasonNone
	ac.state.LastSuccessfulRoundRaw = rSel
	ac.state.Generation++

	return &publishedInterval, nil
}

// TransitionToHoldover marks the transition from SYNCED to HOLDOVER when round unavailable.
func (ac *AssuranceClock) TransitionToHoldover(reason core.StatusReason) {
	ac.BeginHoldover(reason, 0)
}

// BeginHoldover limits continued use from the last successful selection, never
// from a repeated failure. A cap may shorten, but never extend, anchor validity.
func (ac *AssuranceClock) BeginHoldover(reason core.StatusReason, maxAge core.RawNanos) {
	ac.mu.Lock()
	defer ac.mu.Unlock()
	if ac.state.Status != core.StatusSynced && ac.state.Status != core.StatusHoldover {
		return
	}
	if maxAge > 0 && uint64(ac.state.LastSuccessfulRoundRaw) <= math.MaxUint64-uint64(maxAge) {
		limit := ac.state.LastSuccessfulRoundRaw + maxAge
		if ac.state.Anchor != nil && limit < ac.state.Anchor.ValidUntilRaw {
			anchor := *ac.state.Anchor // published snapshots must remain immutable
			anchor.ValidUntilRaw = limit
			ac.state.Anchor = &anchor
		}
	}
	ac.state.Status = core.StatusHoldover
	ac.state.Reason = reason
	ac.state.Generation++
}

// TransitionToDesync marks unrecoverable fault or cap expiration.
func (ac *AssuranceClock) TransitionToDesync(reason core.StatusReason) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	ac.state.Status = core.StatusDesync
	ac.state.Reason = reason
	ac.state.Anchor = nil
	ac.state.Generation++
}

// EvaluateAt evaluates the current assured interval at raw reading r.
func (ac *AssuranceClock) EvaluateAt(
	r core.RawNanos,
	currentRawReadBound core.ErrorNs,
	continuityToken uint64,
) (core.TimeInterval, core.SyncStatus, core.StatusReason, error) {
	ac.mu.RLock()
	defer ac.mu.RUnlock()

	if ac.state.Status == core.StatusUnanchored || ac.state.Status == core.StatusDesync {
		return core.TimeInterval{}, ac.state.Status, ac.state.Reason, nil
	}

	interval, err := PropagateAnchor(
		ac.state.Anchor,
		r,
		currentRawReadBound,
		continuityToken,
		ac.state.LowerDebt,
		ac.state.UpperDebt,
		ac.state.MaxAssuranceWidthNs,
	)
	if err != nil {
		if errors.Is(err, ErrAnchorExpired) || errors.Is(err, ErrBoundTooWide) {
			return core.TimeInterval{}, core.StatusDesync, core.ReasonBoundTooOld, err
		}
		if errors.Is(err, ErrContinuityTokenMismatch) || errors.Is(err, ErrRawEarlierThanAnchor) {
			return core.TimeInterval{}, core.StatusDesync, core.ReasonRawDiscontinuity, err
		}
		return core.TimeInterval{}, core.StatusDesync, core.ReasonArithmeticOverflow, err
	}

	return interval, ac.state.Status, ac.state.Reason, nil
}

func isAnchorMatching(anchor *AssuranceAnchor, leapID, cfgID [32]byte, token uint64, low, upp core.RateScale) bool {
	if anchor == nil {
		return false
	}
	if anchor.LeapHistoryID != leapID || anchor.ConfigID != cfgID {
		return false
	}
	if anchor.ContinuityToken != token {
		return false
	}
	return anchor.RawScaleLower == low && anchor.RawScaleUpper == upp
}
