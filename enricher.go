package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FinancialSummary holds enriched financial data for one stock.
type FinancialSummary struct {
	Symbol string

	// Current quarter estimates (from Nasdaq calendar)
	EPSEstimate    float64 // consensus estimate
	EPSLastYear    float64 // same quarter last year actual (from calendar)
	FiscalQuarter  string  // e.g. "Feb/2026"

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
	ConsensusRating string
	StrongBuy       int
	Buy             int
	Hold            int
	Sell            int
	StrongSell      int
	TotalRatings    int
	AvgPriceTarget  float64
	PriceTargetUpside *float64 // (target - current) / current * 100

	// ── Derived signals ──────────────────────────────────────────────────────

	// 52-week range position and RSI(14).
	Hi52         float64  // 52-week high closing price
	Lo52         float64  // 52-week low closing price
	PctFrom52Hi  *float64 // (current - hi52) / hi52 * 100  (negative = below high)
	PctFrom52Lo  *float64 // (current - lo52) / lo52 * 100  (positive = above low)
	RSI14        *float64 // 14-period Wilder RSI of daily closes

	// Implied move vs historical move ratio.
	// > 1.0 means options are pricing more uncertainty than the stock historically delivers.
	ImpliedVsHistRatio *float64

	// Earnings beat consistency (from last ≤4 EarningsReactions).
	BeatRate   *float64 // fraction of quarters where EPS beat consensus (0–1)
	AvgBeatPct *float64 // average EPS beat percentage across those quarters
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
	EPSActual      float64  // actual reported EPS (from SEC EDGAR)
	EPSEstimate    float64  // consensus EPS estimate at announcement (from Nasdaq calendar)
	EPSBeatPct     *float64 // (actual − estimate) / |estimate| × 100; nil if estimate unavailable
	RevenueActual  float64  // actual reported revenue (from SEC EDGAR)

	// Pre/post earnings drift (calendar days, excluding announcement/reaction days)
	Pre7Ret   *float64 // 7 days before announcement → day before announcement
	Pre7Close float64  // closing price ~7 calendar days before announcement
	Post7Ret  *float64 // reaction day close → 7 calendar days after reaction day
	Post7Close float64 // closing price ~7 calendar days after reaction day

	// VIX closing level on the reaction day — high VIX = macro noise may have overwhelmed earnings signal
	VIX float64

	// Macro events near the announcement date (±2 days)
	MacroContext string
}

// ForwardQuarter holds one future quarter EPS estimate from Nasdaq.
type ForwardQuarter struct {
	FiscalEnd          string
	ConsensusEPS       float64
	HighEPS            float64
	LowEPS             float64
	NumberOfEstimates  int
}

// pricePoint is a single trading day's OHLC snapshot.
type pricePoint struct {
	Date  time.Time
	Open  float64
	Close float64
}

// nasdaqHistoricalResponse is the raw shape from the Nasdaq historical prices API.
type nasdaqHistoricalResponse struct {
	Data struct {
		TradesTable struct {
			Rows []struct {
				Date  string `json:"date"`  // "MM/DD/YYYY"
				Open  string `json:"open"`  // "$123.45"
				Close string `json:"close"` // "$123.45"
			} `json:"rows"`
		} `json:"tradesTable"`
	} `json:"data"`
}

// nasdaqForecastResponse is the raw Nasdaq earnings-forecast API shape.
type nasdaqForecastResponse struct {
	Data struct {
		QuarterlyForecast struct {
			Rows []struct {
				FiscalEnd            string  `json:"fiscalEnd"`
				ConsensusEPSForecast float64 `json:"consensusEPSForecast"`
				HighEPSForecast      float64 `json:"highEPSForecast"`
				LowEPSForecast       float64 `json:"lowEPSForecast"`
				NoOfEstimates        int     `json:"noOfEstimates"`
			} `json:"rows"`
		} `json:"quarterlyForecast"`
	} `json:"data"`
}

// Enricher fetches and computes financial growth metrics for a list of earnings results.
type Enricher struct {
	secClient  *SECClient
	httpClient *http.Client

	// Yahoo Finance requires a crumb token tied to a cookie session.
	// yahooClient holds a cookie jar and is used exclusively for Yahoo API calls.
	yahooClient      *http.Client
	yahooCrumb       string
	yahooCrumbOnce   sync.Once
	yahooCrumbErr    error
}

func NewEnricher() *Enricher {
	jar, _ := cookiejar.New(nil)
	return &Enricher{
		secClient:   NewSECClient(),
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		yahooClient: &http.Client{Timeout: 30 * time.Second, Jar: jar},
	}
}

// EnrichAll fetches financial summaries for all results concurrently (rate-limited).
func (e *Enricher) EnrichAll(results []EarningsResult, calendarRows map[string]nasdaqCalendarRow, macro *MacroCalendar) map[string]*FinancialSummary {
	// Pre-load the SEC ticker map once (single HTTP call).
	if err := e.secClient.LoadTickerMap(); err != nil {
		logf("Warning: could not load SEC ticker map: %v", err)
	}

	out := make(map[string]*FinancialSummary, len(results))
	var mu sync.Mutex

	// SEC allows ≤10 req/s; 5 concurrent goroutines each making 2-3 calls is safe.
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, r := range results {
		wg.Add(1)
		go func(res EarningsResult) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			summary := e.buildSummary(res, calendarRows[res.Symbol], macro)

			mu.Lock()
			out[res.Symbol] = summary
			mu.Unlock()
		}(r)
	}

	wg.Wait()
	return out
}

func (e *Enricher) buildSummary(res EarningsResult, row nasdaqCalendarRow, macro *MacroCalendar) *FinancialSummary {
	s := &FinancialSummary{
		Symbol:        res.Symbol,
		EPSEstimate:   row.EPSForecast,
		EPSLastYear:   row.LastYearEPS,
		FiscalQuarter: row.FiscalQuarterEnding,
	}

	// YoY EPS from calendar data
	if s.EPSLastYear != 0 {
		yoy := pctChange(s.EPSLastYear, s.EPSEstimate)
		s.EPSYoYPct = &yoy
	}

	// Forward EPS estimates from Nasdaq
	fwd, err := e.fetchForwardEPS(res.Symbol)
	if err == nil {
		s.ForwardEPS = fwd
	}

	// Consensus revenue estimate + year-ago same-quarter revenue from stockanalysis.com.
	// stockanalysis correctly identifies the matching fiscal quarter across 10-Q and 10-K
	// filings (e.g. fiscal-year-end quarters that are only in 10-K, not 10-Q).
	if sa, err := e.fetchSAEstimates(res.Symbol); err == nil {
		s.RevenueEstimate = &sa.RevenueEst
		if sa.RevenuePrevYear != 0 {
			s.RevenuePrevYear = &sa.RevenuePrevYear
		}
		// If Nasdaq had no EPS estimate, fall back to stockanalysis value
		if s.EPSEstimate == 0 && sa.EPSEst != 0 {
			s.EPSEstimate = sa.EPSEst
		}
		// Analyst ratings
		s.ConsensusRating = sa.ConsensusRating
		s.StrongBuy = sa.StrongBuy
		s.Buy = sa.Buy
		s.Hold = sa.Hold
		s.Sell = sa.Sell
		s.StrongSell = sa.StrongSell
		s.TotalRatings = sa.TotalRatings
		s.AvgPriceTarget = sa.AvgPriceTarget
	}

	// Insider activity from SEC Form 4 filings (last 90 days).
	since := time.Now().AddDate(0, -3, 0)
	if insider, err := e.secClient.FetchInsiderActivity(res.Symbol, since); err == nil {
		s.Insider = insider
	} else {
		logf("Warning: insider data unavailable for %s: %v", res.Symbol, err)
	}

	// Institutional ownership from Finviz (covers mutual funds, hedge funds, investment advisors).
	if inst, err := e.fetchInstitutionalData(res.Symbol); err == nil {
		s.Institutional = inst
	} else {
		logf("Warning: institutional data unavailable for %s: %v", res.Symbol, err)
	}

	// Historical actuals from SEC EDGAR (all US companies, free).
	history, err := e.secClient.FetchQuarterlyActuals(res.Symbol)
	if err == nil && len(history) >= 2 {
		// Enrich with 8-K announcement dates (more accurate than 10-Q filing dates
		// for determining when the market first saw the results).
		if announceDates, err2 := e.secClient.FetchEarningsAnnouncementDates(res.Symbol, history); err2 == nil {
			for i := range history {
				if ad, ok := announceDates[history[i].Period]; ok {
					history[i].FilingDate = ad
				}
			}
		}
		s.History = history

		// RevenuePrevYear fallback: if stockanalysis didn't provide it, use history[0].
		// Note: history[0] may not be the same fiscal quarter — stockanalysis is preferred.
		oldest := history[0]
		if s.RevenuePrevYear == nil && oldest.Revenue != 0 {
			v := oldest.Revenue
			s.RevenuePrevYear = &v
		}

		// Previous quarter actuals (most recent completed quarter)
		last := history[len(history)-1]
		if last.EPS != 0 {
			v := last.EPS
			s.EPSPrevQtr = &v
		}
		if last.Revenue != 0 {
			v := last.Revenue
			s.RevenuePrevQtr = &v
		}

		// Override EPSLastYear with the authoritative SEC EDGAR value.
		// The Nasdaq calendar's lastYearEPS field is unreliable (e.g. off by 10×).
		// Find the history entry whose period end is closest to one year before the
		// current fiscal quarter end, within a ±46-day window.
		if qEnd, ok := parseFiscalQuarterEnd(s.FiscalQuarter); ok {
			targetYearAgo := qEnd.AddDate(-1, 0, 0)
			bestDiff := time.Duration(math.MaxInt64)
			var bestEPS float64
			for _, q := range history {
				qPeriod, err2 := time.Parse("2006-01-02", q.Period)
				if err2 != nil || q.EPS == 0 {
					continue
				}
				diff := qPeriod.Sub(targetYearAgo)
				if diff < 0 {
					diff = -diff
				}
				if diff <= 46*24*time.Hour && diff < bestDiff {
					bestDiff = diff
					bestEPS = q.EPS
				}
			}
			if bestEPS != 0 {
				// Only override Nasdaq's lastYearEPS with SEC EDGAR data when:
				//   1. Nasdaq provided no prior-year EPS (missing data), OR
				//   2. The ratio is extreme (>4× or <0.25×), indicating a Nasdaq data error
				//      (e.g. NFLX Q1 2025: Nasdaq returned $0.66, SEC had $6.61 — a 10× error).
				// When Nasdaq has a valid non-GAAP value within a reasonable range of the SEC
				// GAAP value, trust Nasdaq — GAAP vs non-GAAP adjustments normally differ by <30%.
				nasdaqEPS := s.EPSLastYear
				var absRatio float64
				if nasdaqEPS != 0 {
					absRatio = math.Abs(bestEPS / nasdaqEPS)
				}
				if nasdaqEPS == 0 || absRatio > 4 || absRatio < 0.25 {
					s.EPSLastYear = bestEPS
					// Recompute YoY pct with the corrected value.
					if s.EPSEstimate != 0 {
						yoy := pctChange(s.EPSLastYear, s.EPSEstimate)
						s.EPSYoYPct = &yoy
					}
				}
			}
		}
	}

	// YoY Revenue: estimate vs same fiscal quarter last year.
	// Mirrors EPS_YoY = pctChange(EPSLastYear, EPSEstimate).
	if s.RevenuePrevYear != nil && *s.RevenuePrevYear != 0 && s.RevenueEstimate != nil && *s.RevenueEstimate != 0 {
		v := pctChange(*s.RevenuePrevYear, *s.RevenueEstimate)
		s.RevenueYoYPct = &v
	}

	// QoQ: current-quarter estimate vs most-recent actual quarter.
	if s.EPSPrevQtr != nil && *s.EPSPrevQtr != 0 && s.EPSEstimate != 0 {
		v := pctChange(*s.EPSPrevQtr, s.EPSEstimate)
		s.EPSQoQPct = &v
	}
	if s.RevenuePrevQtr != nil && *s.RevenuePrevQtr != 0 && s.RevenueEstimate != nil && *s.RevenueEstimate != 0 {
		v := pctChange(*s.RevenuePrevQtr, *s.RevenueEstimate)
		s.RevenueQoQPct = &v
	}

	// ── Valuation ratios ─────────────────────────────────────────────────────

	// PS = MarketCap / TTM Revenue (no stock price needed)
	if len(s.History) >= 4 && res.MarketCapB > 0 {
		ttmRev := 0.0
		for _, q := range s.History[len(s.History)-4:] {
			ttmRev += q.Revenue
		}
		if ttmRev > 0 {
			v := (res.MarketCapB * 1e9) / ttmRev
			s.PS = &v
		}
	}

	// TTM EPS = sum of last 4 quarters
	var ttmEPS float64
	if len(s.History) >= 4 {
		for _, q := range s.History[len(s.History)-4:] {
			ttmEPS += q.EPS
		}
	}

	// Forward Annual EPS: sum of first 4 ForwardEPS quarters, else EPSEstimate * 4
	var fwdEPS float64
	if len(s.ForwardEPS) >= 4 {
		for _, fq := range s.ForwardEPS[:4] {
			fwdEPS += fq.ConsensusEPS
		}
	} else if s.EPSEstimate != 0 {
		fwdEPS = s.EPSEstimate * 4
	}

	// Fetch 1-year daily price history (used for PE ratios + stock returns).
	prices, err := e.fetchPriceHistory(res.Symbol)
	if err != nil || len(prices) == 0 {
		logf("Warning: price history unavailable for %s: %v", res.Symbol, err)
		return s
	}

	// Fetch VIX history for reaction-day macro context (best-effort; failures are non-fatal).
	vixPrices, _ := e.fetchPriceHistory("^VIX")

	current := prices[len(prices)-1].Close
	now := prices[len(prices)-1].Date
	s.CurrentPrice = current

	// Price target upside: (avg target - current) / current * 100
	if s.AvgPriceTarget > 0 {
		v := pctChange(current, s.AvgPriceTarget)
		s.PriceTargetUpside = &v
	}

	// PE(ttm) and PE(forward)
	if len(s.History) >= 4 && ttmEPS != 0 {
		v := current / ttmEPS
		s.PE_TTM = &v
	}
	if fwdEPS != 0 {
		v := current / fwdEPS
		s.PE_Forward = &v
	}

	// Stock price returns vs lookback periods
	for _, lb := range []struct {
		days  int
		field **float64
	}{
		{7, &s.Ret1W},
		{30, &s.Ret1M},
		{182, &s.Ret6M},
		{365, &s.Ret1Y},
	} {
		target := now.AddDate(0, 0, -lb.days)
		if past, ok := closestPrice(prices, target); ok && past != 0 {
			v := pctChange(past, current)
			*lb.field = &v
		}
	}

	// ── Earnings reactions (last ≤4 historical quarters) ─────────────────────
	// For each quarter in History that has a FilingDate, compute the stock's
	// reaction on the next working day after announcement vs the day before.
	quarters := s.History
	if len(quarters) > 4 {
		quarters = quarters[len(quarters)-4:]
	}
	// Build period → QuarterActual lookup for EPS/revenue actuals.
	quarterByPeriod := make(map[string]QuarterActual, len(quarters))
	for _, q := range quarters {
		quarterByPeriod[q.Period] = q
	}
	usedAnnounceDates := make(map[string]bool) // dedup guard: skip if same date used twice
	for _, q := range quarters {
		if q.FilingDate == "" {
			continue
		}
		announceTime, err := time.Parse("2006-01-02", q.FilingDate)
		if err != nil {
			continue
		}
		// Sanity check: announcement must be 10–91 days after period end.
		// If outside this range the 8-K lookup returned a wrong filing date.
		periodEnd, perr := time.Parse("2006-01-02", q.Period)
		if perr == nil {
			days := int(announceTime.Sub(periodEnd).Hours() / 24)
			if days < 10 || days > 91 {
				logf("Warning: skipping reaction for %s period %s — announcement date %s is out of expected range (%d days after period end)", res.Symbol, q.Period, q.FilingDate, days)
				continue
			}
		}
		// Skip duplicate announcement dates (prevents two quarters sharing the same
		// wrong date from the 8-K lookup producing identical reaction values).
		if usedAnnounceDates[q.FilingDate] {
			logf("Warning: skipping reaction for %s period %s — announcement date %s already used for another quarter", res.Symbol, q.Period, q.FilingDate)
			continue
		}
		usedAnnounceDates[q.FilingDate] = true
		nextDay := nextWorkingDay(announceTime)

		// Detect BMO vs AMC by comparing opening gaps:
		//   BMO: earnings released before market open → big gap at announcement-day open
		//   AMC: earnings released after close → big gap at next-day open
		dayBeforeClose, okDayBefore := closestPrice(prices, announceTime.AddDate(0, 0, -1))
		announceDayOpen := openOnDate(prices, announceTime)
		announceDayClose, _ := closestPrice(prices, announceTime)
		nextDayOpen := openOnDate(prices, nextDay)

		isBMO := false
		if okDayBefore && dayBeforeClose > 0 && announceDayOpen > 0 && announceDayClose > 0 && nextDayOpen > 0 {
			gapAnnounce := math.Abs(announceDayOpen-dayBeforeClose) / dayBeforeClose
			gapNextDay := math.Abs(nextDayOpen-announceDayClose) / announceDayClose
			isBMO = gapAnnounce > gapNextDay
		}

		var reactionDay time.Time
		var priorClose float64
		var okPrior bool
		if isBMO {
			// BMO: market reacts on the announcement day itself.
			reactionDay = announceTime
			priorClose, okPrior = dayBeforeClose, okDayBefore
		} else {
			// AMC: market reacts the next working day.
			// Use announcement-day close as baseline (stock closed before earnings came out).
			reactionDay = nextDay
			if announceDayClose > 0 {
				priorClose, okPrior = announceDayClose, true
			} else {
				priorClose, okPrior = dayBeforeClose, okDayBefore
			}
		}

		// Skip if reaction day hasn't happened yet (future quarter).
		if reactionDay.After(now) {
			continue
		}
		reactionClose, okReact := closestPrice(prices, reactionDay)
		if !okPrior || !okReact || priorClose == 0 {
			continue
		}
		reactionOpen := openOnDate(prices, reactionDay) // 0 if unavailable

		// Pre-earnings drift: 7 calendar days before announcement → day before announcement.
		var pre7Ret *float64
		var pre7CloseVal float64
		if pre7Close, ok := closestPrice(prices, announceTime.AddDate(0, 0, -7)); ok && pre7Close != 0 {
			v := pctChange(pre7Close, priorClose)
			pre7Ret = &v
			pre7CloseVal = pre7Close
		}

		// Post-earnings drift: reaction day close → 7 calendar days after reaction day.
		// Skips the immediate reaction day (day +1 after announcement).
		var post7Ret *float64
		var post7CloseVal float64
		post7Target := reactionDay.AddDate(0, 0, 7)
		if !post7Target.After(now) {
			if post7Close, ok := closestPrice(prices, post7Target); ok && post7Close != 0 {
				v := pctChange(reactionClose, post7Close)
				post7Ret = &v
				post7CloseVal = post7Close
			}
		}

		rxn := EarningsReaction{
			Period:           q.Period,
			AnnouncementDate: q.FilingDate,
			ReactionDay:      reactionDay.Format("2006-01-02"),
			PriorClose:       priorClose,
			ReactionOpen:     reactionOpen,
			ReactionClose:    reactionClose,
			GapRetPct:        pctChange(priorClose, reactionOpen),
			RetPct:           pctChange(priorClose, reactionClose),
			EPSActual:        q.EPS,
			RevenueActual:    q.Revenue,
			Pre7Ret:          pre7Ret,
			Pre7Close:        pre7CloseVal,
			Post7Ret:         post7Ret,
			Post7Close:       post7CloseVal,
		}
		// VIX on reaction day.
		if vix, ok := closestPrice(vixPrices, reactionDay); ok && vix > 0 {
			rxn.VIX = vix
		}
		// Macro events near this announcement.
		if macro != nil {
			nearby := macro.EventsNear(q.FilingDate, 2)
			rxn.MacroContext = FormatMacroContext(nearby, q.FilingDate)
		}
		s.EarningsReactions = append(s.EarningsReactions, rxn)
	}

	// ── Macro context for upcoming earnings date ──────────────────────────────
	if macro != nil {
		nearby := macro.EventsNear(res.EarningsDate, 2)
		s.MacroContext = FormatMacroContext(nearby, res.EarningsDate)
	}

	// ── Pre-earnings options snapshot ────────────────────────────────────────
	if snap, err := e.fetchOptionsSnapshot(res.Symbol, res.EarningsDate); err == nil {
		// Attach average historical reaction magnitude for direct comparison with EM%.
		if len(s.EarningsReactions) > 0 {
			total := 0.0
			for _, rxn := range s.EarningsReactions {
				total += math.Abs(rxn.RetPct)
			}
			snap.HistAvgAbsRxn = total / float64(len(s.EarningsReactions))
		}
		s.Options = snap
	} else {
		logf("Warning: options data unavailable for %s: %v", res.Symbol, err)
	}

	// ── Enrich reactions with consensus EPS estimates (Nasdaq calendar) ──────
	// Fetch all EPS estimates concurrently since each is a separate API call.
	if len(s.EarningsReactions) > 0 {
		type epsResult struct {
			period   string
			estimate float64
		}
		estCh := make(chan epsResult, len(s.EarningsReactions))
		for _, rxn := range s.EarningsReactions {
			go func(r EarningsReaction) {
				t, err := time.Parse("2006-01-02", r.AnnouncementDate)
				if err != nil {
					estCh <- epsResult{r.Period, 0}
					return
				}
				est, err := e.fetchNasdaqEPSEstimate(res.Symbol, t)
				if err != nil {
					est = 0
				}
				estCh <- epsResult{r.Period, est}
			}(rxn)
		}
		epsEstimates := make(map[string]float64, len(s.EarningsReactions))
		for range s.EarningsReactions {
			er := <-estCh
			if er.estimate != 0 {
				epsEstimates[er.period] = er.estimate
			}
		}
		for i := range s.EarningsReactions {
			rxn := &s.EarningsReactions[i]
			if est, ok := epsEstimates[rxn.Period]; ok {
				rxn.EPSEstimate = est
				v := pctChange(est, rxn.EPSActual)
				rxn.EPSBeatPct = &v
			}
		}
	}

	// ── Signal 1: short interest already in s.Institutional (populated by fetchInstitutionalData) ──

	// ── Signal 2: 52-week high/low position + RSI(14) ────────────────────────
	// Uses the last 252 trading days (≈1 year) from the already-fetched price history.
	yearAgoTarget := now.AddDate(-1, 0, 0)
	var hi52, lo52 float64
	for _, p := range prices {
		if p.Date.Before(yearAgoTarget) {
			continue
		}
		if hi52 == 0 || p.Close > hi52 {
			hi52 = p.Close
		}
		if lo52 == 0 || p.Close < lo52 {
			lo52 = p.Close
		}
	}
	if hi52 > 0 {
		s.Hi52 = hi52
		v := pctChange(hi52, current) // negative: current is below high
		s.PctFrom52Hi = &v
	}
	if lo52 > 0 {
		s.Lo52 = lo52
		v := pctChange(lo52, current) // positive: current is above low
		s.PctFrom52Lo = &v
	}

	// RSI(14): Wilder's smoothed moving average of gains vs losses.
	if len(prices) >= 15 {
		rsi := computeRSI14(prices)
		s.RSI14 = &rsi
	}

	// ── Signal 3: implied move vs historical move ratio ───────────────────────
	if s.Options != nil && s.Options.ExpectedMovePct > 0 && s.Options.HistAvgAbsRxn > 0 {
		v := s.Options.ExpectedMovePct / s.Options.HistAvgAbsRxn
		s.ImpliedVsHistRatio = &v
	}

	// ── Signal 4: earnings beat consistency ──────────────────────────────────
	if len(s.EarningsReactions) > 0 {
		var beats, total int
		var sumBeatPct float64
		for _, rxn := range s.EarningsReactions {
			if rxn.EPSBeatPct == nil {
				continue
			}
			total++
			if *rxn.EPSBeatPct > 0 {
				beats++
			}
			sumBeatPct += *rxn.EPSBeatPct
		}
		if total > 0 {
			br := float64(beats) / float64(total)
			s.BeatRate = &br
			avg := sumBeatPct / float64(total)
			s.AvgBeatPct = &avg
		}
	}

	return s
}

// computeRSI14 computes the 14-period Wilder RSI from a sorted (oldest→newest)
// price slice. Returns 50 if there is insufficient data.
func computeRSI14(prices []pricePoint) float64 {
	const period = 14
	if len(prices) < period+1 {
		return 50
	}
	// Use only the most recent period+1 prices for the seed average,
	// then apply Wilder's smoothing over any remaining bars.
	tail := prices[len(prices)-min(len(prices), period*3):]

	var avgGain, avgLoss float64
	for i := 1; i <= period && i < len(tail); i++ {
		delta := tail[i].Close - tail[i-1].Close
		if delta > 0 {
			avgGain += delta
		} else {
			avgLoss -= delta
		}
	}
	avgGain /= float64(period)
	avgLoss /= float64(period)

	// Wilder smoothing for remaining bars.
	for i := period + 1; i < len(tail); i++ {
		delta := tail[i].Close - tail[i-1].Close
		gain, loss := 0.0, 0.0
		if delta > 0 {
			gain = delta
		} else {
			loss = -delta
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
	}

	if avgLoss == 0 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}

// fetchForwardEPS calls the Nasdaq analyst earnings-forecast endpoint.
func (e *Enricher) fetchForwardEPS(symbol string) ([]ForwardQuarter, error) {
	url := fmt.Sprintf(
		"https://api.nasdaq.com/api/analyst/%s/earnings-forecast?assetClass=stocks",
		symbol,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://www.nasdaq.com/")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)[:min(80, len(body))])
	}

	var raw nasdaqForecastResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	var out []ForwardQuarter
	for _, r := range raw.Data.QuarterlyForecast.Rows {
		out = append(out, ForwardQuarter{
			FiscalEnd:         r.FiscalEnd,
			ConsensusEPS:      r.ConsensusEPSForecast,
			HighEPS:           r.HighEPSForecast,
			LowEPS:            r.LowEPSForecast,
			NumberOfEstimates: r.NoOfEstimates,
		})
	}
	return out, nil
}

// fetchPriceHistory fetches ~18 months of daily closing prices from the Nasdaq historical API.
// 18 months is needed to cover the full 4-quarter reaction window: the oldest of the last
// 4 reported quarters can have an announcement ~15 months ago, plus a 7-day pre-earnings
// lookback, plus a small buffer. For example, LULU reports Q3 results in early December
// every year; without 18 months the prior December's announcement falls outside the window.
//
// The Nasdaq API silently caps results at ~300 rows regardless of the limit parameter.
// With 18 months (~390 trading days) a single call would drop the oldest ~90 days of data.
// To work around this, two sequential calls cover 9-month halves, then results are merged.
// Returns a slice sorted oldest → newest.
func (e *Enricher) fetchPriceHistory(symbol string) ([]pricePoint, error) {
	now := time.Now()
	mid := now.AddDate(0, -9, 0)   // 9 months ago
	old := now.AddDate(-1, -9, -7) // 21 months ago (covers oldest quarter + pre7 buffer)

	// Two 9-month windows with a small overlap to avoid gaps around the boundary.
	seg1, err1 := e.fetchPriceHistoryRange(symbol, old, mid.AddDate(0, 0, 14))
	seg2, err2 := e.fetchPriceHistoryRange(symbol, mid.AddDate(0, 0, -7), now)

	if err1 != nil && err2 != nil {
		return nil, fmt.Errorf("both price history calls failed: %v; %v", err1, err2)
	}

	// Merge segments, dedup by date, sort oldest-first.
	seen := make(map[string]bool)
	var merged []pricePoint
	for _, seg := range [][]pricePoint{seg1, seg2} {
		for _, p := range seg {
			key := p.Date.Format("2006-01-02")
			if !seen[key] {
				seen[key] = true
				merged = append(merged, p)
			}
		}
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("no price data returned")
	}
	// Sort oldest → newest.
	for i := 0; i < len(merged)-1; i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[i].Date.After(merged[j].Date) {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}
	return merged, nil
}

// fetchPriceHistoryRange fetches daily closing prices for symbol between from and to.
// Returns oldest-first. The Nasdaq API returns newest-first; this function reverses the order.
func (e *Enricher) fetchPriceHistoryRange(symbol string, from, to time.Time) ([]pricePoint, error) {
	url := fmt.Sprintf(
		"https://api.nasdaq.com/api/quote/%s/historical?assetClass=stocks&fromdate=%s&limit=300&todate=%s&type=1",
		symbol, from.Format("2006-01-02"), to.Format("2006-01-02"),
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://www.nasdaq.com/")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)[:min(80, len(body))])
	}

	var raw nasdaqHistoricalResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	var out []pricePoint
	for _, row := range raw.Data.TradesTable.Rows {
		d, err := time.Parse("01/02/2006", row.Date)
		if err != nil {
			continue
		}
		c := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(row.Close), "$", ""), ",", "")
		price, err := strconv.ParseFloat(c, 64)
		if err != nil || price <= 0 {
			continue
		}
		o := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(row.Open), "$", ""), ",", "")
		openPrice, _ := strconv.ParseFloat(o, 64)
		out = append(out, pricePoint{Date: d, Open: openPrice, Close: price})
	}
	// Nasdaq returns newest-first; reverse to oldest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}


// fetchNasdaqEPSEstimate queries the Nasdaq earnings calendar for a specific date
// and returns the consensus EPS estimate for the given symbol on that date.
// This is used to get the pre-earnings consensus for historical quarters.
func (e *Enricher) fetchNasdaqEPSEstimate(symbol string, date time.Time) (float64, error) {
	url := fmt.Sprintf("https://api.nasdaq.com/api/calendar/earnings?date=%s", date.Format("2006-01-02"))
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.nasdaq.com")
	req.Header.Set("Referer", "https://www.nasdaq.com/")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	var raw struct {
		Data struct {
			Rows []nasdaqRow `json:"rows"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return 0, err
	}

	sym := strings.ToUpper(strings.TrimSpace(symbol))
	for _, row := range raw.Data.Rows {
		if strings.ToUpper(strings.TrimSpace(row.Symbol)) == sym {
			return parseEPS(row.EPSForecastRaw), nil
		}
	}
	return 0, fmt.Errorf("symbol %s not in calendar for %s", symbol, date.Format("2006-01-02"))
}

// parseFiscalQuarterEnd converts a Nasdaq FiscalQuarterEnding string (e.g. "Mar/2026")
// to the last calendar day of that month. Returns false if the string cannot be parsed.
func parseFiscalQuarterEnd(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// Parse as the first day of the month, then advance to the last day.
	t, err := time.Parse("Jan/2006", s)
	if err != nil {
		return time.Time{}, false
	}
	// Last day of the month = first day of the next month minus one day.
	lastDay := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	return lastDay, true
}

