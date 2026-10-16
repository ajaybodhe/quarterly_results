package domain

import "time"

// CalendarRow is the parsed earnings calendar entry used throughout the enrichment pipeline.
// (Formerly nasdaqCalendarRow.)
type CalendarRow struct {
	Symbol              string
	Name                string
	FiscalQuarterEnding string
	EPSForecast         float64 // consensus EPS estimate for the current quarter
	LastYearEPS         float64 // same quarter last year actual EPS
}

// QuarterActual holds one quarter of actual reported figures.
type QuarterActual struct {
	PeriodStart string  // YYYY-MM-DD (fiscal quarter start)
	Period      string  // YYYY-MM-DD (fiscal quarter end)
	Revenue     float64 // in USD
	EPS         float64 // diluted EPS
	FilingDate  string  // YYYY-MM-DD when the 10-Q was filed with SEC (proxy for earnings date)
}

// EarningsEvent is the raw event from the Nasdaq calendar before filtering.
type EarningsEvent struct {
	Symbol    string
	Date      string  // YYYY-MM-DD
	Time      string  // "bmo" / "amc" / ""
	MarketCap float64 // in USD
	Name      string
}

// EarningsReaction holds the stock's price reaction to a past quarterly earnings report.
// AnnouncementDate is the date the company filed its earnings 8-K with the SEC (same day
// as the press release). ReactionDay is the next working day — the first full session
// where the market had time to digest the results.
type EarningsReaction struct {
	Period           string  // fiscal quarter end date (YYYY-MM-DD)
	AnnouncementDate string  // 8-K filing date = earnings announcement date (YYYY-MM-DD)
	ReactionDay      string  // next working day after announcement (YYYY-MM-DD)
	PriorClose       float64 // closing price the day before announcement
	ReactionOpen     float64 // opening price on reaction day (first price after market digests earnings)
	ReactionClose    float64 // closing price on reaction day
	GapRetPct        float64 // gap return: (ReactionOpen - PriorClose) / PriorClose * 100
	RetPct           float64 // full-day return: (ReactionClose - PriorClose) / PriorClose * 100

	// Reported vs estimated
	EPSActual     float64  // actual reported EPS (from SEC EDGAR)
	EPSEstimate   float64  // consensus EPS estimate at announcement (from Nasdaq calendar)
	EPSBeatPct    *float64 // (actual − estimate) / |estimate| × 100; nil if estimate unavailable
	RevenueActual float64  // actual reported revenue (from SEC EDGAR)

	// Pre/post earnings drift (calendar days, excluding announcement/reaction days)
	Pre7Ret    *float64 // 7 days before announcement → day before announcement
	Pre7Close  float64  // closing price ~7 calendar days before announcement
	Post7Ret   *float64 // reaction day close → 7 calendar days after reaction day
	Post7Close float64  // closing price ~7 calendar days after reaction day

	// VIX closing level on the reaction day — high VIX = macro noise may have overwhelmed earnings signal
	VIX float64

	// Macro events near the announcement date (±2 days)
	MacroContext string
}

// ForwardQuarter holds one future quarter EPS estimate from Nasdaq.
type ForwardQuarter struct {
	FiscalEnd         string
	ConsensusEPS      float64
	HighEPS           float64
	LowEPS            float64
	NumberOfEstimates int
}

// PricePoint is a single trading day's OHLC snapshot.
type PricePoint struct {
	Date  time.Time
	Open  float64
	Close float64
}
