//go:build integration

package macro

import (
	"testing"
	"time"

	"quarterly_results/internal/exchange"
)

// TestIntegration_MacroCalendar verifies the macro calendar has upcoming events.
func TestIntegration_MacroCalendar(t *testing.T) {
	from := time.Now()
	to := from.AddDate(0, 3, 0)
	mc := LoadCalendar(from, to, exchange.DefaultUSConfig)
	future := mc.EventsNear(from.AddDate(0, 1, 0).Format("2006-01-02"), 45)
	if len(future) == 0 {
		t.Error("expected at least one macro event within the next ~45 days")
	}
}
