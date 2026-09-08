package ntp

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"testing"

	"gosuda.org/gstime/core"
)

func measurementFixture() MeasurementInput {
	return MeasurementInput{
		LocalSendRaw:  100,
		LocalRecvRaw:  102,
		RawScaleLower: core.RateScale(core.OneQ48),
		RawScaleUpper: core.RateScale(core.OneQ48),
	}
}

// The oracle retains fractional SI nanoseconds and never calls fixed-point helpers.
func modeledTime(scale core.RateScale, rawOffset int64) *big.Rat {
	product := new(big.Int).Mul(big.NewInt(int64(scale)), big.NewInt(rawOffset))
	elapsed := new(big.Rat).SetFrac(product, new(big.Int).Lsh(big.NewInt(1), 48))
	return elapsed.Add(elapsed, big.NewRat(1_000_000_000_000, 1))
}

func floorModeledTime(value *big.Rat) core.GstInstant {
	return core.GstInstant(new(big.Int).Div(value.Num(), value.Denom()).Int64())
}

func assertModeledMeasurement(t *testing.T, in MeasurementInput, truth *big.Rat) *MeasurementResult {
	t.Helper()
	got, err := ComputeMeasurement(in)
	if err != nil {
		t.Fatal(err)
	}
	lower := big.NewRat(int64(got.HardInterval.Earliest), 1)
	upper := big.NewRat(int64(got.HardInterval.Latest), 1)
	if lower.Cmp(truth) > 0 || upper.Cmp(truth) < 0 {
		t.Fatalf("hard interval [%s,%s] excludes true SI time %s at raw midpoint %d", lower, upper, truth, got.RawMid)
	}
	return got
}

func TestMeasurementHardIntervalContainsOneSecondAsymmetricTruth(t *testing.T) {
	q := int64(core.OneQ48)
	// Widen 200ppm outward without a floating-point conversion.
	ppm := (q + 4999) / 5000
	lo, hi := core.RateScale(q-ppm), core.RateScale(q+ppm)
	for _, scale := range []core.RateScale{lo, hi} {
		for _, events := range [][2]int64{{0, 0}, {1_000_000_000, 1_000_000_000}, {0, 1_000_000_000}} {
			t.Run(fmt.Sprintf("scale_%d_events_%d_%d", scale, events[0], events[1]), func(t *testing.T) {
				in := MeasurementInput{
					T1LocalSendEstimate:         1_000_000_000_000,
					T4LocalRecvEstimate:         1_001_000_000_000,
					LocalEstimateAtMid:          1_000_500_000_000,
					T2ServerRecv:                floorModeledTime(modeledTime(scale, events[0])),
					T3ServerTx:                  floorModeledTime(modeledTime(scale, events[1])),
					LocalSendRaw:                10,
					LocalRecvRaw:                1_000_000_010,
					RawScaleLower:               lo,
					RawScaleUpper:               hi,
					RemoteTimestampQuantization: 1,
				}
				assertModeledMeasurement(t, in, modeledTime(scale, 500_000_000))
			})
		}
	}
}

func TestMeasurementHardIntervalContainsEveryServerEventPosition(t *testing.T) {
	q := int64(core.OneQ48)
	lo, hi := core.RateScale(q-1), core.RateScale(q+q/4+1)
	for _, span := range []int64{1, 3, 8} {
		for _, scale := range []core.RateScale{lo, hi} {
			in := MeasurementInput{
				T1LocalSendEstimate: 1_000_000_000_000,
				T4LocalRecvEstimate: 1_000_000_000_000 + core.GstInstant(span),
				LocalEstimateAtMid:  1_000_000_000_000 + core.GstInstant(span/2),
				LocalSendRaw:        math.MaxUint64 - 100,
				LocalRecvRaw:        math.MaxUint64 - 100 + core.RawNanos(span),
				RawScaleLower:       lo, RawScaleUpper: hi,
				LocalSendReadError: 2, LocalRecvReadError: 3,
				ServerRootDispersionNs: 4, ServerRootDelayNs: 3,
				ProtocolConversionError: 5, RemoteTimestampQuantization: 1,
			}
			checkServerPositions(t, in, scale, span, [2]int64{-2, span + 3})
		}
	}
}

func checkServerPositions(t *testing.T, in MeasurementInput, scale core.RateScale, span int64, bounds [2]int64) {
	t.Helper()
	for t2 := bounds[0]; t2 <= bounds[1]; t2++ {
		for t3 := t2; t3 <= bounds[1]; t3++ {
			checkServerBias(t, in, scale, span, [2]int64{t2, t3})
		}
	}
}

func checkServerBias(t *testing.T, in MeasurementInput, scale core.RateScale, span int64, events [2]int64) {
	t.Helper()
	for _, bias := range []int64{-11, 11} {
		t.Run(fmt.Sprintf("span_%d_scale_%d_t2_%d_t3_%d_bias_%d", span, scale, events[0], events[1], bias), func(t *testing.T) {
			in.T2ServerRecv = floorModeledTime(modeledTime(scale, events[0])) + core.GstInstant(bias)
			in.T3ServerTx = floorModeledTime(modeledTime(scale, events[1])) + core.GstInstant(bias)
			assertModeledMeasurement(t, in, modeledTime(scale, span/2))
		})
	}
}

func TestMeasurementOddSpanOutwardEndpoints(t *testing.T) {
	for _, send := range []core.RawNanos{100, math.MaxInt64 - 1, math.MaxUint64 - 3} {
		in := measurementFixture()
		in.LocalSendRaw, in.LocalRecvRaw = send, send+3
		in.RawScaleUpper = core.RateScale(int64(core.OneQ48) + 1)
		in.T1LocalSendEstimate, in.T2ServerRecv, in.T3ServerTx, in.T4LocalRecvEstimate = 100, 100, 100, 103
		in.LocalEstimateAtMid = 101
		got, err := ComputeMeasurement(in)
		if err != nil {
			t.Fatal(err)
		}
		// U=1+2^-48 and unequal midpoint halves: ceil(U*2)=3, ceil(U*1)=2.
		if got.RawMid != send+1 || got.HardInterval.Earliest != 97 || got.HardInterval.Latest != 102 {
			t.Fatalf("send=%d: midpoint=%d interval=%+v, want %d and [97,102]", send, got.RawMid, got.HardInterval, send+1)
		}
	}
}

func TestMeasurementHardIntervalIndependentOfEstimate(t *testing.T) {
	in := measurementFixture()
	in.T2ServerRecv, in.T3ServerTx = 200, 200
	in.LocalSendReadError, in.LocalRecvReadError = 2, 3
	first, err := ComputeMeasurement(in)
	if err != nil {
		t.Fatal(err)
	}
	in.T1LocalSendEstimate = -200
	in.T4LocalRecvEstimate = 600
	in.LocalEstimateAtMid = 100
	in.PrecisionFloorNs = 4000
	in.LocalMappingIntegrationErr = 1000
	in.StaticAsymmetryCorrection = 5
	second, err := ComputeMeasurement(in)
	if err != nil {
		t.Fatal(err)
	}
	if second.HardInterval != first.HardInterval || second.Center == first.Center || second.DeltaRaw == first.DeltaRaw {
		t.Fatalf("estimate changes must affect statistics but not hard bounds: first=%+v second=%+v", first, second)
	}
	if second.RawMidReadBound != 0 {
		t.Fatalf("endpoint read errors must not be charged again when propagating the synthetic midpoint: %d", second.RawMidReadBound)
	}
}

func TestMeasurementRejectsInvalidUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*MeasurementInput)
		want   error
	}{
		{"missing_scale", func(in *MeasurementInput) { in.RawScaleLower = 0 }, errInvalidRawScale},
		{"negative_scale", func(in *MeasurementInput) { in.RawScaleLower = -1 }, errInvalidRawScale},
		{"reversed_scale", func(in *MeasurementInput) { in.RawScaleUpper = in.RawScaleLower - 1 }, errInvalidRawScale},
		{"negative_root_delay", func(in *MeasurementInput) { in.ServerRootDelayNs = -1 }, errInvalidMeasurementError},
		{"negative_dispersion", func(in *MeasurementInput) { in.ServerRootDispersionNs = -1 }, errInvalidMeasurementError},
		{"negative_precision", func(in *MeasurementInput) { in.PrecisionFloorNs = -1 }, errInvalidMeasurementError},
		{"negative_turnaround_limit", func(in *MeasurementInput) { in.MaxServerTurnaroundNs = -1 }, errInvalidMeasurementError},
		{"negative_root_limit", func(in *MeasurementInput) { in.MaxRootDistanceNs = -1 }, errInvalidMeasurementError},
		{"raw_span_overflow", func(in *MeasurementInput) { in.LocalRecvRaw = math.MaxUint64 }, core.ErrOverflow},
		{"root_sum_overflow", func(in *MeasurementInput) { in.ServerRootDispersionNs = math.MaxInt64; in.ServerRootDelayNs = 2 }, core.ErrOverflow},
		{"timestamp_error_overflow", func(in *MeasurementInput) {
			in.RemoteTimestampQuantization = math.MaxInt64
			in.ProtocolConversionError = 1
		}, core.ErrOverflow},
		{"read_distance_overflow", func(in *MeasurementInput) { in.LocalSendReadError = math.MaxInt64 }, core.ErrOverflow},
		{"infinite_read_error", func(in *MeasurementInput) { in.LocalRecvReadError = core.ErrorInfinity }, core.ErrOverflow},
		{"unrepresentable_mapping_error", func(in *MeasurementInput) { in.LocalMappingIntegrationErr = math.MaxInt64 + 1 }, core.ErrOverflow},
		{"scale_product_overflow", func(in *MeasurementInput) {
			in.LocalSendRaw = 0
			in.LocalRecvRaw = math.MaxInt64
			in.RawScaleUpper = 2 * core.RateScale(core.OneQ48)
		}, core.ErrOverflow},
		{"turnaround_overflow", func(in *MeasurementInput) { in.T2ServerRecv = math.MinInt64; in.T3ServerTx = math.MaxInt64 }, core.ErrOverflow},
		{"theta_difference_overflow", func(in *MeasurementInput) { in.T1LocalSendEstimate = math.MinInt64 }, core.ErrOverflow},
		{"theta_sum_overflow", func(in *MeasurementInput) { in.T2ServerRecv = math.MaxInt64; in.T3ServerTx = math.MaxInt64 }, core.ErrOverflow},
		{"theta_correction_overflow", func(in *MeasurementInput) {
			in.T2ServerRecv = 1
			in.T3ServerTx = 1
			in.StaticAsymmetryCorrection = math.MaxInt64
		}, core.ErrOverflow},
		{"estimate_span_overflow", func(in *MeasurementInput) {
			in.T1LocalSendEstimate = -5_000_000_000_000_000_000
			in.T4LocalRecvEstimate = 5_000_000_000_000_000_000
		}, core.ErrOverflow},
		{"center_overflow", func(in *MeasurementInput) {
			in.T2ServerRecv = 1
			in.T3ServerTx = 1
			in.LocalEstimateAtMid = math.MaxInt64
		}, core.ErrOverflow},
		{"lower_endpoint_overflow", func(in *MeasurementInput) {
			in.T1LocalSendEstimate = math.MinInt64
			in.T2ServerRecv = math.MinInt64
			in.T3ServerTx = math.MinInt64
			in.T4LocalRecvEstimate = math.MinInt64
		}, core.ErrOverflow},
		{"upper_endpoint_overflow", func(in *MeasurementInput) {
			in.T1LocalSendEstimate = math.MaxInt64
			in.T2ServerRecv = math.MaxInt64
			in.T3ServerTx = math.MaxInt64
			in.T4LocalRecvEstimate = math.MaxInt64
		}, core.ErrOverflow},
		{"inconsistent_turnaround", func(in *MeasurementInput) { in.T3ServerTx = 3 }, errInconsistentMeasurement},
		{"outward_root_cap", func(in *MeasurementInput) { in.ServerRootDelayNs = 3; in.MaxRootDistanceNs = 1 }, ErrRootDistanceExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := measurementFixture()
			tc.change(&in)
			if _, err := ComputeMeasurement(in); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRootFixedPointBoundsRoundOutward(t *testing.T) {
	for _, word := range []uint32{0, 1, 65535, 65536, math.MaxInt32, math.MaxUint32} {
		p := Packet{RootDisp: word}
		exact := new(big.Rat).SetFrac(new(big.Int).Mul(new(big.Int).SetUint64(uint64(word)), big.NewInt(1_000_000_000)), big.NewInt(65536))
		got := p.RootDispersionNanoseconds()
		if big.NewRat(got, 1).Cmp(exact) < 0 || big.NewRat(got-1, 1).Cmp(exact) >= 0 {
			t.Fatalf("dispersion %d rounded to %d, exact=%s", word, got, exact)
		}
	}
	for _, word := range []int32{math.MinInt32, -1, 0, 1, 65535, math.MaxInt32} {
		p := Packet{RootDelay: word}
		exact := new(big.Rat).SetFrac(new(big.Int).Mul(big.NewInt(int64(word)), big.NewInt(1_000_000_000)), big.NewInt(65536))
		got := p.RootDelayNanoseconds()
		if word < 0 {
			if big.NewRat(got, 1).Cmp(exact) > 0 || big.NewRat(got+1, 1).Cmp(exact) <= 0 {
				t.Fatalf("negative delay %d rounded to %d, exact=%s", word, got, exact)
			}
			continue
		}
		if big.NewRat(got, 1).Cmp(exact) < 0 || big.NewRat(got-1, 1).Cmp(exact) >= 0 {
			t.Fatalf("delay %d rounded to %d, exact=%s", word, got, exact)
		}
	}
}
