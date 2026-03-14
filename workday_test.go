package main

import (
	"testing"
	"time"
)

func TestGoodFriday(t *testing.T) {
	cases := []struct {
		year int
		want string
	}{
		{2023, "2023-04-07"},
		{2024, "2024-03-29"},
		{2025, "2025-04-18"},
		{2026, "2026-04-03"},
	}
	for _, tc := range cases {
		got := goodFriday(tc.year).Format("2006-01-02")
		if got != tc.want {
			t.Errorf("goodFriday(%d) = %s, want %s", tc.year, got, tc.want)
		}
	}
}

func TestUsMarketHolidays(t *testing.T) {
	h2025 := usMarketHolidays(2025)

	expected := []string{
		"2025-01-01", // New Year's Day
		"2025-01-20", // MLK Day (3rd Monday of January)
		"2025-02-17", // Presidents' Day (3rd Monday of February)
		"2025-04-18", // Good Friday
		"2025-05-26", // Memorial Day (last Monday of May)
		"2025-06-19", // Juneteenth
		"2025-07-04", // Independence Day (Friday)
		"2025-09-01", // Labor Day (1st Monday of September)
		"2025-11-27", // Thanksgiving (4th Thursday of November)
		"2025-12-25", // Christmas
	}
	for _, d := range expected {
		if !h2025[d] {
			t.Errorf("2025 holidays: expected %s to be a holiday", d)
		}
	}

	// Juneteenth only from 2022
	h2021 := usMarketHolidays(2021)
	if h2021["2021-06-19"] {
		t.Error("2021 holidays: 2021-06-19 should NOT be a holiday (Juneteenth pre-2022)")
	}

	// Independence Day 2026: July 4 is a Saturday → observed Friday July 3
	h2026 := usMarketHolidays(2026)
	if !h2026["2026-07-03"] {
		t.Error("2026 holidays: 2026-07-03 should be a holiday (Independence Day observed)")
	}
	if h2026["2026-07-04"] {
		t.Error("2026 holidays: 2026-07-04 should NOT be marked as holiday (Saturday, observed on Fri)")
	}
}

func TestIsWorkingDay(t *testing.T) {
	cases := []struct {
		date string
		want bool
	}{
		{"2025-01-02", true},  // Thursday, regular day
		{"2025-01-01", false}, // New Year's Day
		{"2025-01-04", false}, // Saturday
		{"2025-01-05", false}, // Sunday
		{"2025-01-20", false}, // MLK Day
		{"2025-04-18", false}, // Good Friday
		{"2025-11-27", false}, // Thanksgiving
		{"2025-12-25", false}, // Christmas
		{"2025-07-04", false}, // Independence Day (Friday)
		{"2025-11-28", true},  // Day after Thanksgiving (trading day)
	}
	for _, tc := range cases {
		d, _ := time.Parse("2006-01-02", tc.date)
		got := isWorkingDay(d)
		if got != tc.want {
			t.Errorf("isWorkingDay(%s) = %v, want %v", tc.date, got, tc.want)
		}
	}
}

func TestNextWorkingDay(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"2025-01-02", "2025-01-03"}, // Thursday → Friday
		{"2025-01-03", "2025-01-06"}, // Friday → skip weekend → Monday
		{"2025-04-17", "2025-04-21"}, // Thursday before Good Friday → skip Good Friday + weekend → Monday (Easter Monday is NOT a market holiday)
		{"2025-11-27", "2025-11-28"}, // Thanksgiving → Friday (trading day)
		{"2025-01-01", "2025-01-02"}, // New Year's (holiday) → next working day
	}
	for _, tc := range cases {
		d, _ := time.Parse("2006-01-02", tc.input)
		got := nextWorkingDay(d).Format("2006-01-02")
		if got != tc.want {
			t.Errorf("nextWorkingDay(%s) = %s, want %s", tc.input, got, tc.want)
		}
	}
}

func TestPrevWorkingDay(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"2025-01-07", "2025-01-06"}, // Tuesday → Monday
		{"2025-01-06", "2025-01-03"}, // Monday → skip weekend → Friday
		{"2025-04-22", "2025-04-21"}, // Tuesday after Easter → previous working day is Monday (Easter Monday is NOT a market holiday)
		{"2025-01-02", "2024-12-31"}, // Thursday, crosses year boundary; Dec 31 2024 is Tuesday
	}
	for _, tc := range cases {
		d, _ := time.Parse("2006-01-02", tc.input)
		got := prevWorkingDay(d).Format("2006-01-02")
		if got != tc.want {
			t.Errorf("prevWorkingDay(%s) = %s, want %s", tc.input, got, tc.want)
		}
	}
}
