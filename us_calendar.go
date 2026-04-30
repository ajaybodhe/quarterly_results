package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const nasdaqCalendarURL = "https://api.nasdaq.com/api/calendar/earnings"

// NasdaqClient fetches earnings data from Nasdaq's public calendar API.
// No API key required; standard browser headers are needed.
type NasdaqClient struct {
	httpClient *http.Client
}

func NewNasdaqClient() *NasdaqClient {
	return &NasdaqClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// nasdaqRow is one row from the Nasdaq earnings calendar API response.
type nasdaqRow struct {
	Symbol              string `json:"symbol"`
	Name                string `json:"name"`
	MarketCap           string `json:"marketCap"`           // e.g. "$84,902,848,858"
	Time                string `json:"time"`                // "time-pre-market", "time-after-hours", "time-not-supplied"
	FiscalQuarterEnding string `json:"fiscalQuarterEnding"` // e.g. "Feb/2026"
	EPSForecastRaw      string `json:"epsForecast"`
	LastYearRptDt       string `json:"lastYearRptDt"`
	LastYearEPSRaw      string `json:"lastYearEPS"` // e.g. "$4.51" or "-$0.12"
}

// CalendarRow is the parsed earnings calendar entry used throughout the enrichment pipeline.
// (Formerly nasdaqCalendarRow.)
type CalendarRow struct {
	Symbol              string
	Name                string
	FiscalQuarterEnding string
	EPSForecast         float64 // consensus EPS estimate for the current quarter
	LastYearEPS         float64 // same quarter last year actual EPS
}

// FetchEarningsCalendar implements CalendarProvider.
// It fetches earnings events for every trading day in [from, to] and returns
// both the flat event list and a symbol→CalendarRow map for enrichment.
func (c *NasdaqClient) FetchEarningsCalendar(from, to time.Time) ([]EarningsEvent, map[string]CalendarRow, error) {
	type dayResult struct {
		date time.Time
		rows []nasdaqRow
	}

	var (
		dayResults []dayResult
		mu         sync.Mutex
		wg         sync.WaitGroup
	)
	sem := make(chan struct{}, 10)

	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		wg.Add(1)
		go func(date time.Time) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			rows, err := c.fetchDay(date)
			if err != nil {
				logf("Warning: failed to fetch earnings for %s: %v", date.Format("2006-01-02"), err)
				return
			}
			mu.Lock()
			dayResults = append(dayResults, dayResult{date, rows})
			mu.Unlock()
		}(d)
	}
	wg.Wait()

	sort.Slice(dayResults, func(i, j int) bool {
		return dayResults[i].date.Before(dayResults[j].date)
	})

	var all []EarningsEvent
	calMap := make(map[string]CalendarRow)

	for _, dr := range dayResults {
		for _, row := range dr.rows {
			sym := strings.TrimSpace(row.Symbol)
			mc, _ := parseMarketCap(row.MarketCap)

			all = append(all, EarningsEvent{
				Symbol:    sym,
				Date:      dr.date.Format("2006-01-02"),
				Time:      normalizeNasdaqTime(row.Time),
				MarketCap: mc,
				Name:      strings.TrimSpace(row.Name),
			})

			calMap[sym] = CalendarRow{
				Symbol:              sym,
				Name:                strings.TrimSpace(row.Name),
				FiscalQuarterEnding: row.FiscalQuarterEnding,
				EPSForecast:         parseEPS(row.EPSForecastRaw),
				LastYearEPS:         parseEPS(row.LastYearEPSRaw),
			}
		}
	}

	return all, calMap, nil
}

// parseEPS converts a Nasdaq EPS string to float64.
// Handles: "$4.51", "-$0.12", "($0.61)" (accountant negative notation).
func parseEPS(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" || s == "-" {
		return 0
	}
	neg := strings.HasPrefix(s, "-") || (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")"))
	s = strings.Trim(s, "()")
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	if neg {
		return -v
	}
	return v
}

func (c *NasdaqClient) fetchDay(date time.Time) ([]nasdaqRow, error) {
	url := fmt.Sprintf("%s?date=%s", nasdaqCalendarURL, date.Format("2006-01-02"))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.nasdaq.com")
	req.Header.Set("Referer", "https://www.nasdaq.com/")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		n := len(body)
		if n > 200 {
			n = 200
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:n]))
	}

	var raw struct {
		Data struct {
			Rows []nasdaqRow `json:"rows"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return raw.Data.Rows, nil
}

// normalizeNasdaqTime converts Nasdaq's timing field to "bmo", "amc", or "".
func normalizeNasdaqTime(t string) string {
	switch t {
	case "time-pre-market":
		return "bmo"
	case "time-after-hours":
		return "amc"
	default:
		return ""
	}
}

// parseMarketCap parses a Nasdaq market cap string like "$84,902,848,858" into float64.
func parseMarketCap(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0, nil
	}
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	return strconv.ParseFloat(s, 64)
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

// ── NasdaqPriceProvider ───────────────────────────────────────────────────────

// NasdaqPriceProvider implements PriceProvider for US stocks via the Nasdaq historical API.
type NasdaqPriceProvider struct {
	httpClient *http.Client
}

// FetchPriceHistory returns ~18 months of daily OHLC data for symbol, oldest→newest.
// Two parallel 9-month Nasdaq API calls are merged to work around the ~300-row cap.
func (p *NasdaqPriceProvider) FetchPriceHistory(symbol string) ([]pricePoint, error) {
	now := time.Now()
	mid := now.AddDate(0, -9, 0)
	old := now.AddDate(-1, -9, -7)

	var (
		seg1, seg2 []pricePoint
		err1, err2 error
		segWg      sync.WaitGroup
	)
	segWg.Add(2)
	go func() { defer segWg.Done(); seg1, err1 = p.fetchRange(symbol, old, mid.AddDate(0, 0, 14)) }()
	go func() { defer segWg.Done(); seg2, err2 = p.fetchRange(symbol, mid.AddDate(0, 0, -7), now) }()
	segWg.Wait()

	if err1 != nil && err2 != nil {
		return nil, fmt.Errorf("both price history calls failed: %v; %v", err1, err2)
	}

	seen := make(map[string]bool)
	var merged []pricePoint
	for _, seg := range [][]pricePoint{seg1, seg2} {
		for _, pp := range seg {
			key := pp.Date.Format("2006-01-02")
			if !seen[key] {
				seen[key] = true
				merged = append(merged, pp)
			}
		}
	}
	if len(merged) == 0 {
		return nil, fmt.Errorf("no price data returned")
	}
	sort.Slice(merged, func(i, j int) bool { return merged[i].Date.Before(merged[j].Date) })
	return merged, nil
}

func (p *NasdaqPriceProvider) fetchRange(symbol string, from, to time.Time) ([]pricePoint, error) {
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

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		n := len(body)
		if n > 80 {
			n = 80
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:n]))
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

// FetchHistoricalEPSEstimate returns the consensus EPS estimate for symbol on the given date
// by querying the Nasdaq earnings calendar for that specific date.
func (p *NasdaqPriceProvider) FetchHistoricalEPSEstimate(symbol string, date time.Time) (float64, error) {
	url := fmt.Sprintf("https://api.nasdaq.com/api/calendar/earnings?date=%s", date.Format("2006-01-02"))
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Origin", "https://www.nasdaq.com")
	req.Header.Set("Referer", "https://www.nasdaq.com/")

	resp, err := p.httpClient.Do(req)
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
