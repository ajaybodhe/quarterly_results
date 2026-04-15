package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"
)

const defaultMinMarketCap = 10_000_000_000.0 // $10 billion

// EarningsEvent is the raw event from the Nasdaq calendar before filtering.
type EarningsEvent struct {
	Symbol    string
	Date      string  // YYYY-MM-DD
	Time      string  // "bmo" / "amc" / ""
	MarketCap float64 // in USD
	Name      string
}

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

	// ── Macro context (scheduled events ±2 days of earnings date) ────────────
	MacroContext string `json:"macro_context,omitempty"`

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

	// Full quarterly history for JSON/CSV output
	History []QuarterActual `json:"history,omitempty"`

	// Forward EPS estimates
	ForwardEPS []ForwardQuarter `json:"forward_eps,omitempty"`

	// Post-earnings stock reactions for last ≤4 quarters
	EarningsReactions []EarningsReaction `json:"earnings_reactions,omitempty"`
}

func main() {
	fromStr := flag.String("from", "", "Start of date range (YYYY-MM-DD) [required]")
	toStr := flag.String("to", "", "End of date range (YYYY-MM-DD) [required]")
	outputFmt := flag.String("output", "table", "Output format: table | csv | json")
	minCapB := flag.Float64("min-cap-b", defaultMinMarketCap/1e9, "Minimum market cap in billions USD")
	flag.Parse()

	if *fromStr == "" || *toStr == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s --from YYYY-MM-DD --to YYYY-MM-DD [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	from, err := time.Parse("2006-01-02", *fromStr)
	if err != nil {
		log.Fatalf("Invalid --from date: %v", err)
	}
	to, err := time.Parse("2006-01-02", *toStr)
	if err != nil {
		log.Fatalf("Invalid --to date: %v", err)
	}
	if to.Before(from) {
		log.Fatal("--to must not be before --from")
	}

	minCap := *minCapB * 1e9

	// ── Step 1: Fetch earnings calendar ─────────────────────────────────────
	logf("Fetching earnings calendar from Nasdaq.com (%s → %s) ...", *fromStr, *toStr)
	nc := NewNasdaqClient()
	events, calMap := nc.FetchEarningsCalendar(from, to)
	logf("Total events fetched: %d", len(events))

	// ── Step 2: Filter by market cap ─────────────────────────────────────────
	var qualified []EarningsEvent
	for _, e := range events {
		if e.MarketCap >= minCap {
			qualified = append(qualified, e)
		}
	}
	logf("Qualifying (market cap > $%.0fB): %d", *minCapB, len(qualified))
	if len(qualified) == 0 {
		return
	}

	// ── Step 2b: Load macro economic calendar ────────────────────────────────
	logf("Loading macro economic calendar (FOMC + BLS schedule) ...")
	macro := LoadMacroCalendar(from, to)

	// ── Step 3: Build preliminary results for enricher input ─────────────────
	preliminary := make([]EarningsResult, 0, len(qualified))
	for _, e := range qualified {
		preliminary = append(preliminary, EarningsResult{
			Symbol:       e.Symbol,
			CompanyName:  e.Name,
			MarketCapB:   e.MarketCap / 1e9,
			EarningsDate: e.Date,
			EarningsTime: e.Time,
			ResultDate:   computeResultDate(e.Date, e.Time),
		})
	}

	// ── Step 4: Enrich with financial summaries ───────────────────────────────
	logf("Fetching financial summaries (EPS estimates, revenue history, trends) ...")
	enricher := NewEnricher()
	summaries := enricher.EnrichAll(preliminary, calMap, macro)

	// ── Step 5: Assemble final results ────────────────────────────────────────
	var results []EarningsResult
	for _, r := range preliminary {
		s := summaries[r.Symbol]
		r.FiscalQuarter = s.FiscalQuarter
		r.EPSEstimate = s.EPSEstimate
		r.EPSLastYear = s.EPSLastYear
		r.EPSYoYPct = fmtPct(s.EPSYoYPct)
		r.RevenueYoYPct = fmtPct(s.RevenueYoYPct)
		r.History = s.History
		r.ForwardEPS = s.ForwardEPS
		if s.EPSPrevQtr != nil {
			r.EPSPrevQtr = fmt.Sprintf("$%.2f", *s.EPSPrevQtr)
		} else {
			r.EPSPrevQtr = "N/A"
		}
		r.EPSQoQ = fmtPct(s.EPSQoQPct)
		if s.RevenuePrevQtr != nil {
			r.RevPrevQtr = fmt.Sprintf("$%.2fB", *s.RevenuePrevQtr/1e9)
		} else {
			r.RevPrevQtr = "N/A"
		}
		if s.RevenueEstimate != nil {
			r.RevEstimate = fmt.Sprintf("$%.2fB", *s.RevenueEstimate/1e9)
		} else {
			r.RevEstimate = "N/A"
		}
		r.RevQoQ = fmtPct(s.RevenueQoQPct)
		if s.RevenuePrevYear != nil {
			r.RevPrevYr = fmt.Sprintf("$%.2fB", *s.RevenuePrevYear/1e9)
		} else {
			r.RevPrevYr = "N/A"
		}
		r.PE_TTM = fmtRatio(s.PE_TTM)
		r.PE_Forward = fmtRatio(s.PE_Forward)
		r.PS = fmtRatio(s.PS)
		if s.CurrentPrice > 0 {
			r.CurrentPrice = fmt.Sprintf("$%.2f", s.CurrentPrice)
		} else {
			r.CurrentPrice = "N/A"
		}
		r.Ret1W = fmtPct(s.Ret1W)
		r.Ret1M = fmtPct(s.Ret1M)
		r.Ret6M = fmtPct(s.Ret6M)
		r.Ret1Y = fmtPct(s.Ret1Y)
		if s.Institutional != nil {
			r.InstActivity = s.Institutional.Activity
			r.InstOwn = fmt.Sprintf("%.2f%%", s.Institutional.InstOwn)
			r.InstTrans = fmtPct(&s.Institutional.InstTrans)
		} else {
			r.InstActivity = "N/A"
			r.InstOwn = "N/A"
			r.InstTrans = "N/A"
		}
		if s.Insider != nil {
			r.InsiderActivity = s.Insider.Activity
			r.InsiderBuyVal = fmtDollars(s.Insider.BuyValue)
			r.InsiderSellVal = fmtDollars(s.Insider.SellValue)
			r.InsiderNetVal = fmtDollars(s.Insider.BuyValue - s.Insider.SellValue)
			r.InsiderFilings = s.Insider.FilingCount
		} else {
			r.InsiderActivity = "N/A"
			r.InsiderBuyVal = "N/A"
			r.InsiderSellVal = "N/A"
			r.InsiderNetVal = "N/A"
		}
		r.ConsensusRating = s.ConsensusRating
		if s.ConsensusRating == "" {
			r.ConsensusRating = "N/A"
		}
		if s.AvgPriceTarget > 0 {
			r.AvgPriceTarget = fmt.Sprintf("$%.2f", s.AvgPriceTarget)
		} else {
			r.AvgPriceTarget = "N/A"
		}
		r.PriceTargetUpside = fmtPct(s.PriceTargetUpside)
		r.AnalystBullish = s.StrongBuy + s.Buy
		r.AnalystNeutral = s.Hold
		r.AnalystBearish = s.Sell + s.StrongSell
		r.AnalystTotal = s.TotalRatings
		r.EarningsReactions = s.EarningsReactions
		r.MacroContext = s.MacroContext
		if opt := s.Options; opt != nil {
			r.OptionsExpiry = opt.Expiry
			r.ExpectedMove = fmt.Sprintf("±$%.2f", opt.ExpectedMove)
			r.ExpectedMovePct = fmt.Sprintf("±%.1f%%", opt.ExpectedMovePct)
			r.IVAtm = fmt.Sprintf("%.1f%%", opt.IVAtm)
			r.PCVol = fmt.Sprintf("%.2f", opt.PCVol)
			r.PCoi = fmt.Sprintf("%.2f", opt.PCoi)
			r.Skew = fmtPct(&opt.Skew)
			r.MaxPain = fmt.Sprintf("$%.2f", opt.MaxPain)
			r.MaxPainVsCurrent = fmtPct(&opt.MaxPainVsCurrent)
			if opt.HistAvgAbsRxn > 0 {
				r.HistAvgAbsRxn = fmt.Sprintf("±%.1f%%", opt.HistAvgAbsRxn)
			} else {
				r.HistAvgAbsRxn = "N/A"
			}
		} else {
			r.OptionsExpiry = "N/A"
			r.ExpectedMove = "N/A"
			r.ExpectedMovePct = "N/A"
			r.IVAtm = "N/A"
			r.PCVol = "N/A"
			r.PCoi = "N/A"
			r.Skew = "N/A"
			r.MaxPain = "N/A"
			r.MaxPainVsCurrent = "N/A"
			r.HistAvgAbsRxn = "N/A"
		}
		results = append(results, r)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].ResultDate != results[j].ResultDate {
			return results[i].ResultDate < results[j].ResultDate
		}
		if results[i].MarketCapB != results[j].MarketCapB {
			return results[i].MarketCapB > results[j].MarketCapB
		}
		return results[i].Symbol < results[j].Symbol
	})

	logf("Done.\n")

	switch strings.ToLower(*outputFmt) {
	case "csv":
		writeCSV(os.Stdout, results)
	case "json":
		writeJSON(os.Stdout, results)
	default:
		writeStockCards(os.Stdout, results)
	}
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
