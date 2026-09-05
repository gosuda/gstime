package source

import (
	"errors"
	"math"
	"sort"

	"github.com/gosuda/gstime/core"
)

var (
	ErrNotEnoughSamples     = errors.New("not enough samples for regression")
	ErrSingularDesignMatrix = errors.New("singular design matrix in weighted regression")
)

const (
	MaxSamplesPerSource   = 64
	MinRegressionSamples  = 3
	DefaultRunsAlpha      = 0.05
	EstimateQuantumSec    = 1e-9
	MachineEpsilon        = 2.220446049250313e-16
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

// AddSample inserts a new sample ordered by RawMid.
func (et *EstimateTrack) AddSample(s EstimateSample) {
	if len(et.samples) > 0 && s.RawMid <= et.samples[len(et.samples)-1].RawMid {
		return // Ignore out-of-order or duplicate raw stamps
	}
	et.samples = append(et.samples, s)
	if len(et.samples) > MaxSamplesPerSource {
		et.samples = et.samples[len(et.samples)-MaxSamplesPerSource:]
	}
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
		w[i] = 1.0 / (div * div)
	}

	newestRaw := samples[n-1].RawMid
	x := make([]float64, n)
	y := make([]float64, n)

	for i, s := range samples {
		dtNanos := int64(s.RawMid) - int64(newestRaw)
		x[i] = float64(dtNanos) / 1e9 // <= 0
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

	delta := sumW*sumWXX - sumWX*sumWX
	if math.Abs(delta) < 1e-18 {
		return nil, ErrSingularDesignMatrix
	}

	intercept := (sumWXX*sumWY - sumWX*sumWXY) / delta
	slope := (sumW*sumWXY - sumWX*sumWY) / delta

	residuals := make([]float64, n)
	var sumWRes2 float64
	var maxAbsModel float64
	for i := 0; i < n; i++ {
		fitted := intercept + slope*x[i]
		res := y[i] - fitted
		residuals[i] = res
		sumWRes2 += w[i] * res * res
		if math.Abs(fitted) > maxAbsModel {
			maxAbsModel = math.Abs(fitted)
		}
	}

	dof := float64(n - 2)
	resStd := math.Sqrt(sumWRes2 / (sumW * (dof / float64(n))))
	phaseStdErr := math.Sqrt(sumWXX/delta) * resStd
	rateStdErr := math.Sqrt(sumW/delta) * resStd

	pVal := ComputeRunsPValue(residuals, maxAbsModel)

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
func ComputeRunsPValue(residuals []float64, maxAbsModelVal float64) float64 {
	deadband := 8.0 * MachineEpsilon * maxAbsModelVal
	if deadband < EstimateQuantumSec {
		deadband = EstimateQuantumSec
	}

	var signs []int
	var nPos, nNeg int
	for _, r := range residuals {
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
		if cnt > 0 {
			counts[r] = cnt
		}
	}

	obsCount := counts[rObs]
	var pSum float64
	for _, cnt := range counts {
		if cnt <= obsCount*(1.0+1e-12) {
			pSum += cnt / totalPerms
		}
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
