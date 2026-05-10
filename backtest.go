package main

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// BacktestPoint is one historical {predicted, actual} pair: what the Tier-1
// backtest signals would have said going into a past earnings announcement,
// and what the stock actually did the next session.
type BacktestPoint struct {
	Symbol           string         `json:"symbol"`
	Period           string         `json:"period"`
	AnnouncementDate string         `json:"announcement_date"`
	Predicted        string         `json:"predicted"`         // "Positive"/"Negative"/"Neutral"
	PredictedScore   float64        `json:"predicted_score"`   // [-100, +100]
	Confidence       float64        `json:"confidence"`        // [0, 1]
	ActualRetPct     float64        `json:"actual_ret_pct"`    // full-day reaction return
	ActualLabel      string         `json:"actual_label"`      // ±1% threshold
	Hit              bool           `json:"hit"`               // predicted == actual when both directional
	Signals          []SignalResult `json:"signals,omitempty"` // populated only when --backtest-verbose
}

// BacktestSummary aggregates BacktestPoints into hit-rate and lift statistics.
type BacktestSummary struct {
	Symbol     string          `json:"symbol"`
	Total      int             `json:"total"`
	Directional int            `json:"directional"`        // points where predicted != "Neutral"
	Hits       int             `json:"hits"`               // directional hits
	HitRate    float64         `json:"hit_rate"`           // hits / directional
	Baseline   float64         `json:"baseline"`           // P(actual is most-common label) — what naive guessing scores
	AvgRetWhenPositive float64 `json:"avg_ret_when_positive"`
	AvgRetWhenNegative float64 `json:"avg_ret_when_negative"`
	AvgRetWhenNeutral  float64 `json:"avg_ret_when_neutral"`
	Points     []BacktestPoint `json:"points"`
}

// RunBacktest evaluates the BacktestSignals subset on each EarningsReaction in
// the summary. For each reaction we:
//
//  1. Reconstruct a thin FinancialSummary with only the inputs the Tier-1
//     signals consume — price history truncated to < announcement_date,
//     prior reactions strictly before this one, sector momentum at that date.
//  2. Run ComputeRecommendationFrom(thin, BacktestSignals).
//  3. Compare predicted label to the actual signed reaction (>+1% Positive,
//     <-1% Negative, else Neutral).
//
// `prices` is the same `[]pricePoint` series fetched in buildSummary so the
// caller doesn't pay for a second fetch. `httpClient` is used by sector
// momentum for the historical ETF lookup; pass a configured client.
//
// Tier scope (what's reconstructable cleanly without lookahead):
//   - position           (RSI, %from-52W, PT-upside) ← computed from price history
//   - reaction_tendency  (sign of prior reactions)    ← reactions before this date
//   - peer_reactions     (peers' reactions same Q)    ← from existing peers list
//   - sector_momentum    (ETF return ending asOf)     ← SectorMomentumAt
//
// Excluded from the backtest because they require *current* fundamentals that
// aren't snapshotted historically (PE/PS at date X, MSPR at date X, etc.):
//   valuation_vs_growth, growth_trajectory, pe_vs_industry, insider,
//   institutional, options_sentiment, beat_history.
func RunBacktest(s *FinancialSummary, prices []pricePoint, sicCode int, httpClient *http.Client) *BacktestSummary {
	if s == nil || len(s.EarningsReactions) == 0 {
		return nil
	}
	out := &BacktestSummary{Symbol: s.Symbol}

	// Sort reactions by announcement date ascending so "prior reactions" for
	// each iteration is a proper prefix.
	rxns := make([]EarningsReaction, len(s.EarningsReactions))
	copy(rxns, s.EarningsReactions)
	sort.Slice(rxns, func(i, j int) bool {
		return rxns[i].AnnouncementDate < rxns[j].AnnouncementDate
	})

	for i, rxn := range rxns {
		announceTime, err := time.Parse("2006-01-02", rxn.AnnouncementDate)
		if err != nil {
			continue
		}

		thin := reconstructThinSummary(s, rxns[:i], prices, announceTime, sicCode, httpClient)
		rec := ComputeRecommendationFrom(thin, BacktestSignals)
		if rec == nil {
			continue
		}
		actualLabel := labelFromReturn(rxn.RetPct)
		predicted := rec.Label

		hit := predicted != "Neutral" && predicted == actualLabel

		bp := BacktestPoint{
			Symbol:           s.Symbol,
			Period:           rxn.Period,
			AnnouncementDate: rxn.AnnouncementDate,
			Predicted:        predicted,
			PredictedScore:   rec.Score,
			Confidence:       rec.Confidence,
			ActualRetPct:     rxn.RetPct,
			ActualLabel:      actualLabel,
			Hit:              hit,
			Signals:          rec.Signals,
		}
		out.Points = append(out.Points, bp)
	}

	out.Total = len(out.Points)
	for _, p := range out.Points {
		if p.Predicted != "Neutral" {
			out.Directional++
			if p.Hit {
				out.Hits++
			}
		}
		switch p.Predicted {
		case "Positive":
			out.AvgRetWhenPositive += p.ActualRetPct
		case "Negative":
			out.AvgRetWhenNegative += p.ActualRetPct
		case "Neutral":
			out.AvgRetWhenNeutral += p.ActualRetPct
		}
	}
	if out.Directional > 0 {
		out.HitRate = float64(out.Hits) / float64(out.Directional)
	}
	out.AvgRetWhenPositive = safeMean(out.AvgRetWhenPositive, countWhere(out.Points, "Positive"))
	out.AvgRetWhenNegative = safeMean(out.AvgRetWhenNegative, countWhere(out.Points, "Negative"))
	out.AvgRetWhenNeutral = safeMean(out.AvgRetWhenNeutral, countWhere(out.Points, "Neutral"))
	out.Baseline = computeBaseline(out.Points)
	return out
}

// reconstructThinSummary builds a FinancialSummary populated only with the
// fields the Tier-1 BacktestSignals consume — at the historical state that
// existed just before `asOf`.
//
// Price-derived fields (RSI, %from52H/L, price-target upside) are recomputed
// from the truncated price slice. Reactions are restricted to the prefix that
// happened strictly before `asOf`. Peers are inherited from the live summary
// (the peers list is already same-quarter, so it's always "as of report time"
// — close enough for a coarse proxy without re-fetching peer histories).
// Sector momentum is fetched at `asOf` via SectorMomentumAt.
func reconstructThinSummary(
	live *FinancialSummary,
	priorReactions []EarningsReaction,
	prices []pricePoint,
	asOf time.Time,
	sicCode int,
	httpClient *http.Client,
) *FinancialSummary {
	thin := &FinancialSummary{
		Symbol:            live.Symbol,
		EarningsReactions: priorReactions,
		Peers:             live.Peers,
		AvgPriceTarget:    live.AvgPriceTarget,
	}

	// Truncate price history to bars strictly before asOf.
	cutoff := asOf
	asOfPrices := make([]pricePoint, 0, len(prices))
	for _, p := range prices {
		if !p.Date.Before(cutoff) {
			break
		}
		asOfPrices = append(asOfPrices, p)
	}
	if len(asOfPrices) == 0 {
		return thin
	}

	current := asOfPrices[len(asOfPrices)-1].Close
	thin.CurrentPrice = current

	if thin.AvgPriceTarget > 0 && current > 0 {
		v := pctChange(current, thin.AvgPriceTarget)
		thin.PriceTargetUpside = &v
	}

	// 52-week window ending at asOf.
	yearAgo := asOf.AddDate(-1, 0, 0)
	var hi, lo float64
	for _, p := range asOfPrices {
		if p.Date.Before(yearAgo) {
			continue
		}
		if hi == 0 || p.Close > hi {
			hi = p.Close
		}
		if lo == 0 || p.Close < lo {
			lo = p.Close
		}
	}
	if hi > 0 {
		thin.Hi52 = hi
		v := pctChange(hi, current)
		thin.PctFrom52Hi = &v
	}
	if lo > 0 {
		thin.Lo52 = lo
		v := pctChange(lo, current)
		thin.PctFrom52Lo = &v
	}
	if len(asOfPrices) >= 15 {
		rsi := computeRSI14(asOfPrices)
		thin.RSI14 = &rsi
	}

	// Sector momentum at the historical date.
	if sicCode > 0 {
		if sm := SectorMomentumAt(sicCode, asOf, httpClient); sm != nil {
			thin.SectorETF = sm.ETF
			thin.SectorRet1M = sm.Ret1M
			thin.SectorRet3M = sm.Ret3M
		}
	}

	return thin
}

// labelFromReturn buckets a reaction return into Positive/Negative/Neutral
// using the same ±1% deadband used throughout the recommender.
func labelFromReturn(retPct float64) string {
	switch {
	case retPct > 1:
		return "Positive"
	case retPct < -1:
		return "Negative"
	default:
		return "Neutral"
	}
}

// computeBaseline returns the share of points whose actual label equals the
// most-common actual label. A predictor that always guessed that single
// most-common label would score this hit rate. Useful as a "did the signals
// add anything?" anchor.
func computeBaseline(points []BacktestPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	counts := map[string]int{}
	for _, p := range points {
		counts[p.ActualLabel]++
	}
	var best int
	for _, c := range counts {
		if c > best {
			best = c
		}
	}
	return float64(best) / float64(len(points))
}

func countWhere(points []BacktestPoint, label string) int {
	n := 0
	for _, p := range points {
		if p.Predicted == label {
			n++
		}
	}
	return n
}

func safeMean(sum float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// FormatBacktestSummary renders a single-stock backtest as a short text block
// suitable for printing under the stock card.
func FormatBacktestSummary(b *BacktestSummary) string {
	if b == nil || b.Total == 0 {
		return ""
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Backtest %s — %d quarters, %d directional, hit %d/%d (%.0f%%) vs baseline %.0f%%\n",
		b.Symbol, b.Total, b.Directional, b.Hits, b.Directional,
		b.HitRate*100, b.Baseline*100)
	for _, p := range b.Points {
		mark := "·"
		if p.Predicted != "Neutral" {
			if p.Hit {
				mark = "✓"
			} else {
				mark = "✗"
			}
		}
		fmt.Fprintf(&sb, "  %s %s  pred=%-8s score=%+6.1f conf=%.2f  actual %+5.2f%% (%s)\n",
			mark, p.AnnouncementDate, p.Predicted, p.PredictedScore, p.Confidence,
			p.ActualRetPct, p.ActualLabel)
	}
	return sb.String()
}

// FormatBacktestAggregate produces a one-line summary across many stocks —
// useful when --backtest is run over a date-range fetch.
func FormatBacktestAggregate(byStock map[string]*BacktestSummary) string {
	var totalPoints, totalDir, totalHits int
	var sumBaseline float64
	stocks := 0
	for _, b := range byStock {
		if b == nil {
			continue
		}
		totalPoints += b.Total
		totalDir += b.Directional
		totalHits += b.Hits
		sumBaseline += b.Baseline
		stocks++
	}
	if totalDir == 0 {
		return fmt.Sprintf("Backtest aggregate: %d stocks, %d points, no directional predictions", stocks, totalPoints)
	}
	hitRate := float64(totalHits) / float64(totalDir)
	avgBaseline := sumBaseline / math.Max(float64(stocks), 1)
	return fmt.Sprintf("Backtest aggregate: %d stocks, %d points, %d directional, hit %.0f%% vs baseline %.0f%% (lift %+.0fpp)",
		stocks, totalPoints, totalDir, hitRate*100, avgBaseline*100, (hitRate-avgBaseline)*100)
}
