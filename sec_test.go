package main

import "testing"

func TestDateAddDays(t *testing.T) {
	cases := []struct {
		date string
		n    int
		want string
	}{
		{"2025-01-01", 14, "2025-01-15"},
		{"2025-01-31", 1, "2025-02-01"},  // month boundary
		{"2024-02-28", 1, "2024-02-29"},  // leap year
		{"2025-02-28", 1, "2025-03-01"},  // non-leap year
		{"2025-03-01", -5, "2025-02-24"}, // negative days
		{"not-a-date", 5, "not-a-date"},  // parse error passthrough
		{"2025-12-31", 1, "2026-01-01"},  // year boundary
	}
	for _, tc := range cases {
		got := dateAddDays(tc.date, tc.n)
		if got != tc.want {
			t.Errorf("dateAddDays(%q, %d) = %q, want %q", tc.date, tc.n, got, tc.want)
		}
	}
}
