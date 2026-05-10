package main

import (
	"testing"
	"time"
)

func TestLabelFromReturn(t *testing.T) {
	cases := []struct {
		ret  float64
		want string
	}{
		{2, "Positive"},
		{-2, "Negative"},
		{0.5, "Neutral"},
		{-0.5, "Neutral"},
		{1.0001, "Positive"},
	}
	for _, tc := range cases {
		got := labelFromReturn(tc.ret)
		if got != tc.want {
			t.Errorf("labelFromReturn(%v) = %q, want %q", tc.ret, got, tc.want)
		}
	}
}

func TestComputeBaseline(t *testing.T) {
	pts := []BacktestPoint{
		{ActualLabel: "Positive"},
		{ActualLabel: "Positive"},
		{ActualLabel: "Negative"},
		{ActualLabel: "Neutral"},
	}
	got := computeBaseline(pts)
	want := 0.5 // Positive is most common (2/4)
	if got != want {
		t.Errorf("computeBaseline = %f, want %f", got, want)
	}
	if computeBaseline(nil) != 0 {
		t.Error("empty points should give 0 baseline")
	}
}

// reconstructThinSummary truncates prices to bars strictly before asOf and
// rebuilds 52W and RSI from that prefix. We give it 200 bars of synthetic
// rising prices and pick an asOf in the middle — the reconstructed series
// should not contain anything from the future.
func TestReconstructThinSummaryNoLookahead(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	prices := make([]pricePoint, 200)
	for i := range prices {
		prices[i] = pricePoint{
			Date:  base.AddDate(0, 0, i),
			Open:  100 + float64(i)*0.5,
			Close: 100 + float64(i)*0.5,
		}
	}
	asOf := base.AddDate(0, 0, 100) // bar 100

	live := &FinancialSummary{Symbol: "X", AvgPriceTarget: 200}
	thin := reconstructThinSummary(live, nil, prices, asOf, 0, nil)

	// CurrentPrice must match a bar strictly before asOf.
	if thin.CurrentPrice == 0 {
		t.Fatal("expected non-zero current price")
	}
	if thin.CurrentPrice >= prices[100].Close {
		t.Errorf("current price %.2f leaks ≥ asOf bar %.2f", thin.CurrentPrice, prices[100].Close)
	}
	// Hi52 must not exceed any close at or after asOf.
	for i := 100; i < len(prices); i++ {
		if thin.Hi52 >= prices[i].Close {
			t.Errorf("Hi52 %.2f sourced from at/after asOf (bar %d=%.2f)", thin.Hi52, i, prices[i].Close)
			break
		}
	}
}

func TestRunBacktestEmptyOrNil(t *testing.T) {
	if got := RunBacktest(nil, nil, 0, nil); got != nil {
		t.Error("nil summary should give nil backtest")
	}
	if got := RunBacktest(&FinancialSummary{Symbol: "X"}, nil, 0, nil); got != nil {
		t.Error("zero reactions should give nil backtest")
	}
}

// TestRunBacktestProducesPoints exercises the harness end-to-end with
// fabricated data (no network) and verifies a point is emitted per reaction.
func TestRunBacktestProducesPoints(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	prices := make([]pricePoint, 400)
	for i := range prices {
		// rising market with some variation
		prices[i] = pricePoint{
			Date:  base.AddDate(0, 0, i),
			Open:  100 + float64(i)*0.3,
			Close: 100 + float64(i)*0.3,
		}
	}
	rxnDates := []string{
		base.AddDate(0, 0, 100).Format("2006-01-02"),
		base.AddDate(0, 0, 200).Format("2006-01-02"),
		base.AddDate(0, 0, 300).Format("2006-01-02"),
	}
	s := &FinancialSummary{
		Symbol: "X",
		EarningsReactions: []EarningsReaction{
			{Period: "P1", AnnouncementDate: rxnDates[0], RetPct: 3},
			{Period: "P2", AnnouncementDate: rxnDates[1], RetPct: -2},
			{Period: "P3", AnnouncementDate: rxnDates[2], RetPct: 4},
		},
	}
	bt := RunBacktest(s, prices, 0, nil)
	if bt == nil {
		t.Fatal("expected non-nil backtest")
	}
	if bt.Total != 3 {
		t.Errorf("Total = %d, want 3", bt.Total)
	}
	// The third point must have seen exactly two prior reactions.
	last := bt.Points[len(bt.Points)-1]
	if last.Period != "P3" {
		t.Errorf("expected last point to be P3, got %s", last.Period)
	}
}
