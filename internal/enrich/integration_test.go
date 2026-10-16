//go:build integration

package enrich

import (
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/macro"
)

// Run with:
//   go test -tags integration -run TestIntegration -v -timeout 120s
//
// These tests call real external APIs (SEC EDGAR, Nasdaq, stockanalysis.com,
// Finviz). They are excluded from normal `go test ./...` runs because they
// are slow, hit rate limits, and require network access. Mark them slow by
// setting a generous -timeout.

// TestIntegration_FullEnrichment picks a mega-cap stock reporting in the next
// 90 days (AAPL is a safe perennial choice), runs the full enrichment pipeline,
// and asserts that the key fields are populated.
func TestIntegration_FullEnrichment(t *testing.T) {
	// Use a known mega-cap ticker that always has good SEC EDGAR data.
	symbol := "AAPL"
	res := domain.EarningsResult{
		Symbol:     symbol,
		MarketCapB: 3000,
	}
	macroCal := macro.LoadCalendar(time.Now(), time.Now().AddDate(0, 3, 0), exchange.DefaultUSConfig)
	enricher := New()
	if err := enricher.secClient.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}

	s := enricher.buildSummary(res, domain.CalendarRow{}, macroCal)

	if len(s.History) < 4 {
		t.Errorf("expected ≥4 quarters of history, got %d", len(s.History))
	}
	if s.CurrentPrice <= 0 {
		t.Errorf("CurrentPrice = %v, want > 0", s.CurrentPrice)
	}
	if s.Ret1Y == nil {
		t.Error("Ret1Y should be populated from 1-year price history")
	}
	if s.RSI14 == nil {
		t.Error("RSI14 should be populated")
	}

	// Material events: AAPL files multiple 8-Ks per quarter.
	// (May be empty if no 8-Ks in the last 90 days — warn but don't fail.)
	if len(s.MaterialEvents) == 0 {
		t.Log("Warning: no material events found for AAPL in last 90 days")
	} else {
		for _, e := range s.MaterialEvents {
			if e.Label == "" {
				t.Errorf("material event %s has empty label", e.Date)
			}
		}
	}

	// Insider activity: should run without error (may be "No Activity").
	if s.Insider == nil {
		t.Error("Insider summary should not be nil")
	}
}
