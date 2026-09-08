package core_test

import (
	"errors"
	"gosuda.org/gstime/core"
	"math"
	"testing"
	"time"
)

func TestUnixNanosToGstInstant(t *testing.T) {
	const transition int64 = 1483228800
	for _, delta := range []int8{-1, 1} {
		lh, err := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: transition, Delta: delta}})
		if err != nil {
			t.Fatal(err)
		}
		for _, sec := range []int64{transition - 2, transition, transition + 100} {
			unix := core.UnixNanos(sec*1_000_000_000 + 123)
			got, err := lh.UnixNanosToGstInstant(unix)
			if err != nil {
				t.Fatal(err)
			}
			want := core.GstInstant(unix)
			if sec >= transition {
				want += core.GstInstant(delta) * 1_000_000_000
			}
			if got != want {
				t.Fatalf("delta=%d sec=%d: got %d want %d", delta, sec, got, want)
			}
			if projected := lh.GstInstantToUnixProjection(got); projected.Nanos != unix || projected.IsLeapSecond {
				t.Fatalf("bad round trip %+v", projected)
			}
		}
		if _, err := lh.UnixNanosToGstInstant(core.UnixNanos((transition - 1) * 1_000_000_000)); err == nil {
			t.Fatal("ambiguous/deleted second accepted")
		}
	}
	lh, _ := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: transition, Delta: 1}})
	if _, err := lh.UnixNanosToGstInstant(math.MaxInt64); err == nil {
		t.Fatal("conversion overflow accepted")
	}
	empty, _ := core.NewLeapHistory(10, nil)
	if got, err := empty.UnixNanosToGstInstant(-1); err != nil || got != -1 {
		t.Fatalf("negative Unix timestamp: %d %v", got, err)
	}
}

func TestUtcToGstInstantSignedRange(t *testing.T) {
	lh, err := core.NewLeapHistory(10, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		date   core.GregorianDate
		second uint32
		nanos  uint32
		want   core.GstInstant
	}{
		{"minimum", core.GregorianDate{Year: 1677, Month: 9, Day: 21}, 763, 145224192, math.MinInt64},
		{"maximum", core.GregorianDate{Year: 2262, Month: 4, Day: 11}, 85636, 854775807, math.MaxInt64},
		{"before epoch", core.GregorianDate{Year: 1969, Month: 12, Day: 31}, 86399, 999999999, -1},
		{"epoch", core.GregorianDate{Year: 1970, Month: 1, Day: 1}, 0, 0, 0},
		{"Gregorian leap day", core.GregorianDate{Year: 2000, Month: 2, Day: 29}, 0, 123, 951782400000000123},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := core.UtcLabel{Date: tc.date, SecondOfDay: tc.second, Nanos: tc.nanos, LeapHistoryID: lh.ID}
			got, err := lh.UtcToGstInstant(label)
			if err != nil || got != tc.want {
				t.Fatalf("UtcToGstInstant(%s) = %d, %v; want %d", label, got, err, tc.want)
			}
			if roundTrip := lh.GstInstantToUtc(got); roundTrip != label {
				t.Fatalf("GstInstantToUtc(%d) = %s; want %s", got, roundTrip, label)
			}
		})
	}
}

func TestUtcToGstInstantRejectsOverflow(t *testing.T) {
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{{
		TransitionUnixSecond: time.Date(2501, 1, 1, 0, 0, 0, 0, time.UTC).Unix(),
		Delta:                1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		date   core.GregorianDate
		second uint32
		nanos  uint32
	}{
		{"below minimum", core.GregorianDate{Year: 1677, Month: 9, Day: 21}, 763, 145224191},
		{"above maximum", core.GregorianDate{Year: 2262, Month: 4, Day: 11}, 85636, 854775808},
		{"year 2500", core.GregorianDate{Year: 2500, Month: 1, Day: 1}, 0, 0},
		{"leap in year 2500", core.GregorianDate{Year: 2500, Month: 12, Day: 31}, 86400, 123},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := core.UtcLabel{Date: tc.date, SecondOfDay: tc.second, Nanos: tc.nanos, LeapHistoryID: lh.ID}
			if got, err := lh.UtcToGstInstant(label); !errors.Is(err, core.ErrOverflow) {
				t.Fatalf("UtcToGstInstant(%s) = %d, %v; want ErrOverflow", label, got, err)
			}
		})
	}
}

func TestUtcToGstInstantRejectsInvalidGregorianDate(t *testing.T) {
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{{
		TransitionUnixSecond: time.Date(2000, 3, 2, 0, 0, 0, 0, time.UTC).Unix(),
		Delta:                1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		date   core.GregorianDate
		second uint32
	}{
		{"zero month", core.GregorianDate{Year: 2000, Month: 0, Day: 1}, 0},
		{"month thirteen", core.GregorianDate{Year: 2000, Month: 13, Day: 1}, 0},
		{"zero day", core.GregorianDate{Year: 2000, Month: 1, Day: 0}, 0},
		{"short month", core.GregorianDate{Year: 2000, Month: 4, Day: 31}, 0},
		{"non leap century", core.GregorianDate{Year: 1900, Month: 2, Day: 29}, 0},
		{"normalized leap date", core.GregorianDate{Year: 2000, Month: 2, Day: 30}, 86400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := core.UtcLabel{Date: tc.date, SecondOfDay: tc.second, LeapHistoryID: lh.ID}
			if got, err := lh.UtcToGstInstant(label); !errors.Is(err, core.ErrInvalidGregorianDate) {
				t.Fatalf("UtcToGstInstant(%s) = %d, %v; want ErrInvalidGregorianDate", label, got, err)
			}
		})
	}
}

func TestUtcToGstInstantRejectsDeletedNegativeLeapSecond(t *testing.T) {
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: 1483228800, Delta: -1}})
	if err != nil {
		t.Fatal(err)
	}
	deleted := core.UtcLabel{
		Date:          core.GregorianDate{Year: 2016, Month: 12, Day: 31},
		SecondOfDay:   86399,
		Nanos:         123,
		LeapHistoryID: lh.ID,
	}
	if got, err := lh.UtcToGstInstant(deleted); !errors.Is(err, core.ErrInvalidSecondOfDay) {
		t.Fatalf("deleted UTC second = %d, %v; want ErrInvalidSecondOfDay", got, err)
	}
	for _, tc := range []struct {
		name   string
		date   core.GregorianDate
		second uint32
		want   core.GstInstant
	}{
		{"last valid second", core.GregorianDate{Year: 2016, Month: 12, Day: 31}, 86398, 1483228798000000123},
		{"next midnight", core.GregorianDate{Year: 2017, Month: 1, Day: 1}, 0, 1483228799000000123},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := core.UtcLabel{Date: tc.date, SecondOfDay: tc.second, Nanos: 123, LeapHistoryID: lh.ID}
			got, err := lh.UtcToGstInstant(label)
			if err != nil || got != tc.want {
				t.Fatalf("UtcToGstInstant(%s) = %d, %v; want %d", label, got, err, tc.want)
			}
			if roundTrip := lh.GstInstantToUtc(got); roundTrip != label {
				t.Fatalf("GstInstantToUtc(%d) = %s; want %s", got, roundTrip, label)
			}
		})
	}
}

func TestUtcLabelStringFormatsPositiveLeapSecond(t *testing.T) {
	lh, err := core.NewLeapHistory(10, []core.LeapEntry{{TransitionUnixSecond: 1483228800, Delta: 1}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		instant core.GstInstant
		want    string
	}{
		{1483228799123456789, "2016-12-31T23:59:59.123456789Z"},
		{1483228800123456789, "2016-12-31T23:59:60.123456789Z"},
		{1483228801123456789, "2017-01-01T00:00:00.123456789Z"},
	} {
		label := lh.GstInstantToUtc(tc.instant)
		if got := label.String(); got != tc.want {
			t.Errorf("UTC label for %d = %q; want %q", tc.instant, got, tc.want)
		}
	}
}
