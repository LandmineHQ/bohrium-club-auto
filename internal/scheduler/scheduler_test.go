package scheduler

import (
	"testing"
	"time"
)

func TestPlannedTimeForAccountDeterministicAndDistinct(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	day1 := time.Date(2026, 11, 25, 0, 0, 0, 0, loc)
	day2 := day1.AddDate(0, 0, 1)

	slotA1 := PlannedTimeForAccount(day1, "acc_alpha", "08:15", 60)
	slotA1Repeat := PlannedTimeForAccount(day1, "acc_alpha", "08:15", 60)
	if !slotA1.Equal(slotA1Repeat) {
		t.Fatalf("expected deterministic planned time across restarts, got %s vs %s", slotA1, slotA1Repeat)
	}

	minBound := time.Date(2026, 11, 25, 8, 15, 0, 0, loc)
	maxBound := minBound.Add(60*time.Minute + 59*time.Second)
	if slotA1.Before(minBound) || slotA1.After(maxBound) {
		t.Fatalf("planned time %s out of expected window [%s, %s]", slotA1, minBound, maxBound)
	}

	// Different accounts on the same day should spread out to distinct timestamps
	slotB1 := PlannedTimeForAccount(day1, "acc_beta", "08:15", 60)
	slotC1 := PlannedTimeForAccount(day1, "acc_gamma", "08:15", 60)
	if slotA1.Equal(slotB1) && slotB1.Equal(slotC1) {
		t.Fatalf("expected distinct randomized check-in slots across accounts, all got %s", slotA1)
	}

	// Same account on consecutive days should fluctuate naturally
	slotA2 := PlannedTimeForAccount(day2, "acc_alpha", "08:15", 60)
	if slotA1.Format("15:04:05") == slotA2.Format("15:04:05") {
		t.Fatalf("expected next-day time-of-day to fluctuate, both days got %s", slotA1.Format("15:04:05"))
	}
}
