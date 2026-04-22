package main

import (
	"math"
	"testing"
)

func TestParseEPS(t *testing.T) {
	cases := []struct {
		input string
		want  float64
	}{
		{"", 0},
		{"N/A", 0},
		{"-", 0},
		{"$4.51", 4.51},
		{"-$0.12", -0.12},
		{"($0.61)", -0.61},
		{"($12.34)", -12.34},
		{"$0", 0},
		{"$1,234.56", 1234.56},
		{"  $2.00  ", 2.00}, // leading/trailing whitespace
	}
	for _, tc := range cases {
		got := parseEPS(tc.input)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("parseEPS(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestParseMarketCap(t *testing.T) {
	cases := []struct {
		input   string
		want    float64
		wantErr bool
	}{
		{"", 0, false},
		{"N/A", 0, false},
		{"$84,902,848,858", 84_902_848_858, false},
		{"$10,000,000,000", 10_000_000_000, false},
		{"$1,000", 1000, false},
		{"not-a-number", 0, true},
	}
	for _, tc := range cases {
		got, err := parseMarketCap(tc.input)
		if tc.wantErr && err == nil {
			t.Errorf("parseMarketCap(%q): expected error, got nil", tc.input)
			continue
		}
		if !tc.wantErr && err != nil {
			t.Errorf("parseMarketCap(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if math.Abs(got-tc.want) > 1 {
			t.Errorf("parseMarketCap(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeNasdaqTime(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"time-pre-market", "bmo"},
		{"time-after-hours", "amc"},
		{"time-not-supplied", ""},
		{"", ""},
		{"anything-else", ""},
	}
	for _, tc := range cases {
		got := normalizeNasdaqTime(tc.input)
		if got != tc.want {
			t.Errorf("normalizeNasdaqTime(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseFiscalQuarterEnd(t *testing.T) {
	cases := []struct {
		input   string
		wantStr string // expected last-day-of-month as YYYY-MM-DD, or "" if invalid
	}{
		{"Mar/2026", "2026-03-31"},
		{"Dec/2025", "2025-12-31"},
		{"Feb/2024", "2024-02-29"}, // leap year
		{"Feb/2025", "2025-02-28"}, // non-leap year
		{"Jan/2026", "2026-01-31"},
		{"",         ""},
		{"bad",      ""},
	}
	for _, tc := range cases {
		got, ok := parseFiscalQuarterEnd(tc.input)
		if tc.wantStr == "" {
			if ok {
				t.Errorf("parseFiscalQuarterEnd(%q): expected failure, got %v", tc.input, got)
			}
			continue
		}
		if !ok {
			t.Errorf("parseFiscalQuarterEnd(%q): unexpected failure", tc.input)
			continue
		}
		if got.Format("2006-01-02") != tc.wantStr {
			t.Errorf("parseFiscalQuarterEnd(%q) = %s, want %s", tc.input, got.Format("2006-01-02"), tc.wantStr)
		}
	}
}
