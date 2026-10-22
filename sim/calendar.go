package sim

import (
	"fmt"
	"time"
)

// Calendar timestamps as the handset prints them, in UTC:
// yy/MM/dd,hh:mm:ss+00.

// timestamp is a moment in UTC, as +CCLK and SMS headers show it.
type timestamp struct {
	year, month, day     int
	hour, minute, second int
}

// timestampAt returns the wall-clock time of moment in UTC, to the second;
// a clock set before 1970 reads as the epoch.
func timestampAt(moment time.Time) timestamp {
	utc := time.Unix(max(moment.Unix(), 0), 0).UTC()
	return timestamp{
		year:   utc.Year(),
		month:  int(utc.Month()),
		day:    utc.Day(),
		hour:   utc.Hour(),
		minute: utc.Minute(),
		second: utc.Second(),
	}
}

// String returns the timestamp with a two-digit year, then UTC's zero
// offset in quarter hours.
func (ts timestamp) String() string {
	return fmt.Sprintf("%02d/%02d/%02d,%02d:%02d:%02d+00",
		ts.year%100, ts.month, ts.day, ts.hour, ts.minute, ts.second)
}
