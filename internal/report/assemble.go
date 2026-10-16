package report

import (
	"fmt"

	"quarterly_results/internal/domain"
)

// AssembleResult maps a FinancialSummary onto the pre-populated EarningsResult
// fields that come from the Nasdaq calendar (Symbol, MarketCapB, etc.).
func AssembleResult(r domain.EarningsResult, s *domain.FinancialSummary) domain.EarningsResult {
	if s == nil {
		return r
	}
	cur := s.Currency
	if cur == "" {
		cur = "USD"
	}
	r.Currency = cur
	r.FiscalQuarter = s.FiscalQuarter
	r.EPSEstimate = s.EPSEstimate
	r.EPSLastYear = s.EPSLastYear
	r.EPSYoYPct = fmtPct(s.EPSYoYPct)
	r.RevenueYoYPct = fmtPct(s.RevenueYoYPct)
	r.History = s.History
	r.ForwardEPS = s.ForwardEPS
	if s.EPSPrevQtr != nil {
		r.EPSPrevQtr = fmtCurrency(*s.EPSPrevQtr, cur)
	} else {
		r.EPSPrevQtr = "N/A"
	}
	r.EPSQoQ = fmtPct(s.EPSQoQPct)
	if s.RevenuePrevQtr != nil {
		r.RevPrevQtr = fmtCurrencyB(*s.RevenuePrevQtr, cur)
	} else {
		r.RevPrevQtr = "N/A"
	}
	if s.RevenueEstimate != nil {
		r.RevEstimate = fmtCurrencyB(*s.RevenueEstimate, cur)
	} else {
		r.RevEstimate = "N/A"
	}
	r.RevQoQ = fmtPct(s.RevenueQoQPct)
	if s.RevenuePrevYear != nil {
		r.RevPrevYr = fmtCurrencyB(*s.RevenuePrevYear, cur)
	} else {
		r.RevPrevYr = "N/A"
	}
	r.PE_TTM = fmtRatio(s.PE_TTM)
	r.PE_Forward = fmtRatio(s.PE_Forward)
	r.PS = fmtRatio(s.PS)
	if s.CurrentPrice > 0 {
		r.CurrentPrice = fmtCurrency(s.CurrentPrice, cur)
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
		r.AvgPriceTarget = fmtCurrency(s.AvgPriceTarget, cur)
	} else {
		r.AvgPriceTarget = "N/A"
	}
	r.PriceTargetUpside = fmtPct(s.PriceTargetUpside)
	r.AnalystBullish = s.StrongBuy + s.Buy
	r.AnalystNeutral = s.Hold
	r.AnalystBearish = s.Sell + s.StrongSell
	r.AnalystTotal = s.TotalRatings
	r.EarningsReactions = s.EarningsReactions
	// When the calendar lookup found no upcoming earnings (common right after a
	// company has just reported), fall back to the most recent past announcement
	// so downstream consumers always have a reference earnings date to anchor on.
	if r.EarningsDate == "" && len(s.EarningsReactions) > 0 {
		latest := s.EarningsReactions[0]
		for _, rxn := range s.EarningsReactions[1:] {
			if rxn.AnnouncementDate > latest.AnnouncementDate {
				latest = rxn
			}
		}
		if latest.AnnouncementDate != "" {
			r.EarningsDate = latest.AnnouncementDate
			if r.ResultDate == "" {
				r.ResultDate = latest.AnnouncementDate
			}
		}
	}
	r.MacroContext = s.MacroContext
	r.SectorETF = s.SectorETF
	r.SectorRet1M = s.SectorRet1M
	r.SectorRet3M = s.SectorRet3M
	r.MaterialEvents = s.MaterialEvents
	r.Peers = s.Peers
	if s.MSPR > 0 {
		r.MSPR = fmt.Sprintf("%.2f", s.MSPR)
	} else {
		r.MSPR = "N/A"
	}
	r.MSPRSignal = s.MSPRSignal
	if r.MSPRSignal == "" {
		r.MSPRSignal = "N/A"
	}
	if opt := s.Options; opt != nil && opt.GEX != nil {
		r.GEX = opt.GEX
	}
	r.Recommendation = s.Recommendation
	r.Backtest = s.Backtest
	r.LLMRating = s.LLMRating

	if s.Hi52 > 0 {
		r.Hi52 = fmtCurrency(s.Hi52, cur)
		r.Lo52 = fmtCurrency(s.Lo52, cur)
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
		r.ExpectedMove = fmt.Sprintf("±%s", fmtCurrency(opt.ExpectedMove, cur))
		r.ExpectedMovePct = fmt.Sprintf("±%.1f%%", opt.ExpectedMovePct)
		r.IVAtm = fmt.Sprintf("%.1f%%", opt.IVAtm)
		r.PCVol = fmt.Sprintf("%.2f", opt.PCVol)
		r.PCoi = fmt.Sprintf("%.2f", opt.PCoi)
		r.Skew = fmtPct(&opt.Skew)
		r.MaxPain = fmtCurrency(opt.MaxPain, cur)
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
