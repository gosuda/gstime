package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"time"
)

// GregorianDate represents a civil calendar date.
type GregorianDate struct {
	Year  int32
	Month uint8
	Day   uint8
}

func (d GregorianDate) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// UtcLabel is a structured civil UTC label that can represent an inserted leap second (secondOfDay=86400).
type UtcLabel struct {
	Date          GregorianDate
	SecondOfDay   uint32 // 0..86399 normally, 86400 on positive leap day
	Nanos         uint32 // 0..999,999,999
	LeapHistoryID [32]byte
}

func (u UtcLabel) String() string {
	sec := u.SecondOfDay % 60
	min := (u.SecondOfDay / 60) % 60
	hour := u.SecondOfDay / 3600
	return fmt.Sprintf("%sT%02d:%02d:%02d.%09dZ", u.Date, hour, min, sec, u.Nanos)
}

// UnixNanos is a POSIX-like nanosecond projection from 1970-01-01T00:00:00 UTC without unique leap second labels.
type UnixNanos int64

// UnixProjection represents the POSIX projection with ambiguity telemetry.
type UnixProjection struct {
	Nanos            UnixNanos
	IsLeapSecond     bool
	SourceGstInstant GstInstant
	LeapHistoryID    [32]byte
}

// LeapEntry represents a single leap second transition.
type LeapEntry struct {
	TransitionUnixSecond int64 // POSIX second of 00:00:00 immediately after the event
	Delta                int8  // +1 or -1
}

// LeapHistory holds a validated set of leap entries and its canonical digest.
type LeapHistory struct {
	InitialTaiMinusUtc int32
	Entries            []LeapEntry
	ID                 [32]byte
	canonicalBytes     []byte
}

var (
	ErrInvalidLeapMagic        = errors.New("invalid leap history magic, must be GSTL1")
	ErrUnsortedLeapTransitions = errors.New("leap transitions must be sorted strictly ascending")
	ErrDuplicateLeapTransition = errors.New("duplicate leap transition unix second")
	ErrInvalidLeapDelta        = errors.New("leap transition delta must be exactly +1 or -1")
	ErrReservedBytesNonzero    = errors.New("reserved bytes in leap entry must be zero")
	ErrInvalidSecondOfDay      = errors.New("invalid second of day")
)

const leapMagic = "GSTL1"

// NewLeapHistory creates and validates a LeapHistory from entries.
func NewLeapHistory(initialTaiMinusUtc int32, entries []LeapEntry) (*LeapHistory, error) {
	buf := new(bytes.Buffer)
	buf.WriteString(leapMagic)

	if err := binary.Write(buf, binary.BigEndian, initialTaiMinusUtc); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, uint32(len(entries))); err != nil {
		return nil, err
	}

	var lastSec int64 = -1 << 63
	for i, e := range entries {
		if i > 0 && e.TransitionUnixSecond <= lastSec {
			if e.TransitionUnixSecond == lastSec {
				return nil, ErrDuplicateLeapTransition
			}
			return nil, ErrUnsortedLeapTransitions
		}
		if e.Delta != 1 && e.Delta != -1 {
			return nil, ErrInvalidLeapDelta
		}
		lastSec = e.TransitionUnixSecond

		if err := binary.Write(buf, binary.BigEndian, e.TransitionUnixSecond); err != nil {
			return nil, err
		}
		if err := binary.Write(buf, binary.BigEndian, e.Delta); err != nil {
			return nil, err
		}
		var reserved [7]byte
		buf.Write(reserved[:])
	}

	canon := buf.Bytes()
	id := sha256.Sum256(canon)

	cloned := make([]LeapEntry, len(entries))
	copy(cloned, entries)

	return &LeapHistory{
		InitialTaiMinusUtc: initialTaiMinusUtc,
		Entries:            cloned,
		ID:                 id,
		canonicalBytes:     canon,
	}, nil
}

// ParseLeapHistory parses the canonical GSTL1 encoding.
func ParseLeapHistory(data []byte) (*LeapHistory, error) {
	if len(data) < 13 {
		return nil, errors.New("leap history data too short")
	}
	if string(data[:5]) != leapMagic {
		return nil, ErrInvalidLeapMagic
	}

	initialTai := int32(binary.BigEndian.Uint32(data[5:9]))
	count := binary.BigEndian.Uint32(data[9:13])

	expectedLen := 13 + int(count)*16
	if len(data) != expectedLen {
		return nil, fmt.Errorf("leap history length mismatch: got %d want %d", len(data), expectedLen)
	}

	entries := make([]LeapEntry, count)
	offset := 13
	var lastSec int64 = -1 << 63

	for i := 0; i < int(count); i++ {
		sec := int64(binary.BigEndian.Uint64(data[offset : offset+8]))
		delta := int8(data[offset+8])
		reserved := data[offset+9 : offset+16]

		if i > 0 && sec <= lastSec {
			if sec == lastSec {
				return nil, ErrDuplicateLeapTransition
			}
			return nil, ErrUnsortedLeapTransitions
		}
		if delta != 1 && delta != -1 {
			return nil, ErrInvalidLeapDelta
		}
		for _, b := range reserved {
			if b != 0 {
				return nil, ErrReservedBytesNonzero
			}
		}

		lastSec = sec
		entries[i] = LeapEntry{
			TransitionUnixSecond: sec,
			Delta:                delta,
		}
		offset += 16
	}

	id := sha256.Sum256(data)
	canon := make([]byte, len(data))
	copy(canon, data)

	return &LeapHistory{
		InitialTaiMinusUtc: initialTai,
		Entries:            entries,
		ID:                 id,
		canonicalBytes:     canon,
	}, nil
}

// CanonicalBytes returns the exact canonical encoding of the leap history.
func (lh *LeapHistory) CanonicalBytes() []byte {
	b := make([]byte, len(lh.canonicalBytes))
	copy(b, lh.canonicalBytes)
	return b
}

// GstInstantToUtc converts a continuous GstInstant to UtcLabel.
func (lh *LeapHistory) GstInstantToUtc(inst GstInstant) UtcLabel {
	siNanos := int64(inst)
	siSec := FloorDiv(siNanos, 1_000_000_000)
	nanoRem := uint32(siNanos - siSec*1_000_000_000)

	var cumDelta int64
	for _, e := range lh.Entries {
		if e.Delta == 1 {
			leapSiSec := e.TransitionUnixSecond + cumDelta
			if siSec == leapSiSec {
				prevUnixSec := e.TransitionUnixSecond - 1
				t := time.Unix(prevUnixSec, 0).UTC()
				return UtcLabel{
					Date: GregorianDate{
						Year:  int32(t.Year()),
						Month: uint8(t.Month()),
						Day:   uint8(t.Day()),
					},
					SecondOfDay:   86400,
					Nanos:         nanoRem,
					LeapHistoryID: lh.ID,
				}
			}
		}
		transSiSec := e.TransitionUnixSecond + cumDelta + int64(e.Delta)
		if siSec >= transSiSec {
			cumDelta += int64(e.Delta)
		}
	}

	unixSec := siSec - cumDelta
	t := time.Unix(unixSec, 0).UTC()
	secOfDay := uint32(t.Hour()*3600 + t.Minute()*60 + t.Second())

	return UtcLabel{
		Date: GregorianDate{
			Year:  int32(t.Year()),
			Month: uint8(t.Month()),
			Day:   uint8(t.Day()),
		},
		SecondOfDay:   secOfDay,
		Nanos:         nanoRem,
		LeapHistoryID: lh.ID,
	}
}

// UtcToGstInstant converts a UtcLabel to continuous GstInstant.
func (lh *LeapHistory) UtcToGstInstant(utc UtcLabel) (GstInstant, error) {
	if utc.LeapHistoryID != lh.ID {
		return 0, ErrLeapHistoryMismatch
	}
	if utc.SecondOfDay > 86400 {
		return 0, ErrInvalidSecondOfDay
	}
	if utc.Nanos >= 1_000_000_000 {
		return 0, errors.New("nanos out of range")
	}

	if utc.SecondOfDay == 86400 {
		civilTime := time.Date(int(utc.Date.Year), time.Month(utc.Date.Month), int(utc.Date.Day), 23, 59, 59, 0, time.UTC)
		targetTransition := civilTime.Unix() + 1

		var cumDelta int64
		found := false
		for _, e := range lh.Entries {
			if e.TransitionUnixSecond == targetTransition && e.Delta == 1 {
				leapSiSec := e.TransitionUnixSecond + cumDelta
				inst := GstInstant(leapSiSec*1_000_000_000 + int64(utc.Nanos))
				return inst, nil
			}
			cumDelta += int64(e.Delta)
		}
		if !found {
			return 0, fmt.Errorf("no positive leap second recorded for date %s", utc.Date)
		}
	}

	hour := int(utc.SecondOfDay / 3600)
	min := int((utc.SecondOfDay / 60) % 60)
	sec := int(utc.SecondOfDay % 60)
	t := time.Date(int(utc.Date.Year), time.Month(utc.Date.Month), int(utc.Date.Day), hour, min, sec, 0, time.UTC)
	unixSec := t.Unix()

	var cumDelta int64
	for _, e := range lh.Entries {
		if unixSec >= e.TransitionUnixSecond {
			cumDelta += int64(e.Delta)
		}
	}

	siSec := unixSec + cumDelta
	inst := GstInstant(siSec*1_000_000_000 + int64(utc.Nanos))
	return inst, nil
}

// UnixNanosToGstInstant converts an unambiguous POSIX instant using this history.
// The repeated second before a positive leap (or deleted second before a
// negative leap) is rejected: POSIX does not identify a unique valid UTC label.
// InitialTaiMinusUtc is metadata, not an additional epoch offset.
func (lh *LeapHistory) UnixNanosToGstInstant(unix UnixNanos) (GstInstant, error) {
	sec := FloorDiv(int64(unix), 1_000_000_000)
	var delta int64
	for _, e := range lh.Entries {
		if e.TransitionUnixSecond != math.MinInt64 && sec == e.TransitionUnixSecond-1 {
			return 0, errors.New("ambiguous or deleted Unix second at leap transition")
		}
		if sec >= e.TransitionUnixSecond {
			delta += int64(e.Delta)
		}
	}
	if delta > math.MaxInt64/1_000_000_000 || delta < math.MinInt64/1_000_000_000 {
		return 0, ErrOverflow
	}
	offset := delta * 1_000_000_000
	v := int64(unix)
	if (offset > 0 && v > math.MaxInt64-offset) || (offset < 0 && v < math.MinInt64-offset) {
		return 0, ErrOverflow
	}
	return GstInstant(v + offset), nil
}

// GstInstantToUnixProjection projects GstInstant to POSIX UnixNanos.
func (lh *LeapHistory) GstInstantToUnixProjection(inst GstInstant) UnixProjection {
	utc := lh.GstInstantToUtc(inst)
	if utc.SecondOfDay == 86400 {
		t := time.Date(int(utc.Date.Year), time.Month(utc.Date.Month), int(utc.Date.Day), 23, 59, 59, 0, time.UTC)
		return UnixProjection{
			Nanos:            UnixNanos(t.Unix()*1_000_000_000 + int64(utc.Nanos)),
			IsLeapSecond:     true,
			SourceGstInstant: inst,
			LeapHistoryID:    lh.ID,
		}
	}

	hour := int(utc.SecondOfDay / 3600)
	min := int((utc.SecondOfDay / 60) % 60)
	sec := int(utc.SecondOfDay % 60)
	t := time.Date(int(utc.Date.Year), time.Month(utc.Date.Month), int(utc.Date.Day), hour, min, sec, 0, time.UTC)
	return UnixProjection{
		Nanos:            UnixNanos(t.Unix()*1_000_000_000 + int64(utc.Nanos)),
		IsLeapSecond:     false,
		SourceGstInstant: inst,
		LeapHistoryID:    lh.ID,
	}
}
