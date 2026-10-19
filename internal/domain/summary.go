package domain

// FinancialSummary holds enriched financial data for one stock.
type FinancialSummary struct {
	Symbol string

	// Current quarter estimates (from Nasdaq calendar)
	EPSEstimate   float64 // consensus estimate
	EPSLastYear   float64 // same quarter last year actual (from calendar)
	FiscalQuarter string  // e.g. "Feb/2026"

	// Forward EPS estimates (from Nasdaq earnings-forecast)
	ForwardEPS []ForwardQuarter

	// Historical actuals from FMP (oldest → newest)
	History []QuarterActual

	// Computed growth metrics
	EPSYoYPct     *float64 // (estimate - lastYear) / |lastYear| × 100
	RevenueYoYPct *float64 // (latest actual - year-ago actual) / |year-ago| × 100

	// Most-recent actual quarter (prior to current reporting quarter)
	EPSPrevQtr     *float64
	RevenuePrevQtr *float64

	// Consensus revenue estimate for the current reporting quarter (from stockanalysis.com)
	RevenueEstimate *float64

	// Same quarter last year actual revenue (history[0])
	RevenuePrevYear *float64

	// QoQ: estimate vs most-recent actual quarter
	EPSQoQPct     *float64
	RevenueQoQPct *float64

	// Valuation ratios
	PE_TTM     *float64
	PE_Forward *float64
	PS         *float64

	// Stock price returns
	CurrentPrice float64
	Ret1W        *float64
	Ret1M        *float64
	Ret6M        *float64
	Ret1Y        *float64

	// Pre-earnings options analytics
	Options *OptionsSnapshot

	// Insider trading (last 3 months)
	Insider *InsiderSummary

	// Institutional ownership (mutual funds, hedge funds — from latest 13F quarter)
	Institutional *InstitutionalData

	// Stock reaction to last 4 quarterly earnings reports
	EarningsReactions []EarningsReaction

	// Macro events near the upcoming earnings date (±2 days)
	MacroContext string

	// Analyst ratings
	ConsensusRating   string
	StrongBuy         int
	Buy               int
	Hold              int
	Sell              int
	StrongSell        int
	TotalRatings      int
	AvgPriceTarget    float64
	PriceTargetUpside *float64 // (target - current) / current * 100

	// ── Derived signals ──────────────────────────────────────────────────────

	// 52-week range position and RSI(14).
	Hi52        float64  // 52-week high closing price
	Lo52        float64  // 52-week low closing price
	PctFrom52Hi *float64 // (current - hi52) / hi52 * 100  (negative = below high)
	PctFrom52Lo *float64 // (current - lo52) / lo52 * 100  (positive = above low)
	RSI14       *float64 // 14-period Wilder RSI of daily closes

	// Implied move vs historical move ratio.
	// > 1.0 means options are pricing more uncertainty than the stock historically delivers.
	ImpliedVsHistRatio *float64

	// Earnings beat consistency (from last ≤4 EarningsReactions).
	BeatRate   *float64 // fraction of quarters where EPS beat consensus (0–1)
	AvgBeatPct *float64 // average EPS beat percentage across those quarters

	// Material 8-K events in the last 90 days, annotated with stock price reaction.
	MaterialEvents []MaterialEvent

	// Sector peers that already reported results for the same fiscal quarter.
	Peers []PeerResult

	// MSPR (Monthly Share Purchase Ratio) from Finnhub insider-sentiment API.
	// Range 0.0–1.0; >0.6 = insiders net buying (Bullish), <0.4 = net selling (Bearish).
	MSPR       float64 // average over last 3 months; 0 = unavailable
	MSPRSignal string  // "Bullish", "Neutral", "Bearish", or "N/A"

	// ── Growth deceleration: the YoY growth rate of the *most recent reported*
	// quarter, computed from History[]. Compare against RevenueYoYPct/EPSYoYPct
	// (which are estimate vs same-quarter-last-year) to detect acceleration or
	// deceleration heading into this report.
	RevenueYoYPctPrev *float64
	EPSYoYPctPrev     *float64

	// ── Industry-relative valuation: median PE_TTM and PS across same-quarter
	// peers in Peers[]. Anchor for "PE > industry average" checks.
	IndustryMedianPE *float64
	IndustryMedianPS *float64

	// ── Sector momentum: the representative ETF's recent return.
	// Used as a coarse "is the sector in favour right now?" gauge.
	SectorETF   string   // e.g. "XLK", "SOXX", "XLF"
	SectorRet1M *float64 // ETF return over last 30 calendar days, %
	SectorRet3M *float64 // ETF return over last 90 calendar days, %

	// Recommendation produced by ComputeRecommendation: a directional score
	// aggregating signals 1–11 (see internal/recommend/signals.go). Nil when scoring was skipped.
	Recommendation *Recommendation

	// Post-earnings drift: set only when the report was just released (the
	// reaction day is among the last few bars). DriftRecommendation predicts
	// the direction of the move over the following 5–20 trading days.
	DriftFeatures       *DriftFeatures
	DriftRecommendation *Recommendation

	// EPSSurprise is the reported EPS surprise vs consensus (%), set once the
	// report is out (Nasdaq calendar). Feeds the post-release signals.
	EPSSurprise *float64

	// Backtest is a walk-forward evaluation of the Tier-1 signals against
	// this stock's last ≤4 reactions. Populated only when RunBacktest is on.
	Backtest *BacktestSummary

	// LLMRating is produced by the sibling `llm-earnings-agent` Python project.
	// Populated only when --llm-rating is set and the subprocess succeeds.
	LLMRating *LLMRating

	// ISO currency code for this stock's exchange (e.g. "USD", "GBP", "EUR").
	Currency string
}
