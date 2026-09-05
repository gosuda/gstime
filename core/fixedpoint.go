package core

import (
	"errors"
	"math"
	"math/bits"
)

// RateFrac is a signed dimensionless rate represented as Q16.48 fixed-point integer.
// 1.0 is (1 << 48) = 281,474,976,710,656.
// 1 ppm = 1e-6 = 281,474,976.710656...
type RateFrac int64

// RateScale represents true-SI nanoseconds per raw nanosecond in Q16.48.
type RateScale int64

// BoundRateFrac is a non-negative rate bound in Q16.48.
type BoundRateFrac uint64

const (
	FracBits = 48
	OneQ48   = RateFrac(1) << FracBits // 1.0 in Q16.48
)

var (
	errSingularRate = errors.New("rate composition denominator is zero")
)

// RateFromPpmEstimate converts parts-per-million to RateFrac with round-to-nearest.
func RateFromPpmEstimate(ppm float64) RateFrac {
	val := ppm * float64(OneQ48) / 1e6
	if val >= 0 {
		return RateFrac(val + 0.5)
	}
	return RateFrac(val - 0.5)
}

// RateFromPpmLower converts parts-per-million to RateFrac rounding toward -infinity (floor_out for lower bound).
func RateFromPpmLower(ppm float64) RateFrac {
	return RateFrac(math.Floor(ppm * float64(OneQ48) / 1e6))
}

// RateFromPpmUpper converts parts-per-million to RateFrac rounding toward +infinity (ceil_out for upper bound).
func RateFromPpmUpper(ppm float64) RateFrac {
	return RateFrac(math.Ceil(ppm * float64(OneQ48) / 1e6))
}

// PpmFromRate converts RateFrac to parts-per-million.
func PpmFromRate(r RateFrac) float64 {
	return float64(r) * 1e6 / float64(OneQ48)
}

// DeltaRate computes (newRate - oldRate) / (1 + oldRate) in Q16.48.
func DeltaRate(oldRate, newRate RateFrac) (RateFrac, error) {
	denom := int64(OneQ48 + oldRate)
	if denom == 0 {
		return 0, errSingularRate
	}
	diff := int64(newRate - oldRate)
	q, err := div128(diff, denom)
	if err != nil {
		return 0, err
	}
	return RateFrac(q), nil
}

// ComposeRate computes oldRate + relativeDelta * (1 + oldRate) in Q16.48.
func ComposeRate(oldRate, relativeDelta RateFrac) (RateFrac, error) {
	scale := int64(OneQ48 + oldRate)
	prod, err := mul128Shift48(int64(relativeDelta), scale)
	if err != nil {
		return 0, err
	}
	res := oldRate + RateFrac(prod)
	return res, nil
}

// mul128 computes exact signed 128-bit product of a and b: hi (signed), lo (unsigned).
func mul128(a, b int64) (hi int64, lo uint64) {
	if a == 0 || b == 0 {
		return 0, 0
	}
	neg := (a < 0) != (b < 0)

	ua := uint64(a)
	if a < 0 {
		ua = uint64(-a)
	}
	ub := uint64(b)
	if b < 0 {
		ub = uint64(-b)
	}

	uhi, ulo := bits.Mul64(ua, ub)
	if neg {
		ulo = ^ulo + 1
		uhi = ^uhi
		if ulo == 0 {
			uhi++
		}
	}
	return int64(uhi), ulo
}

func isQ48Overflow(hi int64) bool {
	check := hi >> 47
	if hi >= 0 {
		return check != 0
	}
	return check != -1
}

// mul128Shift48 multiplies a and b in Q16.48 and returns (a * b) >> 48.
func mul128Shift48(a, b int64) (int64, error) {
	hi, lo := mul128(a, b)
	if isQ48Overflow(hi) {
		return 0, ErrOverflow
	}
	q := (hi << 16) | int64(lo>>FracBits)
	return q, nil
}

// div128 computes (a << 48) / b.
func div128(a, b int64) (int64, error) {
	if b == 0 {
		return 0, ErrOverflow
	}
	neg := (a < 0) != (b < 0)

	ua := uint64(a)
	if a < 0 {
		ua = uint64(-a)
	}
	ub := uint64(b)
	if b < 0 {
		ub = uint64(-b)
	}

	hi := ua >> (64 - FracBits)
	lo := ua << FracBits

	quo, _ := bits.Div64(hi, lo, ub)
	if quo > math.MaxInt64 {
		if neg && quo == uint64(math.MaxInt64)+1 {
			return math.MinInt64, nil
		}
		return 0, ErrOverflow
	}
	if neg {
		return -int64(quo), nil
	}
	return int64(quo), nil
}

// MulRateDurationFloor computes floor_out(rate * d) in nanoseconds (toward -infinity).
func MulRateDurationFloor(rate RateFrac, d DurationNs) (int64, error) {
	hi, lo := mul128(int64(rate), int64(d))
	if isQ48Overflow(hi) {
		return 0, ErrOverflow
	}
	q := (hi << 16) | int64(lo>>FracBits)
	return q, nil
}

// MulRateDurationCeil computes ceil_out(rate * d) in nanoseconds (toward +infinity).
func MulRateDurationCeil(rate RateFrac, d DurationNs) (int64, error) {
	hi, lo := mul128(int64(rate), int64(d))
	if isQ48Overflow(hi) {
		return 0, ErrOverflow
	}
	q := (hi << 16) | int64(lo>>FracBits)
	rem := lo & ((uint64(1) << FracBits) - 1)
	if rem != 0 {
		if q == math.MaxInt64 {
			return 0, ErrOverflow
		}
		q++
	}
	return q, nil
}

// MulScaleDurationFloor computes floor_out(scale * dr) for non-negative raw delta.
func MulScaleDurationFloor(scale RateScale, dr RawNanos) (int64, error) {
	hi, lo := mul128(int64(scale), int64(dr))
	if isQ48Overflow(hi) {
		return 0, ErrOverflow
	}
	q := (hi << 16) | int64(lo>>FracBits)
	return q, nil
}

// MulScaleDurationCeil computes ceil_out(scale * dr) for non-negative raw delta.
func MulScaleDurationCeil(scale RateScale, dr RawNanos) (int64, error) {
	hi, lo := mul128(int64(scale), int64(dr))
	if isQ48Overflow(hi) {
		return 0, ErrOverflow
	}
	q := (hi << 16) | int64(lo>>FracBits)
	rem := lo & ((uint64(1) << FracBits) - 1)
	if rem != 0 {
		if q == math.MaxInt64 {
			return 0, ErrOverflow
		}
		q++
	}
	return q, nil
}

func hasOppositeSign(r, b int64) bool {
	if r > 0 {
		return b < 0
	}
	if r < 0 {
		return b > 0
	}
	return false
}

func hasSameSign(r, b int64) bool {
	if r > 0 {
		return b > 0
	}
	if r < 0 {
		return b < 0
	}
	return false
}

// FloorDiv performs integer division of a by b rounding toward negative infinity.
func FloorDiv(a, b int64) int64 {
	d := a / b
	r := a % b
	if hasOppositeSign(r, b) {
		d--
	}
	return d
}

// CeilDiv performs integer division of a by b rounding toward positive infinity.
func CeilDiv(a, b int64) int64 {
	d := a / b
	r := a % b
	if hasSameSign(r, b) {
		d++
	}
	return d
}
