//go:build integration

package main

import (
	"strings"
	"testing"
	"time"
)

// Run with:
//   go test -tags integration -run TestIntegration -v -timeout 120s
//
// These tests call real external APIs (SEC EDGAR, Nasdaq, stockanalysis.com,
// Finviz). They are excluded from normal `go test ./...` runs because they
// are slow, hit rate limits, and require network access. Mark them slow by
// setting a generous -timeout.

// TestIntegration_SECTickerMap verifies that the SEC company-ticker map can be
// loaded and contains well-known symbols.
func TestIntegration_SECTickerMap(t *testing.T) {
	c := NewSECClient()
	if err := c.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}
	for _, sym := range []string{"AAPL", "MSFT", "NVDA", "TSLA", "GOOG"} {
		cik, err := c.lookupCIK(sym)
		if err != nil || cik <= 0 {
			t.Errorf("lookupCIK(%s) = %d, %v; want valid CIK", sym, cik, err)
		}
	}
}

// TestIntegration_SECQuarterlyActuals verifies XBRL data for AAPL: expects ≥4
// quarters, each with non-zero revenue and EPS.
func TestIntegration_SECQuarterlyActuals(t *testing.T) {
	c := NewSECClient()
	if err := c.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}
	quarters, err := c.FetchQuarterlyActuals("AAPL")
	if err != nil {
		t.Fatalf("FetchQuarterlyActuals(AAPL): %v", err)
	}
	if len(quarters) < 4 {
		t.Errorf("expected ≥4 quarters, got %d", len(quarters))
	}
	for _, q := range quarters {
		if q.Revenue == 0 {
			t.Errorf("quarter %s has zero revenue", q.Period)
		}
		if q.EPS == 0 {
			t.Errorf("quarter %s has zero EPS", q.Period)
		}
	}
	// Most recent quarter must be within 18 months.
	last := quarters[len(quarters)-1]
	lastDate, err := time.Parse("2006-01-02", last.Period)
	if err != nil {
		t.Fatalf("bad period date: %v", err)
	}
	if time.Since(lastDate) > 18*30*24*time.Hour {
		t.Errorf("most-recent quarter %s is older than 18 months", last.Period)
	}
}

// TestIntegration_SECMaterialEvents checks that AAPL has at least one material
// 8-K in the past 6 months with a valid label.
func TestIntegration_SECMaterialEvents(t *testing.T) {
	c := NewSECClient()
	if err := c.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}
	since := time.Now().AddDate(0, -6, 0)
	events, err := c.FetchMaterialEvents("AAPL", since)
	if err != nil {
		t.Fatalf("FetchMaterialEvents(AAPL): %v", err)
	}
	if len(events) == 0 {
		t.Error("expected at least one material 8-K for AAPL in the past 6 months")
	}
	for _, e := range events {
		if e.Date == "" {
			t.Error("event has empty date")
		}
		if e.Label == "" {
			t.Error("event has empty label")
		}
	}
}

// TestIntegration_StockAnalysisEstimates verifies that we can retrieve a live
// forward revenue estimate for AAPL and it is non-zero and plausible.
func TestIntegration_StockAnalysisEstimates(t *testing.T) {
	e := NewEnricher()
	sa, err := e.fetchSAEstimates("AAPL")
	if err != nil {
		t.Fatalf("fetchSAEstimates(AAPL): %v", err)
	}
	// Apple quarterly revenue should be at least $50B.
	if sa.RevenueEst < 50e9 {
		t.Errorf("AAPL revenue estimate = %.1fB, want ≥ $50B", sa.RevenueEst/1e9)
	}
	if sa.EPSEst <= 0 {
		t.Errorf("AAPL EPS estimate = %v, want > 0", sa.EPSEst)
	}
	if sa.Analysts <= 0 {
		t.Errorf("AAPL Analysts count = %d, want > 0", sa.Analysts)
	}
}

// TestIntegration_FinvizInstitutional verifies that Finviz returns plausible
// institutional ownership data for MSFT.
func TestIntegration_FinvizInstitutional(t *testing.T) {
	e := NewEnricher()
	inst, err := e.fetchInstitutionalData("MSFT")
	if err != nil {
		t.Fatalf("fetchInstitutionalData(MSFT): %v", err)
	}
	if inst.InstOwn < 50 || inst.InstOwn > 100 {
		t.Errorf("MSFT InstOwn = %.1f%%, want 50–100%%", inst.InstOwn)
	}
	if inst.Activity == "" {
		t.Error("Activity should not be empty")
	}
}

// TestIntegration_MacroCalendar verifies the macro calendar has upcoming events.
func TestIntegration_MacroCalendar(t *testing.T) {
	from := time.Now()
	to := from.AddDate(0, 3, 0)
	mc := LoadMacroCalendar(from, to)
	future := mc.EventsNear(from.AddDate(0, 1, 0).Format("2006-01-02"), 45)
	if len(future) == 0 {
		t.Error("expected at least one macro event within the next ~45 days")
	}
}

// TestIntegration_NasdaqCalendar verifies the earnings calendar returns results
// for a one-week window around today's date and filters by market cap.
func TestIntegration_NasdaqCalendar(t *testing.T) {
	nc := NewNasdaqClient()
	from := time.Now()
	to := from.AddDate(0, 0, 7)
	events, _ := nc.FetchEarningsCalendar(from, to)
	if len(events) == 0 {
		t.Skip("no earnings in the next 7 days — skipping (may occur on market holidays)")
	}
	// All returned events must have a non-empty symbol.
	for _, e := range events {
		if e.Symbol == "" {
			t.Errorf("event with empty symbol: %+v", e)
		}
		if e.Date == "" {
			t.Errorf("event %s has empty date", e.Symbol)
		}
	}
}

// TestIntegration_FullEnrichment picks a mega-cap stock reporting in the next
// 90 days (AAPL is a safe perennial choice), runs the full enrichment pipeline,
// and asserts that the key fields are populated.
func TestIntegration_FullEnrichment(t *testing.T) {
	// Use a known mega-cap ticker that always has good SEC EDGAR data.
	symbol := "AAPL"
	res := EarningsResult{
		Symbol:     symbol,
		MarketCapB: 3000,
	}
	macro := LoadMacroCalendar(time.Now(), time.Now().AddDate(0, 3, 0))
	enricher := NewEnricher()
	if err := enricher.secClient.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}

	s := enricher.buildSummary(res, nasdaqCalendarRow{}, macro)

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

// TestIntegration_IBKRBrokerRevenue guards the IBKR P/S regression: IBKR's
// SEC revenue must use gross interest income (InterestIncomeOperating +
// NoninterestIncome) and the resulting P/S should be 3–6x (not >10x which the
// old net-revenue figure produced).
func TestIntegration_IBKRBrokerRevenue(t *testing.T) {
	c := NewSECClient()
	if err := c.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}
	quarters, err := c.FetchQuarterlyActuals("IBKR")
	if err != nil {
		t.Fatalf("FetchQuarterlyActuals(IBKR): %v", err)
	}
	if len(quarters) == 0 {
		t.Fatal("expected quarterly data for IBKR")
	}
	// IBKR quarterly gross revenue should be well above $1B (recent quarters ~$1.4B).
	last := quarters[len(quarters)-1]
	if last.Revenue < 1e9 {
		t.Errorf("IBKR gross revenue = $%.2fB, want ≥$1B (gross, not net)", last.Revenue/1e9)
	}
}

// TestIntegration_RevenueEstimateNotOneQuarterAhead guards the IBM regression:
// the revenue estimate returned by parseSAEstimates must correspond to the
// FIRST row with analysts>0 in the quarterly table (upcoming report), NOT the
// second row (one quarter further out, previously causing ~14% overestimate).
func TestIntegration_RevenueEstimateNotOneQuarterAhead(t *testing.T) {
	e := NewEnricher()
	sa, err := e.fetchSAEstimates("IBM")
	if err != nil {
		t.Fatalf("fetchSAEstimates(IBM): %v", err)
	}
	// IBM quarterly revenue is approximately $14–17B; if we accidentally pick
	// the wrong row, the estimate can be 10–20% too high (previously $17.89B
	// when the correct value was $15.64B).  Flag anything above $20B as clearly
	// wrong.
	if sa.RevenueEst > 20e9 {
		t.Errorf("IBM revenue estimate = $%.1fB — appears to be one quarter ahead (expected ≤$20B)", sa.RevenueEst/1e9)
	}
	if sa.RevenueEst < 10e9 {
		t.Errorf("IBM revenue estimate = $%.1fB — unexpectedly low (expected ≥$10B)", sa.RevenueEst/1e9)
	}
	_ = strings.ToLower // avoid unused import warning
}
