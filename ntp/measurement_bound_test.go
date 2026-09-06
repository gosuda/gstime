package ntp

import (
	"gosuda.org/gstime/core"
	"math"
	"testing"
)

func TestMeasurementCarriesMidpointReadBound(t *testing.T) {
	for _, tc := range []struct {
		name       string
		send, recv core.ErrorNs
		span       core.RawNanos
		want       core.ErrorNs
	}{
		{"exact", 0, 0, 2, 0}, {"odd_midpoint", 0, 0, 3, 1},
		{"send_dominates", 1000, 5, 2, 1000}, {"receive_dominates", 5, 1000, 2, 1000},
		{"rounding", 1000, 5, 3, 1001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ComputeMeasurement(MeasurementInput{LocalSendRaw: 100, LocalRecvRaw: 100 + tc.span, LocalSendReadError: tc.send, LocalRecvReadError: tc.recv})
			if err != nil {
				t.Fatal(err)
			}
			if got.RawMidReadBound != tc.want {
				t.Fatalf("got %d want %d", got.RawMidReadBound, tc.want)
			}
		})
	}
}

func TestMeasurementRejectsUnrepresentableReadBound(t *testing.T) {
	for _, bound := range []core.ErrorNs{math.MaxUint64, math.MaxInt64 + 1} {
		if _, err := ComputeMeasurement(MeasurementInput{LocalSendRaw: 100, LocalRecvRaw: 103, LocalSendReadError: bound}); err == nil {
			t.Fatal("unrepresentable bound accepted")
		}
	}
}
