package session

import (
	"testing"
	"time"
)

// helper: ms returns a time.Time as Unix milliseconds.
func ms(t time.Time) int64 {
	return t.UnixMilli()
}

func TestEvaluateFreshness_ZeroUpdatedAt(t *testing.T) {
	policy := ResetPolicy{Mode: "daily", AtHour: 4, IdleMinutes: 120}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(0, now, policy); got != Stale {
		t.Errorf("updatedAt=0 should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_DailyMode_FreshAfterReset(t *testing.T) {
	// Reset at 04:00. Now is 10:00 on June 15.
	// Updated at 05:00 today → after reset → Fresh.
	policy := ResetPolicy{Mode: "daily", AtHour: 4}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 5, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Fresh {
		t.Errorf("updated after reset should be Fresh, got %v", got)
	}
}

func TestEvaluateFreshness_DailyMode_StaleBeforeReset(t *testing.T) {
	// Reset at 04:00. Now is 10:00 on June 15.
	// Updated at 03:00 today → before reset → Stale.
	policy := ResetPolicy{Mode: "daily", AtHour: 4}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 3, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Stale {
		t.Errorf("updated before reset should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_DailyMode_StaleYesterday(t *testing.T) {
	// Reset at 04:00. Now is 10:00 on June 15.
	// Updated at 23:00 yesterday → before today's reset → Stale.
	policy := ResetPolicy{Mode: "daily", AtHour: 4}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 14, 23, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Stale {
		t.Errorf("updated yesterday should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_DailyMode_BeforeResetHour(t *testing.T) {
	// Reset at 04:00. Now is 02:00 on June 15 (before today's reset).
	// resolveDailyResetAtMs should return yesterday's reset (June 14 at 04:00).
	// Updated at 05:00 yesterday → after yesterday's reset → Fresh.
	policy := ResetPolicy{Mode: "daily", AtHour: 4}
	now := ms(time.Date(2025, 6, 15, 2, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 14, 5, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Fresh {
		t.Errorf("before reset hour, updated after yesterday's reset should be Fresh, got %v", got)
	}
}

func TestEvaluateFreshness_DailyMode_BeforeResetHour_Stale(t *testing.T) {
	// Reset at 04:00. Now is 02:00 on June 15 (before today's reset).
	// resolveDailyResetAtMs → June 14 at 04:00.
	// Updated at 03:00 on June 14 → before yesterday's reset → Stale.
	policy := ResetPolicy{Mode: "daily", AtHour: 4}
	now := ms(time.Date(2025, 6, 15, 2, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 14, 3, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Stale {
		t.Errorf("before reset hour, updated before yesterday's reset should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_IdleMode_Fresh(t *testing.T) {
	// Idle mode only with 120-minute timeout.
	// Updated 60 minutes ago → within timeout → Fresh.
	policy := ResetPolicy{Mode: "idle", IdleMinutes: 120}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 9, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Fresh {
		t.Errorf("within idle timeout should be Fresh, got %v", got)
	}
}

func TestEvaluateFreshness_IdleMode_Stale(t *testing.T) {
	// Idle mode only with 120-minute timeout.
	// Updated 180 minutes ago → beyond timeout → Stale.
	policy := ResetPolicy{Mode: "idle", IdleMinutes: 120}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 7, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Stale {
		t.Errorf("beyond idle timeout should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_IdleMode_ExactBoundary(t *testing.T) {
	// Idle mode, 120-minute timeout.
	// Updated exactly 120 minutes ago → now == updatedAt + 120*60*1000.
	// Condition is now > deadline, so exactly at boundary → NOT stale (Fresh).
	policy := ResetPolicy{Mode: "idle", IdleMinutes: 120}
	base := time.Date(2025, 6, 15, 8, 0, 0, 0, time.Local)
	updatedAt := ms(base)
	now := updatedAt + 120*60*1000

	if got := EvaluateFreshness(updatedAt, now, policy); got != Fresh {
		t.Errorf("exactly at idle boundary should be Fresh, got %v", got)
	}
}

func TestEvaluateFreshness_IdleMode_OneMsOverBoundary(t *testing.T) {
	// One millisecond past the idle boundary → Stale.
	policy := ResetPolicy{Mode: "idle", IdleMinutes: 120}
	base := time.Date(2025, 6, 15, 8, 0, 0, 0, time.Local)
	updatedAt := ms(base)
	now := updatedAt + 120*60*1000 + 1

	if got := EvaluateFreshness(updatedAt, now, policy); got != Stale {
		t.Errorf("1ms past idle boundary should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_DailyPlusIdle(t *testing.T) {
	// Daily mode with idle. Reset at 04:00, idle 120 min.
	// Updated at 05:00 (after reset), now is 10:00 → 300 minutes idle → Stale via idle.
	policy := ResetPolicy{Mode: "daily", AtHour: 4, IdleMinutes: 120}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 5, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Stale {
		t.Errorf("daily+idle: past idle timeout should be Stale, got %v", got)
	}
}

func TestEvaluateFreshness_DailyPlusIdle_BothFresh(t *testing.T) {
	// Daily mode with idle. Reset at 04:00, idle 120 min.
	// Updated at 09:00, now is 10:00 → after daily reset, within idle → Fresh.
	policy := ResetPolicy{Mode: "daily", AtHour: 4, IdleMinutes: 120}
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 9, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Fresh {
		t.Errorf("daily+idle: both fresh should be Fresh, got %v", got)
	}
}

func TestEvaluateFreshness_NoIdleCheck_WhenZero(t *testing.T) {
	// Daily mode, IdleMinutes=0 → no idle check.
	// Updated at 05:00, now is 23:00 → 18 hours idle but no idle check → Fresh.
	policy := ResetPolicy{Mode: "daily", AtHour: 4, IdleMinutes: 0}
	now := ms(time.Date(2025, 6, 15, 23, 0, 0, 0, time.Local))
	updatedAt := ms(time.Date(2025, 6, 15, 5, 0, 0, 0, time.Local))

	if got := EvaluateFreshness(updatedAt, now, policy); got != Fresh {
		t.Errorf("no idle check when IdleMinutes=0 should be Fresh, got %v", got)
	}
}

func TestResolveDailyResetAtMs_AfterResetHour(t *testing.T) {
	// Now is 10:00 on June 15 with reset at 04:00.
	// Should return June 15 04:00:00.
	now := ms(time.Date(2025, 6, 15, 10, 0, 0, 0, time.Local))
	got := resolveDailyResetAtMs(now, 4)
	want := ms(time.Date(2025, 6, 15, 4, 0, 0, 0, time.Local))
	if got != want {
		t.Errorf("resolveDailyResetAtMs after hour = %d, want %d", got, want)
	}
}

func TestResolveDailyResetAtMs_BeforeResetHour(t *testing.T) {
	// Now is 02:00 on June 15 with reset at 04:00.
	// Should return June 14 04:00:00.
	now := ms(time.Date(2025, 6, 15, 2, 0, 0, 0, time.Local))
	got := resolveDailyResetAtMs(now, 4)
	want := ms(time.Date(2025, 6, 14, 4, 0, 0, 0, time.Local))
	if got != want {
		t.Errorf("resolveDailyResetAtMs before hour = %d, want %d", got, want)
	}
}

func TestResolveDailyResetAtMs_ExactlyAtResetHour(t *testing.T) {
	// Now is exactly 04:00:00 on June 15 with reset at 04:00.
	// time.Before returns false when equal, so should return June 15 04:00:00.
	now := ms(time.Date(2025, 6, 15, 4, 0, 0, 0, time.Local))
	got := resolveDailyResetAtMs(now, 4)
	want := ms(time.Date(2025, 6, 15, 4, 0, 0, 0, time.Local))
	if got != want {
		t.Errorf("resolveDailyResetAtMs exactly at hour = %d, want %d", got, want)
	}
}

func TestResolveDailyResetAtMs_Midnight(t *testing.T) {
	// Reset at 0 (midnight). Now is 01:00 → should return today 00:00.
	now := ms(time.Date(2025, 6, 15, 1, 0, 0, 0, time.Local))
	got := resolveDailyResetAtMs(now, 0)
	want := ms(time.Date(2025, 6, 15, 0, 0, 0, 0, time.Local))
	if got != want {
		t.Errorf("resolveDailyResetAtMs midnight = %d, want %d", got, want)
	}
}

func TestResolveDailyResetAtMs_Hour23(t *testing.T) {
	// Reset at 23:00. Now is 22:00 → before reset → should return yesterday 23:00.
	now := ms(time.Date(2025, 6, 15, 22, 0, 0, 0, time.Local))
	got := resolveDailyResetAtMs(now, 23)
	want := ms(time.Date(2025, 6, 14, 23, 0, 0, 0, time.Local))
	if got != want {
		t.Errorf("resolveDailyResetAtMs hour 23 = %d, want %d", got, want)
	}
}
