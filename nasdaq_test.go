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
