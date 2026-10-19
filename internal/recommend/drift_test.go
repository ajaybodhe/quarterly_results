package recommend

import (
	"testing"

	"quarterly_results/internal/domain"
)

func f64(v float64) *float64 { return &v }

func TestComputeDrift_ReactionAndSurpriseAgree(t *testing.T) {
	f := &domain.DriftFeatures{ReactionRetPct: 6, EPSSurprisePct: f64(12), SectorETF: "XLK", SectorRet1M: f64(2)}
	r := ComputeDrift(f, nil)
	if r.Label != "Positive" || r.Score <= 15 {
		t.Errorf("expected Positive, got %+v", r)
	}
	f = &domain.DriftFeatures{ReactionRetPct: -6, EPSSurprisePct: f64(-12), SectorRet1M: f64(-2)}
	if r := ComputeDrift(f, nil); r.Label != "Negative" {
		t.Errorf("expected Negative, got %+v", r)
	}
}

func TestComputeDrift_SmallReactionNoDataIsNeutral(t *testing.T) {
	r := ComputeDrift(&domain.DriftFeatures{ReactionRetPct: 0.3}, nil)
	if r.Label != "Neutral" || r.Confidence >= 0.4 {
		t.Errorf("expected low-confidence Neutral, got %+v", r)
	}
	if ComputeDrift(nil, nil) != nil {
		t.Error("nil features should give nil")
	}
}

func TestComputeDrift_GuidanceCounts(t *testing.T) {
	f := &domain.DriftFeatures{ReactionRetPct: 1, EPSSurprisePct: f64(1)}
	without := ComputeDrift(f, nil)
	with := ComputeDrift(f, &domain.LLMSubAnalysis{Score: 0.9, Confidence: 0.5})
	if with.Score <= without.Score {
		t.Errorf("positive guidance should raise the score: %v → %v", without.Score, with.Score)
	}
}
