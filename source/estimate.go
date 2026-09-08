package source

import (
	"errors"
	"math"
	"sort"

	"gosuda.org/gstime/core"
)

var (
	ErrNotEnoughSamples      = errors.New("not enough samples for regression")
	ErrSingularDesignMatrix  = errors.New("singular design matrix in weighted regression")
	ErrInvalidEstimateSample = errors.New("nonfinite estimate sample or distance")
	ErrUnstableRegression    = errors.New("nonfinite or unstable weighted regression")
)

const (
	MaxSamplesPerSource  = 64
	MinRegressionSamples = 3
	DefaultRunsAlpha     = 0.05
	EstimateQuantumSec   = 1e-9
	MachineEpsilon       = 2.220446049250313e-16
)

// EstimateSample is a single measurement retained by EstimateTrack.
type EstimateSample struct {
	RawMid                 core.RawNanos
	PhaseErrorSlowPositive float64 // in seconds
	RoundTripDelay         float64 // in seconds
	RootDelay              float64 // in seconds
	RootDispersion         float64 // in seconds
}

// RegressionResult contains fitted parameters and statistical telemetry (Section 4.5).
type RegressionResult struct {
	PhaseEstimate             float64 // phase at newest sample in seconds
	RateErrorEstimate         float64 // dimensionless rate error
	PhaseStandardError        float64
	RateStandardError         float64
	ResidualStandardDeviation float64
	ResidualRunsPValue        float64
	SampleCount               int
}

// EstimateTrack maintains sample history and performs weighted regression.
type EstimateTrack struct {
	samples []EstimateSample
}

// NewEstimateTrack creates a new EstimateTrack instance.
func NewEstimateTrack() *EstimateTrack {
	return &EstimateTrack{
		samples: make([]EstimateSample, 0, MaxSamplesPerSource),
	}
}

// AddSample ignores duplicate or out-of-order stamps and rejects nonfinite inputs
// without changing the retained history.
func (et *EstimateTrack) AddSample(s EstimateSample) error {
	if !finiteEstimateValue(s.PhaseErrorSlowPositive) ||
		!finiteEstimateValue(s.RoundTripDelay) ||
		!finiteEstimateValue(s.RootDelay) ||
		!finiteEstimateValue(s.RootDispersion) ||
		!finiteEstimateValue(s.RootDispersion+s.RootDelay/2+s.RoundTripDelay/2) {
		return ErrInvalidEstimateSample
	}
	if len(et.samples) > 0 && s.RawMid <= et.samples[len(et.samples)-1].RawMid {
		return nil
	}
	if len(et.samples) == MaxSamplesPerSource {
		copy(et.samples, et.samples[1:])
		et.samples = et.samples[:MaxSamplesPerSource-1]
	}
	et.samples = append(et.samples, s)
	return nil
}

// Fit performs weighted regression and residual-runs pruning (Section 4.5 & 4.6).
func (et *EstimateTrack) Fit() (*RegressionResult, error) {
	if len(et.samples) < MinRegressionSamples {
		return nil, ErrNotEnoughSamples
	}

	// Repeatedly prune oldest sample while runs p-value < DefaultRunsAlpha
	currentSamples := et.samples
	for len(currentSamples) >= MinRegressionSamples {
		res, err := fitSamples(currentSamples)
		if err != nil {
			return nil, err
		}

		if res.ResidualRunsPValue >= DefaultRunsAlpha || len(currentSamples) == MinRegressionSamples {
			if len(currentSamples) != len(et.samples) {
				copy(et.samples, currentSamples)
				et.samples = et.samples[:len(currentSamples)]
			}
			return res, nil
		}
		// Prune oldest sample
		currentSamples = currentSamples[1:]
	}

	return fitSamples(currentSamples)
}

func fitSamples(samples []EstimateSample) (*RegressionResult, error) {
	n := len(samples)
	if n < MinRegressionSamples {
		return nil, ErrNotEnoughSamples
	}

	// Compute distance metrics d_i = RootDisp + RootDelay/2 + RoundTrip/2
	d := make([]float64, n)
	dSorted := make([]float64, n)
	for i, s := range samples {
		dist := s.RootDispersion + s.RootDelay/2.0 + s.RoundTripDelay/2.0
		d[i] = dist
		dSorted[i] = dist
	}
	sort.Float64s(dSorted)
	m := dSorted[0]
	q := dSorted[n/2]

	scale := (q - m) / 0.7
	if !finiteEstimateValue(scale) {
		return nil, ErrUnstableRegression
	}
	if scale < EstimateQuantumSec {
		scale = EstimateQuantumSec
	}

	w := make([]float64, n)
	for i := range samples {
		diff := d[i] - m
		if diff < 0 {
			diff = 0
		}
		div := 1.0 + diff/scale
		denominator := div * div
		if !finiteEstimateValue(denominator) || denominator <= 0 {
			return nil, ErrUnstableRegression
		}
		w[i] = 1.0 / denominator
		if !finiteEstimateValue(w[i]) || w[i] <= 0 {
			return nil, ErrUnstableRegression
		}
	}

	newestRaw := samples[n-1].RawMid
	x := make([]float64, n)
	y := make([]float64, n)

	for i, s := range samples {
		dtNanos := uint64(newestRaw) - uint64(s.RawMid)
		x[i] = -float64(dtNanos) / 1e9
		y[i] = s.PhaseErrorSlowPositive
	}

	var sumW, sumWX, sumWY, sumWXX, sumWXY float64
	for i := 0; i < n; i++ {
		sumW += w[i]
		sumWX += w[i] * x[i]
		sumWY += w[i] * y[i]
		sumWXX += w[i] * x[i] * x[i]
		sumWXY += w[i] * x[i] * y[i]
	}

	if !finiteEstimateValue(sumW) || !finiteEstimateValue(sumWX) ||
		!finiteEstimateValue(sumWY) || !finiteEstimateValue(sumWXX) ||
		!finiteEstimateValue(sumWXY) {
		return nil, ErrUnstableRegression
	}
	diagonalProduct := sumW * sumWXX
	offDiagonalProduct := sumWX * sumWX
	if !finiteEstimateValue(diagonalProduct) || !finiteEstimateValue(offDiagonalProduct) {
		return nil, ErrUnstableRegression
	}
	delta := diagonalProduct - offDiagonalProduct
	if sumW <= 0 || sumWXX <= 0 || delta < max(1e-18, 8*MachineEpsilon*diagonalProduct) {
		return nil, ErrSingularDesignMatrix
	}

	intercept := (sumWXX*sumWY - sumWX*sumWXY) / delta
	slope := (sumW*sumWXY - sumWX*sumWY) / delta
	if !finiteEstimateValue(intercept) || !finiteEstimateValue(slope) {
		return nil, ErrUnstableRegression
	}

	residuals := make([]float64, n)
	var sumWRes2 float64
	var maxAbsModel float64
	for i := 0; i < n; i++ {
		fitted := intercept + slope*x[i]
		res := y[i] - fitted
		if !finiteEstimateValue(fitted) || !finiteEstimateValue(res) {
			return nil, ErrUnstableRegression
		}
		residuals[i] = res
		sumWRes2 += w[i] * res * res
		if math.Abs(fitted) > maxAbsModel {
			maxAbsModel = math.Abs(fitted)
		}
	}

	if !finiteEstimateValue(sumWRes2) || sumWRes2 < 0 {
		return nil, ErrUnstableRegression
	}

	dof := float64(n - 2)
	resStd := math.Sqrt(sumWRes2 / (sumW * (dof / float64(n))))
	phaseStdErr := math.Sqrt(sumWXX/delta) * resStd
	rateStdErr := math.Sqrt(sumW/delta) * resStd
	if !finiteEstimateValue(resStd) || !finiteEstimateValue(phaseStdErr) || !finiteEstimateValue(rateStdErr) {
		return nil, ErrUnstableRegression
	}

	pVal := ComputeRunsPValue(residuals, maxAbsModel)
	if !finiteEstimateValue(pVal) || pVal < 0 || pVal > 1 {
		return nil, ErrUnstableRegression
	}

	return &RegressionResult{
		PhaseEstimate:             intercept,
		RateErrorEstimate:         slope,
		PhaseStandardError:        phaseStdErr,
		RateStandardError:         rateStdErr,
		ResidualStandardDeviation: resStd,
		ResidualRunsPValue:        pVal,
		SampleCount:               n,
	}, nil
}

// ComputeRunsPValue calculates the exact conditional two-sided p-value for runs (Section 4.6).
// Invalid inputs or an unrepresentable distribution return NaN.
func ComputeRunsPValue(residuals []float64, maxAbsModelVal float64) float64 {
	if !finiteEstimateValue(maxAbsModelVal) || maxAbsModelVal < 0 {
		return math.NaN()
	}
	deadband := 8.0 * MachineEpsilon * maxAbsModelVal
	if deadband < EstimateQuantumSec {
		deadband = EstimateQuantumSec
	}

	var signs []int
	var nPos, nNeg int
	for _, r := range residuals {
		if !finiteEstimateValue(r) {
			return math.NaN()
		}
		if r > deadband {
			signs = append(signs, 1)
			nPos++
		} else if r < -deadband {
			signs = append(signs, -1)
			nNeg++
		}
	}

	if nPos == 0 || nNeg == 0 || len(signs) < 2 {
		return 1.0
	}

	// Count observed runs
	rObs := 1
	for i := 1; i < len(signs); i++ {
		if signs[i] != signs[i-1] {
			rObs++
		}
	}

	// Exact combinatorial distribution of runs:
	// Let N = nPos + nNeg.
	// Number of permutations with r runs:
	// If r = 2k (even): 2 * C(nPos-1, k-1) * C(nNeg-1, k-1)
	// If r = 2k+1 (odd): C(nPos-1, k) * C(nNeg-1, k-1) + C(nPos-1, k-1) * C(nNeg-1, k)
	maxR := 2*min(nPos, nNeg) + 1
	if nPos == nNeg {
		maxR = 2 * nPos
	}

	counts := make(map[int]float64)
	totalPerms := math.Exp(logComb(nPos+nNeg, nPos))
	if !finiteEstimateValue(totalPerms) || totalPerms <= 0 {
		return math.NaN()
	}

	for r := 2; r <= maxR; r++ {
		var cnt float64
		if r%2 == 0 {
			k := r / 2
			if k-1 <= nPos-1 && k-1 <= nNeg-1 && k-1 >= 0 {
				logC := math.Ln2 + logComb(nPos-1, k-1) + logComb(nNeg-1, k-1)
				cnt = math.Exp(logC)
			}
		} else {
			k := (r - 1) / 2
			var t1, t2 float64
			if k <= nPos-1 && k-1 <= nNeg-1 && k-1 >= 0 {
				t1 = math.Exp(logComb(nPos-1, k) + logComb(nNeg-1, k-1))
			}
			if k-1 <= nPos-1 && k <= nNeg-1 && k-1 >= 0 {
				t2 = math.Exp(logComb(nPos-1, k-1) + logComb(nNeg-1, k))
			}
			cnt = t1 + t2
		}
		if !finiteEstimateValue(cnt) {
			return math.NaN()
		}
		if cnt > 0 {
			counts[r] = cnt
		}
	}

	obsCount := counts[rObs]
	if obsCount <= 0 {
		return math.NaN()
	}
	var pSum float64
	for _, cnt := range counts {
		if cnt <= obsCount*(1.0+1e-12) {
			pSum += cnt / totalPerms
		}
	}
	if !finiteEstimateValue(pSum) {
		return math.NaN()
	}

	return math.Min(1.0, pSum)
}

func logComb(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1)
	}
	if k == 0 || k == n {
		return 0
	}
	lg1, _ := math.Lgamma(float64(n + 1))
	lg2, _ := math.Lgamma(float64(k + 1))
	lg3, _ := math.Lgamma(float64(n - k + 1))
	return lg1 - lg2 - lg3
}

func finiteEstimateValue(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
