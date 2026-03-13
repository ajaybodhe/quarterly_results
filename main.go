package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
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
	InstActivity string  `json:"inst_activity"` // "Net Buyer" / "Net Seller" / "No Activity"
	InstOwn      string  `json:"inst_own"`      // current % of shares held by institutions
	InstTrans    string  `json:"inst_trans"`    // quarter-over-quarter change in institutional %

	// ── Insider trading (last 3 months) ──────────────────────────────────────
	InsiderActivity string `json:"insider_activity"` // "Net Buyer" / "Net Seller" / "No Activity"
	InsiderBuyVal   string `json:"insider_buy_val"`
	InsiderSellVal  string `json:"insider_sell_val"`
	InsiderNetVal   string `json:"insider_net_val"`
	InsiderFilings  int    `json:"insider_filings"`

	// ── Analyst ratings ───────────────────────────────────────────────────────
	ConsensusRating    string `json:"consensus_rating"`
	AvgPriceTarget     string `json:"avg_price_target"`
	PriceTargetUpside  string `json:"price_target_upside"`
	AnalystBullish     int    `json:"analyst_bullish"`  // StrongBuy + Buy
	AnalystNeutral     int    `json:"analyst_neutral"`  // Hold
	AnalystBearish     int    `json:"analyst_bearish"`  // Sell + StrongSell
	AnalystTotal       int    `json:"analyst_total"`

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

	// ── Step 2: Filter by market cap ────────────────────────────────────────
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

	// ── Step 3: Enrich with financial summaries ──────────────────────────────
	// Build preliminary EarningsResult slice for enricher input.
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

	logf("Fetching financial summaries (EPS estimates, revenue history, trends) ...")
	enricher := NewEnricher()
	summaries := enricher.EnrichAll(preliminary, calMap)

	// ── Step 4: Assemble final results ───────────────────────────────────────
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
		r.PE_TTM     = fmtRatio(s.PE_TTM)
		r.PE_Forward = fmtRatio(s.PE_Forward)
		r.PS         = fmtRatio(s.PS)
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
			r.InstOwn      = fmt.Sprintf("%.2f%%", s.Institutional.InstOwn)
			r.InstTrans    = fmtPct(&s.Institutional.InstTrans)
		} else {
			r.InstActivity = "N/A"
			r.InstOwn      = "N/A"
			r.InstTrans    = "N/A"
		}
		if s.Insider != nil {
			r.InsiderActivity = s.Insider.Activity
			r.InsiderBuyVal   = fmtDollars(s.Insider.BuyValue)
			r.InsiderSellVal  = fmtDollars(s.Insider.SellValue)
			r.InsiderNetVal   = fmtDollars(s.Insider.BuyValue - s.Insider.SellValue)
			r.InsiderFilings  = s.Insider.FilingCount
		} else {
			r.InsiderActivity = "N/A"
			r.InsiderBuyVal   = "N/A"
			r.InsiderSellVal  = "N/A"
			r.InsiderNetVal   = "N/A"
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
		results = append(results, r)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].ResultDate != results[j].ResultDate {
			return results[i].ResultDate < results[j].ResultDate
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
		writeTable(os.Stdout, results)
		writeReturnsTable(os.Stdout, results)
		writeEarningsReactionTable(os.Stdout, results)
		writeAnalystTable(os.Stdout, results)
		writeInsiderTable(os.Stdout, results)
		writeInstitutionalTable(os.Stdout, results)
	}
}

// computeResultDate: "bmo" → previous working day; everything else → same date.
func computeResultDate(dateStr, earningsTime string) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	if earningsTime == "bmo" {
		return prevWorkingDay(t).Format("2006-01-02")
	}
	return dateStr
}

// fmtDollars formats a USD value as "$1.2M", "$450K", or "$0" for display.
func fmtDollars(v float64) string {
	if v == 0 {
		return "$0"
	}
	if v >= 1_000_000 {
		return fmt.Sprintf("$%.1fM", v/1_000_000)
	}
	return fmt.Sprintf("$%.0fK", v/1_000)
}

// fmtRatio formats a *float64 ratio as "23.4x" / "N/A".
func fmtRatio(p *float64) string {
	if p == nil {
		return "N/A"
	}
	return fmt.Sprintf("%.1fx", *p)
}

// fmtPct formats a *float64 percentage pointer as "+8.7%" / "-3.2%" / "N/A".
func fmtPct(p *float64) string {
	if p == nil {
		return "N/A"
	}
	if *p >= 0 {
		return fmt.Sprintf("+%.1f%%", *p)
	}
	return fmt.Sprintf("%.1f%%", *p)
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

func writeTable(w io.Writer, results []EarningsResult) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SYMBOL\tCOMPANY\tMKT_CAP_B\tRESULT_DATE\tFISCAL_Q\t"+
		"EPS_EST\tEPS_PREV_QTR\tEPS_QoQ\tEPS_PREV_YR\tEPS_YoY\t"+
		"REV_EST\tREV_PREV_QTR\tREV_QoQ\tREV_PREV_YR\tREV_YoY\t"+
		"PE_TTM\tPE_FWD\tPS")
	for _, r := range results {
		epsEst := "N/A"
		if r.EPSEstimate != 0 {
			epsEst = fmt.Sprintf("$%.2f", r.EPSEstimate)
		}
		epsPrev := "N/A"
		if r.EPSLastYear != 0 {
			epsPrev = fmt.Sprintf("$%.2f", r.EPSLastYear)
		}
		fmt.Fprintf(tw, "%s\t%s\t$%.1f\t%s (%s)\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Symbol,
			truncate(r.CompanyName, 28),
			r.MarketCapB,
			r.ResultDate,
			labelTime(r.EarningsTime),
			r.FiscalQuarter,
			epsEst,
			r.EPSPrevQtr,
			r.EPSQoQ,
			epsPrev,
			r.EPSYoYPct,
			r.RevEstimate,
			r.RevPrevQtr,
			r.RevQoQ,
			r.RevPrevYr,
			r.RevenueYoYPct,
			r.PE_TTM,
			r.PE_Forward,
			r.PS,
		)
	}
	tw.Flush()

	// Print quarterly history detail block for each result
	if len(results) > 0 && len(results[0].History) > 0 {
		fmt.Fprintln(w, "\n── Quarterly History (oldest → newest) ──────────────────────────────────")
		for _, r := range results {
			if len(r.History) == 0 {
				continue
			}
			fmt.Fprintf(w, "\n%s  %s\n", r.Symbol, r.CompanyName)
			htw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(htw, "  PERIOD\tREVENUE_B\tEPS\tREV_QoQ\tEPS_QoQ")
			for i, q := range r.History {
				revQoQ, epsQoQ := "—", "—"
				if i > 0 {
					prev := r.History[i-1]
					if prev.Revenue != 0 {
						revQoQ = fmtPct(ptr(pctChange(prev.Revenue, q.Revenue)))
					}
					if prev.EPS != 0 {
						epsQoQ = fmtPct(ptr(pctChange(prev.EPS, q.EPS)))
					}
				}
				fmt.Fprintf(htw, "  %s\t$%.2f\t$%.2f\t%s\t%s\n",
					q.Period,
					q.Revenue/1e9,
					q.EPS,
					revQoQ,
					epsQoQ,
				)
			}
			htw.Flush()

			// Forward EPS estimates
			if len(r.ForwardEPS) > 0 {
				fmt.Fprintf(w, "  Forward EPS estimates:\n")
				for _, fq := range r.ForwardEPS[:min(3, len(r.ForwardEPS))] {
					fmt.Fprintf(w, "    %-12s  consensus $%.2f  (range $%.2f–$%.2f, %d ests)\n",
						fq.FiscalEnd, fq.ConsensusEPS, fq.LowEPS, fq.HighEPS, fq.NumberOfEstimates)
				}
			}
		}
	}
}

func writeCSV(w io.Writer, results []EarningsResult) {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"symbol", "company", "market_cap_b", "earnings_date", "earnings_time", "result_date",
		"fiscal_quarter",
		"eps_estimate", "eps_prev_qtr", "eps_qoq_pct", "eps_last_year", "eps_yoy_pct",
		"rev_estimate", "rev_prev_qtr_b", "rev_qoq_pct", "rev_prev_yr_b", "revenue_yoy_pct",
		"history_periods", "history_revenues_b", "history_eps",
		"pe_ttm", "pe_forward", "ps",
		"current_price", "ret_1w", "ret_1m", "ret_6m", "ret_1y",
		"inst_activity", "inst_own", "inst_trans",
		"insider_activity", "insider_buy_val", "insider_sell_val", "insider_net_val", "insider_filings",
		"consensus_rating", "avg_price_target", "price_target_upside",
		"analyst_bullish", "analyst_neutral", "analyst_bearish", "analyst_total",
	})
	for _, r := range results {
		periods, revs, eps := "", "", ""
		for i, q := range r.History {
			sep := ""
			if i > 0 {
				sep = "|"
			}
			periods += sep + q.Period
			revs += sep + fmt.Sprintf("%.2f", q.Revenue/1e9)
			eps += sep + fmt.Sprintf("%.2f", q.EPS)
		}
		_ = cw.Write([]string{
			r.Symbol, r.CompanyName,
			fmt.Sprintf("%.2f", r.MarketCapB),
			r.EarningsDate, r.EarningsTime, r.ResultDate,
			r.FiscalQuarter,
			fmt.Sprintf("%.2f", r.EPSEstimate), r.EPSPrevQtr, r.EPSQoQ,
			fmt.Sprintf("%.2f", r.EPSLastYear), r.EPSYoYPct,
			r.RevEstimate, r.RevPrevQtr, r.RevQoQ, r.RevPrevYr, r.RevenueYoYPct,
			periods, revs, eps,
			r.PE_TTM, r.PE_Forward, r.PS,
			r.CurrentPrice, r.Ret1W, r.Ret1M, r.Ret6M, r.Ret1Y,
			r.InstActivity, r.InstOwn, r.InstTrans,
			r.InsiderActivity, r.InsiderBuyVal, r.InsiderSellVal, r.InsiderNetVal,
			fmt.Sprintf("%d", r.InsiderFilings),
			r.ConsensusRating, r.AvgPriceTarget, r.PriceTargetUpside,
			fmt.Sprintf("%d", r.AnalystBullish), fmt.Sprintf("%d", r.AnalystNeutral),
			fmt.Sprintf("%d", r.AnalystBearish), fmt.Sprintf("%d", r.AnalystTotal),
		})
	}
	cw.Flush()
}

func writeJSON(w io.Writer, results []EarningsResult) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(results)
}

func labelTime(t string) string {
	switch t {
	case "bmo":
		return "BMO"
	case "amc":
		return "AMC"
	default:
		return "N/A"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func ptr(f float64) *float64 { return &f }

func writeInstitutionalTable(w io.Writer, results []EarningsResult) {
	fmt.Fprintln(w, "\n── Institutional Ownership (mutual funds, hedge funds — latest 13F quarter) ──")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SYMBOL\tCOMPANY\tACTIVITY\tINST_OWN\tQoQ_CHANGE")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			r.Symbol,
			truncate(r.CompanyName, 28),
			r.InstActivity,
			r.InstOwn,
			r.InstTrans,
		)
	}
	tw.Flush()
}

func writeInsiderTable(w io.Writer, results []EarningsResult) {
	fmt.Fprintln(w, "\n── Insider Trading (last 3 months, open-market only) ────────────────────")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SYMBOL\tCOMPANY\tACTIVITY\tBUY_VAL\tSELL_VAL\tNET_VAL\tFORM4s")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			r.Symbol,
			truncate(r.CompanyName, 28),
			r.InsiderActivity,
			r.InsiderBuyVal,
			r.InsiderSellVal,
			r.InsiderNetVal,
			r.InsiderFilings,
		)
	}
	tw.Flush()
}

func writeAnalystTable(w io.Writer, results []EarningsResult) {
	fmt.Fprintln(w, "\n── Analyst Ratings ──────────────────────────────────────────────────────")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SYMBOL\tCOMPANY\tCONSENSUS\tPRICE_TARGET\tVS_CURRENT\tBULLISH\tNEUTRAL\tBEARISH\tTOTAL")
	for _, r := range results {
		bullPct, neutPct, bearPct := "N/A", "N/A", "N/A"
		if r.AnalystTotal > 0 {
			bullPct = fmt.Sprintf("%d (%.0f%%)", r.AnalystBullish, float64(r.AnalystBullish)/float64(r.AnalystTotal)*100)
			neutPct = fmt.Sprintf("%d (%.0f%%)", r.AnalystNeutral, float64(r.AnalystNeutral)/float64(r.AnalystTotal)*100)
			bearPct = fmt.Sprintf("%d (%.0f%%)", r.AnalystBearish, float64(r.AnalystBearish)/float64(r.AnalystTotal)*100)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			r.Symbol,
			truncate(r.CompanyName, 28),
			r.ConsensusRating,
			r.AvgPriceTarget,
			r.PriceTargetUpside,
			bullPct,
			neutPct,
			bearPct,
			r.AnalystTotal,
		)
	}
	tw.Flush()
}

func writeReturnsTable(w io.Writer, results []EarningsResult) {
	fmt.Fprintln(w, "\n── Stock Price Returns ──────────────────────────────────────────────────")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SYMBOL\tCOMPANY\tCURRENT_PRICE\t1W_RET\t1M_RET\t6M_RET\t1Y_RET")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Symbol,
			truncate(r.CompanyName, 28),
			r.CurrentPrice,
			r.Ret1W,
			r.Ret1M,
			r.Ret6M,
			r.Ret1Y,
		)
	}
	tw.Flush()
}

func writeEarningsReactionTable(w io.Writer, results []EarningsResult) {
	fmt.Fprintln(w, "\n── Post-Earnings Stock Reaction (next trading day after report) ─────────")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SYMBOL\tCOMPANY\tQUARTER\tANNOUNCED\tRXN_DAY\tPRIOR_CLS\tRXN_CLS\tPRE7\tRXN_RET\tPOST7\tEPS_EST\tEPS_ACT\tEPS_BEAT\tREV_ACT")
	for _, r := range results {
		if len(r.EarningsReactions) == 0 {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Symbol, truncate(r.CompanyName, 28),
				"N/A", "N/A", "N/A", "N/A", "N/A", "N/A", "N/A", "N/A", "N/A", "N/A", "N/A", "N/A",
			)
			continue
		}
		for i, rxn := range r.EarningsReactions {
			sym, name := r.Symbol, truncate(r.CompanyName, 28)
			if i > 0 {
				sym, name = "", ""
			}
			epsEst := "N/A"
			if rxn.EPSEstimate != 0 {
				epsEst = fmt.Sprintf("$%.2f", rxn.EPSEstimate)
			}
			epsAct := "N/A"
			if rxn.EPSActual != 0 {
				epsAct = fmt.Sprintf("$%.2f", rxn.EPSActual)
			}
			revAct := "N/A"
			if rxn.RevenueActual != 0 {
				revAct = fmt.Sprintf("$%.2fB", rxn.RevenueActual/1e9)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t$%.2f\t$%.2f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				sym, name,
				rxn.Period,
				rxn.AnnouncementDate,
				rxn.ReactionDay,
				rxn.PriorClose,
				rxn.ReactionClose,
				fmtPct(rxn.Pre7Ret),
				fmtPct(&rxn.RetPct),
				fmtPct(rxn.Post7Ret),
				epsEst,
				epsAct,
				fmtPct(rxn.EPSBeatPct),
				revAct,
			)
		}
	}
	tw.Flush()
}
