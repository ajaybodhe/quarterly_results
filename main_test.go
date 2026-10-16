package main

import (
	"testing"

	"quarterly_results/internal/domain"
)

func events(caps ...float64) []domain.EarningsEvent {
	out := make([]domain.EarningsEvent, len(caps))
	for i, c := range caps {
		out[i] = domain.EarningsEvent{Symbol: "X", MarketCap: c}
	}
	return out
}

func TestFilterByMarketCap_MinOnly(t *testing.T) {
	// minCap=50B, no upper bound (maxCap=0).
	in := events(10e9, 50e9, 100e9, 200e9)
	got := filterByMarketCap(in, 50e9, 0)
	if len(got) != 3 {
		t.Errorf("expected 3 results (≥$50B), got %d", len(got))
	}
}

func TestFilterByMarketCap_MinAndMax(t *testing.T) {
	in := events(5e9, 20e9, 50e9, 100e9, 500e9)
	// $10B–$100B window → 20B, 50B, 100B qualify.
	got := filterByMarketCap(in, 10e9, 100e9)
	if len(got) != 3 {
		t.Errorf("expected 3 results ($10B–$100B), got %d: %+v", len(got), got)
	}
}

func TestFilterByMarketCap_MaxZeroMeansNoUpperBound(t *testing.T) {
	in := events(1e9, 999e9)
	got := filterByMarketCap(in, 1, 0) // minCap=$1, no max
	if len(got) != 2 {
		t.Errorf("maxCap=0 should include all ≥minCap, got %d", len(got))
	}
}

func TestFilterByMarketCap_BoundaryInclusion(t *testing.T) {
	// Exactly on the boundaries — both endpoints are inclusive.
	in := events(10e9, 50e9)
	got := filterByMarketCap(in, 10e9, 50e9)
	if len(got) != 2 {
		t.Errorf("boundary values should be included, got %d", len(got))
	}
}

func TestFilterByMarketCap_NoneQualify(t *testing.T) {
	in := events(1e9, 2e9, 3e9)
	got := filterByMarketCap(in, 100e9, 0)
	if len(got) != 0 {
		t.Errorf("expected 0 results, got %d", len(got))
	}
}

func TestFilterByMarketCap_EmptyInput(t *testing.T) {
	got := filterByMarketCap(nil, 10e9, 100e9)
	if len(got) != 0 {
		t.Errorf("expected 0 results for nil input, got %d", len(got))
	}
}

func TestFilterByMarketCap_MaxBelowMin(t *testing.T) {
	// maxCap < minCap → nothing passes both filters.
	in := events(10e9, 20e9, 30e9)
	got := filterByMarketCap(in, 50e9, 10e9) // 50B min, 10B max: impossible range
	if len(got) != 0 {
		t.Errorf("impossible range should return 0 results, got %d", len(got))
	}
}
