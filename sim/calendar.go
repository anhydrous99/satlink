package sim

import "time"

// clockLayout is the handset's calendar time as a time.Format layout:
// yy/MM/dd,hh:mm:ss, then the time zone in quarter hours. The simulated
// clock keeps UTC, so the zone is always the literal +00.
const clockLayout = "06/01/02,15:04:05+00"

// timestampAt returns moment as +CCLK shows it, in UTC to the second; a
// clock set before 1970 reads as the epoch.
func timestampAt(moment time.Time) string {
	return time.Unix(max(moment.Unix(), 0), 0).UTC().Format(clockLayout)
}
