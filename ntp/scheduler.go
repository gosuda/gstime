package ntp

import (
	"math"
	"sync"

	"gosuda.org/gstime/core"
)

// PollScheduler manages poll intervals and stability scoring (Section 2.14).
type PollScheduler struct {
	mu             sync.Mutex
	minPoll        core.PollExp
	maxPoll        core.PollExp
	currentPoll    core.PollExp
	stabilityScore float64
	missCount      int
}

// NewPollScheduler creates a new scheduler clamped between minPoll and maxPoll.
func NewPollScheduler(minPoll, maxPoll core.PollExp) *PollScheduler {
	if minPoll < 4 {
		minPoll = 4
	}
	if maxPoll < minPoll {
		maxPoll = minPoll
	}
	return &PollScheduler{
		minPoll:     minPoll,
		maxPoll:     maxPoll,
		currentPoll: minPoll,
	}
}

// CurrentPoll returns the active poll exponent.
func (s *PollScheduler) CurrentPoll() core.PollExp {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentPoll
}

// OnUsableSample updates stability score after an accepted sample.
func (s *PollScheduler) OnUsableSample(innovationNs, expectedErrorNs float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.missCount = 0
	if expectedErrorNs > 0 && innovationNs > expectedErrorNs {
		ratio := innovationNs / expectedErrorNs
		penalty := math.Min(1.0, math.Log2(ratio))
		if penalty > 0 {
			s.stabilityScore -= penalty
		}
	} else {
		// Stable increment
		s.stabilityScore += 0.5
	}

	if s.stabilityScore >= 1.0 {
		if s.currentPoll < s.maxPoll {
			s.currentPoll++
		}
		s.stabilityScore = 0
	} else if s.stabilityScore <= -1.0 {
		if s.currentPoll > s.minPoll {
			s.currentPoll--
		}
		s.stabilityScore = 0
	}
}

// OnTimeout records a missed response and adjusts backoff.
func (s *PollScheduler) OnTimeout() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.missCount++
	// Do not hammer unreachable server: repeated misses back off toward maxPoll
	if s.missCount >= 3 && s.currentPoll < s.maxPoll {
		s.currentPoll++
		s.stabilityScore = 0
	}
}
