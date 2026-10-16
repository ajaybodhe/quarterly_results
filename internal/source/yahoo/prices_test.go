package yahoo

import (
	"encoding/json"
	"testing"
	"time"

	"quarterly_results/internal/indicators"
)

func mustChart(t *testing.T, body string) yahooChartResponse {
	t.Helper()
	var raw yahooChartResponse
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// Yahoo stamps US daily bars with the 09:30 ET session open (13:30 UTC).
// Bars must come back dated at midnight UTC of the trading day so that
// ClosestPrice(prices, <YYYY-MM-DD>) finds that day's bar, not the previous one.
func TestParseChart_NormalizesSessionOpenToTradingDate(t *testing.T) {
	raw := mustChart(t, `{"chart":{"result":[{
		"meta":{"exchangeTimezoneName":"America/New_York","gmtoffset":-14400},
		"timestamp":[1761744600,1761831000,1761917400],
		"indicators":{"quote":[{"open":[269.28,271.99,276.99],"close":[269.70,271.40,270.37]}]}}]}}`)
	prices, err := parseChart(raw, "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2025-10-29", "2025-10-30", "2025-10-31"}
	for i, p := range prices {
		if got := p.Date.Format(time.RFC3339); got != want[i]+"T00:00:00Z" {
			t.Errorf("bar %d date = %s, want %sT00:00:00Z", i, got, want[i])
		}
	}
	oct30, _ := time.Parse("2006-01-02", "2025-10-30")
	if c, ok := indicators.ClosestPrice(prices, oct30); !ok || c != 271.40 {
		t.Errorf("ClosestPrice(2025-10-30) = %v, %v; want the Oct 30 close 271.40", c, ok)
	}
}

// Sydney opens at 10:00 AEDT = 23:00 UTC on the previous calendar day; the
// bar must be dated by the exchange-local day, not the UTC day.
func TestParseChart_UsesExchangeLocalDate(t *testing.T) {
	raw := mustChart(t, `{"chart":{"result":[{
		"meta":{"exchangeTimezoneName":"Australia/Sydney","gmtoffset":39600},
		"timestamp":[1761865200],
		"indicators":{"quote":[{"open":[10.0],"close":[10.5]}]}}]}}`)
	prices, err := parseChart(raw, "BHP.AX")
	if err != nil {
		t.Fatal(err)
	}
	if got := prices[0].Date.Format("2006-01-02"); got != "2025-10-31" {
		t.Errorf("date = %s, want 2025-10-31 (Sydney trading day)", got)
	}
}

func TestParseChart_FallsBackToGMTOffsetThenUTC(t *testing.T) {
	if loc := chartLocation("Not/AZone", 3600); loc.String() != "exchange" {
		t.Errorf("expected fixed-offset zone, got %s", loc)
	}
	if loc := chartLocation("", 0); loc != time.UTC {
		t.Errorf("expected UTC, got %s", loc)
	}
}
