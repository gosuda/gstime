package clock_test

import (
	"gosuda.org/gstime/clock"
	"testing"
)

func TestSystemRawClockDoesNotPromiseSuspend(t *testing.T) {
	if clock.NewSystemRawClock().IncludesSuspend() {
		t.Fatal("portable time.Since clock must not promise suspend-inclusive elapsed time")
	}
}
