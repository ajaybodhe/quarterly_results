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

// DriftFeatures are the facts a post-earnings-drift prediction may use. All
// are known at the close of the reaction day, which is the decision time; the
// drift itself is measured from that close forward (see internal/events).
type DriftFeatures struct {
	AnnouncementDate string   `json:"announcement_date"`
	ReactionDay      string   `json:"reaction_day"`
	ReactionRetPct   float64  `json:"reaction_ret_pct"` // prior close → reaction-day close
	GapRetPct        float64  `json:"gap_ret_pct"`      // prior close → reaction-day open
	EPSSurprisePct   *float64 `json:"eps_surprise_pct,omitempty"`
	Pre7Ret          *float64 `json:"pre7_ret,omitempty"`        // ~7 days before → day before announcement
	AbnormalVolume   *float64 `json:"abnormal_volume,omitempty"` // reaction-day volume ÷ prior 20-bar mean
	SectorETF        string   `json:"sector_etf,omitempty"`
	SectorRet1M      *float64 `json:"sector_ret_1m,omitempty"` // ETF return over ~30 days to the reaction close
	// SUE is the consensus-free surprise: (EPS − EPS a year earlier) ÷ σ of
	// the previous 8 such differences (seasonal random walk). Historical only:
	// live, the new quarter isn't in SEC XBRL until the 10-Q is filed.
	SUE           *float64 `json:"sue,omitempty"`
	Ret1MPre      *float64 `json:"ret_1m_pre,omitempty"`     // ~30 days → day before announcement
	Ret6MPre      *float64 `json:"ret_6m_pre,omitempty"`     // ~182 days → day before announcement
	MarketRet1M   *float64 `json:"market_ret_1m,omitempty"`  // SPY over ~30 days to the reaction close
	ReleaseTiming string   `json:"release_timing,omitempty"` // bmo / amc as used for the reaction day
}

// PostRelease holds the post-release signals for one released report, as
// computed by `llm-earnings-agent pead live` (see the README):
//   - Signal1, at the release: direction of the reaction-day move, from the
//     historical calibration of the consensus EPS surprise.
//   - Signal2, at the reaction-day open: open→close minus SPY, from a GBDT on
//     surprise, gap and context, with an out-of-sample decile calibration.
type PostRelease struct {
	Symbol           string         `json:"symbol"`
	AnnouncementDate string         `json:"announcement_date"`
	ReactionDay      string         `json:"reaction_day,omitempty"`
	Model            string         `json:"model,omitempty"`
	Signal1          *ReleaseSignal `json:"signal1,omitempty"`
	Signal2          *OpenSignal    `json:"signal2,omitempty"`
}

// ReleaseSignal is Signal 1: how reaction days went historically (2021+)
// for consensus surprises of this size.
type ReleaseSignal struct {
	SurprisePct     float64 `json:"surprise_pct"`
	PUp             float64 `json:"p_up"` // share of positive reaction days
	N               int     `json:"n"`
	MeanReactionPct float64 `json:"mean_reaction_pct"`
	Label           string  `json:"label"` // Positive (P≥55%) / Negative (P≤45%) / Neutral
}

// OpenSignal is Signal 2: the predicted reaction-day open→close minus SPY,
// its decile among out-of-sample predictions, and that decile's history.
type OpenSignal struct {
	PredictedPct float64  `json:"predicted_pct"`
	Decile       int      `json:"decile"` // 1..10
	Lean         string   `json:"lean"`   // "LONG" (decile 10 only; SPY hedge optional) / "no trade"
	HistMeanPct  float64  `json:"hist_mean_pct"`
	HistHit      *float64 `json:"hist_hit"`
	GapPct       float64  `json:"gap_pct"`
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
	Model             string   `json:"model"`
	Runtime           string   `json:"runtime"`
	PromptVersion     string   `json:"prompt_version"`
	Timestamp         string   `json:"timestamp"`
	CostEstimateUSD   float64  `json:"cost_estimate_usd"`
	TranscriptQuarter string   `json:"transcript_quarter,omitempty"`
	Agents            []string `json:"agents,omitempty"` // specialist agents requested for this run
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
