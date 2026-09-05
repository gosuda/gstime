package clock

import (
	"gosuda.org/gstime/core"
	"sync"
)

// ContinuityGuard detects regressions observed within one surviving process.
// It cannot detect a full VM memory rollback, an unobserved rewind followed by
// catch-up, or sleep omitted by the underlying clock. Those events require an
// external signal and service invalidation, or a fresh process.
type ContinuityGuard struct {
	mu          sync.Mutex
	raw         RawClock
	last        RawReading
	initialized bool
	token       uint64
}

func NewContinuityGuard(raw RawClock) *ContinuityGuard {
	return &ContinuityGuard{raw: raw, token: 1}
}

func (g *ContinuityGuard) Read() RawReading {
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.raw.Read()
	if g.initialized && (r.Raw < g.last.Raw || r.ContinuityToken != g.last.ContinuityToken || r.BackendGeneration != g.last.BackendGeneration) {
		g.token++
	}
	g.last = r
	g.initialized = true
	r.ContinuityToken = g.token
	return r
}

func (g *ContinuityGuard) ScaleEnvelope() (core.RateScale, core.RateScale) {
	return g.raw.ScaleEnvelope()
}
func (g *ContinuityGuard) IncludesSuspend() bool { return g.raw.IncludesSuspend() }
