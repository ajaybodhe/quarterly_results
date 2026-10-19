package nasdaq

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/logging"
	"quarterly_results/internal/source/yahoo"
)

const nasdaqCalendarURL = "https://api.nasdaq.com/api/calendar/earnings"

// setNasdaqBrowserHeaders installs a full Chrome-like header set on the
// request. api.nasdaq.com is fronted by Akamai's Bot Manager, which scores
// requests using both JA3 TLS fingerprint and header profile. A minimal set
// (UA + Accept + Origin + Referer) has been observed to hard-fail with a
// 403 "Access Denied" HTML page even when the same URL succeeds from curl,
// because Bot Manager expects the modern Client Hints and Sec-Fetch-*
// headers a real Chrome sends. Setting them here nudges the score below
// the block threshold.
func setNasdaqBrowserHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Intentionally leave Accept-Encoding unset — Go's Transport adds "gzip"
	// automatically and transparently decompresses, which only happens when
	// the caller has not set the header explicitly.
	req.Header.Set("Origin", "https://www.nasdaq.com")
	req.Header.Set("Referer", "https://www.nasdaq.com/")
	req.Header.Set("Sec-Ch-Ua", `"Not_A Brand";v="8", "Chromium";v="120", "Google Chrome";v="120"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"macOS"`)
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Connection", "keep-alive")
}

// nasdaqRetryBackoffs is the retry schedule used by nasdaqGetJSON on WAF
// responses (403, 429, or 200-with-HTML challenge). Package-level so tests
// can shrink it.
var nasdaqRetryBackoffs = []time.Duration{500 * time.Millisecond, 1 * time.Second, 2 * time.Second}

// nasdaqMinInterval is the minimum spacing between successive api.nasdaq.com
// requests. Every caller passes through nasdaqSerializer, so this puts a
// hard cap on how bursty we look to Akamai — parallel goroutines fan into
// a single stream paced this many ms apart. Package-level so tests can
// zero it out.
var nasdaqMinInterval = 300 * time.Millisecond

var (
	nasdaqSerializer sync.Mutex
	nasdaqLastCall   time.Time
)

// waitForNasdaqSlot blocks until enough time has passed since the previous
// call to release the next one. Caller must hold nasdaqSerializer.
func waitForNasdaqSlot() {
	if nasdaqLastCall.IsZero() {
		return
	}
	gap := time.Since(nasdaqLastCall)
	if gap < nasdaqMinInterval {
		time.Sleep(nasdaqMinInterval - gap)
	}
}

// nasdaqGetJSON does a GET against api.nasdaq.com with browser headers and
// retry-on-403/429. `dest` is the JSON destination for the decoded body
// (must be a pointer). On a non-OK status the response body is read into an
// error string (truncated) so the caller can distinguish WAF blocks
// ("HTTP 403 (WAF)") from real errors. Retries: 3 attempts with jittered
// backoff at 500ms, 1s, 2s.
//
// All Nasdaq calls funnel through this function under a package-level
// serializer with nasdaqMinInterval spacing — the fan-in prevents the
// parallel enrichment goroutines from firing bursts that trip the WAF.
func nasdaqGetJSON(client *http.Client, url string, dest interface{}) error {
	nasdaqSerializer.Lock()
	defer nasdaqSerializer.Unlock()
	waitForNasdaqSlot()
	defer func() { nasdaqLastCall = time.Now() }()

	backoffs := nasdaqRetryBackoffs
	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		if attempt > 0 {
			jitter := time.Duration(rand.Int63n(int64(300 * time.Millisecond)))
			time.Sleep(backoffs[attempt-1] + jitter)
		}
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return err
		}
		setNasdaqBrowserHeaders(req)

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("HTTP GET: %w", err)
			continue
		}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d (WAF)", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			n := len(body)
			if n > 120 {
				n = 120
			}
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:n]))
		}
		// Akamai's Bot Manager also returns HTTP 200 with a text/html challenge
		// page (UTF-8-BOM-prefixed <html>...) instead of a 403 for some
		// requests. Treat any non-JSON content-type as a WAF block and retry.
		ct := resp.Header.Get("Content-Type")
		if !strings.Contains(ct, "json") {
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP 200 non-JSON (WAF challenge): content-type=%s", ct)
			continue
		}
		err = json.NewDecoder(resp.Body).Decode(dest)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return nil
	}
	return lastErr
}

// Client fetches earnings data from Nasdaq's public calendar API.
// No API key required; standard browser headers are needed.
type Client struct {
	httpClient *http.Client
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewClientWithHTTP returns a Client that uses the given http.Client.
func NewClientWithHTTP(client *http.Client) *Client {
	return &Client{httpClient: client}
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
	SurpriseRaw         string `json:"surprise"`    // past dates only, e.g. "3.37" (%)
	EPSRaw              string `json:"eps"`         // past dates only: reported (street) EPS, e.g. "$4.60"
	NoOfEstsRaw         string `json:"noOfEsts"`
}

// FetchEarningsCalendar implements CalendarProvider.
// It fetches earnings events for every trading day in [from, to] and returns
// both the flat event list and a symbol→CalendarRow map for enrichment.
func (c *Client) FetchEarningsCalendar(from, to time.Time) ([]domain.EarningsEvent, map[string]domain.CalendarRow, error) {
	type dayResult struct {
		date time.Time
		rows []nasdaqRow
	}

	var dayResults []dayResult

	// The per-request pacing / retries / WAF handling live in nasdaqGetJSON,
	// which serializes all Nasdaq calls through a single mutex with a
	// minimum interval. The loop here just walks trading days.
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			continue
		}
		rows, err := c.fetchDay(d)
		if err != nil {
			logging.Logf("Warning: failed to fetch earnings for %s: %v", d.Format("2006-01-02"), err)
			continue
		}
		dayResults = append(dayResults, dayResult{d, rows})
	}

	sort.Slice(dayResults, func(i, j int) bool {
		return dayResults[i].date.Before(dayResults[j].date)
	})

	var all []domain.EarningsEvent
	calMap := make(map[string]domain.CalendarRow)

	for _, dr := range dayResults {
		for _, row := range dr.rows {
			sym := strings.TrimSpace(row.Symbol)
			mc, _ := parseMarketCap(row.MarketCap)

			all = append(all, domain.EarningsEvent{
				Symbol:    sym,
				Date:      dr.date.Format("2006-01-02"),
				Time:      normalizeNasdaqTime(row.Time),
				MarketCap: mc,
				Name:      strings.TrimSpace(row.Name),
			})

			calMap[sym] = domain.CalendarRow{
				Symbol:              sym,
				Name:                strings.TrimSpace(row.Name),
				FiscalQuarterEnding: row.FiscalQuarterEnding,
				EPSForecast:         parseEPS(row.EPSForecastRaw),
				LastYearEPS:         parseEPS(row.LastYearEPSRaw),
				EPSSurprisePct:      parseSurprise(row.SurpriseRaw),
			}
		}
	}

	return all, calMap, nil
}

// parseEPS converts a Nasdaq EPS string to float64.
// Handles: "$4.51", "-$0.12", "($0.61)" (accountant negative notation).
// ReportedEarnings is one past-date calendar row: the consensus going in and
// what the company reported, on the street (adjusted) basis the market trades.
type ReportedEarnings struct {
	Symbol      string   `json:"symbol"`
	Time        string   `json:"time"` // bmo / amc / ""
	EPSActual   *float64 `json:"eps_actual,omitempty"`
	EPSForecast float64  `json:"eps_forecast"`
	SurprisePct *float64 `json:"surprise_pct,omitempty"`
	Estimates   int      `json:"estimates"`
}

// FetchReported returns every calendar row for one date (consensus, actual
// and surprise are filled once the date has reported).
func (c *Client) FetchReported(date time.Time) ([]ReportedEarnings, error) {
	rows, err := c.fetchDay(date)
	if err != nil {
		return nil, err
	}
	out := make([]ReportedEarnings, 0, len(rows))
	for _, r := range rows {
		re := ReportedEarnings{
			Symbol:      strings.ToUpper(strings.TrimSpace(r.Symbol)),
			Time:        normalizeNasdaqTime(r.Time),
			EPSForecast: parseEPS(r.EPSForecastRaw),
			SurprisePct: parseSurprise(r.SurpriseRaw),
		}
		if v := strings.TrimSpace(r.EPSRaw); v != "" && v != "N/A" && v != "-" {
			eps := parseEPS(v)
			re.EPSActual = &eps
		}
		re.Estimates, _ = strconv.Atoi(strings.TrimSpace(r.NoOfEstsRaw))
		out = append(out, re)
	}
	return out, nil
}

// parseSurprise parses Nasdaq's "% Surprise" column ("3.37", "-12.5", "N/A",
// ""), returning nil when the date hasn't reported or the value is missing.
func parseSurprise(s string) *float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	if s == "" || s == "N/A" || s == "-" {
		return nil
	}
	v, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64)
	if err != nil {
		return nil
	}
	return &v
}

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

func (c *Client) fetchDay(date time.Time) ([]nasdaqRow, error) {
	url := fmt.Sprintf("%s?date=%s", nasdaqCalendarURL, date.Format("2006-01-02"))
	var raw struct {
		Data struct {
			Rows []nasdaqRow `json:"rows"`
		} `json:"data"`
	}
	if err := nasdaqGetJSON(c.httpClient, url, &raw); err != nil {
		return nil, err
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

// ── PriceProvider ─────────────────────────────────────────────────────────────

// PriceProvider implements PriceProvider for US stocks via the Nasdaq historical API.
type PriceProvider struct {
	httpClient *http.Client
}

func NewPriceProvider(client *http.Client) *PriceProvider {
	return &PriceProvider{httpClient: client}
}

// FetchPriceHistory returns ~2 years of daily OHLC data for symbol,
// oldest→newest.
//
// Historically this hit Nasdaq's /api/quote/{sym}/historical endpoint via
// two parallel 9-month calls to work around a ~300-row cap. That endpoint
// sits behind the same Akamai Bot Manager as the calendar and — because
// the parallel fan-out is 2×N-qualifying-stocks bursts — was the biggest
// contributor to IP bans. Yahoo's chart API returns ~500 daily bars in
// one call for US tickers with no rate limits, so we delegate here and
// keep our Nasdaq footprint to the calendar plus per-date EPS lookups.
func (p *PriceProvider) FetchPriceHistory(symbol string) ([]domain.PricePoint, error) {
	return yahoo.FetchChartHistory(p.httpClient, symbol)
}

// FetchHistoricalEPSEstimate returns the consensus EPS estimate for symbol on the given date
// by querying the Nasdaq earnings calendar for that specific date.
func (p *PriceProvider) FetchHistoricalEPSEstimate(symbol string, date time.Time) (float64, error) {
	url := fmt.Sprintf("https://api.nasdaq.com/api/calendar/earnings?date=%s", date.Format("2006-01-02"))
	var raw struct {
		Data struct {
			Rows []nasdaqRow `json:"rows"`
		} `json:"data"`
	}
	if err := nasdaqGetJSON(p.httpClient, url, &raw); err != nil {
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

// FetchForwardEPS calls the Nasdaq analyst earnings-forecast endpoint.
func FetchForwardEPS(client *http.Client, symbol string) ([]domain.ForwardQuarter, error) {
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

	resp, err := client.Do(req)
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

	var raw nasdaqForecastResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	var out []domain.ForwardQuarter
	for _, r := range raw.Data.QuarterlyForecast.Rows {
		out = append(out, domain.ForwardQuarter{
			FiscalEnd:         r.FiscalEnd,
			ConsensusEPS:      r.ConsensusEPSForecast,
			HighEPS:           r.HighEPSForecast,
			LowEPS:            r.LowEPSForecast,
			NumberOfEstimates: r.NoOfEstimates,
		})
	}
	return out, nil
}
