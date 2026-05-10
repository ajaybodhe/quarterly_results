package main

import (
	"strings"
	"testing"
)

func TestComputeRecommendationNeutralWithoutData(t *testing.T) {
	rec := ComputeRecommendation(&FinancialSummary{Symbol: "ABC"})
	if rec == nil {
		t.Fatal("expected non-nil recommendation")
	}
	if rec.Label != "Neutral" {
		t.Errorf("empty summary should be Neutral, got %q", rec.Label)
	}
	if rec.Confidence != 0 {
		t.Errorf("expected zero confidence with no data, got %f", rec.Confidence)
	}
}

func TestComputeRecommendationBullish(t *testing.T) {
	// Low valuation vs strong growth + accelerating + sector tailwind +
	// strong reaction history → strongly Positive with high confidence.
	s := &FinancialSummary{
		Symbol:            "BUL",
		PS:                ptrFloat(5),
		PE_Forward:        ptrFloat(15),
		RevenueYoYPct:     ptrFloat(25),
		EPSYoYPct:         ptrFloat(20),
		RevenueYoYPctPrev: ptrFloat(15),
		EPSYoYPctPrev:     ptrFloat(10),
		IndustryMedianPE:  ptrFloat(25),
		PE_TTM:            ptrFloat(20),
		RSI14:             ptrFloat(45),
		PctFrom52Hi:       ptrFloat(-12),
		PriceTargetUpside: ptrFloat(20),
		SectorETF:         "XLK",
		SectorRet1M:       ptrFloat(4),
		EarningsReactions: []EarningsReaction{
			{RetPct: 4}, {RetPct: 6}, {RetPct: -1.5}, {RetPct: 3},
		},
	}
	rec := ComputeRecommendation(s)
	if rec.Label != "Positive" {
		t.Errorf("expected Positive, got %q (score %.1f, conf %.2f)", rec.Label, rec.Score, rec.Confidence)
	}
	if rec.Score <= 0 {
		t.Errorf("expected positive score, got %f", rec.Score)
	}
	if len(rec.TopReasons) == 0 {
		t.Error("expected at least one top reason")
	}
}

func TestComputeRecommendationBearish(t *testing.T) {
	// High valuation, decelerating growth, near 52W high, RSI overbought,
	// negative reaction history.
	s := &FinancialSummary{
		Symbol:            "BER",
		PS:                ptrFloat(25),
		PE_Forward:        ptrFloat(80),
		RevenueYoYPct:     ptrFloat(8),
		EPSYoYPct:         ptrFloat(5),
		RevenueYoYPctPrev: ptrFloat(20),
		EPSYoYPctPrev:     ptrFloat(15),
		IndustryMedianPE:  ptrFloat(20),
		PE_TTM:            ptrFloat(70),
		RSI14:             ptrFloat(82),
		PctFrom52Hi:       ptrFloat(-1),
		PriceTargetUpside: ptrFloat(-10),
		EarningsReactions: []EarningsReaction{
			{RetPct: -3}, {RetPct: -5}, {RetPct: -2}, {RetPct: 1.5},
		},
	}
	rec := ComputeRecommendation(s)
	if rec.Label != "Negative" {
		t.Errorf("expected Negative, got %q (score %.1f, conf %.2f)", rec.Label, rec.Score, rec.Confidence)
	}
	if rec.Score >= 0 {
		t.Errorf("expected negative score, got %f", rec.Score)
	}
}

func TestTopReasonsRanksByImpact(t *testing.T) {
	// Two contributing signals: one with massive weight × score, another with
	// small impact. The big one should rank first.
	results := []SignalResult{
		{Name: "small", Weight: 2, Score: 0.5, Confidence: 0.5, Reason: "minor"},
		{Name: "big", Weight: 18, Score: -0.9, Confidence: 1.0, Reason: "major"},
		{Name: "noop", Weight: 5, Score: 0, Confidence: 0, Reason: ""}, // filtered
	}
	got := topReasons(results, 3)
	if len(got) != 2 {
		t.Fatalf("expected 2 reasons (noop filtered), got %d", len(got))
	}
	if !strings.Contains(got[0], "big") {
		t.Errorf("expected highest-impact first, got %q", got[0])
	}
	if !strings.HasPrefix(got[0], "[−]") {
		t.Errorf("expected negative direction marker, got %q", got[0])
	}
}

func TestLabelForScoreThresholds(t *testing.T) {
	cases := []struct {
		score, conf float64
		want        string
	}{
		{20, 0.8, "Positive"},
		{-20, 0.8, "Negative"},
		{14, 0.8, "Neutral"},  // below score threshold
		{50, 0.2, "Neutral"},  // below confidence threshold
		{-50, 0.39, "Neutral"}, // just below confidence threshold
	}
	for _, tc := range cases {
		got := labelForScore(tc.score, tc.conf)
		if got != tc.want {
			t.Errorf("labelForScore(%v,%v) = %q, want %q", tc.score, tc.conf, got, tc.want)
		}
	}
}
