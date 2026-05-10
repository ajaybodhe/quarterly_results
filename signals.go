package main

import (
	"fmt"
	"math"
	"strings"
)

// SignalResult is the per-signal output captured for explainability and for
// the backtest harness's hit-rate decomposition.
type SignalResult struct {
	Name       string  `json:"name"`
	Weight     float64 `json:"weight"`
	Score      float64 `json:"score"`      // [-1, +1]; positive = bullish
	Confidence float64 `json:"confidence"` // [0, 1]; 0 = data missing
	Reason     string  `json:"reason"`     // human-readable, may be ""
}

// Signal binds a name and weight to a scoring function.
type Signal struct {
	Name    string
	Weight  float64
	Compute func(s *FinancialSummary) (score, confidence float64, reason string)
}

// DefaultSignals lists every signal applied by ComputeRecommendation.
// Total weight should sum to 100. Adjust weights here without touching the
// scoring math.
var DefaultSignals = []Signal{
	{"valuation_vs_growth", 18, signalValuationVsGrowth},
	{"growth_trajectory", 14, signalGrowthTrajectory},
	{"pe_vs_industry", 8, signalPEvsIndustry},
	{"position", 12, signalPosition},
	{"insider", 8, signalInsider},
	{"institutional", 4, signalInstitutional},
	{"options_sentiment", 10, signalOptionsSentiment},
	{"beat_history", 8, signalBeatHistory},
	{"reaction_tendency", 6, signalReactionTendency},
	{"peer_reactions", 8, signalPeerReactions},
	{"sector_momentum", 4, signalSectorMomentum},
}

// BacktestSignals is a strict subset of DefaultSignals: only those signals
// reconstructable from price history and prior reactions/peers at a historical
// point in time without lookahead bias. The weights are renormalised inside
// ComputeRecommendationFrom.
var BacktestSignals = []Signal{
	{"position", 12, signalPosition},
	{"reaction_tendency", 6, signalReactionTendency},
	{"peer_reactions", 8, signalPeerReactions},
	{"sector_momentum", 4, signalSectorMomentum},
}

// ── helpers ──────────────────────────────────────────────────────────────────

func clamp1(x float64) float64 {
	if x > 1 {
		return 1
	}
	if x < -1 {
		return -1
	}
	return x
}

// pegScore converts a PEG-like ratio to a [-1, +1] score where
// peg=0.5 → +1 (cheap vs growth), peg=1.25 → 0, peg=2 → -1 (expensive).
func pegScore(peg float64) float64 {
	return clamp1((1.25 - peg) / 0.75)
}

// ── Signal 1: valuation vs growth ────────────────────────────────────────────

// signalValuationVsGrowth computes PEG-style ratios from PS/RevYoY and
// PE_Forward/EPSYoY. Stocks priced at a heavy multiple relative to their
// growth rate score bearish; cheap-vs-growth scores bullish. Negative or
// zero growth with any positive valuation is treated as outright bearish.
func signalValuationVsGrowth(s *FinancialSummary) (float64, float64, string) {
	var scores []float64
	var labels []string

	if s.PS != nil && *s.PS > 0 && s.RevenueYoYPct != nil {
		switch {
		case *s.RevenueYoYPct > 0:
			peg := *s.PS / *s.RevenueYoYPct
			scores = append(scores, pegScore(peg))
			labels = append(labels, fmt.Sprintf("PEG_rev=%.1f", peg))
		case *s.PS > 1:
			scores = append(scores, -1)
			labels = append(labels, fmt.Sprintf("PS=%.1f w/ shrinking rev", *s.PS))
		}
	}
	if s.PE_Forward != nil && *s.PE_Forward > 0 && s.EPSYoYPct != nil {
		switch {
		case *s.EPSYoYPct > 0:
			peg := *s.PE_Forward / *s.EPSYoYPct
			scores = append(scores, pegScore(peg))
			labels = append(labels, fmt.Sprintf("PEG_eps=%.1f", peg))
		default:
			scores = append(scores, -1)
			labels = append(labels, fmt.Sprintf("PE_fwd=%.0f w/ EPS contraction", *s.PE_Forward))
		}
	}

	if len(scores) == 0 {
		return 0, 0, ""
	}
	var sum float64
	for _, x := range scores {
		sum += x
	}
	avg := sum / float64(len(scores))
	conf := float64(len(scores)) / 2.0 // both ratios available → 1.0
	return avg, conf, strings.Join(labels, "; ")
}

// ── Signal 2: growth trajectory (acceleration vs deceleration) ───────────────

// signalGrowthTrajectory detects whether YoY growth is accelerating or
// decelerating into the upcoming quarter. The "previous" YoY is the most
// recent reported quarter's YoY (computed from History[]); the "current"
// YoY is the consensus estimate vs same-quarter-last-year. A 5pp drop maps
// to score -1 (clearly decelerating); a 5pp rise maps to +1.
func signalGrowthTrajectory(s *FinancialSummary) (float64, float64, string) {
	var deltas []float64
	var labels []string

	if s.RevenueYoYPct != nil && s.RevenueYoYPctPrev != nil {
		d := *s.RevenueYoYPct - *s.RevenueYoYPctPrev
		deltas = append(deltas, d)
		labels = append(labels, fmt.Sprintf("rev YoY %.0f%%→%.0f%% (Δ%+.0fpp)",
			*s.RevenueYoYPctPrev, *s.RevenueYoYPct, d))
	}
	if s.EPSYoYPct != nil && s.EPSYoYPctPrev != nil {
		d := *s.EPSYoYPct - *s.EPSYoYPctPrev
		deltas = append(deltas, d)
		labels = append(labels, fmt.Sprintf("EPS YoY %.0f%%→%.0f%% (Δ%+.0fpp)",
			*s.EPSYoYPctPrev, *s.EPSYoYPct, d))
	}
	if len(deltas) == 0 {
		return 0, 0, ""
	}
	var sum float64
	for _, d := range deltas {
		sum += d
	}
	avgDelta := sum / float64(len(deltas))
	score := clamp1(avgDelta / 5.0) // ±5pp = full ±1
	conf := float64(len(deltas)) / 2.0
	return score, conf, strings.Join(labels, "; ")
}

// ── Signal 3: PE vs industry median ──────────────────────────────────────────

func signalPEvsIndustry(s *FinancialSummary) (float64, float64, string) {
	if s.IndustryMedianPE == nil || *s.IndustryMedianPE <= 0 {
		return 0, 0, ""
	}
	var ratios []float64
	var labels []string
	if s.PE_TTM != nil && *s.PE_TTM > 0 {
		r := *s.PE_TTM / *s.IndustryMedianPE
		ratios = append(ratios, r)
		labels = append(labels, fmt.Sprintf("PE_ttm %.1fx vs industry %.1fx", *s.PE_TTM, *s.IndustryMedianPE))
	}
	if s.PE_Forward != nil && *s.PE_Forward > 0 && s.IndustryMedianPE != nil {
		r := *s.PE_Forward / *s.IndustryMedianPE
		ratios = append(ratios, r)
	}
	if len(ratios) == 0 {
		return 0, 0, ""
	}
	var sum float64
	for _, r := range ratios {
		sum += r
	}
	avgRatio := sum / float64(len(ratios))
	// avgRatio = 1 → 0; avgRatio = 1.2 → -1; avgRatio = 0.85 → +1
	var score float64
	if avgRatio >= 1 {
		score = clamp1(-(avgRatio - 1) / 0.2)
	} else {
		score = clamp1((1 - avgRatio) / 0.15)
	}
	return score, 1, strings.Join(labels, "; ")
}

// ── Signal 4: position (RSI / 52W / PT upside) ───────────────────────────────

// signalPosition fires bearish when a stock is technically over-extended into
// earnings (high RSI, near 52W high, trading above analyst price target) and
// bullish when it is over-sold and well below targets. Each component
// contributes ±0.5; the final score is clamped.
func signalPosition(s *FinancialSummary) (float64, float64, string) {
	var score float64
	var labels []string
	have := 0

	if s.RSI14 != nil {
		have++
		switch {
		case *s.RSI14 > 70:
			score -= 0.4
			labels = append(labels, fmt.Sprintf("RSI=%.0f (overbought)", *s.RSI14))
		case *s.RSI14 < 35:
			score += 0.4
			labels = append(labels, fmt.Sprintf("RSI=%.0f (oversold)", *s.RSI14))
		}
	}
	if s.PctFrom52Hi != nil {
		have++
		switch {
		case *s.PctFrom52Hi > -3: // within 3% of the high
			score -= 0.3
			labels = append(labels, fmt.Sprintf("at 52W high (%.0f%%)", *s.PctFrom52Hi))
		case *s.PctFrom52Hi < -25:
			score += 0.2
			labels = append(labels, fmt.Sprintf("%.0f%% from 52W high", *s.PctFrom52Hi))
		}
	}
	if s.PriceTargetUpside != nil {
		have++
		switch {
		case *s.PriceTargetUpside < -2: // trading above target
			score -= 0.3
			labels = append(labels, fmt.Sprintf("above PT (%.0f%% upside)", *s.PriceTargetUpside))
		case *s.PriceTargetUpside > 15:
			score += 0.3
			labels = append(labels, fmt.Sprintf("PT upside %+.0f%%", *s.PriceTargetUpside))
		}
	}
	if have == 0 {
		return 0, 0, ""
	}
	conf := float64(have) / 3.0
	if len(labels) == 0 {
		return 0, conf, "neutral position"
	}
	return clamp1(score), conf, strings.Join(labels, "; ")
}

// ── Signal 5: insider activity + MSPR ────────────────────────────────────────

func signalInsider(s *FinancialSummary) (float64, float64, string) {
	var score float64
	var labels []string
	have := 0

	if s.Insider != nil && s.Insider.FilingCount > 0 {
		have++
		switch s.Insider.Activity {
		case "Net Buyer":
			score += 0.6
			labels = append(labels, "insiders net buying")
		case "Net Seller":
			score -= 0.6
			labels = append(labels, "insiders net selling")
		}
	}
	if s.MSPRSignal != "" && s.MSPRSignal != "N/A" {
		have++
		switch s.MSPRSignal {
		case "Bullish":
			score += 0.5
			labels = append(labels, fmt.Sprintf("MSPR=%.2f bullish", s.MSPR))
		case "Bearish":
			score -= 0.5
			labels = append(labels, fmt.Sprintf("MSPR=%.2f bearish", s.MSPR))
		}
	}
	if have == 0 {
		return 0, 0, ""
	}
	conf := float64(have) / 2.0
	if len(labels) == 0 {
		return 0, conf, "insider activity neutral"
	}
	return clamp1(score), conf, strings.Join(labels, "; ")
}

// ── Signal 6: institutional flow (QoQ) ───────────────────────────────────────

func signalInstitutional(s *FinancialSummary) (float64, float64, string) {
	if s.Institutional == nil {
		return 0, 0, ""
	}
	delta := s.Institutional.InstTrans
	score := clamp1(delta / 2.0) // ±2pp QoQ = full ±1
	if math.Abs(delta) < 0.1 {
		return 0, 0.5, "institutional flow flat"
	}
	return score, 1, fmt.Sprintf("institutional QoQ %+.1fpp", delta)
}

// ── Signal 7: options sentiment ──────────────────────────────────────────────

// signalOptionsSentiment combines P/C ratios, skew, and implied-vs-historical
// move. Heavy puts and steep skew score bearish; quiet options pricing
// (low PC ratios, low skew) and a market underpricing the move (IV/Hist < 1)
// score bullish.
func signalOptionsSentiment(s *FinancialSummary) (float64, float64, string) {
	if s.Options == nil {
		return 0, 0, ""
	}
	o := s.Options
	var score float64
	var labels []string

	if o.PCVol > 0 {
		switch {
		case o.PCVol > 1.2:
			score -= 0.4
			labels = append(labels, fmt.Sprintf("P/C_vol=%.2f (heavy puts)", o.PCVol))
		case o.PCVol < 0.7:
			score += 0.3
			labels = append(labels, fmt.Sprintf("P/C_vol=%.2f (heavy calls)", o.PCVol))
		}
	}
	if o.Skew != 0 {
		switch {
		case o.Skew > 5:
			score -= 0.3
			labels = append(labels, fmt.Sprintf("skew=%.1f (downside hedging)", o.Skew))
		case o.Skew < 2 && o.Skew > -10:
			score += 0.2
		}
	}
	if s.ImpliedVsHistRatio != nil {
		switch {
		case *s.ImpliedVsHistRatio < 0.9:
			score += 0.2
			labels = append(labels, fmt.Sprintf("IV/hist=%.2fx (move under-priced)", *s.ImpliedVsHistRatio))
		case *s.ImpliedVsHistRatio > 1.5:
			score -= 0.1
		}
	}

	if len(labels) == 0 && o.PCVol == 0 && o.Skew == 0 {
		return 0, 0, ""
	}
	return clamp1(score), 1, strings.Join(labels, "; ")
}

// ── Signal 8: beat-history consistency ───────────────────────────────────────

func signalBeatHistory(s *FinancialSummary) (float64, float64, string) {
	if s.BeatRate == nil {
		return 0, 0, ""
	}
	br := *s.BeatRate
	var avgBeat float64
	if s.AvgBeatPct != nil {
		avgBeat = *s.AvgBeatPct
	}
	var score float64
	switch {
	case br >= 0.75 && avgBeat >= 3:
		score = 0.8
	case br >= 0.75:
		score = 0.5
	case br <= 0.25:
		score = -0.8
	case br < 0.5:
		score = -0.4
	}
	conf := 0.5
	if len(s.EarningsReactions) >= 4 {
		conf = 1.0
	}
	return score, conf, fmt.Sprintf("beat rate %.0f%%, avg beat %+.1f%%", br*100, avgBeat)
}

// ── Signal 9: reaction tendency (sign of last 4 reactions) ───────────────────

func signalReactionTendency(s *FinancialSummary) (float64, float64, string) {
	if len(s.EarningsReactions) == 0 {
		return 0, 0, ""
	}
	pos, neg := 0, 0
	for _, r := range s.EarningsReactions {
		switch {
		case r.RetPct > 1:
			pos++
		case r.RetPct < -1:
			neg++
		}
	}
	total := len(s.EarningsReactions)
	score := float64(pos-neg) / float64(total)
	conf := math.Min(float64(total)/4.0, 1.0)
	return clamp1(score), conf, fmt.Sprintf("%d/%d positive reactions", pos, total)
}

// ── Signal 10: peer reactions to same quarter ────────────────────────────────

func signalPeerReactions(s *FinancialSummary) (float64, float64, string) {
	if len(s.Peers) == 0 {
		return 0, 0, ""
	}
	var sum float64
	var n int
	pos, neg := 0, 0
	for _, p := range s.Peers {
		sum += p.DayRetPct
		n++
		switch {
		case p.DayRetPct > 1:
			pos++
		case p.DayRetPct < -1:
			neg++
		}
	}
	if n == 0 {
		return 0, 0, ""
	}
	avg := sum / float64(n)
	score := clamp1(avg / 3.0) // ±3% peer avg = full ±1
	conf := math.Min(float64(n)/4.0, 1.0)
	return score, conf, fmt.Sprintf("peer avg %+.1f%% (%d↑ / %d↓)", avg, pos, neg)
}

// ── Signal 11: sector momentum ───────────────────────────────────────────────

func signalSectorMomentum(s *FinancialSummary) (float64, float64, string) {
	if s.SectorRet1M == nil {
		return 0, 0, ""
	}
	r := *s.SectorRet1M
	score := clamp1(r / 5.0) // ±5% = full ±1
	return score, 1, fmt.Sprintf("%s 1M %+.1f%%", s.SectorETF, r)
}
