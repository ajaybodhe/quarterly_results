//go:build integration

package sec

import (
	"testing"
	"time"
)

// TestIntegration_SECTickerMap verifies that the SEC company-ticker map can be
// loaded and contains well-known symbols.
func TestIntegration_SECTickerMap(t *testing.T) {
	c := NewClient()
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
	c := NewClient()
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
	c := NewClient()
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

// TestIntegration_IBKRBrokerRevenue guards the IBKR P/S regression: IBKR's
// SEC revenue must use gross interest income (InterestIncomeOperating +
// NoninterestIncome) and the resulting P/S should be 3–6x (not >10x which the
// old net-revenue figure produced).
func TestIntegration_IBKRBrokerRevenue(t *testing.T) {
	c := NewClient()
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
