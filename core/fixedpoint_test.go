package core

import (
	"math"
	"math/big"
	"testing"
)

func TestPpmConversions(t *testing.T) {
	cases := []float64{0.0, 1.0, -1.0, 500.0, -500.0, 123.456, -789.012}
	for _, ppm := range cases {
		rEst := RateFromPpmEstimate(ppm)
		rLow := RateFromPpmLower(ppm)
		rUpp := RateFromPpmUpper(ppm)

		if rLow > rEst || rEst > rUpp {
			t.Fatalf("ordering violated for ppm %f: low=%d est=%d upp=%d", ppm, rLow, rEst, rUpp)
		}

		gotPpm := PpmFromRate(rEst)
		if math.Abs(gotPpm-ppm) > 1e-6 {
			t.Fatalf("ppm round-trip failed: got %f want %f", gotPpm, ppm)
		}
	}
}

func TestRateComposition(t *testing.T) {
	oldRate := RateFromPpmEstimate(100.0)
	newRate := RateFromPpmEstimate(150.0)

	delta, err := DeltaRate(oldRate, newRate)
	if err != nil {
		t.Fatalf("DeltaRate failed: %v", err)
	}

	recomposed, err := ComposeRate(oldRate, delta)
	if err != nil {
		t.Fatalf("ComposeRate failed: %v", err)
	}

	if math.Abs(PpmFromRate(recomposed)-150.0) > 1e-4 {
		t.Fatalf("recomposed rate mismatch: got %f want 150.0", PpmFromRate(recomposed))
	}
}

func TestMulOutwardRoundingVsBigInt(t *testing.T) {
	rates := []RateFrac{
		0,
		OneQ48,
		-OneQ48,
		RateFromPpmEstimate(100.0),
		RateFromPpmEstimate(-100.0),
		RateFromPpmEstimate(0.0001),
		RateFromPpmEstimate(-0.0001),
	}
	durations := []DurationNs{
		0,
		1,
		-1,
		1_000_000_000,
		-1_000_000_000,
		86400 * 1_000_000_000,
		123456789,
		-987654321,
	}

	for _, r := range rates {
		for _, d := range durations {
			gotFloor, errF := MulRateDurationFloor(r, d)
			if errF != nil {
				t.Fatalf("floor error: %v", errF)
			}
			gotCeil, errC := MulRateDurationCeil(r, d)
			if errC != nil {
				t.Fatalf("ceil error: %v", errC)
			}

			if gotFloor > gotCeil {
				t.Fatalf("floor (%d) > ceil (%d) for r=%d, d=%d", gotFloor, gotCeil, r, d)
			}

			// Reference using big.Int
			bigR := big.NewInt(int64(r))
			bigD := big.NewInt(int64(d))
			prod := new(big.Int).Mul(bigR, bigD)

			divisor := new(big.Int).Lsh(big.NewInt(1), FracBits)
			quo := new(big.Int)
			rem := new(big.Int)
			quo.DivMod(prod, divisor, rem) // big.Int DivMod uses Euclidean division (rem >= 0)

			wantFloor := quo.Int64()
			wantCeil := wantFloor
			if rem.Sign() != 0 {
				wantCeil++
			}

			if gotFloor != wantFloor {
				t.Fatalf("floor mismatch for r=%d, d=%d: got %d want %d", r, d, gotFloor, wantFloor)
			}
			if gotCeil != wantCeil {
				t.Fatalf("ceil mismatch for r=%d, d=%d: got %d want %d", r, d, gotCeil, wantCeil)
			}
		}
	}
}

func TestFloorCeilDiv(t *testing.T) {
	testCases := []struct {
		a, b      int64
		wantFloor int64
		wantCeil  int64
	}{
		{5, 2, 2, 3},
		{4, 2, 2, 2},
		{-5, 2, -3, -2},
		{-4, 2, -2, -2},
		{5, -2, -3, -2},
		{-5, -2, 2, 3},
	}
	for _, tc := range testCases {
		f := FloorDiv(tc.a, tc.b)
		c := CeilDiv(tc.a, tc.b)
		if f != tc.wantFloor || c != tc.wantCeil {
			t.Fatalf("div %d/%d: got floor=%d ceil=%d, want floor=%d ceil=%d",
				tc.a, tc.b, f, c, tc.wantFloor, tc.wantCeil)
		}
	}
}
