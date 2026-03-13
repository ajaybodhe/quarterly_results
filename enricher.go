package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
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

	// Insider trading (last 3 months)
	Insider *InsiderSummary

	// Institutional ownership (mutual funds, hedge funds — from latest 13F quarter)
	Institutional *InstitutionalData

	// Stock reaction to last 4 quarterly earnings reports
	EarningsReactions []EarningsReaction

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
	ReactionClose    float64 // closing price on reaction day
	RetPct           float64 // pct change: (ReactionClose - PriorClose) / PriorClose * 100
}

// ForwardQuarter holds one future quarter EPS estimate from Nasdaq.
type ForwardQuarter struct {
	FiscalEnd          string
	ConsensusEPS       float64
	HighEPS            float64
	LowEPS             float64
	NumberOfEstimates  int
}

// pricePoint is a single end-of-day closing price.
type pricePoint struct {
	Date  time.Time
	Close float64
}

// nasdaqHistoricalResponse is the raw shape from the Nasdaq historical prices API.
type nasdaqHistoricalResponse struct {
	Data struct {
		TradesTable struct {
			Rows []struct {
				Date  string `json:"date"`  // "MM/DD/YYYY"
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
}

func NewEnricher() *Enricher {
	return &Enricher{
		secClient:  NewSECClient(),
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// EnrichAll fetches financial summaries for all results concurrently (rate-limited).
func (e *Enricher) EnrichAll(results []EarningsResult, calendarRows map[string]nasdaqCalendarRow) map[string]*FinancialSummary {
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

			summary := e.buildSummary(res, calendarRows[res.Symbol])

			mu.Lock()
			out[res.Symbol] = summary
			mu.Unlock()
		}(r)
	}

	wg.Wait()
	return out
}

func (e *Enricher) buildSummary(res EarningsResult, row nasdaqCalendarRow) *FinancialSummary {
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
	// reaction on the next working day after filing vs the day before filing.
	quarters := s.History
	if len(quarters) > 4 {
		quarters = quarters[len(quarters)-4:]
	}
	for _, q := range quarters {
		if q.FilingDate == "" {
			continue
		}
		announceTime, err := time.Parse("2006-01-02", q.FilingDate)
		if err != nil {
			continue
		}
		reactionDay := nextWorkingDay(announceTime)
		// Skip if reaction day hasn't happened yet (future quarter).
		if reactionDay.After(now) {
			continue
		}
		priorClose, okPrior := closestPrice(prices, announceTime.AddDate(0, 0, -1))
		reactionClose, okReact := closestPrice(prices, reactionDay)
		if !okPrior || !okReact || priorClose == 0 {
			continue
		}
		s.EarningsReactions = append(s.EarningsReactions, EarningsReaction{
			Period:           q.Period,
			AnnouncementDate: q.FilingDate,
			ReactionDay:      reactionDay.Format("2006-01-02"),
			PriorClose:       priorClose,
			ReactionClose:    reactionClose,
			RetPct:           pctChange(priorClose, reactionClose),
		})
	}

	return s
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

// fetchPriceHistory fetches ~1 year of daily closing prices from the Nasdaq historical API.
// Returns a slice sorted oldest → newest.
func (e *Enricher) fetchPriceHistory(symbol string) ([]pricePoint, error) {
	to := time.Now()
	from := to.AddDate(-1, 0, -7) // 1 year + 1 week buffer
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
		out = append(out, pricePoint{Date: d, Close: price})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no price data returned")
	}
	// Nasdaq returns newest-first; reverse to oldest-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// closestPrice returns the most recent closing price at or before target.
func closestPrice(points []pricePoint, target time.Time) (float64, bool) {
	var result float64
	found := false
	for _, p := range points {
		if p.Date.After(target) {
			break
		}
		result = p.Close
		found = true
	}
	return result, found
}

// ── Growth helpers ────────────────────────────────────────────────────────────

// pctChange returns (newVal - oldVal) / |oldVal| * 100.
func pctChange(oldVal, newVal float64) float64 {
	if oldVal == 0 {
		return 0
	}
	return (newVal - oldVal) / math.Abs(oldVal) * 100
}

