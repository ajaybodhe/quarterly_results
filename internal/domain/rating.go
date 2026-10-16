package domain

// SignalResult is the per-signal output captured for explainability and for
// the backtest harness's hit-rate decomposition.
type SignalResult struct {
	Name       string  `json:"name"`
	Weight     float64 `json:"weight"`
	Score      float64 `json:"score"`      // [-1, +1]; positive = bullish
	Confidence float64 `json:"confidence"` // [0, 1]; 0 = data missing
	Reason     string  `json:"reason"`     // human-readable, may be ""
}

// Recommendation is the directional prediction emitted by ComputeRecommendation.
// Score is in the range [-100, +100] (positive = bullish); Confidence is in
// [0, 1] reflecting how much of the maximum possible signal weight had data.
type Recommendation struct {
	Label      string         `json:"label"`       // "Positive" / "Negative" / "Neutral"
	Score      float64        `json:"score"`       // [-100, +100]
	Confidence float64        `json:"confidence"`  // [0, 1]
	Signals    []SignalResult `json:"signals"`     // every signal evaluated, in priority order
	TopReasons []string       `json:"top_reasons"` // up to 3 highest-magnitude reason strings
}

// LLMRating is the JSON contract emitted by the sibling `llm-earnings-agent`
// Python project. It is rendered alongside the deterministic Recommendation
// but does not feed back into ComputeRecommendation — the two ratings stay
// independent until the LLM track record warrants mixing.
type LLMRating struct {
	Symbol     string         `json:"symbol"`
	AsOf       string         `json:"asof"`
	Rating     LLMRatingScore `json:"rating"`
	SubRatings LLMSubRatings  `json:"sub_ratings"`
	Metadata   LLMMetadata    `json:"metadata"`
}

type LLMRatingScore struct {
	Label      string   `json:"label"` // Positive | Negative | Neutral
	Score      float64  `json:"score"` // [-100, +100]
	Confidence float64  `json:"confidence"`
	TopReasons []string `json:"top_reasons"`
}

type LLMSubAnalysis struct {
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

type LLMSubRatings struct {
	Fundamentals *LLMSubAnalysis `json:"fundamentals,omitempty"`
	Transcript   *LLMSubAnalysis `json:"transcript,omitempty"`
	News         *LLMSubAnalysis `json:"news,omitempty"`
	Macro        *LLMSubAnalysis `json:"macro,omitempty"`
	Dynamic      *LLMSubAnalysis `json:"dynamic,omitempty"`
}

type LLMMetadata struct {
	Model             string  `json:"model"`
	Runtime           string  `json:"runtime"`
	PromptVersion     string  `json:"prompt_version"`
	Timestamp         string  `json:"timestamp"`
	CostEstimateUSD   float64 `json:"cost_estimate_usd"`
	TranscriptQuarter string  `json:"transcript_quarter,omitempty"`
}

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
	Symbol             string          `json:"symbol"`
	Total              int             `json:"total"`
	Directional        int             `json:"directional"` // points where predicted != "Neutral"
	Hits               int             `json:"hits"`        // directional hits
	HitRate            float64         `json:"hit_rate"`    // hits / directional
	Baseline           float64         `json:"baseline"`    // P(actual is most-common label) — what naive guessing scores
	AvgRetWhenPositive float64         `json:"avg_ret_when_positive"`
	AvgRetWhenNegative float64         `json:"avg_ret_when_negative"`
	AvgRetWhenNeutral  float64         `json:"avg_ret_when_neutral"`
	Points             []BacktestPoint `json:"points"`
}
