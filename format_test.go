package main

import (
	"testing"
	"time"
)

// ── pctChange ─────────────────────────────────────────────────────────────────

func TestPctChange(t *testing.T) {
	cases := []struct {
		old, new float64
		want     float64
	}{
		{0, 5, 0},      // divide-by-zero guard
		{10, 12, 20},   // basic positive
		{10, 8, -20},   // basic negative
		{100, 100, 0},  // no change
		{1, 2, 100},    // double
		{-4, -2, 50},   // both negative, improving (less negative)
		{-4, -6, -50},  // both negative, worsening
		{-1, 1, 200},   // cross zero from negative
		{50, 0, -100},  // drop to zero
	}
	for _, tc := range cases {
		got := pctChange(tc.old, tc.new)
		if got != tc.want {
			t.Errorf("pctChange(%v, %v) = %v, want %v", tc.old, tc.new, got, tc.want)
		}
	}
}

// ── fmtPct ────────────────────────────────────────────────────────────────────

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

// ── fmtDollars ────────────────────────────────────────────────────────────────

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
		{"Hello", 10, "Hello"},       // shorter than limit
		{"Hello", 5, "Hello"},        // exactly at limit
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

// ── ptr ───────────────────────────────────────────────────────────────────────

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

// ── computeResultDate ─────────────────────────────────────────────────────────

func TestComputeResultDate(t *testing.T) {
	cases := []struct {
		date, timing, want string
	}{
		{"2025-01-10", "amc", "2025-01-10"}, // AMC → same date
		{"2025-01-10", "", "2025-01-10"},     // unspecified → same date
		{"2025-01-10", "bmo", "2025-01-09"}, // Friday BMO → previous Thursday
		{"2025-01-06", "bmo", "2025-01-03"}, // Monday BMO → previous Friday
		{"not-a-date", "bmo", "not-a-date"}, // parse error passthrough
	}
	for _, tc := range cases {
		got := computeResultDate(tc.date, tc.timing)
		if got != tc.want {
			t.Errorf("computeResultDate(%q, %q) = %q, want %q", tc.date, tc.timing, got, tc.want)
		}
	}
}

// ── openOnDate ───────────────────────────────────────────────────────────────

func TestOpenOnDate(t *testing.T) {
	pts := []pricePoint{
		{Date: mustDate("2025-01-08"), Open: 110, Close: 112},
		{Date: mustDate("2025-01-09"), Open: 115, Close: 118},
	}
	if got := openOnDate(pts, mustDate("2025-01-08")); got != 110 {
		t.Errorf("openOnDate exact match = %v, want 110", got)
	}
	if got := openOnDate(pts, mustDate("2025-01-09")); got != 115 {
		t.Errorf("openOnDate Jan 9 = %v, want 115", got)
	}
	if got := openOnDate(pts, mustDate("2025-01-10")); got != 0 {
		t.Errorf("openOnDate missing date = %v, want 0", got)
	}
	if got := openOnDate(nil, mustDate("2025-01-08")); got != 0 {
		t.Errorf("openOnDate nil slice = %v, want 0", got)
	}
}

// ── closestPrice ─────────────────────────────────────────────────────────────

func mustDate(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func TestClosestPrice(t *testing.T) {
	pts := []pricePoint{
		{Date: mustDate("2025-01-08"), Close: 100},
		{Date: mustDate("2025-01-10"), Close: 200},
		{Date: mustDate("2025-01-14"), Close: 300},
	}

	t.Run("empty slice", func(t *testing.T) {
		_, ok := closestPrice(nil, mustDate("2025-01-10"))
		if ok {
			t.Error("expected ok=false for empty slice")
		}
	})
	t.Run("target before all points", func(t *testing.T) {
		_, ok := closestPrice(pts, mustDate("2025-01-07"))
		if ok {
			t.Error("expected ok=false when target is before all points")
		}
	})
	t.Run("exact match", func(t *testing.T) {
		price, ok := closestPrice(pts, mustDate("2025-01-10"))
		if !ok || price != 200 {
			t.Errorf("got (%v, %v), want (200, true)", price, ok)
		}
	})
	t.Run("target between points", func(t *testing.T) {
		// Jan 9 is between Jan 8 and Jan 10 — should return Jan 8's price
		price, ok := closestPrice(pts, mustDate("2025-01-09"))
		if !ok || price != 100 {
			t.Errorf("got (%v, %v), want (100, true)", price, ok)
		}
	})
	t.Run("target after all points", func(t *testing.T) {
		price, ok := closestPrice(pts, mustDate("2025-01-20"))
		if !ok || price != 300 {
			t.Errorf("got (%v, %v), want (300, true)", price, ok)
		}
	})
}
