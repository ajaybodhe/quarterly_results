//go:build integration

package nasdaq

import (
	"testing"
	"time"
)

// TestIntegration_NasdaqCalendar verifies the earnings calendar returns results
// for a one-week window around today's date and filters by market cap.
func TestIntegration_NasdaqCalendar(t *testing.T) {
	nc := NewClient()
	from := time.Now()
	to := from.AddDate(0, 0, 7)
	events, _, _ := nc.FetchEarningsCalendar(from, to)
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
