// Package clock is the canonical wall-clock source for desktop-via-clyde's
// internal packages.
package clock

import "time"

// Now returns the current wall clock time.
func Now() time.Time {
	return time.Now()
}

// Since reports the elapsed time since start.
func Since(start time.Time) time.Duration {
	return time.Since(start)
}
