package sim

import (
	"testing"
	"time"
)

// The dates come from package time. The tests pin the layout at known
// moments.

func atUnixSeconds(seconds int64) string {
	return timestampAt(time.Unix(seconds, 0))
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
