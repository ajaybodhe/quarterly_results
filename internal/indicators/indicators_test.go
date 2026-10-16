package indicators

import (
	"math"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/testutil"
)

func TestDailyVolatility_OnlyRecentPricesUsed(t *testing.T) {
	// Prices older than the window should be excluded from the vol calculation.
	// Construct two groups: a flat block far in the past and a volatile block
	// within the 5-day window. If the old prices were included, vol would be 0;
	// if only recent prices are used, vol should be positive.
	base := time.Now().AddDate(0, 0, -60)
	var pts []domain.PricePoint
	// 50 flat days far in the past.
	for i := 0; i < 50; i++ {
		pts = append(pts, domain.PricePoint{Date: base.AddDate(0, 0, i), Close: 100})
	}
	// 5 volatile days within the last 5 calendar days.
	recent := time.Now().AddDate(0, 0, -4)
	prices := []float64{100, 110, 95, 108, 102}
	for i, p := range prices {
		pts = append(pts, domain.PricePoint{Date: recent.AddDate(0, 0, i), Close: p})
	}
	vol := DailyVolatility(pts, 5)
	if vol == 0 {
		t.Error("expected non-zero vol from recent volatile prices; old flat prices should not dominate")
	}
}

func TestPctChange(t *testing.T) {
	cases := []struct {
		old, new float64
		want     float64
	}{
		{0, 5, 0},     // divide-by-zero guard
		{10, 12, 20},  // basic positive
		{10, 8, -20},  // basic negative
		{100, 100, 0}, // no change
		{1, 2, 100},   // double
		{-4, -2, 50},  // both negative, improving (less negative)
		{-4, -6, -50}, // both negative, worsening
		{-1, 1, 200},  // cross zero from negative
		{50, 0, -100}, // drop to zero
	}
	for _, tc := range cases {
		got := PctChange(tc.old, tc.new)
		if got != tc.want {
			t.Errorf("pctChange(%v, %v) = %v, want %v", tc.old, tc.new, got, tc.want)
		}
	}
}

func TestOpenOnDate(t *testing.T) {
	pts := []domain.PricePoint{
		{Date: testutil.MustDate("2025-01-08"), Open: 110, Close: 112},
		{Date: testutil.MustDate("2025-01-09"), Open: 115, Close: 118},
	}
	if got := OpenOnDate(pts, testutil.MustDate("2025-01-08")); got != 110 {
		t.Errorf("openOnDate exact match = %v, want 110", got)
	}
	if got := OpenOnDate(pts, testutil.MustDate("2025-01-09")); got != 115 {
		t.Errorf("openOnDate Jan 9 = %v, want 115", got)
	}
	if got := OpenOnDate(pts, testutil.MustDate("2025-01-10")); got != 0 {
		t.Errorf("openOnDate missing date = %v, want 0", got)
	}
	if got := OpenOnDate(nil, testutil.MustDate("2025-01-08")); got != 0 {
		t.Errorf("openOnDate nil slice = %v, want 0", got)
	}
}

func TestClosestPrice(t *testing.T) {
	pts := []domain.PricePoint{
		{Date: testutil.MustDate("2025-01-08"), Close: 100},
		{Date: testutil.MustDate("2025-01-10"), Close: 200},
		{Date: testutil.MustDate("2025-01-14"), Close: 300},
	}

	t.Run("empty slice", func(t *testing.T) {
		_, ok := ClosestPrice(nil, testutil.MustDate("2025-01-10"))
		if ok {
			t.Error("expected ok=false for empty slice")
		}
	})
	t.Run("target before all points", func(t *testing.T) {
		_, ok := ClosestPrice(pts, testutil.MustDate("2025-01-07"))
		if ok {
			t.Error("expected ok=false when target is before all points")
		}
	})
	t.Run("exact match", func(t *testing.T) {
		price, ok := ClosestPrice(pts, testutil.MustDate("2025-01-10"))
		if !ok || price != 200 {
			t.Errorf("got (%v, %v), want (200, true)", price, ok)
		}
	})
	t.Run("target between points", func(t *testing.T) {
		// Jan 9 is between Jan 8 and Jan 10 — should return Jan 8's price
		price, ok := ClosestPrice(pts, testutil.MustDate("2025-01-09"))
		if !ok || price != 100 {
			t.Errorf("got (%v, %v), want (100, true)", price, ok)
		}
	})
	t.Run("target after all points", func(t *testing.T) {
		price, ok := ClosestPrice(pts, testutil.MustDate("2025-01-20"))
		if !ok || price != 300 {
			t.Errorf("got (%v, %v), want (300, true)", price, ok)
		}
	})
}

// mkPrices builds a domain.PricePoint slice from a close-price series; dates are
// sequential starting from 2025-01-01.
func mkPrices(closes ...float64) []domain.PricePoint {
	base, _ := time.Parse("2006-01-02", "2025-01-01")
	out := make([]domain.PricePoint, len(closes))
	for i, c := range closes {
		out[i] = domain.PricePoint{
			Date:  base.AddDate(0, 0, i),
			Open:  c,
			Close: c,
		}
	}
	return out
}

func TestComputeRSI14_InsufficientData(t *testing.T) {
	// < period+1 inputs → neutral 50.
	for _, n := range []int{0, 1, 14} {
		got := RSI14(mkPrices(make([]float64, n)...))
		if got != 50 {
			t.Errorf("computeRSI14(n=%d) = %v, want 50 (insufficient data)", n, got)
		}
	}
}

func TestComputeRSI14_AllGains(t *testing.T) {
	// Monotonically rising price → no losses → RSI must clamp to 100.
	prices := make([]float64, 30)
	for i := range prices {
		prices[i] = 100 + float64(i)
	}
	got := RSI14(mkPrices(prices...))
	if got != 100 {
		t.Errorf("all-gains RSI = %v, want 100", got)
	}
}

func TestComputeRSI14_AllLosses(t *testing.T) {
	// Monotonically falling price → no gains → avgGain=0 → RSI=0.
	prices := make([]float64, 30)
	for i := range prices {
		prices[i] = 200 - float64(i)
	}
	got := RSI14(mkPrices(prices...))
	if got != 0 {
		t.Errorf("all-losses RSI = %v, want 0", got)
	}
}

func TestComputeRSI14_BalancedAlternating(t *testing.T) {
	// Perfectly alternating ±1 moves across 30 bars → avg gain = avg loss → RSI ≈ 50.
	prices := make([]float64, 30)
	for i := range prices {
		if i%2 == 0 {
			prices[i] = 100
		} else {
			prices[i] = 101
		}
	}
	got := RSI14(mkPrices(prices...))
	// Wilder's smoothing on a finite alternating series converges near 50 but
	// not exactly 50; tolerance of 5 catches clearly directional readings.
	if math.Abs(got-50) > 5.0 {
		t.Errorf("balanced alternating RSI = %v, want within 5 of 50", got)
	}
}

func TestDailyVolatility_InsufficientData(t *testing.T) {
	if got := DailyVolatility(nil, 30); got != 0 {
		t.Errorf("empty prices: want 0, got %v", got)
	}
	if got := DailyVolatility(mkPrices(100), 30); got != 0 {
		t.Errorf("single price: want 0, got %v", got)
	}
}

func TestDailyVolatility_ZeroVol(t *testing.T) {
	// Flat price → no daily changes → vol = 0.
	prices := mkPrices(100, 100, 100, 100, 100, 100, 100, 100, 100, 100)
	got := DailyVolatility(prices, 30)
	if got != 0 {
		t.Errorf("flat price: want 0, got %v", got)
	}
}

func TestDailyVolatility_Positive(t *testing.T) {
	// Alternating price → nonzero vol.
	prices := mkPrices(100, 101, 100, 101, 100, 101, 100, 101, 100, 101, 100)
	got := DailyVolatility(prices, 30)
	if got <= 0 {
		t.Errorf("alternating prices: expected positive vol, got %v", got)
	}
}

func TestComputeRSI14_Range(t *testing.T) {
	// Realistic-ish path with a small positive drift → RSI should land in (50, 100).
	prices := []float64{
		100, 101, 100.5, 102, 103, 101, 100, 102, 104, 103,
		105, 107, 106, 108, 109, 111, 110, 112, 114, 113,
	}
	got := RSI14(mkPrices(prices...))
	if got <= 50 || got >= 100 {
		t.Errorf("upward-drift RSI = %v, want in (50, 100)", got)
	}
}
