package session

import "time"

// Freshness indicates whether a session is still usable or should be rotated.
type Freshness int

const (
	Fresh Freshness = iota
	Stale
)

// ResetPolicy describes when a session should be considered stale.
type ResetPolicy struct {
	Mode        string // "daily" or "idle"
	AtHour      int    // 0-23, used in daily mode
	IdleMinutes int    // idle timeout in minutes; applies in both modes when > 0
}

// EvaluateFreshness determines whether a session with the given updatedAt
// timestamp (Unix milliseconds) is Fresh or Stale according to the policy.
//
// Daily mode: stale if updatedAt < resolveDailyResetAtMs(now, atHour).
// Idle check: stale if now > updatedAt + idleMinutes*60*1000.
// The idle check applies in BOTH modes when IdleMinutes > 0.
// If updatedAt is 0, the session is always Stale (never been touched).
func EvaluateFreshness(updatedAt, now int64, policy ResetPolicy) Freshness {
	// A session that has never been touched is always stale.
	if updatedAt == 0 {
		return Stale
	}

	// Daily reset check
	if policy.Mode == "daily" {
		resetMs := resolveDailyResetAtMs(now, policy.AtHour)
		if updatedAt < resetMs {
			return Stale
		}
	}

	// Idle timeout check (applies in both modes when IdleMinutes > 0)
	if policy.IdleMinutes > 0 {
		idleDeadline := updatedAt + int64(policy.IdleMinutes)*60*1000
		if now > idleDeadline {
			return Stale
		}
	}

	return Fresh
}

// resolveDailyResetAtMs returns the most recent daily-reset boundary in Unix
// milliseconds. The reset boundary is today at atHour:00 local time, unless
// now is before that hour, in which case yesterday's boundary is used.
func resolveDailyResetAtMs(nowMs int64, atHour int) int64 {
	t := time.UnixMilli(nowMs)
	loc := t.Location()

	// Today's reset time
	y, m, d := t.Date()
	resetTime := time.Date(y, m, d, atHour, 0, 0, 0, loc)

	if t.Before(resetTime) {
		// Before today's reset hour → use yesterday's reset
		resetTime = resetTime.AddDate(0, 0, -1)
	}

	return resetTime.UnixMilli()
}
