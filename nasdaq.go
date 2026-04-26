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

// nasdaqCalendarRow is the parsed version of nasdaqRow with numeric fields resolved.
type nasdaqCalendarRow struct {
	Symbol              string
	Name                string
	FiscalQuarterEnding string
	EPSForecast         float64 // consensus EPS estimate for current quarter
	LastYearEPS         float64 // same quarter last year actual EPS
}

// FetchEarningsCalendar fetches earnings events for every trading day in [from, to].
// Also returns a map of symbol → nasdaqCalendarRow for enrichment use.
// Days are fetched concurrently (up to 10 at a time) and reassembled in date order.
func (c *NasdaqClient) FetchEarningsCalendar(from, to time.Time) ([]EarningsEvent, map[string]nasdaqCalendarRow) {
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

	// Reassemble in chronological order.
	sort.Slice(dayResults, func(i, j int) bool {
		return dayResults[i].date.Before(dayResults[j].date)
	})

	var all []EarningsEvent
	calMap := make(map[string]nasdaqCalendarRow)

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

			calMap[sym] = nasdaqCalendarRow{
				Symbol:              sym,
				Name:                strings.TrimSpace(row.Name),
				FiscalQuarterEnding: row.FiscalQuarterEnding,
				EPSForecast:         parseEPS(row.EPSForecastRaw),
				LastYearEPS:         parseEPS(row.LastYearEPSRaw),
			}
		}
	}

	return all, calMap
}

// parseEPS converts a Nasdaq EPS string to float64.
// Handles formats: "$4.51", "-$0.12", "($0.61)" (accountant negative notation).
func parseEPS(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" || s == "-" {
		return 0
	}
	// Parentheses indicate negative: "($0.61)" → -0.61
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

// fetchDay calls the Nasdaq API for a single date and returns the raw rows.
func (c *NasdaqClient) fetchDay(date time.Time) ([]nasdaqRow, error) {
	url := fmt.Sprintf("%s?date=%s", nasdaqCalendarURL, date.Format("2006-01-02"))

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	// Nasdaq requires browser-like headers; without them the request is rejected.
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
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)[:min(200, len(body))])
	}

	var raw struct {
		Data struct {
			Rows []nasdaqRow `json:"rows"`
		} `json:"data"`
		Status struct {
			RCode   int    `json:"rCode"`
			BCodeMessage string `json:"bCodeMessage"`
		} `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return raw.Data.Rows, nil
}

// normalizeNasdaqTime converts Nasdaq's time field to "bmo", "amc", or "".
func normalizeNasdaqTime(t string) string {
	switch t {
	case "time-pre-market":
		return "bmo"
	case "time-after-hours":
		return "amc"
	default: // "time-not-supplied" or anything else
		return ""
	}
}

// parseMarketCap parses a Nasdaq market cap string like "$84,902,848,858" into a float64.
func parseMarketCap(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0, nil
	}
	// Remove $ and commas
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	return strconv.ParseFloat(s, 64)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
