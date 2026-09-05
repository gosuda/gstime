package clock

import (
	"sync"
	"time"

	"github.com/gosuda/gstime/core"
)

// RawReading represents a single read from RawClock (Section 5.1).
type RawReading struct {
	Raw               core.RawNanos
	ReadBound         core.ErrorNs
	ContinuityToken   uint64
	BackendGeneration uint64
}

// RawClock defines the hardware/OS raw clock backend interface.
type RawClock interface {
	Read() RawReading
	ScaleEnvelope() (lower, upper core.RateScale)
	IncludesSuspend() bool
}

// SimulatedRawClock is a deterministic, controllable raw clock backend for testing and simulation.
type SimulatedRawClock struct {
	mu                sync.Mutex
	currentRaw        core.RawNanos
	readBound         core.ErrorNs
	continuityToken   uint64
	backendGeneration uint64
	scaleLower        core.RateScale
	scaleUpper        core.RateScale
}

// NewSimulatedRawClock creates a simulated raw clock initialized at raw0.
func NewSimulatedRawClock(raw0 core.RawNanos) *SimulatedRawClock {
	return &SimulatedRawClock{
		currentRaw:        raw0,
		readBound:         100, // 100ns default read bound
		continuityToken:   1,
		backendGeneration: 1,
		// Nominal 1.0 in Q16.48 with +/- 50 ppm envelope
		scaleLower: core.RateScale(core.OneQ48 + core.RateFromPpmLower(-50.0)),
		scaleUpper: core.RateScale(core.OneQ48 + core.RateFromPpmUpper(50.0)),
	}
}

func (s *SimulatedRawClock) Read() RawReading {
	s.mu.Lock()
	defer s.mu.Unlock()
	return RawReading{
		Raw:               s.currentRaw,
		ReadBound:         s.readBound,
		ContinuityToken:   s.continuityToken,
		BackendGeneration: s.backendGeneration,
	}
}

func (s *SimulatedRawClock) ScaleEnvelope() (core.RateScale, core.RateScale) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scaleLower, s.scaleUpper
}

func (s *SimulatedRawClock) IncludesSuspend() bool {
	return true
}

// Advance moves simulated raw time forward by deltaNanos.
func (s *SimulatedRawClock) Advance(deltaNanos core.RawNanos) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentRaw += deltaNanos
}

// SetContinuityToken simulates a platform discontinuity or counter reset.
func (s *SimulatedRawClock) SetContinuityToken(token uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.continuityToken = token
	s.backendGeneration++
}

// SystemRawClock implements RawClock using platform monotonic time.
type SystemRawClock struct {
	startMono time.Time
	startRaw  core.RawNanos
}

// NewSystemRawClock creates a standard monotonic RawClock.
func NewSystemRawClock() *SystemRawClock {
	return &SystemRawClock{
		startMono: time.Now(),
		startRaw:  1_000_000_000,
	}
}

func (c *SystemRawClock) Read() RawReading {
	elapsed := time.Since(c.startMono).Nanoseconds()
	if elapsed < 0 {
		elapsed = 0
	}
	return RawReading{
		Raw:               c.startRaw + core.RawNanos(elapsed),
		ReadBound:         1000, // 1us conservative read latency bound
		ContinuityToken:   1,
		BackendGeneration: 1,
	}
}

func (c *SystemRawClock) ScaleEnvelope() (core.RateScale, core.RateScale) {
	// Conservative system clock envelope: +/- 200 ppm
	low := core.RateScale(core.OneQ48 + core.RateFromPpmLower(-200.0))
	upp := core.RateScale(core.OneQ48 + core.RateFromPpmUpper(200.0))
	return low, upp
}

func (c *SystemRawClock) IncludesSuspend() bool {
	return true
}
