package report

import (
	"fmt"
	"math"

	"quarterly_results/internal/exchange"
)

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

// fmtGEX formats a GEX value (in USD) as "$1.23B", "$450M", "$12M", etc.
// GEX values for large-caps are typically in the hundreds of millions to billions.
func fmtGEX(v float64) string {
	abs := math.Abs(v)
	sign := ""
	if v < 0 {
		sign = "-"
	}
	switch {
	case abs >= 1e9:
		return fmt.Sprintf("%s$%.2fB", sign, abs/1e9)
	case abs >= 1e6:
		return fmt.Sprintf("%s$%.1fM", sign, abs/1e6)
	case abs >= 1e3:
		return fmt.Sprintf("%s$%.0fK", sign, abs/1e3)
	default:
		return fmt.Sprintf("%s$%.0f", sign, abs)
	}
}

// fmtCurrency formats a value in the given ISO currency with sign prefix: £, €, or $.
func fmtCurrency(v float64, currency string) string {
	sym := exchange.CurrencySymbol(currency)
	return fmt.Sprintf("%s%.2f", sym, v)
}

// fmtCurrencyB formats a value (in raw units) as billions with currency sign.
func fmtCurrencyB(v float64, currency string) string {
	sym := exchange.CurrencySymbol(currency)
	return fmt.Sprintf("%s%.2fB", sym, v/1e9)
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
