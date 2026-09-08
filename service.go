package gstime

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"gosuda.org/gstime/assurance"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/publish"
	"gosuda.org/gstime/source"
)

var errEstimateUnavailable = errors.New("estimate unavailable")

// ClockService coordinates acquisition, discipline, and assurance publishing (Section 1.3).
type ClockService struct {
	writerMu       sync.Mutex
	rawClock       clock.RawClock
	estimateClock  *clock.EstimateClock
	assuranceClock *assurance.AssuranceClock
	publicClock    *clock.PublicClock
	publisher      *publish.Publisher
	leapHistory    *LeapHistory
	configID       [32]byte
}

// NewClockService creates and initializes a ClockService.
func NewClockService(
	raw clock.RawClock,
	lh *LeapHistory,
	cfgID [32]byte,
	maxAssuranceWidthNs int64,
) *ClockService {
	raw = clock.NewContinuityGuard(raw)
	reading := raw.Read()
	scaleLow, scaleUpp := raw.ScaleEnvelope()
	estClk := clock.NewEstimateClock()
	assClk := assurance.NewAssuranceClock(maxAssuranceWidthNs)
	pubClk := clock.NewPublicClock()

	initialSnap := &publish.Snapshot{
		EstimateMapping:  estClk.Snapshot(),
		Assurance:        assClk.Snapshot(),
		RawScaleLower:    scaleLow,
		RawScaleUpper:    scaleUpp,
		ContinuityToken:  reading.ContinuityToken,
		LeapHistoryID:    lh.ID,
		ConfigID:         cfgID,
		AssuranceEpochID: assClk.Snapshot().AssuranceEpochID,
		Generation:       1,
	}

	publisher := publish.NewPublisher(initialSnap)

	return &ClockService{
		rawClock:       raw,
		estimateClock:  estClk,
		assuranceClock: assClk,
		publicClock:    pubClk,
		publisher:      publisher,
		leapHistory:    lh,
		configID:       cfgID,
	}
}

// Now performs a single raw clock read and evaluates the assured snapshot (Section 7.2).
func (s *ClockService) Now() AssuredNow {
	snap := s.publisher.Acquire()
	reading := s.rawClock.Read()
	if rawDomainChanged(snap, reading.ContinuityToken) {
		return AssuredNow{
			Status: StatusDesync, Reason: ReasonRawDiscontinuity,
			AssuranceGeneration: snap.Assurance.Generation, MappingGeneration: snap.EstimateMapping.MappingGeneration,
			AssuranceEpochID: snap.AssuranceEpochID, LeapHistoryID: snap.LeapHistoryID, ConfigID: snap.ConfigID,
		}
	}

	est, errEst := snap.EstimateMapping.Evaluate(reading.Raw)
	hasEst := (errEst == nil)

	if snap.Assurance.Status == StatusSynced || snap.Assurance.Status == StatusHoldover {
		inv, err := s.propagateSnapshot(snap, reading)

		if err == nil {
			symEps := core.ErrorInfinity
			if hasEst {
				symEps = core.SymmetricEpsilon(est, inv, 0)
			}

			return AssuredNow{
				Interval:            inv,
				HasInterval:         true,
				Estimate:            est,
				HasEstimate:         hasEst,
				SymmetricEpsilon:    symEps,
				HasSymmetricEpsilon: hasEst,
				Status:              snap.Assurance.Status,
				Reason:              snap.Assurance.Reason,
				AssuranceGeneration: snap.Assurance.Generation,
				MappingGeneration:   snap.EstimateMapping.MappingGeneration,
				AssuranceEpochID:    snap.AssuranceEpochID,
				LeapHistoryID:       snap.LeapHistoryID,
				ConfigID:            snap.ConfigID,
				FaultBudget:         snap.Assurance.Anchor.SourceFaultBudget,
				EligibleDomains:     snap.Assurance.Anchor.EligibleDomains,
				Age:                 DurationNs(reading.Raw - snap.Assurance.Anchor.CreatedRaw),
			}
		}

		// Propagation error: anchor expired, continuity mismatch, or bound too wide
		reason := mapPropagationError(err)
		return AssuredNow{
			Estimate:            est,
			HasEstimate:         hasEst,
			Status:              StatusDesync,
			Reason:              reason,
			AssuranceGeneration: snap.Assurance.Generation,
			MappingGeneration:   snap.EstimateMapping.MappingGeneration,
			AssuranceEpochID:    snap.AssuranceEpochID,
			LeapHistoryID:       snap.LeapHistoryID,
			ConfigID:            snap.ConfigID,
		}
	}

	// UNANCHORED or DESYNC: return no interval
	return AssuredNow{
		Estimate:            est,
		HasEstimate:         hasEst,
		Status:              snap.Assurance.Status,
		Reason:              snap.Assurance.Reason,
		AssuranceGeneration: snap.Assurance.Generation,
		MappingGeneration:   snap.EstimateMapping.MappingGeneration,
		AssuranceEpochID:    snap.AssuranceEpochID,
		LeapHistoryID:       snap.LeapHistoryID,
		ConfigID:            snap.ConfigID,
	}
}

// NowUtc converts Now endpoints and estimate to UtcLabel using the snapshot leap history (Section 7.3).
func (s *ClockService) NowUtc(expectedLeapID [32]byte) (AssuredUtcNow, error) {
	if expectedLeapID != s.leapHistory.ID {
		return AssuredUtcNow{Status: StatusDesync, Reason: ReasonLeapHistoryMismatch}, ErrLeapHistoryMismatch
	}
	now := s.Now()
	utc := AssuredUtcNow{
		HasInterval: now.HasInterval, HasEstimate: now.HasEstimate,
		Status: now.Status, Reason: now.Reason,
		AssuranceEpochID: now.AssuranceEpochID, LeapHistoryID: now.LeapHistoryID, ConfigID: now.ConfigID,
	}
	if now.HasEstimate {
		utc.Estimate = s.leapHistory.GstInstantToUtc(now.Estimate)
	}
	if now.HasInterval {
		utc.Earliest = s.leapHistory.GstInstantToUtc(now.Interval.Earliest)
		utc.Latest = s.leapHistory.GstInstantToUtc(now.Interval.Latest)
	}
	return utc, nil
}

// NowUnixProjection returns the POSIX projection with leap ambiguity flags (Section 7.4).
func (s *ClockService) NowUnixProjection() (UnixProjection, error) {
	now := s.Now()
	if !now.HasEstimate {
		return UnixProjection{}, errEstimateUnavailable
	}
	return s.leapHistory.GstInstantToUnixProjection(now.Estimate), nil
}

// NowPublicAssured returns the public clock's assured presentation view (Section 7.8).
func (s *ClockService) NowPublicAssured() PublicAssuredNow {
	snap := s.publisher.Acquire()
	reading := s.rawClock.Read()
	if rawDomainChanged(snap, reading.ContinuityToken) {
		return PublicAssuredNow{Center: s.publicClock.LastPublicInstant(), PublicSymmetricEpsilon: ErrorInfinity, Status: StatusDesync, Reason: ReasonRawDiscontinuity}
	}

	est, err := snap.EstimateMapping.Evaluate(reading.Raw)
	if err != nil {
		status, reason := StatusDesync, ReasonArithmeticOverflow
		if !snap.EstimateMapping.Anchored && snap.Assurance.Status == StatusUnanchored {
			status, reason = snap.Assurance.Status, snap.Assurance.Reason
		}
		return PublicAssuredNow{Center: s.publicClock.LastPublicInstant(), PublicSymmetricEpsilon: ErrorInfinity, Status: status, Reason: reason}
	}
	pCenter, _, err := s.publicClock.ReadPublicInstant(est, reading.Raw)
	if err != nil {
		return PublicAssuredNow{Center: pCenter, PublicSymmetricEpsilon: ErrorInfinity, Status: StatusDesync, Reason: ReasonArithmeticOverflow}
	}

	if snap.Assurance.Status == StatusSynced || snap.Assurance.Status == StatusHoldover {
		inv, err := s.propagateSnapshot(snap, reading)

		if err == nil {
			pubEps := clock.ComputePublicSymmetricEpsilon(pCenter, &inv, reading.ReadBound)
			if pubEps >= PublicEpsilonCapDefault {
				return PublicAssuredNow{
					Center:                 pCenter,
					PublicSymmetricEpsilon: pubEps,
					Status:                 StatusDesync,
					Reason:                 ReasonPublicBoundTooWide,
				}
			}

			return PublicAssuredNow{
				Center:                 pCenter,
				Interval:               inv,
				HasInterval:            true,
				PublicSymmetricEpsilon: pubEps,
				Status:                 snap.Assurance.Status,
				Reason:                 snap.Assurance.Reason,
			}
		}

		return PublicAssuredNow{
			Center:                 pCenter,
			PublicSymmetricEpsilon: ErrorInfinity,
			Status:                 StatusDesync,
			Reason:                 mapPropagationError(err),
		}
	}

	return PublicAssuredNow{
		Center:                 pCenter,
		PublicSymmetricEpsilon: ErrorInfinity,
		Status:                 snap.Assurance.Status,
		Reason:                 snap.Assurance.Reason,
	}
}

func (s *ClockService) propagateSnapshot(snap *publish.Snapshot, reading clock.RawReading) (core.TimeInterval, error) {
	low, upp := s.rawClock.ScaleEnvelope()
	if snap.Assurance.Anchor != nil && (low != snap.Assurance.Anchor.RawScaleLower || upp != snap.Assurance.Anchor.RawScaleUpper) {
		return core.TimeInterval{}, assurance.ErrContinuityTokenMismatch
	}
	return assurance.PropagateAnchor(snap.Assurance.Anchor, reading.Raw, reading.ReadBound,
		reading.ContinuityToken, snap.Assurance.LowerDebt, snap.Assurance.UpperDebt, snap.Assurance.MaxAssuranceWidthNs)
}

func (s *ClockService) markRoundFailure(maxHoldoverAge int64) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	snap := s.publisher.Acquire()
	if snap.Assurance.Status == StatusUnanchored || snap.Assurance.Status == StatusDesync {
		return nil
	}
	if _, err := s.propagateSnapshot(snap, s.rawClock.Read()); err != nil {
		s.assuranceClock.TransitionToDesync(mapPropagationError(err))
	} else {
		s.assuranceClock.BeginHoldover(core.ReasonInsufficientDomains, core.RawNanos(maxHoldoverAge))
		r := s.rawClock.Read()
		_, status, reason, _ := s.assuranceClock.EvaluateAt(r.Raw, r.ReadBound, r.ContinuityToken)
		if status == StatusDesync {
			s.assuranceClock.TransitionToDesync(reason)
		}
	}
	return s.publishCurrent()
}

func mapPropagationError(err error) StatusReason {
	if errors.Is(err, assurance.ErrBoundTooWide) {
		return ReasonBoundTooWide
	}
	if errors.Is(err, assurance.ErrContinuityTokenMismatch) || errors.Is(err, assurance.ErrRawEarlierThanAnchor) {
		return ReasonRawDiscontinuity
	}
	if errors.Is(err, core.ErrOverflow) {
		return ReasonArithmeticOverflow
	}
	return ReasonBoundTooOld
}

// SetSmearPlan configures the public clock's active smear plan (Section 5.14).
func (s *ClockService) SetSmearPlan(plan *clock.SmearPlan) {
	s.publicClock.SetSmearPlan(plan)
}

// PublicClock returns the underlying PublicClock instance.
func (s *ClockService) PublicClock() *clock.PublicClock {
	return s.publicClock
}

// After implements the tri-state decision operation for t (Section 7.5).
func (s *ClockService) After(t GstInstant) (Decision, SyncStatus, StatusReason) {
	now := s.Now()
	if !now.HasInterval {
		return Unknown, now.Status, now.Reason
	}
	if now.Interval.Earliest > t {
		return CertainYes, now.Status, now.Reason
	}
	if now.Interval.Latest <= t {
		return CertainNo, now.Status, now.Reason
	}
	return Unknown, now.Status, now.Reason
}

// Before implements the tri-state decision operation for t (Section 7.5).
func (s *ClockService) Before(t GstInstant) (Decision, SyncStatus, StatusReason) {
	now := s.Now()
	if !now.HasInterval {
		return Unknown, now.Status, now.Reason
	}
	if now.Interval.Latest < t {
		return CertainYes, now.Status, now.Reason
	}
	if now.Interval.Earliest >= t {
		return CertainNo, now.Status, now.Reason
	}
	return Unknown, now.Status, now.Reason
}

// PastWatermark returns the certified monotonic lower watermark (Section 7.6).
func (s *ClockService) PastWatermark(
	epochID [16]byte,
	leapID [32]byte,
	cfgID [32]byte,
) (GstInstant, SyncStatus, error) {
	snap := s.publisher.Acquire()

	if snap.AssuranceEpochID != epochID {
		return 0, snap.Assurance.Status, ErrConfigurationMismatch
	}
	if snap.LeapHistoryID != leapID {
		return 0, snap.Assurance.Status, ErrLeapHistoryMismatch
	}
	if snap.ConfigID != cfgID {
		return 0, snap.Assurance.Status, ErrConfigurationMismatch
	}

	if snap.Assurance.Status == StatusUnanchored {
		return 0, snap.Assurance.Status, ErrUnanchored
	}
	if snap.Assurance.Status == StatusDesync {
		return 0, snap.Assurance.Status, ErrDesynchronized
	}

	interval, err := s.propagateSnapshot(snap, s.rawClock.Read())
	if err != nil {
		return 0, StatusDesync, ErrDesynchronized
	}
	return snap.Assurance.AdvanceWatermark(interval.Earliest), snap.Assurance.Status, nil
}

// CommitWait blocks until certified PastWatermark is strictly greater than commitTs (Section 7.7).
func (s *ClockService) CommitWait(
	ctx context.Context,
	commitTs GstInstant,
	epochID [16]byte,
	leapID [32]byte,
	cfgID [32]byte,
) error {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		if err := ctx.Err(); err != nil {
			return contextAPIError(err)
		}
		wm, _, err := s.PastWatermark(epochID, leapID, cfgID)
		if err != nil {
			return err
		}
		if wm > commitTs {
			return nil
		}
		if timer == nil {
			timer = time.NewTimer(100 * time.Microsecond)
		} else {
			timer.Reset(100 * time.Microsecond)
		}
		select {
		case <-ctx.Done():
			return contextAPIError(ctx.Err())
		case <-timer.C:
		}
	}
}

func contextAPIError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrDeadlineExceeded
	}
	return ErrCancelled
}

// WaitSync blocks until the service reaches StatusSynced, or ctx is done.
func (s *ClockService) WaitSync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return contextAPIError(err)
	}
	if s.Now().Status == StatusSynced {
		return nil
	}

	timer := time.NewTimer(0)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()

	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return ErrDeadlineExceeded
			}
			return ErrCancelled
		}

		if s.Now().Status == StatusSynced {
			return nil
		}

		timer.Reset(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrDeadlineExceeded
			}
			return ErrCancelled
		case <-timer.C:
		}
	}
}

// publishSyncRound preserves the query round's reference reading through the
// writer transaction. A discontinuity between selection and publication cannot
// relabel old evidence with the new clock's continuity token or scale.
func (s *ClockService) publishSyncRound(selection clock.RawReading, low, upp core.RateScale,
	consensus *source.AssuranceConsensusResult, rate core.RateFrac, validUntil RawNanos) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	if err := assurance.ValidateHull(consensus.Hull, s.assuranceClock.Snapshot().MaxAssuranceWidthNs); err != nil {
		return err
	}
	if consensus.ControlTarget < consensus.Hull.Earliest || consensus.ControlTarget > consensus.Hull.Latest {
		return core.ErrInvalidRange
	}
	current := s.rawClock.Read()
	currentLow, currentUpp := s.rawClock.ScaleEnvelope()
	sameDomain := selection.ContinuityToken != 0 && current.ContinuityToken == selection.ContinuityToken && current.BackendGeneration == selection.BackendGeneration
	sameScale := currentLow == low && currentUpp == upp
	if !sameDomain || !sameScale || current.Raw < selection.Raw {
		s.assuranceClock.TransitionToDesync(ReasonRawDiscontinuity)
		return errors.Join(assurance.ErrContinuityTokenMismatch, s.publishCurrent())
	}
	if validUntil < current.Raw {
		return assurance.ErrAnchorExpired
	}
	mapping := s.estimateClock.Snapshot()
	initialize := !mapping.Anchored || rawDomainChanged(s.publisher.Acquire(), selection.ContinuityToken)
	if mapping.MappingGeneration == math.MaxUint64 || initialize && mapping.StepGeneration == math.MaxUint64 {
		return core.ErrOverflow
	}
	scale, err := core.CheckedInstantAdd(core.GstInstant(core.OneQ48), int64(rate))
	if err != nil || scale <= 0 {
		return errors.Join(core.ErrInvalidRange, err)
	}
	var addedDebt core.PhaseNs
	if !initialize {
		if selection.Raw < mapping.RawAnchor {
			return core.ErrInvalidRange
		}
		value, err := mapping.Evaluate(selection.Raw)
		if err != nil {
			return err
		}
		desiredDebt, err := phaseDifference(consensus.ControlTarget, value)
		if err != nil {
			return err
		}
		remaining := int64(mapping.PhaseDebt) - mapping.SlewApplied(selection.Raw)
		overflowsLower := remaining > 0 && int64(desiredDebt) < math.MinInt64+remaining
		overflowsUpper := remaining < 0 && int64(desiredDebt) > math.MaxInt64+remaining
		if overflowsLower || overflowsUpper {
			return core.ErrOverflow
		}
		addedDebt = core.PhaseNs(int64(desiredDebt) - remaining)
	}
	_, err = s.assuranceClock.ProcessFullRound(selection.Raw, consensus.Hull, selection.ReadBound,
		low, upp, selection.ContinuityToken, validUntil, uint32(consensus.FaultBudget), uint32(consensus.EligibleDomains),
		uint32(consensus.ThresholdK), uint32(len(consensus.Components)), s.leapHistory.ID, s.configID)
	if err != nil {
		return errors.Join(err, s.publishCurrent())
	}
	if initialize {
		err = s.estimateClock.InitializeAnchors(selection.Raw, consensus.ControlTarget, rate)
	} else {
		_, err = s.estimateClock.ReanchorContinuity(selection.Raw, rate, addedDebt, clock.ChangeSlewReplan)
	}
	if err != nil {
		s.assuranceClock.TransitionToDesync(ReasonArithmeticOverflow)
		return errors.Join(err, s.publishCurrent())
	}
	if err := s.publishCurrent(); err != nil {
		return err
	}
	if s.publisher.Acquire().Assurance.Status != StatusSynced {
		return ErrRawFault
	}
	return nil
}

// PublishAssuranceRound updates internal state and publishes a new snapshot.
func (s *ClockService) PublishAssuranceRound(
	rSel RawNanos,
	hull TimeInterval,
	faultBudget uint32,
	eligibleDomains uint32,
	coverageThreshold uint32,
	componentCount uint32,
	validUntilRaw RawNanos,
) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	if err := assurance.ValidateHull(hull, s.assuranceClock.Snapshot().MaxAssuranceWidthNs); err != nil {
		return err
	}
	reading := s.rawClock.Read()
	scaleLow, scaleUpp := s.rawClock.ScaleEnvelope()
	if reading.ContinuityToken == 0 || rSel > reading.Raw {
		return assurance.ErrContinuityTokenMismatch
	}
	if validUntilRaw < reading.Raw {
		return assurance.ErrAnchorExpired
	}

	_, err := s.assuranceClock.ProcessFullRound(
		rSel,
		hull,
		reading.ReadBound,
		scaleLow,
		scaleUpp,
		reading.ContinuityToken,
		validUntilRaw,
		faultBudget,
		eligibleDomains,
		coverageThreshold,
		componentCount,
		s.leapHistory.ID,
		s.configID,
	)
	if err != nil {
		// Preserve both the transition and publication errors.
		return errors.Join(err, s.publishCurrent())
	}

	return s.publishCurrent()
}

// InitializeEstimate anchors the estimate clock.
func (s *ClockService) InitializeEstimate(raw0 RawNanos, target GstInstant, baseRate RateFrac) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	if err := s.estimateClock.InitializeAnchors(raw0, target, baseRate); err != nil {
		return err
	}
	return s.publishCurrent()
}

// Invalidate discards active assurance after an externally reported clock event.
// Stop outstanding synchronization work before calling it after resume/restore;
// only a new trusted round may restore SYNCED. Full-memory rollback recovery
// requires a fresh process or an external monotonic epoch, not this local guard.
func (s *ClockService) Invalidate() error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	s.assuranceClock.TransitionToDesync(ReasonRawDiscontinuity)
	return s.publishCurrent()
}

// publishCurrent is called with writerMu held, making generation allocation and
// state capture one serialized writer transaction. Readers retain atomic snapshots.
func (s *ClockService) publishCurrent() error {
	reading := s.rawClock.Read()
	scaleLow, scaleUpp := s.rawClock.ScaleEnvelope()
	assured := s.assuranceClock.Snapshot()
	if assured.Anchor != nil {
		sameDomain := reading.ContinuityToken != 0 && reading.ContinuityToken == assured.Anchor.ContinuityToken
		sameScale := scaleLow == assured.Anchor.RawScaleLower && scaleUpp == assured.Anchor.RawScaleUpper
		if !sameDomain || !sameScale {
			s.assuranceClock.TransitionToDesync(ReasonRawDiscontinuity)
			assured = s.assuranceClock.Snapshot()
		}
	}

	currSnap := s.publisher.Acquire()
	gen := currSnap.Generation + 1

	nextSnap := &publish.Snapshot{
		EstimateMapping:  s.estimateClock.Snapshot(),
		Assurance:        assured,
		RawScaleLower:    scaleLow,
		RawScaleUpper:    scaleUpp,
		ContinuityToken:  reading.ContinuityToken,
		LeapHistoryID:    s.leapHistory.ID,
		ConfigID:         s.configID,
		AssuranceEpochID: assured.AssuranceEpochID,
		Generation:       gen,
	}

	return s.publisher.Publish(nextSnap)
}

func rawDomainChanged(snapshot *publish.Snapshot, token uint64) bool {
	return token == 0 || token != snapshot.ContinuityToken || snapshot.Assurance.Reason == ReasonRawDiscontinuity
}
