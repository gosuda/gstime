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
	raw         RawClock
	token       atomic.Uint64
	initialized atomic.Bool
	lastRaw     atomic.Int64
	lastToken   atomic.Uint64
	lastGen     atomic.Uint64
}

func NewContinuityGuard(raw RawClock) *ContinuityGuard {
	g := &ContinuityGuard{raw: raw}
	g.token.Store(1)
	return g
}

func (g *ContinuityGuard) Read() RawReading {
	r := g.raw.Read()
	curRaw := int64(r.Raw)

	if !g.initialized.Load() {
		g.lastRaw.Store(curRaw)
		g.lastToken.Store(r.ContinuityToken)
		g.lastGen.Store(r.BackendGeneration)
		g.initialized.Store(true)
		r.ContinuityToken = g.token.Load()
		return r
	}

	prevRaw := g.lastRaw.Load()
	prevToken := g.lastToken.Load()
	prevGen := g.lastGen.Load()

	if isDiscontinuousReading(curRaw, prevRaw, r.ContinuityToken, prevToken, r.BackendGeneration, prevGen) {
		g.token.Add(1)
		g.lastRaw.Store(curRaw)
		g.lastToken.Store(r.ContinuityToken)
		g.lastGen.Store(r.BackendGeneration)
	} else if curRaw > prevRaw {
		g.lastRaw.CompareAndSwap(prevRaw, curRaw)
	}

	r.ContinuityToken = g.token.Load()
	return r
}

func isDiscontinuousReading(curRaw, prevRaw int64, curToken, prevToken, curGen, prevGen uint64) bool {
	if curRaw < prevRaw {
		return true
	}
	if curToken != prevToken {
		return true
	}
	return curGen != prevGen
}

func (g *ContinuityGuard) ScaleEnvelope() (core.RateScale, core.RateScale) {
	return g.raw.ScaleEnvelope()
}

func (g *ContinuityGuard) IncludesSuspend() bool {
	return g.raw.IncludesSuspend()
}
