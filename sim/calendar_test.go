package sim

import (
	"testing"
	"time"
)

// The dates come from package time. The tests check them against known
// moments, and against a day-by-day count from the epoch.

const secondsPerDay = 24 * 60 * 60

func atUnixSeconds(seconds int64) string {
	return timestampAt(time.Unix(seconds, 0)).String()
}

func TestTimestampFormatsKnownMoments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		seconds int64
		want    string
	}{
		{seconds: 0, want: "70/01/01,00:00:00+00"},
		{seconds: 951_782_400, want: "00/02/29,00:00:00+00"},
		{seconds: 1_729_252_800, want: "24/10/18,12:00:00+00"},
		{seconds: 4_102_444_799, want: "99/12/31,23:59:59+00"},
	}

	for _, test := range cases {
		if got := atUnixSeconds(test.seconds); got != test.want {
			t.Errorf("at %d s: %q; want %q", test.seconds, got, test.want)
		}
	}
}

func TestAClockBefore1970ReadsAsTheEpoch(t *testing.T) {
	t.Parallel()

	if got := atUnixSeconds(-10); got != "70/01/01,00:00:00+00" {
		t.Errorf("ten seconds before the epoch: %q", got)
	}
}

func TestTimestampAgreesWithDayCountingForDecades(t *testing.T) {
	t.Parallel()
	expected := timestamp{year: 1970, month: 1, day: 1}

	for days := range int64(60 * 366) {
		if got := timestampAt(time.Unix(days*secondsPerDay, 0)); got != expected {
			t.Fatalf("day %d: %v; want %v", days, got, expected)
		}
		expected = nextDay(expected)
	}
}

// monthLengths are the days of each month of a common year.
var monthLengths = [...]int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// nextDay counts one day on from day, the long way round.
func nextDay(day timestamp) timestamp {
	monthLength := monthLengths[day.month-1]
	if day.month == 2 && isLeapYear(day.year) {
		monthLength++
	}
	switch {
	case day.day < monthLength:
		day.day++
	case day.month < 12:
		day.month, day.day = day.month+1, 1
	default:
		day.year, day.month, day.day = day.year+1, 1, 1
	}
	return day
}

func isLeapYear(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}
