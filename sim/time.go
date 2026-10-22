package sim

import "time"

// Time arithmetic that stays sensible whatever durations the configuration
// or a caller's timeout supply.

// longestWait is the longest wait the simulation represents: nothing in a
// test or a demo waits a year, and anything longer would only hide an
// absurd setting.
const longestWait = 365 * 24 * time.Hour

// later returns the time wait after start. Waits longer than a year count
// as a year, and negative ones as none.
func later(start time.Time, wait time.Duration) time.Time {
	return start.Add(min(max(wait, 0), longestWait))
}
