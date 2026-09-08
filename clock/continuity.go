package clock

import (
	"sync/atomic"

	"gosuda.org/gstime/core"
)

// ContinuityGuard detects regressions observed within one surviving process.
// It cannot detect a full VM memory rollback, an unobserved rewind followed by
// catch-up, or sleep omitted by the underlying clock. Those events require an
// external signal and service invalidation, or a fresh process.
type ContinuityGuard struct {
	raw   RawClock
	epoch atomic.Pointer[continuityEpoch]
}

type continuityEpoch struct {
	backendToken      uint64
	backendGeneration uint64
	token             uint64
	highRaw           atomic.Uint64
}

func NewContinuityGuard(raw RawClock) *ContinuityGuard {
	return &ContinuityGuard{raw: raw}
}

func (g *ContinuityGuard) Read() RawReading {
	before := g.epoch.Load()
	var precedingRaw uint64
	if before != nil {
		// Only readings published before acquisition can prove a regression.
		precedingRaw = before.highRaw.Load()
	}
	r := g.raw.Read()

	if before == nil {
		initial := &continuityEpoch{
			backendToken:      r.ContinuityToken,
			backendGeneration: r.BackendGeneration,
			token:             1,
		}
		initial.highRaw.Store(uint64(r.Raw))
		if g.epoch.CompareAndSwap(nil, initial) {
			r.ContinuityToken = initial.token
			return r
		}
		before = g.epoch.Load()
		if before.token != 1 || !before.matches(r) {
			r.ContinuityToken = 0
			return r
		}
	}

	if before.matches(r) && uint64(r.Raw) >= precedingRaw {
		before.observe(uint64(r.Raw))
		r.ContinuityToken = before.token
		if g.epoch.Load() != before {
			r.ContinuityToken = 0
		}
		return r
	}

	// A read straddling another transition cannot establish a new epoch.
	if g.epoch.Load() != before || before.token == 0 {
		r.ContinuityToken = 0
		return r
	}
	next := &continuityEpoch{
		backendToken:      r.ContinuityToken,
		backendGeneration: r.BackendGeneration,
		token:             before.token + 1,
	}
	next.highRaw.Store(uint64(r.Raw))
	if g.epoch.CompareAndSwap(before, next) {
		r.ContinuityToken = next.token
		return r
	}
	r.ContinuityToken = 0
	return r
}

func (e *continuityEpoch) matches(r RawReading) bool {
	return r.ContinuityToken == e.backendToken && r.BackendGeneration == e.backendGeneration
}

func (e *continuityEpoch) observe(raw uint64) {
	for high := e.highRaw.Load(); raw > high; high = e.highRaw.Load() {
		if e.highRaw.CompareAndSwap(high, raw) {
			return
		}
	}
}

func (g *ContinuityGuard) ScaleEnvelope() (core.RateScale, core.RateScale) {
	return g.raw.ScaleEnvelope()
}

func (g *ContinuityGuard) IncludesSuspend() bool {
	return g.raw.IncludesSuspend()
}
