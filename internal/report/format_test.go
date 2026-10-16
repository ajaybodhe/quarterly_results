package report

import (
	"testing"
)

// ── fmtPct ───────────────────────────────────────────────────────────────────

func TestFmtPct(t *testing.T) {
	cases := []struct {
		input *float64
		want  string
	}{
		{nil, "N/A"},
		{ptr(0.0), "+0.0%"},
		{ptr(8.7), "+8.7%"},
		{ptr(-3.2), "-3.2%"},
		{ptr(100.0), "+100.0%"},
		{ptr(-100.0), "-100.0%"},
		{ptr(0.14), "+0.1%"}, // rounds down
		{ptr(0.15), "+0.1%"}, // 0.15 is stored slightly below 0.15 in IEEE 754 so %.1f rounds down
	}
	for _, tc := range cases {
		got := fmtPct(tc.input)
		if got != tc.want {
			t.Errorf("fmtPct(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── fmtDollars ───────────────────────────────────────────────────────────────

func TestFmtDollars(t *testing.T) {
	cases := []struct {
		input float64
		want  string
	}{
		{0, "$0"},
		{1_000, "$1K"},
		{450_000, "$450K"},
		{999_499, "$999K"},
		{1_000_000, "$1.0M"},
		{1_500_000, "$1.5M"},
		{12_345_678, "$12.3M"},
		{-500_000, "$-500K"}, // negative values: documents current behaviour
	}
	for _, tc := range cases {
		got := fmtDollars(tc.input)
		if got != tc.want {
			t.Errorf("fmtDollars(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── fmtRatio ─────────────────────────────────────────────────────────────────

func TestFmtRatio(t *testing.T) {
	cases := []struct {
		input *float64
		want  string
	}{
		{nil, "N/A"},
		{ptr(23.4), "23.4x"},
		{ptr(0.0), "0.0x"},
		{ptr(-1.5), "-1.5x"},
		{ptr(100.0), "100.0x"},
	}
	for _, tc := range cases {
		got := fmtRatio(tc.input)
		if got != tc.want {
			t.Errorf("fmtRatio(%v) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── labelTime ────────────────────────────────────────────────────────────────

func TestLabelTime(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"bmo", "BMO"},
		{"amc", "AMC"},
		{"", "N/A"},
		{"time-not-supplied", "N/A"},
		{"unknown", "N/A"},
	}
	for _, tc := range cases {
		got := labelTime(tc.input)
		if got != tc.want {
			t.Errorf("labelTime(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── truncate ─────────────────────────────────────────────────────────────────

func TestTruncate(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"Hello", 10, "Hello"},         // shorter than limit
		{"Hello", 5, "Hello"},          // exactly at limit
		{"Hello World", 8, "Hello W…"}, // truncated
		{"", 5, ""},
		{"ABCDE", 4, "ABC…"},
		{"A", 1, "A"},
	}
	for _, tc := range cases {
		got := truncate(tc.s, tc.n)
		if got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// ── ptr ──────────────────────────────────────────────────────────────────────

func TestPtr(t *testing.T) {
	cases := []float64{3.14, 0, -1}
	for _, v := range cases {
		p := ptr(v)
		if p == nil {
			t.Fatalf("ptr(%v) returned nil", v)
		}
		if *p != v {
			t.Errorf("*ptr(%v) = %v, want %v", v, *p, v)
		}
	}
}

// ── fmtCurrency / fmtCurrencyB ───────────────────────────────────────────────

func TestFmtCurrency(t *testing.T) {
	cases := []struct {
		v        float64
		currency string
		want     string
	}{
		{123.45, "USD", "$123.45"},
		{123.45, "GBP", "£123.45"},
		{123.45, "EUR", "€123.45"},
		{0.50, "USD", "$0.50"},
	}
	for _, tc := range cases {
		got := fmtCurrency(tc.v, tc.currency)
		if got != tc.want {
			t.Errorf("fmtCurrency(%.2f, %q) = %q, want %q", tc.v, tc.currency, got, tc.want)
		}
	}
}

func TestFmtCurrencyB(t *testing.T) {
	cases := []struct {
		v        float64
		currency string
		want     string
	}{
		{5_000_000_000, "USD", "$5.00B"},
		{12_340_000_000, "GBP", "£12.34B"},
		{1_500_000_000, "EUR", "€1.50B"},
	}
	for _, tc := range cases {
		got := fmtCurrencyB(tc.v, tc.currency)
		if got != tc.want {
			t.Errorf("fmtCurrencyB(%.0f, %q) = %q, want %q", tc.v, tc.currency, got, tc.want)
		}
	}
}
