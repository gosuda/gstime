package core

import (
	"bytes"
	"testing"
)

func TestLeapHistoryCanonicalEncodingAndValidation(t *testing.T) {
	// 1972-07-01 00:00:00 UTC transition (78796800)
	entries := []LeapEntry{
		{TransitionUnixSecond: 78796800, Delta: 1},
		{TransitionUnixSecond: 94694400, Delta: 1}, // 1973-01-01
	}

	lh, err := NewLeapHistory(10, entries)
	if err != nil {
		t.Fatalf("NewLeapHistory failed: %v", err)
	}

	canon := lh.CanonicalBytes()
	if !bytes.HasPrefix(canon, []byte("GSTL1")) {
		t.Fatalf("expected GSTL1 prefix")
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
}

func TestLeapHistoryRejections(t *testing.T) {
	// Unsorted
	_, err := NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 100, Delta: 1},
		{TransitionUnixSecond: 50, Delta: 1},
	})
	if err == nil {
		t.Fatalf("expected error for unsorted entries")
	}

	// Duplicate
	_, err = NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 100, Delta: 1},
		{TransitionUnixSecond: 100, Delta: 1},
	})
	if err == nil {
		t.Fatalf("expected error for duplicate entry")
	}

	// Invalid delta
	_, err = NewLeapHistory(10, []LeapEntry{
		{TransitionUnixSecond: 100, Delta: 2},
	})
	if err == nil {
		t.Fatalf("expected error for invalid delta")
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
