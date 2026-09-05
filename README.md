# GSTime & GSTimeAssure 4.0

GSTime is a high-assurance, fault-tolerant time synchronization and certification engine in Go. It implements continuous SI nanosecond tracking, dual-track separation between statistical estimation and fault-tolerant interval certification, RFC 8915 Network Time Security (NTS), bounded trapezoidal leap smearing, and lock-free linearizable snapshot publication.

---

## Key Capabilities

- **Continuous SI Nanosecond Axis**: Monotonically advancing signed 64-bit nanosecond instants (`GstInstant`) anchored to GPS/TAI epochs, eliminating negative time jumps.
- **Dual-Track Separation**:
  - *Estimate Track*: High-frequency Allan-deviation-weighted regression, dynamic-programming runs test validation, and bounded slew rate adaptation.
  - *Assurance Track*: Exact $N-F$ coverage sweep consensus hull (Appendix B), absolute anchor holdover propagation, and certified lower watermark monotonicity.
- **RFC 8915 Network Time Security (NTS)**: NTS-KE TLS key export (`EXPORTER-network-time-security`), AEAD 15 (AES-SIV-CMAC-256) and AEAD 30 (AES-128-GCM-SIV), single-ownership cookie lifecycle with burst reserves.
- **Trapezoidal Leap Smear**: Configurable smear windows with exact piecewise polynomial replanning from arbitrary mid-smear states (Appendix C).
- **Strict Linearizable Concurrency**: Lock-free reads via `atomic.Pointer[Snapshot]`, generation counters, and guaranteed bitwise continuity across re-anchoring ($E_{\text{old}}(c) == E_{\text{new}}(c)$).
- **Distributed Transaction Support**: `CommitWait` blocking semantics for external consistency (Spanner-style TrueTime commit wait).

---

## Package Architecture

```
github.com/gosuda/gstime
├── core/         # Semantic types, Q16.48 fixed-point math, GSTL1 leap codec
├── ntp/          # NTPv4 wire framing, 2036 era unfolding, out-of-order reachability
├── nts/          # NTS-KE client, AEAD authenticator extension records, cookie jar
├── source/       # Source filtering, DP runs test, fault-domain consolidation, consensus sweep
├── clock/        # Raw clock drivers, EstimateClock, slew planner, smear engine, monotonic clamp
├── assurance/    # Anchor propagation, holdover tracking, status state machine
├── publish/      # Lock-free atomic snapshot publication
├── config/       # Canonical JSON configuration and RFC 8785 SHA-256 config hashing
├── telemetry/    # Atomic metrics collectors (offsets, errors, smear, re-anchors)
├── conformance/  # Conformance test suites (Levels A-F) and property verifications (P1-P14)
└── service.go    # Unified ClockService top-level facade
```

---

## Installation

Requires Go 1.25 or newer.

```bash
go get github.com/gosuda/gstime
```

---

## Usage

### Initializing the Clock Service

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/gosuda/gstime"
	"github.com/gosuda/gstime/clock"
	"github.com/gosuda/gstime/core"
)

func main() {
	// Initialize hardware raw monotonic clock source
	rawClock := clock.NewSystemRawClock()

	// Load canonical GSTL1 leap history
	leapHistory, err := core.NewLeapHistory(1, nil)
	if err != nil {
		panic(err)
	}

	configID := [32]byte{0x01} // Canonical RFC 8785 SHA-256 digest
	maxHoldover := int64(32 * time.Second)

	svc := gstime.NewClockService(rawClock, leapHistory, configID, maxHoldover)

	// Publish assurance round from upstream sources
	hull := core.TimeInterval{
		Earliest: 1_700_000_000_000_000_000,
		Latest:   1_700_000_000_050_000_000,
	}
	_ = svc.PublishAssuranceRound(rawClock.Read().RawNanos, hull, 1, 3, 2, 1, rawClock.Read().RawNanos+int64(10*time.Second))

	// Query public reading
	reading := svc.Now()
	fmt.Printf("Time: %d ns, Public Epsilon: ±%d ns\n", reading.Instant, reading.PublicSymmetricEpsilonNs)
}
```

### Distributed Transaction Commit Wait

`CommitWait` blocks until the certified lower watermark strictly exceeds the designated commit timestamp, guaranteeing that no subsequent transaction can receive an earlier timestamp.

```go
ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
defer cancel()

commitTs := reading.Instant
snap := svc.Now()

err := svc.CommitWait(ctx, commitTs, snap.AssuranceEpochID, leapHistory.ID, configID)
if err != nil {
	// Handle ErrDesynchronized, ErrEpochMismatch, or context timeout
	panic(err)
}
// Commit is now externally linearizable
```

### Interval Comparisons

```go
invA := core.TimeInterval{Earliest: 100, Latest: 200}
invB := core.TimeInterval{Earliest: 300, Latest: 400}

if invA.Before(invB) {
	fmt.Println("A strictly precedes B with certainty")
}
if invB.After(invA) {
	fmt.Println("B strictly succeeds A with certainty")
}
if invA.Disjoint(invB) {
	fmt.Println("A and B do not overlap")
}
```

---

## Conformance Levels

| Level | Name | Description | Verification Target |
|:---:|:---|:---|:---|
| **A** | Wire & Cryptographic Framing | NTPv4 48-byte header, 2036 era unfolding, NTS-KE framing, AEAD 15/30, cookie jar single-ownership. | `conformance.TestConformance_LevelA_Wire` |
| **B** | Dual-Track Source Processing | Weighted regression, DP runs test p-value, fault-domain consolidation, Appendix B $N-F$ sweep. | `conformance.TestConformance_LevelB_Estimate` |
| **C** | Formal Interval Assurance | Absolute anchor propagation, holdover growth, watermark monotonicity, status state machine. | `conformance.TestConformance_LevelC_Assurance` |
| **D** | Continuous Clock & Smear | Re-anchor bitwise continuity, bounded slew rate, trapezoidal smear replanning, atomic clamp. | `conformance.TestConformance_LevelD_Clock` |
| **E** | Concurrency & Monotonicity | Lock-free generation coherence, race-free reader/writer isolation, public monotonicity. | `conformance.TestConformance_LevelE_Concurrency` |
| **F** | Full System Integration | Top-level `ClockService`, `CommitWait`, UTC projection, canonical config hashing, telemetry. | `conformance.TestConformance_LevelF_System` |

---

## Formal Invariant Models (P1 – P14)

The test suite validates fourteen mathematical invariants:

- **P1 (ReanchorContinuity)**: $E_{\text{old}}(c) == E_{\text{new}}(c)$ exactly in implementation fixed-point arithmetic.
- **P2 (DebtAccrualNoInstantStep)**: Time updates accrue as rate debt without instantaneous jumps.
- **P3 (CoverageSetContainsHonestIntersection)**: The $N-F$ consensus set contains the intersection of honest intervals.
- **P4 (CoverageHullContainsEveryCoverageComponent)**: The consensus hull spans all connected coverage components.
- **P5 (AssuranceIndependentOfEstimateStatistics)**: Assurance intervals are computed independently of estimation heuristics.
- **P6 (AbsolutePropagationContainsSimulatedTrueTime)**: Propagated intervals always enclose true time under bounded drift.
- **P7 (PastWatermarkMonotonic)**: Certified lower watermark never decreases.
- **P8 (ReachBitmapCorrectUnderResponsePermutation)**: 8-bit reachability bitmap correctly tracks reordered responses.
- **P9 (CookieSingleOwnership)**: NTS cookies are never duplicated or concurrently assigned.
- **P10 (PublicBiasAndClampIncludedInPublicEpsilon)**: Smear offset and monotonic clamp bias are included in `PublicSymmetricEpsilonNs`.
- **P11 (ContinuousLeapAxisNeverRepeats)**: Continuous SI nanosecond instants map uniquely to and from UTC across leap seconds.
- **P12 (CommitWaitStrictlyAfterLatest)**: `CommitWait` returns only when the watermark strictly exceeds the target timestamp.
- **P13 (SnapshotGenerationCoherence)**: Snapshots read under concurrent updates exhibit consistent internal generation numbers.
- **P14 (DesyncReturnsNoFiniteAssuredInterval)**: When desynchronized, no finite assurance interval is emitted.

---

## Verification & Testing

Execute unit and race-detector tests across all packages:

```bash
go test -v -race -count=1 ./...
```

Execute Go engineering practices AST analysis and linting:

```bash
gojgp check
```
