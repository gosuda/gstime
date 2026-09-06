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
	epochID        [16]byte
}

// NewClockService creates and initializes a ClockService.
func NewClockService(
	raw clock.RawClock,
	lh *LeapHistory,
	cfgID [32]byte,
	maxAssuranceWidthNs int64,
) *ClockService {
	raw = clock.NewContinuityGuard(raw)
	scaleLow, scaleUpp := raw.ScaleEnvelope()
	estClk := clock.NewEstimateClock()
	assClk := assurance.NewAssuranceClock(maxAssuranceWidthNs)
	pubClk := clock.NewPublicClock()

	initialSnap := &publish.Snapshot{
		EstimateMapping:  estClk.Snapshot(),
		Assurance:        assClk.Snapshot(),
		RawScaleLower:    scaleLow,
		RawScaleUpper:    scaleUpp,
		ContinuityToken:  1,
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
		epochID:        assClk.Snapshot().AssuranceEpochID,
	}
}

// Now performs a single raw clock read and evaluates the assured snapshot (Section 7.2).
func (s *ClockService) Now() AssuredNow {
	snap := s.publisher.Acquire()
	reading := s.rawClock.Read()

	est, errEst := snap.EstimateMapping.Evaluate(reading.Raw)
	hasEst := (errEst == nil)

	if snap.Assurance.Status == StatusSynced || snap.Assurance.Status == StatusHoldover {
		inv, err := s.propagateSnapshot(snap, reading)

		if err == nil {
			d1 := math.Abs(float64(est - inv.Earliest))
			d2 := math.Abs(float64(inv.Latest - est))
			symEps := ErrorNs(math.Max(d1, d2))

			return AssuredNow{
				Interval:            inv,
				HasInterval:         true,
				Estimate:            est,
				HasEstimate:         hasEst,
				SymmetricEpsilon:    symEps,
				HasSymmetricEpsilon: true,
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
func (s *ClockService) NowUtc(expectedLeapID [32]byte) (earliest, latest UtcLabel, est *UtcLabel, status SyncStatus, err error) {
	if expectedLeapID != s.leapHistory.ID {
		return UtcLabel{}, UtcLabel{}, nil, StatusDesync, ErrLeapHistoryMismatch
	}

	now := s.Now()
	if now.HasEstimate {
		e := s.leapHistory.GstInstantToUtc(now.Estimate)
		est = &e
	}

	if !now.HasInterval {
		return UtcLabel{}, UtcLabel{}, est, now.Status, nil
	}

	earliest = s.leapHistory.GstInstantToUtc(now.Interval.Earliest)
	latest = s.leapHistory.GstInstantToUtc(now.Interval.Latest)
	return earliest, latest, est, now.Status, nil
}

// NowUnixProjection returns the POSIX projection with leap ambiguity flags (Section 7.4).
func (s *ClockService) NowUnixProjection() (UnixProjection, error) {
	now := s.Now()
	if !now.HasEstimate {
		return UnixProjection{}, errors.New("estimate unavailable")
	}
	return s.leapHistory.GstInstantToUnixProjection(now.Estimate), nil
}

// NowPublicAssured returns the public clock's assured presentation view (Section 7.8).
func (s *ClockService) NowPublicAssured() PublicAssuredNow {
	snap := s.publisher.Acquire()
	reading := s.rawClock.Read()

	est, _ := snap.EstimateMapping.Evaluate(reading.Raw)
	pCenter, _ := s.publicClock.ReadPublicInstant(est, reading.Raw)

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

	if _, err := s.propagateSnapshot(snap, s.rawClock.Read()); err != nil {
		return 0, StatusDesync, ErrDesynchronized
	}
	return snap.Assurance.PastWatermark, snap.Assurance.Status, nil
}

// CommitWait blocks until certified PastWatermark is strictly greater than commitTs (Section 7.7).
func (s *ClockService) CommitWait(
	ctx context.Context,
	commitTs GstInstant,
	epochID [16]byte,
	leapID [32]byte,
	cfgID [32]byte,
) error {
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

		wm, status, err := s.PastWatermark(epochID, leapID, cfgID)
		if err != nil {
			return err
		}
		if status == StatusDesync {
			return ErrDesynchronized
		}

		if wm > commitTs {
			return nil // Guaranteed committed strictly after commitTs
		}

		timer.Reset(100 * time.Microsecond)
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

// WaitSync blocks until the service reaches StatusSynced, or ctx is done.
func (s *ClockService) WaitSync(ctx context.Context) error {
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
	consensus *source.AssuranceConsensusResult, validUntil RawNanos) error {
	s.writerMu.Lock()
	defer s.writerMu.Unlock()
	if err := assurance.ValidateHull(consensus.Hull, s.assuranceClock.Snapshot().MaxAssuranceWidthNs); err != nil {
		return err
	}
	current := s.rawClock.Read()
	currentLow, currentUpp := s.rawClock.ScaleEnvelope()
	if current.Raw < selection.Raw || current.ContinuityToken != selection.ContinuityToken ||
		current.BackendGeneration != selection.BackendGeneration || currentLow != low || currentUpp != upp {
		s.assuranceClock.TransitionToDesync(ReasonRawDiscontinuity)
		return errors.Join(assurance.ErrContinuityTokenMismatch, s.publishCurrent())
	}
	_, err := s.assuranceClock.ProcessFullRound(selection.Raw, consensus.Hull, selection.ReadBound,
		low, upp, selection.ContinuityToken, validUntil, uint32(consensus.FaultBudget), uint32(consensus.EligibleDomains),
		uint32(consensus.ThresholdK), uint32(len(consensus.Components)), s.leapHistory.ID, s.configID)
	if err != nil {
		return errors.Join(err, s.publishCurrent())
	}
	// ValidateHull bounds the unsigned span before conversion, including intervals
	// crossing zero or adjacent to either signed endpoint.
	span := uint64(consensus.Hull.Latest) - uint64(consensus.Hull.Earliest)
	center := consensus.Hull.Earliest + core.GstInstant(span/2)
	s.estimateClock.InitializeAnchors(selection.Raw, center, 0)
	return s.publishCurrent()
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
	s.estimateClock.InitializeAnchors(raw0, target, baseRate)
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

	currSnap := s.publisher.Acquire()
	gen := currSnap.Generation + 1

	nextSnap := &publish.Snapshot{
		EstimateMapping:  s.estimateClock.Snapshot(),
		Assurance:        s.assuranceClock.Snapshot(),
		RawScaleLower:    scaleLow,
		RawScaleUpper:    scaleUpp,
		ContinuityToken:  reading.ContinuityToken,
		LeapHistoryID:    s.leapHistory.ID,
		ConfigID:         s.configID,
		AssuranceEpochID: s.epochID,
		Generation:       gen,
	}

	return s.publisher.Publish(nextSnap)
}
