package main

import (
	"math"
	"testing"
	"time"
)

// mkPrices builds a pricePoint slice from a close-price series; dates are
// sequential starting from 2025-01-01.
func mkPrices(closes ...float64) []pricePoint {
	base, _ := time.Parse("2006-01-02", "2025-01-01")
	out := make([]pricePoint, len(closes))
	for i, c := range closes {
		out[i] = pricePoint{
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
		got := computeRSI14(mkPrices(make([]float64, n)...))
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
	got := computeRSI14(mkPrices(prices...))
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
	got := computeRSI14(mkPrices(prices...))
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
	got := computeRSI14(mkPrices(prices...))
	// Wilder's smoothing on a finite alternating series converges near 50 but
	// not exactly 50; tolerance of 5 catches clearly directional readings.
	if math.Abs(got-50) > 5.0 {
		t.Errorf("balanced alternating RSI = %v, want within 5 of 50", got)
	}
}

func TestNewEnricher(t *testing.T) {
	e := NewEnricher()
	if e == nil || e.httpClient == nil || e.yahooClient == nil || e.secClient == nil {
		t.Error("NewEnricher returned incomplete Enricher")
	}
}

func TestDailyVolatility_InsufficientData(t *testing.T) {
	if got := dailyVolatility(nil, 30); got != 0 {
		t.Errorf("empty prices: want 0, got %v", got)
	}
	if got := dailyVolatility(mkPrices(100), 30); got != 0 {
		t.Errorf("single price: want 0, got %v", got)
	}
}

func TestDailyVolatility_ZeroVol(t *testing.T) {
	// Flat price → no daily changes → vol = 0.
	prices := mkPrices(100, 100, 100, 100, 100, 100, 100, 100, 100, 100)
	got := dailyVolatility(prices, 30)
	if got != 0 {
		t.Errorf("flat price: want 0, got %v", got)
	}
}

func TestDailyVolatility_Positive(t *testing.T) {
	// Alternating price → nonzero vol.
	prices := mkPrices(100, 101, 100, 101, 100, 101, 100, 101, 100, 101, 100)
	got := dailyVolatility(prices, 30)
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
	got := computeRSI14(mkPrices(prices...))
	if got <= 50 || got >= 100 {
		t.Errorf("upward-drift RSI = %v, want in (50, 100)", got)
	}
}
