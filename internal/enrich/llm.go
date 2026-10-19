package enrich

import (
	"context"
	"encoding/json"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/logging"
	"quarterly_results/internal/macro"
	"quarterly_results/internal/recommend"
	"quarterly_results/internal/report"
	"quarterly_results/internal/source/llmagent"
)

// summarize builds the stock's summary and, when the selected LLM agents read
// this repo's output, attaches the LLM rating computed from that output.
func (e *Enricher) summarize(res domain.EarningsResult, row domain.CalendarRow, macroCal *macro.Calendar) *domain.FinancialSummary {
	s := e.buildSummary(res, row, macroCal)
	if s != nil && e.Config.LLMRating && llmagent.NeedsPayload(e.Config.LLMAgents) {
		e.attachPayloadLLMRating(res, s)
	}
	if s != nil && s.DriftFeatures != nil {
		s.DriftRecommendation = recommend.ComputeDrift(s.DriftFeatures, freshPressRelease(s))
	}
	return s
}

// attachPayloadLLMRating sends the stock's assembled JSON result to the LLM
// agent on stdin, so the agent doesn't re-run quarterly_results to get the
// same data.
func (e *Enricher) attachPayloadLLMRating(res domain.EarningsResult, s *domain.FinancialSummary) {
	payload, err := json.Marshal(report.AssembleResult(res, s))
	if err != nil {
		logging.Logf("Warning: LLM payload for %s: %v", res.Symbol, err)
		return
	}
	r, err := llmagent.FetchRating(context.Background(), res.Symbol, llmagent.Options{
		Agents:  e.Config.LLMAgents,
		Payload: payload,
	})
	if err != nil {
		logging.Logf("Warning: LLM rating unavailable for %s: %v", res.Symbol, err)
		return
	}
	s.LLMRating = r
}
