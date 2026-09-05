package source

import (
	"errors"
	"sort"

	"github.com/gosuda/gstime/core"
)

var (
	ErrInsufficientDomains = errors.New("insufficient eligible fault domains")
	ErrEmptyCoverageSet    = errors.New("empty N-F coverage set")
	ErrInvalidFaultBudget  = errors.New("invalid fault budget configuration")
)

// ConsolidatedDomainInterval represents the single consolidated interval of a fault domain.
type ConsolidatedDomainInterval struct {
	DomainID     core.FaultDomainID
	Interval     core.TimeInterval
	Inconsistent bool
}

// ConsolidateDomainIntervals computes the intersection of all eligible endpoint intervals in a domain (Section 4.2).
func ConsolidateDomainIntervals(domainID core.FaultDomainID, intervals []core.TimeInterval) ConsolidatedDomainInterval {
	if len(intervals) == 0 {
		return ConsolidatedDomainInterval{
			DomainID:     domainID,
			Inconsistent: true,
		}
	}

	maxLo := intervals[0].Earliest
	minHi := intervals[0].Latest

	for i := 1; i < len(intervals); i++ {
		if intervals[i].Earliest > maxLo {
			maxLo = intervals[i].Earliest
		}
		if intervals[i].Latest < minHi {
			minHi = intervals[i].Latest
		}
	}

	if maxLo > minHi {
		// Empty intersection: domain is internally inconsistent
		return ConsolidatedDomainInterval{
			DomainID:     domainID,
			Inconsistent: true,
		}
	}

	return ConsolidatedDomainInterval{
		DomainID: domainID,
		Interval: core.TimeInterval{
			Earliest: maxLo,
			Latest:   minHi,
		},
		Inconsistent: false,
	}
}

// AssuranceConsensusResult contains the published hull, components, and primary component.
type AssuranceConsensusResult struct {
	Hull             core.TimeInterval
	Components       []core.TimeInterval
	PrimaryComponent core.TimeInterval
	ControlTarget    core.GstInstant
	ControlBridge    core.DurationNs
	EligibleDomains  int
	FaultBudget      int
	ThresholdK       int
}

// ComputeCoverageComponents implements the exact N-F coverage sweep from Appendix B.
func ComputeCoverageComponents(intervals []core.TimeInterval, F int) ([]core.TimeInterval, error) {
	N := len(intervals)
	if N < 2*F+1 {
		return nil, ErrInsufficientDomains
	}
	k := N - F

	type event struct {
		lows  int
		highs int
	}
	events := make(map[core.GstInstant]*event)

	for _, I := range intervals {
		if I.Earliest > I.Latest {
			return nil, core.ErrInvalidRange
		}
		if events[I.Earliest] == nil {
			events[I.Earliest] = &event{}
		}
		events[I.Earliest].lows++

		if events[I.Latest] == nil {
			events[I.Latest] = &event{}
		}
		events[I.Latest].highs++
	}

	xs := make([]core.GstInstant, 0, len(events))
	for x := range events {
		xs = append(xs, x)
	}
	sort.Slice(xs, func(i, j int) bool {
		return xs[i] < xs[j]
	})

	depthBetween := 0
	var components []core.TimeInterval
	var openComp *core.TimeInterval

	for index, x := range xs {
		e := events[x]
		depthAtPoint := depthBetween + e.lows
		pointIncluded := depthAtPoint >= k

		depthAfter := depthAtPoint - e.highs
		spanIncluded := (index+1 < len(xs)) && (depthAfter >= k)

		if pointIncluded {
			if openComp == nil {
				openComp = &core.TimeInterval{Earliest: x, Latest: x}
			} else {
				openComp.Latest = x
			}
		}

		if spanIncluded {
			if openComp == nil {
				openComp = &core.TimeInterval{Earliest: x, Latest: x}
			}
			openComp.Latest = xs[index+1]
		} else if openComp != nil {
			components = append(components, *openComp)
			openComp = nil
		}

		depthBetween = depthAfter
	}

	if openComp != nil {
		components = append(components, *openComp)
	}

	components = mergeTouchingComponents(components)
	return components, nil
}

func mergeTouchingComponents(comps []core.TimeInterval) []core.TimeInterval {
	if len(comps) <= 1 {
		return comps
	}
	merged := make([]core.TimeInterval, 0, len(comps))
	current := comps[0]

	for i := 1; i < len(comps); i++ {
		if comps[i].Earliest <= current.Latest {
			if comps[i].Latest > current.Latest {
				current.Latest = comps[i].Latest
			}
		} else {
			merged = append(merged, current)
			current = comps[i]
		}
	}
	merged = append(merged, current)
	return merged
}

// SelectPrimaryComponent deterministically chooses the control component (Section 4.14).
func SelectPrimaryComponent(components []core.TimeInterval, priorTarget core.GstInstant, intervals []core.TimeInterval) core.TimeInterval {
	if len(components) == 1 {
		return components[0]
	}

	type compMetric struct {
		comp     core.TimeInterval
		contains bool
		dist     core.DurationNs
		maxDepth int
		width    core.DurationNs
	}

	metrics := make([]compMetric, len(components))
	for i, c := range components {
		contains := c.Earliest <= priorTarget && priorTarget <= c.Latest
		var dist core.DurationNs
		if contains {
			dist = 0
		} else if priorTarget < c.Earliest {
			dist = core.DurationNs(c.Earliest - priorTarget)
		} else {
			dist = core.DurationNs(priorTarget - c.Latest)
		}

		// Calculate max depth within component
		mid := c.Earliest + (c.Latest-c.Earliest)/2
		depth := 0
		for _, inv := range intervals {
			if inv.Earliest <= mid && mid <= inv.Latest {
				depth++
			}
		}

		metrics[i] = compMetric{
			comp:     c,
			contains: contains,
			dist:     dist,
			maxDepth: depth,
			width:    core.DurationNs(c.Latest - c.Earliest),
		}
	}

	// Tie-breaking:
	// 1. contains prior target
	// 2. minimum distance to prior target
	// 3. greater depth
	// 4. smaller width
	// 5. lower endpoint
	sort.SliceStable(metrics, func(i, j int) bool {
		m1 := metrics[i]
		m2 := metrics[j]

		if m1.contains != m2.contains {
			return m1.contains // prefer containing prior target
		}
		if m1.dist != m2.dist {
			return m1.dist < m2.dist // prefer minimum distance
		}
		if m1.maxDepth != m2.maxDepth {
			return m1.maxDepth > m2.maxDepth // prefer greater depth
		}
		if m1.width != m2.width {
			return m1.width < m2.width // prefer smaller width
		}
		return m1.comp.Earliest < m2.comp.Earliest // prefer lower endpoint
	})

	return metrics[0].comp
}

// ComputeAssuranceConsensus executes the full assurance round consensus (Sections 4.11-4.14).
func ComputeAssuranceConsensus(
	domainIntervals []core.TimeInterval,
	F int,
	minVotingDomains int,
	minHonestCoverage int,
	priorTarget core.GstInstant,
	estimateTarget core.GstInstant,
) (*AssuranceConsensusResult, error) {
	N := len(domainIntervals)
	minRequired := minVotingDomains
	if 2*F+1 > minRequired {
		minRequired = 2*F + 1
	}
	if N < minRequired {
		return nil, ErrInsufficientDomains
	}

	k := N - F
	if k < minHonestCoverage {
		return nil, ErrInsufficientDomains
	}

	components, err := ComputeCoverageComponents(domainIntervals, F)
	if err != nil {
		return nil, err
	}
	if len(components) == 0 {
		return nil, ErrEmptyCoverageSet
	}

	// Full hull consensus: MUST NOT select only one component
	hull := core.TimeInterval{
		Earliest: components[0].Earliest,
		Latest:   components[len(components)-1].Latest,
	}

	primary := SelectPrimaryComponent(components, priorTarget, domainIntervals)

	// Control target clamped to primary component
	controlTarget := estimateTarget
	if controlTarget < primary.Earliest {
		controlTarget = primary.Earliest
	} else if controlTarget > primary.Latest {
		controlTarget = primary.Latest
	}

	controlBridge := core.DurationNs(controlTarget - estimateTarget)

	return &AssuranceConsensusResult{
		Hull:             hull,
		Components:       components,
		PrimaryComponent: primary,
		ControlTarget:    controlTarget,
		ControlBridge:    controlBridge,
		EligibleDomains:  N,
		FaultBudget:      F,
		ThresholdK:       k,
	}, nil
}
