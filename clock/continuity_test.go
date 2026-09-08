package clock

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"gosuda.org/gstime/core"
)

type guardedReadStage struct {
	reading  RawReading
	captured chan struct{}
	release  chan struct{}
	result   chan RawReading
	once     sync.Once
}

func (s *guardedReadStage) unblock() {
	s.once.Do(func() { close(s.release) })
}

type guardedReadScript struct {
	stages  []*guardedReadStage
	next    atomic.Uint64
	readers sync.WaitGroup
}

func newGuardedReadScript(t *testing.T, readings ...RawReading) *guardedReadScript {
	t.Helper()
	s := &guardedReadScript{}
	for _, r := range readings {
		s.stages = append(s.stages, &guardedReadStage{
			reading:  r,
			captured: make(chan struct{}),
			release:  make(chan struct{}),
			result:   make(chan RawReading, 1),
		})
	}
	t.Cleanup(func() {
		for _, stage := range s.stages {
			stage.unblock()
		}
		s.readers.Wait()
	})
	return s
}

func (s *guardedReadScript) Read() RawReading {
	stage := s.stages[s.next.Add(1)-1]
	r := stage.reading
	close(stage.captured)
	<-stage.release
	return r
}

func (s *guardedReadScript) ScaleEnvelope() (core.RateScale, core.RateScale) {
	return core.RateScale(core.OneQ48), core.RateScale(core.OneQ48)
}

func (s *guardedReadScript) IncludesSuspend() bool { return true }

func (s *guardedReadScript) start(g *ContinuityGuard, index int) *guardedReadStage {
	stage := s.stages[index]
	s.readers.Add(1)
	go func() {
		defer s.readers.Done()
		stage.result <- g.Read()
	}()
	<-stage.captured
	return stage
}

func (s *guardedReadStage) complete() RawReading {
	s.unblock()
	return <-s.result
}

func TestContinuityGuardAcceptsOverlappingCompletionOrder(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "initial overlap", true: "steady overlap"}[initialized], func(t *testing.T) {
			readings := []RawReading{{Raw: 101, ContinuityToken: 7, BackendGeneration: 9}, {Raw: 102, ContinuityToken: 7, BackendGeneration: 9}, {Raw: 103, ContinuityToken: 7, BackendGeneration: 9}, {Raw: 102, ContinuityToken: 7, BackendGeneration: 9}}
			if initialized {
				readings = append([]RawReading{{Raw: 100, ContinuityToken: 7, BackendGeneration: 9}}, readings...)
			}
			script := newGuardedReadScript(t, readings...)
			guard := NewContinuityGuard(script)
			start := 0
			if initialized {
				if got := script.start(guard, 0).complete(); got.ContinuityToken != 1 {
					t.Fatalf("initial token = %d", got.ContinuityToken)
				}
				start = 1
			}
			first := script.start(guard, start)
			second := script.start(guard, start+1)
			third := script.start(guard, start+2)
			for _, stage := range []*guardedReadStage{first, third, second} {
				got := stage.complete()
				if got.ContinuityToken != 1 {
					t.Fatalf("raw %d token = %d, want 1", got.Raw, got.ContinuityToken)
				}
			}
			if got := script.start(guard, start+3).complete(); got.ContinuityToken != 2 {
				t.Fatalf("sequential regression after overlap token = %d, want 2", got.ContinuityToken)
			}
		})
	}
}

func TestContinuityGuardDetectsSequentialEpochChanges(t *testing.T) {
	readings := []RawReading{
		{Raw: 100, ContinuityToken: 7, BackendGeneration: 9},
		{Raw: 99, ContinuityToken: 7, BackendGeneration: 9},
		{Raw: 100, ContinuityToken: 8, BackendGeneration: 9},
		{Raw: 101, ContinuityToken: 8, BackendGeneration: 10},
		{Raw: math.MaxInt64, ContinuityToken: 8, BackendGeneration: 10},
		{Raw: math.MaxInt64 + 1, ContinuityToken: 8, BackendGeneration: 10},
		{Raw: math.MaxUint64, ContinuityToken: 8, BackendGeneration: 10},
		{Raw: 0, ContinuityToken: 8, BackendGeneration: 10},
	}
	wantTokens := []uint64{1, 2, 3, 4, 4, 4, 4, 5}
	script := newGuardedReadScript(t, readings...)
	guard := NewContinuityGuard(script)
	for i, want := range wantTokens {
		got := script.start(guard, i).complete()
		if got.ContinuityToken != want || got.BackendGeneration != readings[i].BackendGeneration {
			t.Fatalf("reading %d = %+v, want token %d and backend generation %d", i, got, want, readings[i].BackendGeneration)
		}
	}
}

func TestContinuityGuardDoesNotRelabelDelayedOldEpoch(t *testing.T) {
	for _, changed := range []RawReading{
		{Raw: 200, ContinuityToken: 8, BackendGeneration: 9},
		{Raw: 200, ContinuityToken: 7, BackendGeneration: 10},
		{Raw: 50, ContinuityToken: 7, BackendGeneration: 9},
	} {
		script := newGuardedReadScript(t,
			RawReading{Raw: 100, ContinuityToken: 7, BackendGeneration: 9},
			RawReading{Raw: 101, ContinuityToken: 7, BackendGeneration: 9},
			changed, changed,
		)
		guard := NewContinuityGuard(script)
		initial := script.start(guard, 0).complete()
		delayed := script.start(guard, 1)
		transition := script.start(guard, 2).complete()
		old := delayed.complete()
		current := script.start(guard, 3).complete()
		if transition.ContinuityToken != initial.ContinuityToken+1 {
			t.Fatalf("transition %+v did not advance token: %+v", changed, transition)
		}
		if old.ContinuityToken != 0 || old.Raw != 101 || old.BackendGeneration != 9 {
			t.Fatalf("delayed evidence was relabeled: %+v", old)
		}
		if current.ContinuityToken != transition.ContinuityToken {
			t.Fatalf("delayed old read changed epoch: transition=%+v current=%+v", transition, current)
		}
	}
}
