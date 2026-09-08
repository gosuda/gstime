package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"testing"
	"time"
)

func TestLeapHistoryCanonicalEncodingAndValidation(t *testing.T) {
	// 1972-07-01 00:00:00 UTC transition (78796800)
	entries := []LeapEntry{
		{TransitionUnixSecond: 78796800, Delta: 1},
		{TransitionUnixSecond: 94694400, Delta: -1}, // 1973-01-01
	}

	lh, err := NewLeapHistory(10, entries)
	if err != nil {
		t.Fatalf("NewLeapHistory failed: %v", err)
	}

	wantCanonical, err := hex.DecodeString("4753544c310000000a000000020000000004b2580001000000000000000000000005a4ec00ff00000000000000")
	if err != nil {
		t.Fatal(err)
	}
	canon := lh.CanonicalBytes()
	if !bytes.Equal(canon, wantCanonical) {
		t.Fatalf("canonical bytes = %x; want %x", canon, wantCanonical)
	}
	if wantID := sha256.Sum256(wantCanonical); lh.ID != wantID {
		t.Fatalf("canonical digest = %x; want %x", lh.ID, wantID)
	}

	parsed, err := ParseLeapHistory(canon)
	if err != nil {
		t.Fatalf("ParseLeapHistory failed: %v", err)
	}
	if parsed.ID != lh.ID {
		t.Fatalf("parsed ID mismatch: got %x want %x", parsed.ID, lh.ID)
	}
	if len(parsed.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(parsed.Entries))
	}
	if !bytes.Equal(parsed.CanonicalBytes(), wantCanonical) {
		t.Fatalf("parsed canonical bytes = %x; want %x", parsed.CanonicalBytes(), wantCanonical)
	}
	label := UtcLabel{Date: GregorianDate{Year: 1973, Month: 1, Day: 1}, LeapHistoryID: parsed.ID}
	if got, err := parsed.UtcToGstInstant(label); err != nil || got != 94694400000000000 {
		t.Fatalf("parsed mixed-sign history conversion = %d, %v; want 94694400000000000", got, err)
	}
}

func TestLeapHistoryRejections(t *testing.T) {
	// Unsorted
	_, err := NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 172800, Delta: 1},
		{TransitionUnixSecond: 86400, Delta: 1},
	})
	if !errors.Is(err, ErrUnsortedLeapTransitions) {
		t.Fatalf("unsorted entries: got %v; want ErrUnsortedLeapTransitions", err)
	}

	// Duplicate
	_, err = NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 86400, Delta: 1},
		{TransitionUnixSecond: 86400, Delta: 1},
	})
	if !errors.Is(err, ErrDuplicateLeapTransition) {
		t.Fatalf("duplicate entry: got %v; want ErrDuplicateLeapTransition", err)
	}

	// Invalid delta
	_, err = NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 86400, Delta: 2},
	})
	if !errors.Is(err, ErrInvalidLeapDelta) {
		t.Fatalf("invalid delta: got %v; want ErrInvalidLeapDelta", err)
	}
}

func TestGstInstantToUtcAcrossLeapSecond(t *testing.T) {
	// 1972-07-01 00:00:00 is unix second 78796800.
	// Positive leap second inserted at 1972-06-30 23:59:60.
	entries := []LeapEntry{
		{TransitionUnixSecond: 78796800, Delta: 1},
	}
	lh, err := NewLeapHistory(10, entries)
	if err != nil {
		t.Fatalf("NewLeapHistory: %v", err)
	}

	// SI seconds:
	// 78796799: 1972-06-30 23:59:59
	// 78796800: 1972-06-30 23:59:60 (secondOfDay = 86400)
	// 78796801: 1972-07-01 00:00:00 (secondOfDay = 0)

	tBefore := GstInstant(78796799 * 1_000_000_000)
	utcBefore := lh.GstInstantToUtc(tBefore)
	if utcBefore.SecondOfDay != 86399 {
		t.Fatalf("expected secondOfDay 86399, got %d", utcBefore.SecondOfDay)
	}
	if utcBefore.Date.Day != 30 || utcBefore.Date.Month != 6 || utcBefore.Date.Year != 1972 {
		t.Fatalf("unexpected date before leap: %v", utcBefore.Date)
	}

	tLeap := GstInstant(78796800 * 1_000_000_000)
	utcLeap := lh.GstInstantToUtc(tLeap)
	if utcLeap.SecondOfDay != 86400 {
		t.Fatalf("expected secondOfDay 86400 for leap second, got %d", utcLeap.SecondOfDay)
	}
	if utcLeap.Date.Day != 30 || utcLeap.Date.Month != 6 || utcLeap.Date.Year != 1972 {
		t.Fatalf("unexpected date for leap second: %v", utcLeap.Date)
	}

	tAfter := GstInstant(78796801 * 1_000_000_000)
	utcAfter := lh.GstInstantToUtc(tAfter)
	if utcAfter.SecondOfDay != 0 {
		t.Fatalf("expected secondOfDay 0 after leap, got %d", utcAfter.SecondOfDay)
	}
	if utcAfter.Date.Day != 1 || utcAfter.Date.Month != 7 || utcAfter.Date.Year != 1972 {
		t.Fatalf("unexpected date after leap: %v", utcAfter.Date)
	}

	// Round-trip UtcToGstInstant
	rtBefore, err := lh.UtcToGstInstant(utcBefore)
	if err != nil || rtBefore != tBefore {
		t.Fatalf("round-trip before failed: got %d want %d err %v", rtBefore, tBefore, err)
	}

	rtLeap, err := lh.UtcToGstInstant(utcLeap)
	if err != nil || rtLeap != tLeap {
		t.Fatalf("round-trip leap failed: got %d want %d err %v", rtLeap, tLeap, err)
	}

	rtAfter, err := lh.UtcToGstInstant(utcAfter)
	if err != nil || rtAfter != tAfter {
		t.Fatalf("round-trip after failed: got %d want %d err %v", rtAfter, tAfter, err)
	}

	// Unix projection ambiguity
	projLeap := lh.GstInstantToUnixProjection(tLeap)
	if !projLeap.IsLeapSecond {
		t.Fatalf("expected IsLeapSecond true in projection")
	}

	projAfter := lh.GstInstantToUnixProjection(tAfter)
	if projAfter.IsLeapSecond {
		t.Fatalf("expected IsLeapSecond false in projection")
	}
}

func TestLeapHistoryRejectsInvalidTransitions(t *testing.T) {
	valid, err := NewLeapHistory(10, []LeapEntry{{TransitionUnixSecond: 0, Delta: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		second int64
	}{
		{"not midnight", 1},
		{"unrepresentable future date", math.MaxInt64 / 86400 * 86400},
		{"unrepresentable past date", math.MinInt64 / 86400 * 86400},
		{"unrepresentable previous date", time.Date(math.MinInt32, 1, 1, 0, 0, 0, 0, time.UTC).Unix()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLeapHistory(10, []LeapEntry{{TransitionUnixSecond: tc.second, Delta: 1}})
			if !errors.Is(err, ErrInvalidLeapTransition) {
				t.Errorf("NewLeapHistory transition %d: got %v; want ErrInvalidLeapTransition", tc.second, err)
			}
			encoded := valid.CanonicalBytes()
			binary.BigEndian.PutUint64(encoded[13:21], uint64(tc.second))
			if _, err := ParseLeapHistory(encoded); !errors.Is(err, ErrInvalidLeapTransition) {
				t.Errorf("ParseLeapHistory transition %d: got %v; want ErrInvalidLeapTransition", tc.second, err)
			}
		})
	}
}
