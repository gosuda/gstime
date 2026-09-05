package telemetry

import (
	"testing"
)

func TestMetricsRecord(t *testing.T) {
	m := &Metrics{}
	m.RecordNowCall()
	if m.NowCallsTotal.Load() != 1 {
		t.Fatalf("expected 1 call")
	}

	m.UpdateAssuranceWatermark(12345)
	if m.LastPastWatermark.Load() != 12345 {
		t.Fatalf("watermark mismatch")
	}

	m.UpdateSymmetricEpsilon(50)
	if m.LastSymmetricEpsNs.Load() != 50 {
		t.Fatalf("epsilon mismatch")
	}

	h := HealthState{
		AcquisitionHealthy: true,
		SecurityHealthy:    true,
		EstimateHealthy:    true,
		AssuranceHealthy:   true,
		PublicClockHealthy: true,
	}
	m.SetHealth(h)
	if !m.GetHealth().AssuranceHealthy {
		t.Fatalf("health mismatch")
	}
}
