package recommend

import (
	"fmt"
	"math"

	"quarterly_results/internal/domain"
)

// DriftSignal scores one driver of post-earnings-announcement drift (PEAD):
// the move in the days *after* the reaction day. Inputs are known at the
// reaction-day close, so the reaction itself is a legitimate input here.
type DriftSignal struct {
	Name    string
	Weight  float64
	Compute func(f *domain.DriftFeatures, guidance *domain.LLMSubAnalysis) (score, confidence float64, reason string)
}

// DriftSignals are rule-based hypotheses from the PEAD literature, not fitted
// weights: they are the baseline that `--export-events` evaluates and that a
// trained model must beat (see the Zhu, Liu & Sheng plan).
var DriftSignals = []DriftSignal{
	{"drift_reaction", 35, driftReaction},
	{"drift_surprise", 30, driftSurprise},
	{"drift_guidance", 20, driftGuidance},
	{"drift_sector", 15, driftSector},
}

// ComputeDrift predicts the direction of post-reaction drift. guidance is the
// LLM's read of the fresh earnings press release; nil when unavailable (always
// the case for historical events).
func ComputeDrift(f *domain.DriftFeatures, guidance *domain.LLMSubAnalysis) *domain.Recommendation {
	if f == nil {
		return nil
	}
	results := make([]domain.SignalResult, 0, len(DriftSignals))
	for _, sig := range DriftSignals {
		score, conf, reason := sig.Compute(f, guidance)
		results = append(results, signalResult(sig.Name, sig.Weight, score, conf, reason))
	}
	return aggregate(results)
}

// driftReaction: the earnings-announcement return. Drift tends to continue in
// the direction of the initial reaction; reactions under ~2% carry little
// information, so confidence ramps up to |2%|.
func driftReaction(f *domain.DriftFeatures, _ *domain.LLMSubAnalysis) (float64, float64, string) {
	r := f.ReactionRetPct
	return clamp1(r / 5), math.Min(1, math.Abs(r)/2), fmt.Sprintf("reaction %+.1f%%", r)
}

// driftSurprise: the EPS surprise, the classic PEAD driver — versus consensus
// when known (live, from the calendar), else the consensus-free SUE (history).
func driftSurprise(f *domain.DriftFeatures, _ *domain.LLMSubAnalysis) (float64, float64, string) {
	if f.EPSSurprisePct != nil {
		s := *f.EPSSurprisePct
		return clamp1(s / 10), 1, fmt.Sprintf("EPS surprise %+.1f%%", s)
	}
	if f.SUE != nil {
		s := *f.SUE
		return clamp1(s / 2), 1, fmt.Sprintf("SUE %+.1fσ vs a year ago", s)
	}
	return 0, 0, ""
}

// driftGuidance: the LLM transcript agent's score for the fresh press release
// (guidance direction, tone), at the agent's own confidence.
func driftGuidance(_ *domain.DriftFeatures, g *domain.LLMSubAnalysis) (float64, float64, string) {
	if g == nil {
		return 0, 0, ""
	}
	return g.Score, g.Confidence, fmt.Sprintf("press release read %+.2f", g.Score)
}

// driftSector: the sector ETF's 1-month trend into the reaction.
func driftSector(f *domain.DriftFeatures, _ *domain.LLMSubAnalysis) (float64, float64, string) {
	if f.SectorRet1M == nil {
		return 0, 0, ""
	}
	r := *f.SectorRet1M
	return clamp1(r / 5), 1, fmt.Sprintf("%s 1M %+.1f%%", f.SectorETF, r)
}
