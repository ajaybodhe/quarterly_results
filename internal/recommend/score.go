package recommend

import (
	"fmt"
	"sort"

	"quarterly_results/internal/domain"
)

// Recommendation label thresholds. Tuned conservatively: the system only
// expresses an opinion when the weighted score is at least ±15 points and
// confidence is at least 0.4. Below that, output is "Neutral" — better to
// abstain than mislead when data is sparse.
const (
	labelThresholdScore      = 15.0
	labelThresholdConfidence = 0.4
)

// ComputeRecommendation runs every DefaultSignal against the summary and
// aggregates a single label/score/confidence. Convenience wrapper around
// ComputeRecommendationFrom.
func ComputeRecommendation(s *domain.FinancialSummary) *domain.Recommendation {
	return ComputeRecommendationFrom(s, DefaultSignals)
}

// ComputeRecommendationFrom evaluates an arbitrary signal list. The backtest
// harness uses this with BacktestSignals (a Tier-1 subset that can be
// reconstructed at a historical point in time without lookahead bias).
//
// Aggregation:
//
//	raw_score   = Σ weight_i × score_i × confidence_i
//	max_weight  = Σ weight_i
//	used_weight = Σ weight_i × confidence_i
//	final_score = raw_score / max_weight × 100
//	confidence  = used_weight / max_weight
//
// Multiplying score by confidence inside the sum means a low-confidence signal
// pulls the score toward zero rather than swinging it. This is the right
// behaviour for "data missing" and graceful degradation.
func ComputeRecommendationFrom(s *domain.FinancialSummary, signals []Signal) *domain.Recommendation {
	if s == nil || len(signals) == 0 {
		return nil
	}

	results := make([]domain.SignalResult, 0, len(signals))
	for _, sig := range signals {
		score, conf, reason := sig.Compute(s)
		results = append(results, signalResult(sig.Name, sig.Weight, score, conf, reason))
	}
	return aggregate(results)
}

// signalResult clamps a signal's output into range (defensively, in case a
// signal returns out-of-range values) and packages it.
func signalResult(name string, weight, score, conf float64, reason string) domain.SignalResult {
	if score > 1 {
		score = 1
	} else if score < -1 {
		score = -1
	}
	if conf < 0 {
		conf = 0
	} else if conf > 1 {
		conf = 1
	}
	return domain.SignalResult{Name: name, Weight: weight, Score: score, Confidence: conf, Reason: reason}
}

// aggregate combines evaluated signals into one Recommendation using the
// weight × score × confidence formula documented on ComputeRecommendationFrom.
func aggregate(results []domain.SignalResult) *domain.Recommendation {
	var rawScore, maxWeight, usedWeight float64
	for _, r := range results {
		rawScore += r.Weight * r.Score * r.Confidence
		maxWeight += r.Weight
		usedWeight += r.Weight * r.Confidence
	}

	if maxWeight == 0 {
		return &domain.Recommendation{Label: "Neutral", Signals: results}
	}

	finalScore := rawScore / maxWeight * 100
	confidence := usedWeight / maxWeight

	rec := &domain.Recommendation{
		Score:      finalScore,
		Confidence: confidence,
		Signals:    results,
		Label:      labelForScore(finalScore, confidence),
		TopReasons: topReasons(results, 3),
	}
	return rec
}

// labelForScore picks a directional label given the aggregated score and
// confidence. Below the confidence floor we always return "Neutral" — even a
// strong-magnitude score is meaningless if only one signal had data.
func labelForScore(score, confidence float64) string {
	if confidence < labelThresholdConfidence {
		return "Neutral"
	}
	switch {
	case score >= labelThresholdScore:
		return "Positive"
	case score <= -labelThresholdScore:
		return "Negative"
	default:
		return "Neutral"
	}
}

// topReasons returns up to n reason strings ranked by the absolute weighted
// contribution of their signal (|weight × score × confidence|). Reasons from
// signals that contribute nothing (no data, score=0) are filtered out.
func topReasons(results []domain.SignalResult, n int) []string {
	type entry struct {
		impact float64
		text   string
	}
	picked := make([]entry, 0, len(results))
	for _, r := range results {
		if r.Reason == "" {
			continue
		}
		impact := abs(r.Weight * r.Score * r.Confidence)
		if impact == 0 {
			continue
		}
		direction := "+"
		if r.Score < 0 {
			direction = "−"
		}
		picked = append(picked, entry{
			impact: impact,
			text:   fmt.Sprintf("[%s] %s: %s", direction, r.Name, r.Reason),
		})
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].impact > picked[j].impact })
	if len(picked) > n {
		picked = picked[:n]
	}
	out := make([]string, len(picked))
	for i, e := range picked {
		out[i] = e.text
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
