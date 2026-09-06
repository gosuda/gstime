package gstime

import (
	"testing"

	"gosuda.org/gstime/clock"
)

func setupBenchService() (*ClockService, [32]byte) {
	raw := clock.NewSimulatedRawClock(1_000_000_000)
	lh, _ := NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 78796800, Delta: 1},
	})
	var cfgID [32]byte
	cfgID[0] = 0xab

	svc := NewClockService(raw, lh, cfgID, 32_000_000_000)
	targetTime := GstInstant(1_700_000_000 * 1_000_000_000)
	_ = svc.InitializeEstimate(1_000_000_000, targetTime, 0)
	hull := TimeInterval{
		Earliest: targetTime - 50_000,
		Latest:   targetTime + 50_000,
	}
	_ = svc.PublishAssuranceRound(1_000_000_000, hull, 1, 3, 2, 1, 100_000_000_000)
	return svc, lh.ID
}

func BenchmarkNow(b *testing.B) {
	svc, _ := setupBenchService()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = svc.Now()
	}
}

func BenchmarkNowParallel(b *testing.B) {
	svc, _ := setupBenchService()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = svc.Now()
		}
	})
}

func BenchmarkNowPublicAssured(b *testing.B) {
	svc, _ := setupBenchService()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_ = svc.NowPublicAssured()
	}
}

func BenchmarkNowPublicAssuredParallel(b *testing.B) {
	svc, _ := setupBenchService()
	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = svc.NowPublicAssured()
		}
	})
}

func BenchmarkNowUnixProjection(b *testing.B) {
	svc, _ := setupBenchService()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = svc.NowUnixProjection()
	}
}

func BenchmarkNowUtc(b *testing.B) {
	svc, lhID := setupBenchService()
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, _, _, _, _ = svc.NowUtc(lhID)
	}
}
