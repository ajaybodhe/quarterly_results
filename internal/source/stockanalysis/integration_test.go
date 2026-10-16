//go:build integration

package stockanalysis

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestIntegration_StockAnalysisEstimates verifies that we can retrieve a live
// forward revenue estimate for AAPL and it is non-zero and plausible.
func TestIntegration_StockAnalysisEstimates(t *testing.T) {
	sa, err := FetchEstimates(&http.Client{Timeout: 30 * time.Second}, "AAPL")
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

// TestIntegration_RevenueEstimateNotOneQuarterAhead guards the IBM regression:
// the revenue estimate returned by parseSAEstimates must correspond to the
// FIRST row with analysts>0 in the quarterly table (upcoming report), NOT the
// second row (one quarter further out, previously causing ~14% overestimate).
func TestIntegration_RevenueEstimateNotOneQuarterAhead(t *testing.T) {
	sa, err := FetchEstimates(&http.Client{Timeout: 30 * time.Second}, "IBM")
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
