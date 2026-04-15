package main

import (
	"fmt"
	"math"
	"time"
)

// ── Math helpers ──────────────────────────────────────────────────────────────

// pctChange returns (newVal - oldVal) / |oldVal| × 100.
// Returns 0 when oldVal is 0 to avoid division by zero.
func pctChange(oldVal, newVal float64) float64 {
	if oldVal == 0 {
		return 0
	}
	return (newVal - oldVal) / math.Abs(oldVal) * 100
}

// closestPrice returns the most recent closing price at or before target.
// The points slice must be sorted oldest → newest.
func closestPrice(points []pricePoint, target time.Time) (float64, bool) {
	var result float64
	found := false
	for _, p := range points {
		if p.Date.After(target) {
			break
		}
		result = p.Close
		found = true
	}
	return result, found
}

// openOnDate returns the opening price for the exact calendar date of target.
// Returns 0 if no entry matches (e.g. data unavailable or market closed).
func openOnDate(points []pricePoint, target time.Time) float64 {
	targetDate := target.Format("2006-01-02")
	for _, p := range points {
		if p.Date.Format("2006-01-02") == targetDate {
			return p.Open
		}
	}
	return 0
}

// ── String formatting helpers ─────────────────────────────────────────────────

// fmtPct formats a *float64 percentage pointer as "+8.7%" / "-3.2%" / "N/A".
func fmtPct(p *float64) string {
	if p == nil {
		return "N/A"
	}
	if *p >= 0 {
		return fmt.Sprintf("+%.1f%%", *p)
	}
	return fmt.Sprintf("%.1f%%", *p)
}

// fmtDollars formats a USD value as "$1.2M", "$450K", or "$0".
func fmtDollars(v float64) string {
	if v == 0 {
		return "$0"
	}
	abs := math.Abs(v)
	if abs >= 1_000_000 {
		return fmt.Sprintf("$%.1fM", v/1_000_000)
	}
	return fmt.Sprintf("$%.0fK", v/1_000)
}

// fmtRatio formats a *float64 ratio as "23.4x" / "N/A".
func fmtRatio(p *float64) string {
	if p == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.1fx", *p)
}

// labelTime converts an earnings timing code to a display label.
func labelTime(t string) string {
	switch t {
	case "bmo":
		return "BMO"
	case "amc":
		return "AMC"
	default:
		return "N/A"
	}
}

// truncate shortens s to at most n bytes, appending "…" if trimmed.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// ptr returns a pointer to f — convenience for inline *float64 construction.
func ptr(f float64) *float64 { return &f }

// computeResultDate returns the date the market can react to an earnings release.
// BMO ("before market open") results are already reflected in the open price of
// that day, so the "result date" is the previous working day's close.
// AMC or unspecified results are reflected at the next open, so same date.
func computeResultDate(dateStr, earningsTime string) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	if earningsTime == "bmo" {
		return prevWorkingDay(t).Format("2006-01-02")
	}
	return dateStr
}
