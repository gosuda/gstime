package gstime

import (
	"github.com/gosuda/gstime/core"
)

type RawNanos = core.RawNanos
type GstInstant = core.GstInstant
type DurationNs = core.DurationNs
type PhaseNs = core.PhaseNs
type ErrorNs = core.ErrorNs
type PollExp = core.PollExp
type FaultDomainID = core.FaultDomainID
type Generation = core.Generation
type SyncStatus = core.SyncStatus

const (
	StatusUnanchored = core.StatusUnanchored
	StatusSynced     = core.StatusSynced
	StatusHoldover   = core.StatusHoldover
	StatusDesync     = core.StatusDesync
)

type StatusReason = core.StatusReason

const (
	ReasonNone                  = core.ReasonNone
	ReasonNoEligibleDomains     = core.ReasonNoEligibleDomains
	ReasonInsufficientDomains   = core.ReasonInsufficientDomains
	ReasonRequiredSourceMissing = core.ReasonRequiredSourceMissing
	ReasonEmptyCoverageSet      = core.ReasonEmptyCoverageSet
	ReasonDomainInconsistent    = core.ReasonDomainInconsistent
	ReasonAssuranceConflict     = core.ReasonAssuranceConflict
	ReasonRawDiscontinuity      = core.ReasonRawDiscontinuity
	ReasonRawBoundExpired       = core.ReasonRawBoundExpired
	ReasonRawScaleInvalid       = core.ReasonRawScaleInvalid
	ReasonLeapHistoryMismatch   = core.ReasonLeapHistoryMismatch
	ReasonConfigurationChanged  = core.ReasonConfigurationChanged
	ReasonBoundTooWide          = core.ReasonBoundTooWide
	ReasonBoundTooOld           = core.ReasonBoundTooOld
	ReasonArithmeticOverflow    = core.ReasonArithmeticOverflow
	ReasonPublicBoundTooWide    = core.ReasonPublicBoundTooWide
	ReasonPublicationInvariant  = core.ReasonPublicationInvariant
	ReasonAuthenticationPolicy  = core.ReasonAuthenticationPolicy
	ReasonOperatorInvalidation  = core.ReasonOperatorInvalidation
)

type Decision = core.Decision

const (
	CertainYes = core.CertainYes
	CertainNo  = core.CertainNo
	Unknown    = core.Unknown
)

type ApiError = core.ApiError

const (
	ErrUnanchored             = core.ErrUnanchored
	ErrDesynchronized         = core.ErrDesynchronized
	ErrBoundExpired           = core.ErrBoundExpired
	ErrRawFault               = core.ErrRawFault
	ErrLeapHistoryMismatch    = core.ErrLeapHistoryMismatch
	ErrConfigurationMismatch  = core.ErrConfigurationMismatch
	ErrPublicationUnavailable = core.ErrPublicationUnavailable
	ErrArithmeticFault        = core.ErrArithmeticFault
	ErrCancelled              = core.ErrCancelled
	ErrDeadlineExceeded       = core.ErrDeadlineExceeded
)

type TimeInterval = core.TimeInterval
type AssuredNow = core.AssuredNow
type PublicAssuredNow = core.PublicAssuredNow

type RateFrac = core.RateFrac
type RateScale = core.RateScale
type BoundRateFrac = core.BoundRateFrac

const (
	FracBits = core.FracBits
	OneQ48   = core.OneQ48
)

var (
	RateFromPpmEstimate = core.RateFromPpmEstimate
	RateFromPpmLower    = core.RateFromPpmLower
	RateFromPpmUpper    = core.RateFromPpmUpper
	PpmFromRate         = core.PpmFromRate
	DeltaRate           = core.DeltaRate
	ComposeRate         = core.ComposeRate
)

type GregorianDate = core.GregorianDate
type UtcLabel = core.UtcLabel
type UnixNanos = core.UnixNanos
type UnixProjection = core.UnixProjection
type LeapEntry = core.LeapEntry
type LeapHistory = core.LeapHistory

var (
	NewLeapHistory   = core.NewLeapHistory
	ParseLeapHistory = core.ParseLeapHistory
)
