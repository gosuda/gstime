package gstime_test

import (
	"math/rand"
	"testing"
	"time"

	"gosuda.org/gstime"
	"gosuda.org/gstime/clock"
	"gosuda.org/gstime/core"
	"gosuda.org/gstime/source"
)

// DSTSimulator implements Deterministic Simulation Testing (DST) for clock stability,
// PRNG-seeded fault injection (VM snapshot rollbacks, hypervisor freezes, OS clock shaking),
// and strict mathematical invariant validation at every discrete step.
type DSTSimulator struct {
	seed             int64
	rng              *rand.Rand
	raw              *clock.SimulatedRawClock
	svc              *gstime.ClockService
	physicalTime     gstime.GstInstant // ground truth physical time (universe time)
	lastPublicCenter gstime.GstInstant
	continuityToken  uint64
	configID         [32]byte
	leapHistoryID    [32]byte
}

func newDSTSimulator(seed int64) *DSTSimulator {
	rng := rand.New(rand.NewSource(seed))
	startRaw := core.RawNanos(10_000_000_000)
	startPhysical := gstime.GstInstant(1_700_000_000 * 1_000_000_000)

	rawClock := clock.NewSimulatedRawClock(startRaw)
	// Set hardware oscillator envelope to +/- 200 ppm (matching SystemRawClock)
	lowScale := core.RateScale(core.OneQ48 + core.RateFromPpmLower(-200.0))
	uppScale := core.RateScale(core.OneQ48 + core.RateFromPpmUpper(200.0))
	rawClock.SetScaleEnvelope(lowScale, uppScale)

	lh, _ := core.NewLeapHistory(10, nil)
	var cfgID [32]byte
	cfgID[0] = 0xAA

	svc := gstime.NewClockService(rawClock, lh, cfgID, 32_000_000_000)
	_ = svc.InitializeEstimate(startRaw, startPhysical, 0)

	sim := &DSTSimulator{
		seed:             seed,
		rng:              rng,
		raw:              rawClock,
		svc:              svc,
		physicalTime:     startPhysical,
		lastPublicCenter: 0,
		continuityToken:  1,
		configID:         cfgID,
		leapHistoryID:    lh.ID,
	}

	// Publish initial valid round
	sim.publishConsensusRound(false)
	return sim
}

func (sim *DSTSimulator) publishConsensusRound(byzantine bool) {
	nowRaw := sim.raw.Read().Raw
	validUntil := nowRaw + 10_000_000_000 // 10s horizon

	var intervals []core.TimeInterval
	// 3 honest sources enclosing physical time
	for i := 0; i < 3; i++ {
		bound := int64(10_000_000 + sim.rng.Int63n(20_000_000)) // 10ms - 30ms uncertainty
		centerOffset := int64(-5_000_000 + sim.rng.Int63n(10_000_000))
		intervals = append(intervals, core.TimeInterval{
			Earliest: sim.physicalTime + gstime.GstInstant(centerOffset) - gstime.GstInstant(bound),
			Latest:   sim.physicalTime + gstime.GstInstant(centerOffset) + gstime.GstInstant(bound),
		})
	}

	if byzantine {
		// 1 Byzantine faulty source far outside ground truth
		badOffset := gstime.GstInstant(3600 * 1_000_000_000) // 1 hour ahead
		intervals = append(intervals, core.TimeInterval{
			Earliest: sim.physicalTime + badOffset - 1000,
			Latest:   sim.physicalTime + badOffset + 1000,
		})
	} else {
		// Honest 4th source
		bound := int64(15_000_000 + sim.rng.Int63n(10_000_000))
		intervals = append(intervals, core.TimeInterval{
			Earliest: sim.physicalTime - gstime.GstInstant(bound),
			Latest:   sim.physicalTime + gstime.GstInstant(bound),
		})
	}

	consensus, err := source.ComputeAssuranceConsensus(intervals, 1, 3, 2, 0, 0)
	if err == nil {
		center := (consensus.Hull.Earliest + consensus.Hull.Latest) / 2
		_ = sim.svc.InitializeEstimate(nowRaw, center, 0)
		_ = sim.svc.PublishAssuranceRound(
			nowRaw,
			consensus.Hull,
			1,
			uint32(len(intervals)),
			2,
			uint32(len(consensus.Components)),
			validUntil,
		)
	}
}

// Step performs one discrete deterministic simulation tick with random fault injection.
func (sim *DSTSimulator) Step(t *testing.T, step int) {
	// 1. Physical ground truth time advances by 20ms..100ms
	dtTrue := time.Duration(20+sim.rng.Intn(80)) * time.Millisecond
	sim.physicalTime += gstime.GstInstant(dtTrue.Nanoseconds())

	// 2. Hardware raw clock advances under bounded oscillator drift (+/- 180 ppm)
	driftPpm := -180.0 + sim.rng.Float64()*360.0
	rawDelta := int64(float64(dtTrue.Nanoseconds()) * (1.0 + driftPpm/1e6))
	if rawDelta < 1 {
		rawDelta = 1
	}
	sim.raw.Advance(core.RawNanos(rawDelta))

	// 3. PRNG-seeded Fault Injection
	faultRoll := sim.rng.Float64()
	switch {
	case faultRoll < 0.05:
		// Fault A: VM Snapshot Rollback (Time reverses by 5s..30s, continuity token changes)
		rollbackNanos := core.RawNanos((5 + sim.rng.Intn(25)) * 1_000_000_000)
		sim.continuityToken++
		sim.raw.Rewind(rollbackNanos)
		sim.raw.SetContinuityToken(sim.continuityToken)

		// Immediate Invariant Check: Token mismatch must cause instant fail-fast DESYNC
		nowPostRollback := sim.svc.Now()
		if nowPostRollback.Status != gstime.StatusDesync {
			t.Fatalf("[DST Seed %d, Step %d] Invariant Violation: Expected immediate StatusDesync on rollback token change, got %s",
				sim.seed, step, nowPostRollback.Status)
		}

	case faultRoll < 0.10:
		// Fault B: VM Suspend / Pause / Migration (VM freezes for 10s..60s while universe advances)
		pauseNanos := (10 + sim.rng.Intn(50)) * 1_000_000_000
		sim.physicalTime += gstime.GstInstant(pauseNanos)
		sim.raw.Advance(core.RawNanos(pauseNanos))

	case faultRoll < 0.20:
		// Fault C: OS Clock Shake (rapid thermal wander / interrupt frequency fluctuation)
		shakePpm := -180.0 + sim.rng.Float64()*360.0
		shakeRawDelta := int64(float64(dtTrue.Nanoseconds()) * (shakePpm / 1e6))
		if shakeRawDelta > 0 {
			sim.raw.Advance(core.RawNanos(shakeRawDelta))
		}
	}

	// 4. Periodically publish fresh consensus round or reprobe
	if step%10 == 0 {
		byzantineFaulty := sim.rng.Float64() < 0.5
		sim.publishConsensusRound(byzantineFaulty)
	}

	// 5. Invariant Checking
	pub := sim.svc.NowPublicAssured()
	now := sim.svc.Now()

	// Invariant 1: Public Clock Strict Monotonicity (P_{k+1} >= P_k)
	if sim.lastPublicCenter > 0 && pub.Center < sim.lastPublicCenter {
		t.Fatalf("[DST Seed %d, Step %d] Invariant Violation: PublicClock regressed! P_k=%d < P_{k-1}=%d",
			sim.seed, step, pub.Center, sim.lastPublicCenter)
	}
	sim.lastPublicCenter = pub.Center

	// Invariant 2: Public Epsilon Safety
	// When status is SYNCED, true physical time MUST be strictly contained in [Center - Epsilon, Center + Epsilon]
	if pub.Status == gstime.StatusSynced && pub.PublicSymmetricEpsilon < gstime.ErrorInfinity {
		eps := gstime.GstInstant(pub.PublicSymmetricEpsilon)
		low := pub.Center - eps
		upp := pub.Center + eps
		if sim.physicalTime < low || sim.physicalTime > upp {
			t.Fatalf("[DST Seed %d, Step %d] Invariant Violation: True time %d outside public epsilon [%d, %d] (Center: %d, Epsilon: ±%d)",
				sim.seed, step, sim.physicalTime, low, upp, pub.Center, pub.PublicSymmetricEpsilon)
		}
	}

	// Invariant 3: GSTimeAssure Interval Safety
	// When status is SYNCED and interval is non-nil, true physical time MUST be within [Earliest, Latest]
	if now.Status == gstime.StatusSynced && now.Interval != nil {
		if sim.physicalTime < now.Interval.Earliest || sim.physicalTime > now.Interval.Latest {
			t.Fatalf("[DST Seed %d, Step %d] Invariant Violation: True time %d outside assured interval [%d, %d]",
				sim.seed, step, sim.physicalTime, now.Interval.Earliest, now.Interval.Latest)
		}
	}
}

// TestDST_ClockStabilityAndVMSnapshotJumps executes Deterministic Simulation Testing (DST)
// across multiple seeds, proving public clock stability under OS clock shaking and VM snapshot jumps.
func TestDST_ClockStabilityAndVMSnapshotJumps(t *testing.T) {
	seeds := []int64{42, 1337, 2026, 888888, 99999999}
	totalStepsPerSeed := 300

	for _, seed := range seeds {
		t.Run("Seed", func(t *testing.T) {
			sim := newDSTSimulator(seed)
			for step := 1; step <= totalStepsPerSeed; step++ {
				sim.Step(t, step)
			}
		})
	}
}

// TestDST_Reproducibility proves that running DST with the exact same seed produces
// 100% bitwise-identical public clock traces across hundreds of steps.
func TestDST_Reproducibility(t *testing.T) {
	const testSeed = int64(777)
	const steps = 200

	runSim := func() []gstime.GstInstant {
		sim := newDSTSimulator(testSeed)
		trace := make([]gstime.GstInstant, steps)
		for i := 0; i < steps; i++ {
			sim.Step(t, i+1)
			trace[i] = sim.svc.NowPublicAssured().Center
		}
		return trace
	}

	trace1 := runSim()
	trace2 := runSim()

	for i := range trace1 {
		if trace1[i] != trace2[i] {
			t.Fatalf("DST Reproducibility Failure at step %d: run1=%d != run2=%d",
				i+1, trace1[i], trace2[i])
		}
	}
}
