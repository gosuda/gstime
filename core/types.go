package core

import (
	"errors"
	"math"
)

// RawNanos is a monotonically non-decreasing counter supplied by RawClock.
type RawNanos uint64

// GstInstant is signed nanoseconds on a continuous SI-second axis whose zero is 1970-01-01T00:00:00 UTC.
type GstInstant int64

// DurationNs is a signed duration in nanoseconds.
type DurationNs int64

// PhaseNs is a signed phase correction in nanoseconds; positive advances the estimate.
type PhaseNs int64

// ErrorNs is a non-negative uncertainty bound in nanoseconds.
type ErrorNs uint64

// ErrorInfinity represents an unbounded or unknown error.
const ErrorInfinity ErrorNs = math.MaxUint64

// PollExp is a base-2 poll interval exponent in seconds.
type PollExp int8

// FaultDomainID is an independent failure-domain identifier.
type FaultDomainID string

// Generation is a publication or mapping generation counter.
type Generation uint64

// SyncStatus represents the synchronization state machine status.
type SyncStatus uint8

const (
	StatusUnanchored SyncStatus = iota
	StatusSynced
	StatusHoldover
	StatusDesync
)

func (s SyncStatus) String() string {
	switch s {
	case StatusUnanchored:
		return "UNANCHORED"
	case StatusSynced:
		return "SYNCED"
	case StatusHoldover:
		return "HOLDOVER"
	case StatusDesync:
		return "DESYNC"
	default:
		return "UNKNOWN"
	}
}

// StatusReason enumerates reasons for status transitions and degraded states.
type StatusReason string

const (
	ReasonNone                  StatusReason = "NONE"
	ReasonNoEligibleDomains     StatusReason = "NO_ELIGIBLE_DOMAINS"
	ReasonInsufficientDomains   StatusReason = "INSUFFICIENT_DOMAINS"
	ReasonRequiredSourceMissing StatusReason = "REQUIRED_SOURCE_MISSING"
	ReasonEmptyCoverageSet      StatusReason = "EMPTY_COVERAGE_SET"
	ReasonDomainInconsistent    StatusReason = "DOMAIN_INCONSISTENT"
	ReasonAssuranceConflict     StatusReason = "ASSURANCE_CONFLICT"
	ReasonRawDiscontinuity      StatusReason = "RAW_DISCONTINUITY"
	ReasonRawBoundExpired       StatusReason = "RAW_BOUND_EXPIRED"
	ReasonRawScaleInvalid       StatusReason = "RAW_SCALE_INVALID"
	ReasonLeapHistoryMismatch   StatusReason = "LEAP_HISTORY_MISMATCH"
	ReasonConfigurationChanged  StatusReason = "CONFIGURATION_CHANGED"
	ReasonBoundTooWide          StatusReason = "BOUND_TOO_WIDE"
	ReasonBoundTooOld           StatusReason = "BOUND_TOO_OLD"
	ReasonArithmeticOverflow    StatusReason = "ARITHMETIC_OVERFLOW"
	ReasonPublicBoundTooWide    StatusReason = "PUBLIC_BOUND_TOO_WIDE"
	ReasonPublicationInvariant  StatusReason = "PUBLICATION_INVARIANT"
	ReasonAuthenticationPolicy  StatusReason = "AUTHENTICATION_POLICY"
	ReasonOperatorInvalidation  StatusReason = "OPERATOR_INVALIDATION"
)

// Decision represents a tri-state certainty result.
type Decision uint8

const (
	CertainYes Decision = iota
	CertainNo
	Unknown
)

func (d Decision) String() string {
	switch d {
	case CertainYes:
		return "CertainYes"
	case CertainNo:
		return "CertainNo"
	default:
		return "Unknown"
	}
}

// ApiError defines API-level errors.
type ApiError string

const (
	ErrUnanchored             ApiError = "Unanchored"
	ErrDesynchronized         ApiError = "Desynchronized"
	ErrBoundExpired           ApiError = "BoundExpired"
	ErrRawFault               ApiError = "RawFault"
	ErrLeapHistoryMismatch    ApiError = "LeapHistoryMismatch"
	ErrConfigurationMismatch  ApiError = "ConfigurationMismatch"
	ErrPublicationUnavailable ApiError = "PublicationUnavailable"
	ErrArithmeticFault        ApiError = "ArithmeticFault"
	ErrCancelled              ApiError = "Cancelled"
	ErrDeadlineExceeded       ApiError = "DeadlineExceeded"
)

func (e ApiError) Error() string {
	return string(e)
}

// Standard errors for internal checks.
var (
	ErrOverflow     = errors.New("arithmetic overflow")
	ErrInvalidRange = errors.New("invalid range: lower exceeds upper")
)

// TimeInterval is a closed interval [Earliest, Latest] on the GstInstant axis.
type TimeInterval struct {
	Earliest GstInstant
	Latest   GstInstant
}

// AssuredNow contains the published state evaluated at a single raw reading.
type AssuredNow struct {
	Interval            TimeInterval
	HasInterval         bool
	Estimate            GstInstant
	HasEstimate         bool
	SymmetricEpsilon    ErrorNs
	HasSymmetricEpsilon bool
	Status              SyncStatus
	Reason              StatusReason
	AssuranceGeneration Generation
	MappingGeneration   Generation
	AssuranceEpochID    [16]byte
	LeapHistoryID       [32]byte
	ConfigID            [32]byte
	FaultBudget         uint32
	EligibleDomains     uint32
	Age                 DurationNs
}

// PublicAssuredNow represents the public clock's assured presentation view.
type PublicAssuredNow struct {
	Center                 GstInstant
	Interval               TimeInterval
	HasInterval            bool
	PublicSymmetricEpsilon ErrorNs
	Status                 SyncStatus
	Reason                 StatusReason
}

// Default normative constants from Appendix A.
const (
	MaxEndpoints             = 64
	MaxFaultDomains          = 32
	MaxEstimateSamples       = 64
	MaxOutstandingPerPeer    = 4
	MinPollDefault           = PollExp(6)
	MaxPollDefault           = PollExp(10)
	InitialBurstGoodDefault  = 4
	InitialBurstTxDefault    = 8
	CookieCapacityDefault    = 8
	CookieTargetDefault      = 8
	CookieLowWatermarkDef    = 2
	CookieBurstReserveDef    = 1
	NtsKeRequestCapDefault   = 16 * 1024
	NtsKeResponseCapDefault  = 64 * 1024
	FaultBudgetDefault       = 1
	MinVotingDomainsDefault  = 3
	MinHonestCoverageDefault = 2
	MaxAssuranceWidthDefault = 32 * 1_000_000_000 // 32 seconds in ns
	RunsAlphaDefault         = 0.05
	BaseRateLimitPpmDefault  = 500.0
	AbsRateMathLimitPpm      = 500_000.0
	MaxSlewRateFrac          = 1.0 / 12.0 // ~83,333.33 ppm
	MinSlewDurationNs        = 1 * 1_000_000_000
	MaxSlewDurationNs        = 10_000 * 1_000_000_000
	PhaseNegligibleNs        = PhaseNs(1)
	PublicEpsilonCapDefault  = 16 * 1_000_000_000 // 16 seconds in ns
	SmearMaxRatePpmDefault   = 100.0
	SmearMaxWanderPpmSecDef  = 10.0
)
