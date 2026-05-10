package main

import (
	"math"
	"testing"
)

func ptrFloat(v float64) *float64 { return &v }

// Each signal must respect three contracts:
//   1. score is in [-1, +1]
//   2. confidence is in [0, 1]
//   3. confidence == 0 ⇒ score == 0  (no data → no opinion)
func TestSignalContracts(t *testing.T) {
	cases := []struct {
		name string
		sum  *FinancialSummary
	}{
		{"empty", &FinancialSummary{}},
		{"partial", &FinancialSummary{
			PS:            ptrFloat(8),
			RevenueYoYPct: ptrFloat(12),
			RSI14:         ptrFloat(72),
		}},
	}
	for _, tc := range cases {
		for _, sig := range DefaultSignals {
			score, conf, _ := sig.Compute(tc.sum)
			if score < -1 || score > 1 {
				t.Errorf("%s/%s: score %f out of [-1,1]", tc.name, sig.Name, score)
			}
			if conf < 0 || conf > 1 {
				t.Errorf("%s/%s: confidence %f out of [0,1]", tc.name, sig.Name, conf)
			}
			if conf == 0 && score != 0 {
				t.Errorf("%s/%s: zero confidence but score %f != 0", tc.name, sig.Name, score)
			}
		}
	}
}

func TestSignalValuationVsGrowthExpensive(t *testing.T) {
	// PS=20, growth=10% → PEG=2 → bearish (-1).
	s := &FinancialSummary{
		PS:            ptrFloat(20),
		RevenueYoYPct: ptrFloat(10),
	}
	score, conf, reason := signalValuationVsGrowth(s)
	if score >= 0 {
		t.Errorf("expected bearish, got score=%f", score)
	}
	if conf == 0 {
		t.Error("expected non-zero confidence")
	}
	if reason == "" {
		t.Error("expected non-empty reason")
	}
}

func TestSignalValuationVsGrowthCheap(t *testing.T) {
	// PS=4, growth=20% → PEG=0.2 → bullish (+1).
	s := &FinancialSummary{
		PS:            ptrFloat(4),
		RevenueYoYPct: ptrFloat(20),
	}
	score, _, _ := signalValuationVsGrowth(s)
	if score <= 0 {
		t.Errorf("expected bullish, got score=%f", score)
	}
}

func TestSignalGrowthTrajectoryDecel(t *testing.T) {
	// rev YoY drops from 30% to 20% (Δ -10pp): clearly decelerating → -1.
	s := &FinancialSummary{
		RevenueYoYPct:     ptrFloat(20),
		RevenueYoYPctPrev: ptrFloat(30),
	}
	score, _, _ := signalGrowthTrajectory(s)
	if score >= 0 {
		t.Errorf("expected negative for decel, got %f", score)
	}
}

func TestSignalGrowthTrajectoryAccel(t *testing.T) {
	// rev YoY rises from 10% to 25%, EPS rises 8%→18%: clearly accelerating → +1.
	s := &FinancialSummary{
		RevenueYoYPct:     ptrFloat(25),
		RevenueYoYPctPrev: ptrFloat(10),
		EPSYoYPct:         ptrFloat(18),
		EPSYoYPctPrev:     ptrFloat(8),
	}
	score, conf, _ := signalGrowthTrajectory(s)
	if score <= 0 {
		t.Errorf("expected positive for accel, got %f", score)
	}
	if conf < 0.99 {
		t.Errorf("expected full confidence with both rev+EPS, got %f", conf)
	}
}

func TestSignalPosition(t *testing.T) {
	// RSI 80 + at 52W high + above PT → strong bearish.
	s := &FinancialSummary{
		RSI14:             ptrFloat(80),
		PctFrom52Hi:       ptrFloat(-1),
		PriceTargetUpside: ptrFloat(-5),
	}
	score, _, _ := signalPosition(s)
	if score >= -0.5 {
		t.Errorf("expected very bearish, got %f", score)
	}
}

func TestSignalReactionTendency(t *testing.T) {
	s := &FinancialSummary{
		EarningsReactions: []EarningsReaction{
			{RetPct: 5}, {RetPct: 3}, {RetPct: -1.5}, {RetPct: 4},
		},
	}
	score, conf, _ := signalReactionTendency(s)
	if score <= 0 {
		t.Errorf("3 of 4 positive should score positive, got %f", score)
	}
	if math.Abs(conf-1.0) > 1e-9 {
		t.Errorf("expected full confidence with 4 reactions, got %f", conf)
	}
}

func TestSignalSectorMomentumBullish(t *testing.T) {
	s := &FinancialSummary{SectorETF: "XLK", SectorRet1M: ptrFloat(6)}
	score, _, _ := signalSectorMomentum(s)
	if score < 0.99 { // 6% / 5 = 1.2 → clamped to 1.0
		t.Errorf("expected clamped to +1, got %f", score)
	}
}

func TestSignalPEvsIndustryExpensive(t *testing.T) {
	s := &FinancialSummary{
		PE_TTM:           ptrFloat(40),
		IndustryMedianPE: ptrFloat(25),
	}
	score, _, _ := signalPEvsIndustry(s)
	if score >= 0 {
		t.Errorf("expected bearish for 1.6× industry PE, got %f", score)
	}
}

func TestPegScore(t *testing.T) {
	cases := []struct {
		peg  float64
		want float64
	}{
		{0.5, 1},
		{1.25, 0},
		{2, -1},
		{4, -1}, // clamped
	}
	for _, tc := range cases {
		got := pegScore(tc.peg)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("pegScore(%f) = %f, want %f", tc.peg, got, tc.want)
		}
	}
}
