package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)


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
		"options_expiry", "expected_move", "expected_move_pct", "iv_atm",
		"pc_vol", "pc_oi", "skew", "max_pain", "max_pain_vs_current", "hist_avg_abs_rxn",
		"macro_context",
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
			r.OptionsExpiry, r.ExpectedMove, r.ExpectedMovePct, r.IVAtm,
			r.PCVol, r.PCoi, r.Skew, r.MaxPain, r.MaxPainVsCurrent, r.HistAvgAbsRxn,
			r.MacroContext,
		})
	}
	cw.Flush()
}

func writeJSON(w io.Writer, results []EarningsResult) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(results)
}

// writeStockCards prints all data for each stock together as a card.
func writeStockCards(w io.Writer, results []EarningsResult) {
	for i, r := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		writeStockCard(w, r)
	}
}

func writeStockCard(w io.Writer, r EarningsResult) {
	// ── Header ──────────────────────────────────────────────────────────────
	header := fmt.Sprintf(" %s  %s  ·  $%.1fB  ·  Result: %s (%s)  ·  %s",
		r.Symbol, r.CompanyName, r.MarketCapB,
		r.ResultDate, labelTime(r.EarningsTime), r.FiscalQuarter)
	divider := repeat("═", len(header)+2)
	fmt.Fprintln(w, divider)
	fmt.Fprintln(w, header)
	fmt.Fprintln(w, divider)

	// ── EPS & Revenue ───────────────────────────────────────────────────────
	epsEst := "N/A"
	if r.EPSEstimate != 0 {
		epsEst = fmt.Sprintf("$%.2f", r.EPSEstimate)
	}
	epsPrevYr := "N/A"
	if r.EPSLastYear != 0 {
		epsPrevYr = fmt.Sprintf("$%.2f", r.EPSLastYear)
	}
	fmt.Fprintf(w, "EPS    Est %-8s  Prev Qtr %-8s (QoQ %-7s)  Last Year %-8s (YoY %s)\n",
		epsEst, r.EPSPrevQtr, r.EPSQoQ, epsPrevYr, r.EPSYoYPct)
	fmt.Fprintf(w, "Rev    Est %-8s  Prev Qtr %-8s (QoQ %-7s)  Last Year %-8s (YoY %s)\n",
		r.RevEstimate, r.RevPrevQtr, r.RevQoQ, r.RevPrevYr, r.RevenueYoYPct)

	// ── Valuation & Price ────────────────────────────────────────────────────
	fmt.Fprintf(w, "Val    PE(TTM) %-6s  PE(Fwd) %-6s  PS %s\n",
		r.PE_TTM, r.PE_Forward, r.PS)
	fmt.Fprintf(w, "Price  %-8s  1W %-7s  1M %-7s  6M %-7s  1Y %s\n",
		r.CurrentPrice, r.Ret1W, r.Ret1M, r.Ret6M, r.Ret1Y)

	// ── Macro ────────────────────────────────────────────────────────────────
	if r.MacroContext != "" {
		fmt.Fprintf(w, "Macro  %s\n", r.MacroContext)
	}

	// ── Quarterly History ────────────────────────────────────────────────────
	if len(r.History) > 0 {
		fmt.Fprintln(w, "\n  Quarterly History:")
		htw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(htw, "    PERIOD\tREVENUE_B\tEPS\tREV_QoQ\tEPS_QoQ\tREV_YoY\tEPS_YoY")
		// Display only the last 5 quarters; the earlier ones serve as YoY anchors.
		history := r.History
		displayFrom := 0
		if len(history) > 5 {
			displayFrom = len(history) - 5
		}
		for i := displayFrom; i < len(history); i++ {
			q := history[i]
			period := q.Period
			if q.PeriodStart != "" {
				period = q.PeriodStart + " – " + q.Period
			}
			revQoQ, epsQoQ := "—", "—"
			if i > 0 {
				prev := history[i-1]
				if prev.Revenue != 0 {
					revQoQ = fmtPct(ptr(pctChange(prev.Revenue, q.Revenue)))
				}
				if prev.EPS != 0 {
					epsQoQ = fmtPct(ptr(pctChange(prev.EPS, q.EPS)))
				}
			}
			revYoY, epsYoY := "—", "—"
			if i >= 4 {
				yoy := history[i-4]
				if yoy.Revenue != 0 {
					revYoY = fmtPct(ptr(pctChange(yoy.Revenue, q.Revenue)))
				}
				if yoy.EPS != 0 {
					epsYoY = fmtPct(ptr(pctChange(yoy.EPS, q.EPS)))
				}
			}
			fmt.Fprintf(htw, "    %s\t$%.2f\t$%.2f\t%s\t%s\t%s\t%s\n",
				period, q.Revenue/1e9, q.EPS, revQoQ, epsQoQ, revYoY, epsYoY)
		}
		htw.Flush()
		if len(r.ForwardEPS) > 0 {
			fmt.Fprint(w, "  Forward EPS:")
			for _, fq := range r.ForwardEPS[:min(3, len(r.ForwardEPS))] {
				fmt.Fprintf(w, "  %s $%.2f ($%.2f–$%.2f, %d ests)",
					fq.FiscalEnd, fq.ConsensusEPS, fq.LowEPS, fq.HighEPS, fq.NumberOfEstimates)
			}
			fmt.Fprintln(w)
		}
	}

	// ── Analyst Ratings ──────────────────────────────────────────────────────
	fmt.Fprintln(w)
	bullPct, neutPct, bearPct := "N/A", "N/A", "N/A"
	if r.AnalystTotal > 0 {
		bullPct = fmt.Sprintf("%d (%.0f%%)", r.AnalystBullish, float64(r.AnalystBullish)/float64(r.AnalystTotal)*100)
		neutPct = fmt.Sprintf("%d (%.0f%%)", r.AnalystNeutral, float64(r.AnalystNeutral)/float64(r.AnalystTotal)*100)
		bearPct = fmt.Sprintf("%d (%.0f%%)", r.AnalystBearish, float64(r.AnalystBearish)/float64(r.AnalystTotal)*100)
	}
	fmt.Fprintf(w, "Analyst  %-12s  PT %-8s (upside %s)  Bullish %-10s  Neutral %-10s  Bearish %s\n",
		r.ConsensusRating, r.AvgPriceTarget, r.PriceTargetUpside,
		bullPct, neutPct, bearPct)

	// ── Institutional & Insider ──────────────────────────────────────────────
	fmt.Fprintf(w, "Inst     Activity %-12s  Own %-8s  QoQ %s\n",
		r.InstActivity, r.InstOwn, r.InstTrans)
	fmt.Fprintf(w, "Insider  Activity %-12s  Buy %-8s  Sell %-8s  Net %-8s  Filings %d\n",
		r.InsiderActivity, r.InsiderBuyVal, r.InsiderSellVal, r.InsiderNetVal, r.InsiderFilings)

	// ── Options ──────────────────────────────────────────────────────────────
	fmt.Fprintf(w, "Options  Exp %-10s  Move %-6s (%-6s)  IV %-6s  P/C_Vol %-5s  P/C_OI %-5s  Skew %-6s  MaxPain %-8s (%s)  HistAvg %s\n",
		r.OptionsExpiry, r.ExpectedMove, r.ExpectedMovePct, r.IVAtm,
		r.PCVol, r.PCoi, r.Skew, r.MaxPain, r.MaxPainVsCurrent, r.HistAvgAbsRxn)

	// ── Past Earnings Reactions ───────────────────────────────────────────────
	if len(r.EarningsReactions) > 0 {
		fmt.Fprintln(w, "\n  Past Earnings Reactions:")
		rtw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintln(rtw, "    QUARTER\tANNOUNCED\tRXN_DAY\tPRIOR_CLS\tRXN_OPEN\tGAP_RET\tRXN_CLS\tDAY_RET\tPRE7\tPOST7\tEPS_EST\tEPS_ACT\tEPS_BEAT\tREV_ACT\tVIX\tMACRO")
		for _, rxn := range r.EarningsReactions {
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
			rxnOpenStr := "N/A"
			gapRetStr := "N/A"
			if rxn.ReactionOpen > 0 {
				rxnOpenStr = fmt.Sprintf("$%.2f", rxn.ReactionOpen)
				gapRetStr = fmtPct(&rxn.GapRetPct)
			}
			vixStr := "N/A"
			if rxn.VIX > 0 {
				vixStr = fmt.Sprintf("%.1f", rxn.VIX)
			}
			macroStr := rxn.MacroContext
			if macroStr == "" {
				macroStr = "—"
			}
			fmt.Fprintf(rtw, "    %s\t%s\t%s\t$%.2f\t%s\t%s\t$%.2f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				rxn.Period, rxn.AnnouncementDate, rxn.ReactionDay,
				rxn.PriorClose, rxnOpenStr, gapRetStr, rxn.ReactionClose,
				fmtPct(&rxn.RetPct),
				fmtPct(rxn.Pre7Ret), fmtPct(rxn.Post7Ret),
				epsEst, epsAct, fmtPct(rxn.EPSBeatPct), revAct,
				vixStr, macroStr,
			)
		}
		rtw.Flush()
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n*len(s))
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
