package domain

// EarningsResult is the final, filtered record with enriched financial data.
type EarningsResult struct {
	Symbol       string  `json:"symbol"`
	CompanyName  string  `json:"company_name"`
	MarketCapB   float64 `json:"market_cap_b"`
	EarningsDate string  `json:"earnings_date"`
	EarningsTime string  `json:"earnings_time"` // "bmo" / "amc" / ""
	ResultDate   string  `json:"result_date"`

	// ── EPS ──────────────────────────────────────────────────────────────────
	FiscalQuarter string  `json:"fiscal_quarter"`
	EPSEstimate   float64 `json:"eps_estimate"`
	EPSPrevQtr    string  `json:"eps_prev_qtr"`  // most-recent actual quarter
	EPSQoQ        string  `json:"eps_qoq_pct"`   // estimate vs prev quarter
	EPSLastYear   float64 `json:"eps_last_year"` // same quarter last year actual
	EPSYoYPct     string  `json:"eps_yoy_pct"`   // estimate vs same quarter last year
	// ── Revenue ──────────────────────────────────────────────────────────────
	RevEstimate   string `json:"rev_estimate"`   // consensus estimate, current quarter
	RevPrevQtr    string `json:"rev_prev_qtr_b"` // most-recent actual quarter (billions)
	RevQoQ        string `json:"rev_qoq_pct"`    // estimate vs prev quarter
	RevPrevYr     string `json:"rev_prev_yr_b"`  // same quarter last year actual (billions)
	RevenueYoYPct string `json:"revenue_yoy_pct"`

	// ── Valuation ratios ─────────────────────────────────────────────────────
	PE_TTM     string `json:"pe_ttm"`
	PE_Forward string `json:"pe_forward"`
	PS         string `json:"ps"`

	// ── Stock price returns ───────────────────────────────────────────────────
	CurrentPrice string `json:"current_price"`
	Ret1W        string `json:"ret_1w"`
	Ret1M        string `json:"ret_1m"`
	Ret6M        string `json:"ret_6m"`
	Ret1Y        string `json:"ret_1y"`

	// ── Institutional ownership (mutual funds, hedge funds) ──────────────────
	InstActivity string `json:"inst_activity"` // "Net Buyer" / "Net Seller" / "No Activity"
	InstOwn      string `json:"inst_own"`      // current % of shares held by institutions
	InstTrans    string `json:"inst_trans"`    // quarter-over-quarter change in institutional %
	ShortFloat   string `json:"short_float"`   // short interest as % of float
	ShortRatio   string `json:"short_ratio"`   // days-to-cover

	// ── Insider trading (last 3 months) ──────────────────────────────────────
	InsiderActivity string `json:"insider_activity"` // "Net Buyer" / "Net Seller" / "No Activity"
	InsiderBuyVal   string `json:"insider_buy_val"`
	InsiderSellVal  string `json:"insider_sell_val"`
	InsiderNetVal   string `json:"insider_net_val"`
	InsiderFilings  int    `json:"insider_filings"`

	// ── Analyst ratings ───────────────────────────────────────────────────────
	ConsensusRating   string `json:"consensus_rating"`
	AvgPriceTarget    string `json:"avg_price_target"`
	PriceTargetUpside string `json:"price_target_upside"`
	AnalystBullish    int    `json:"analyst_bullish"` // StrongBuy + Buy
	AnalystNeutral    int    `json:"analyst_neutral"` // Hold
	AnalystBearish    int    `json:"analyst_bearish"` // Sell + StrongSell
	AnalystTotal      int    `json:"analyst_total"`

	// ── Derived signals ──────────────────────────────────────────────────────
	Hi52               string `json:"hi_52,omitempty"`
	Lo52               string `json:"lo_52,omitempty"`
	PctFrom52Hi        string `json:"pct_from_52hi,omitempty"`
	PctFrom52Lo        string `json:"pct_from_52lo,omitempty"`
	RSI14              string `json:"rsi14,omitempty"`
	ImpliedVsHistRatio string `json:"implied_vs_hist_ratio,omitempty"`
	BeatRate           string `json:"beat_rate,omitempty"`
	AvgBeatPct         string `json:"avg_beat_pct,omitempty"`

	// ── MSPR (Monthly Share Purchase Ratio from Finnhub) ─────────────────────
	MSPR       string `json:"mspr,omitempty"`        // formatted, e.g. "0.72"
	MSPRSignal string `json:"mspr_signal,omitempty"` // "Bullish", "Neutral", "Bearish", or "N/A"

	// ── Macro context (scheduled events ±2 days of earnings date) ────────────
	MacroContext string `json:"macro_context,omitempty"`

	// ── Sector momentum (representative ETF over recent windows) ────────────
	SectorETF   string   `json:"sector_etf,omitempty"`
	SectorRet1M *float64 `json:"sector_ret_1m,omitempty"`
	SectorRet3M *float64 `json:"sector_ret_3m,omitempty"`

	// ── Options setup (pre-earnings) ─────────────────────────────────────────
	OptionsExpiry    string `json:"options_expiry,omitempty"`
	ExpectedMove     string `json:"expected_move,omitempty"`
	ExpectedMovePct  string `json:"expected_move_pct,omitempty"`
	IVAtm            string `json:"iv_atm,omitempty"`
	PCVol            string `json:"pc_vol,omitempty"`
	PCoi             string `json:"pc_oi,omitempty"`
	Skew             string `json:"skew,omitempty"`
	MaxPain          string `json:"max_pain,omitempty"`
	MaxPainVsCurrent string `json:"max_pain_vs_current,omitempty"`
	HistAvgAbsRxn    string `json:"hist_avg_abs_rxn,omitempty"`

	// Currency code for display (USD, GBP, EUR).
	Currency string `json:"currency,omitempty"`

	// GEX breakdown (populated only when --gex flag is set)
	GEX *GEXSnapshot `json:"gex,omitempty"`

	// Full quarterly history for JSON/CSV output
	History []QuarterActual `json:"history,omitempty"`

	// Forward EPS estimates
	ForwardEPS []ForwardQuarter `json:"forward_eps,omitempty"`

	// Post-earnings stock reactions for last ≤4 quarters
	EarningsReactions []EarningsReaction `json:"earnings_reactions,omitempty"`

	// Material 8-K events in the last 90 days
	MaterialEvents []MaterialEvent `json:"material_events,omitempty"`

	// Sector peers that already reported the same fiscal quarter
	Peers []PeerResult `json:"peers,omitempty"`

	// Aggregated directional recommendation (Positive/Negative/Neutral) with
	// score, confidence, and the per-signal breakdown. See internal/recommend.
	Recommendation *Recommendation `json:"recommendation,omitempty"`

	// Post-earnings drift prediction, made after the reaction-day close for a
	// report that was just released. See internal/events and recommend.ComputeDrift.
	DriftFeatures       *DriftFeatures  `json:"drift_features,omitempty"`
	DriftRecommendation *Recommendation `json:"drift_recommendation,omitempty"`

	// Consensus EPS surprise (%) once the report is out, and the post-release
	// signals computed from it (--post-release).
	EPSSurprisePct *float64     `json:"eps_surprise_pct,omitempty"`
	PostRelease    *PostRelease `json:"post_release,omitempty"`

	// Walk-forward backtest of the Tier-1 signals on this stock's last
	// ≤4 reactions. Populated only when --backtest is set.
	Backtest *BacktestSummary `json:"backtest,omitempty"`

	// LLM rating from the sibling llm-earnings-agent Python project.
	// Populated only when --llm-rating is set.
	LLMRating *LLMRating `json:"llm_rating,omitempty"`
}
