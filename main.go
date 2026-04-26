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

	// Material 8-K events in the last 90 days
	MaterialEvents []MaterialEvent `json:"material_events,omitempty"`

	// Sector peers that already reported the same fiscal quarter
	Peers []PeerResult `json:"peers,omitempty"`
}

func main() {
	fromStr := flag.String("from", "", "Start of date range (YYYY-MM-DD); optional when --symbol is set")
	toStr := flag.String("to", "", "End of date range (YYYY-MM-DD); optional when --symbol is set")
	outputFmt := flag.String("output", "table", "Output format: table | csv | json")
	minCapB := flag.Float64("min-cap-b", defaultMinMarketCap/1e9, "Minimum market cap in billions USD (default 10)")
	maxCapB := flag.Float64("max-cap-b", 0, "Maximum market cap in billions USD (0 = no upper limit)")
	symbolFlag := flag.String("symbol", "", "Single stock symbol to analyse (skips market-cap filter; from/to optional)")
	noPeers := flag.Bool("no-peers", false, "Disable sector peer analysis (also: DISABLE_PEERS=1)")
	noNews := flag.Bool("no-news", false, "Disable material 8-K events analysis (also: DISABLE_NEWS=1)")
	flag.Parse()

	// Resolve date range.
	// When --symbol is given without dates, default to today → today+90 days.
	if *fromStr == "" && *symbolFlag != "" {
		*fromStr = time.Now().Format("2006-01-02")
	}
	if *toStr == "" && *symbolFlag != "" {
		*toStr = time.Now().AddDate(0, 0, 90).Format("2006-01-02")
	}

	if *fromStr == "" || *toStr == "" {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s --from YYYY-MM-DD --to YYYY-MM-DD [flags]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --symbol TSLA [flags]\n\nFlags:\n", os.Args[0])
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
	maxCap := *maxCapB * 1e9 // 0 means no upper limit
	filterSymbol := strings.ToUpper(strings.TrimSpace(*symbolFlag))

	// ── Step 1: Fetch earnings calendar ─────────────────────────────────────
	logf("Fetching earnings calendar from Nasdaq.com (%s → %s) ...", *fromStr, *toStr)
	nc := NewNasdaqClient()
	events, calMap := nc.FetchEarningsCalendar(from, to)
	logf("Total events fetched: %d", len(events))

	// ── Step 2: Filter by symbol or market cap ────────────────────────────────
	var qualified []EarningsEvent
	if filterSymbol != "" {
		for _, e := range events {
			if strings.ToUpper(e.Symbol) == filterSymbol {
				qualified = append(qualified, e)
				break
			}
		}
		if len(qualified) == 0 {
			// Symbol not found in calendar window — synthesise a minimal entry so
			// enrichment still runs (EarningsDate will be empty).
			logf("Symbol %s not found in earnings calendar for %s→%s; running enrichment without earnings date.", filterSymbol, *fromStr, *toStr)
			qualified = []EarningsEvent{{Symbol: filterSymbol}}
		}
	} else {
		qualified = filterByMarketCap(events, minCap, maxCap)
		if maxCap > 0 {
			logf("Qualifying ($%.0fB–$%.0fB market cap): %d", *minCapB, *maxCapB, len(qualified))
		} else {
			logf("Qualifying (market cap > $%.0fB): %d", *minCapB, len(qualified))
		}
		if len(qualified) == 0 {
			return
		}
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
	prelimMap := make(map[string]EarningsResult, len(preliminary))
	for _, r := range preliminary {
		prelimMap[r.Symbol] = r
	}

	// ── Step 4: Enrich and output ─────────────────────────────────────────────
	logf("Fetching financial summaries (EPS estimates, revenue history, trends) ...")
	enricher := NewEnricher()
	if *noPeers {
		enricher.cfg.DisablePeers = true
	}
	if *noNews {
		enricher.cfg.DisableNews = true
	}

	// Assemble each stock as it finishes and forward to the output channel.
	resultCh := make(chan EarningsResult, len(preliminary))
	go func() {
		for es := range enricher.EnrichStream(preliminary, calMap, macro) {
			resultCh <- assembleResult(prelimMap[es.Symbol], es.Summary)
		}
		close(resultCh)
	}()

	switch strings.ToLower(*outputFmt) {
	case "json":
		// JSON requires a complete sorted list before it can be written.
		var results []EarningsResult
		for r := range resultCh {
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
		writeJSON(os.Stdout, results)
	case "csv":
		// Header is written immediately; rows stream as each stock completes.
		writeCSVStream(os.Stdout, resultCh)
		logf("Done.\n")
	default:
		// Each stock card is printed as soon as its enrichment finishes.
		writeTableStream(os.Stdout, resultCh)
		logf("Done.\n")
	}
}

// assembleResult maps a FinancialSummary onto the pre-populated EarningsResult
// fields that come from the Nasdaq calendar (Symbol, MarketCapB, etc.).
func assembleResult(r EarningsResult, s *FinancialSummary) EarningsResult {
	if s == nil {
		return r
	}
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
		if s.Institutional.ShortFloat > 0 {
			r.ShortFloat = fmt.Sprintf("%.2f%%", s.Institutional.ShortFloat)
		} else {
			r.ShortFloat = "N/A"
		}
		if s.Institutional.ShortRatio > 0 {
			r.ShortRatio = fmt.Sprintf("%.1fd", s.Institutional.ShortRatio)
		} else {
			r.ShortRatio = "N/A"
		}
	} else {
		r.InstActivity = "N/A"
		r.InstOwn = "N/A"
		r.InstTrans = "N/A"
		r.ShortFloat = "N/A"
		r.ShortRatio = "N/A"
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
	r.MaterialEvents = s.MaterialEvents
	r.Peers = s.Peers

	if s.Hi52 > 0 {
		r.Hi52 = fmt.Sprintf("$%.2f", s.Hi52)
		r.Lo52 = fmt.Sprintf("$%.2f", s.Lo52)
		r.PctFrom52Hi = fmtPct(s.PctFrom52Hi)
		r.PctFrom52Lo = fmtPct(s.PctFrom52Lo)
	} else {
		r.Hi52, r.Lo52, r.PctFrom52Hi, r.PctFrom52Lo = "N/A", "N/A", "N/A", "N/A"
	}
	if s.RSI14 != nil {
		r.RSI14 = fmt.Sprintf("%.1f", *s.RSI14)
	} else {
		r.RSI14 = "N/A"
	}
	if s.ImpliedVsHistRatio != nil {
		r.ImpliedVsHistRatio = fmt.Sprintf("%.2fx", *s.ImpliedVsHistRatio)
	} else {
		r.ImpliedVsHistRatio = "N/A"
	}
	if s.BeatRate != nil {
		r.BeatRate = fmt.Sprintf("%.0f%%", *s.BeatRate*100)
		r.AvgBeatPct = fmtPct(s.AvgBeatPct)
	} else {
		r.BeatRate, r.AvgBeatPct = "N/A", "N/A"
	}
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
	return r
}

// filterByMarketCap returns the subset of events whose MarketCap is in
// [minCap, maxCap]. maxCap == 0 means no upper bound.
func filterByMarketCap(events []EarningsEvent, minCap, maxCap float64) []EarningsEvent {
	var out []EarningsEvent
	for _, e := range events {
		if e.MarketCap < minCap {
			continue
		}
		if maxCap > 0 && e.MarketCap > maxCap {
			continue
		}
		out = append(out, e)
	}
	return out
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}
