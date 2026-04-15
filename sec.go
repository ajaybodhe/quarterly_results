package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// QuarterActual holds one quarter of actual reported figures.
type QuarterActual struct {
	PeriodStart string  // YYYY-MM-DD (fiscal quarter start)
	Period      string  // YYYY-MM-DD (fiscal quarter end)
	Revenue     float64 // in USD
	EPS         float64 // diluted EPS
	FilingDate  string  // YYYY-MM-DD when the 10-Q was filed with SEC (proxy for earnings date)
}

// SECClient fetches quarterly financial data from SEC EDGAR's XBRL API.
// No API key required. Rate limit: ≤10 req/s.
// SEC requires a descriptive User-Agent per their policy.
const secUserAgent = "quarterly-results-tool research@example.com"

type SECClient struct {
	httpClient *http.Client
	tickerCIK  map[string]int // upper-case ticker → CIK int
	once       sync.Once
	loadErr    error
}

func NewSECClient() *SECClient {
	return &SECClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// tickerEntry is one entry from SEC company_tickers.json.
type tickerEntry struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
}

// LoadTickerMap fetches the SEC ticker→CIK mapping (called once, result cached).
func (c *SECClient) LoadTickerMap() error {
	c.once.Do(func() {
		req, _ := http.NewRequest("GET", "https://www.sec.gov/files/company_tickers.json", nil)
		req.Header.Set("User-Agent", secUserAgent)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			c.loadErr = fmt.Errorf("SEC ticker map fetch: %w", err)
			return
		}
		defer resp.Body.Close()

		var raw map[string]tickerEntry
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			c.loadErr = fmt.Errorf("SEC ticker map decode: %w", err)
			return
		}
		c.tickerCIK = make(map[string]int, len(raw))
		for _, e := range raw {
			c.tickerCIK[strings.ToUpper(e.Ticker)] = e.CIK
		}
	})
	return c.loadErr
}

// FetchQuarterlyActuals returns the last 5 quarters of EPS (diluted) and revenue
// for a stock symbol, sourced directly from SEC XBRL filings.
func (c *SECClient) FetchQuarterlyActuals(symbol string) ([]QuarterActual, error) {
	cik, ok := c.tickerCIK[strings.ToUpper(symbol)]
	if !ok {
		return nil, fmt.Errorf("symbol %s not in SEC ticker map", symbol)
	}

	// Fetch EPS and revenue concurrently.
	type conceptResult struct {
		data         map[string]float64
		filingDates  map[string]string // period-end → filing date
		periodStarts map[string]string // period-end → period start date
		err          error
	}
	epsCh := make(chan conceptResult, 1)
	revCh := make(chan conceptResult, 1)

	go func() {
		d, fd, ps, err := c.fetchConcept(cik, "EarningsPerShareDiluted")
		epsCh <- conceptResult{d, fd, ps, err}
	}()
	go func() {
		// Revenue concept names differ by sector/reporting standard. Try in order.
		for _, name := range []string{
			"Revenues",
			"RevenueFromContractWithCustomerExcludingAssessedTax",
			"RevenueFromContractWithCustomerIncludingAssessedTax",
			"SalesRevenueNet",
			"SalesRevenueGoodsNet",
		} {
			d, fd, ps, err := c.fetchConcept(cik, name)
			if err == nil && len(d) > 0 {
				revCh <- conceptResult{d, fd, ps, nil}
				return
			}
		}
		revCh <- conceptResult{nil, nil, nil, fmt.Errorf("no revenue concept found")}
	}()

	epsRes := <-epsCh
	revRes := <-revCh

	if epsRes.err != nil && revRes.err != nil {
		return nil, fmt.Errorf("no SEC data: %v | %v", epsRes.err, revRes.err)
	}

	// Merge EPS and revenue by period-end date.
	// Use EPS filing dates as authoritative (EPS is always present in the 10-Q).
	merged := make(map[string]*QuarterActual)
	for period, v := range epsRes.data {
		if _, ok := merged[period]; !ok {
			merged[period] = &QuarterActual{Period: period}
		}
		merged[period].EPS = v
		if fd, ok := epsRes.filingDates[period]; ok {
			merged[period].FilingDate = fd
		}
		if ps, ok := epsRes.periodStarts[period]; ok {
			merged[period].PeriodStart = ps
		}
	}
	for period, v := range revRes.data {
		if _, ok := merged[period]; !ok {
			merged[period] = &QuarterActual{Period: period}
		}
		merged[period].Revenue = v
		// Fill FilingDate and PeriodStart from revenue if EPS didn't provide them.
		if merged[period].FilingDate == "" {
			if fd, ok := revRes.filingDates[period]; ok {
				merged[period].FilingDate = fd
			}
		}
		if merged[period].PeriodStart == "" {
			if ps, ok := revRes.periodStarts[period]; ok {
				merged[period].PeriodStart = ps
			}
		}
	}

	var sorted []QuarterActual
	for _, q := range merged {
		sorted = append(sorted, *q)
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Period < sorted[j].Period
	})
	// Keep 9 quarters: 5 to display + 4 prior-year quarters needed for YoY comparison.
	if len(sorted) > 9 {
		sorted = sorted[len(sorted)-9:]
	}
	return sorted, nil
}

// secConceptEntry is one data point from the XBRL concept API.
type secConceptEntry struct {
	Start  string  `json:"start"`  // fiscal period start date: "2024-07-01"
	End    string  `json:"end"`    // fiscal period end date: "2024-09-28"
	Form   string  `json:"form"`   // "10-Q" or "10-K"
	Filed  string  `json:"filed"`  // filing date: "2024-11-01"
	Val    float64 `json:"val"`
}

// fetchConcept fetches quarterly values for one SEC XBRL concept.
// Returns: values map (period-end → value), filing dates map (period-end → filed date),
// period starts map (period-end → period start date).
func (c *SECClient) fetchConcept(cik int, concept string) (map[string]float64, map[string]string, map[string]string, error) {
	url := fmt.Sprintf(
		"https://data.sec.gov/api/xbrl/companyconcept/CIK%010d/us-gaap/%s.json",
		cik, concept,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	req.Header.Set("User-Agent", secUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, nil, fmt.Errorf("concept %s not found (404)", concept)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)[:min(80, len(body))])
	}

	var raw struct {
		Units map[string][]secConceptEntry `json:"units"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, nil, nil, fmt.Errorf("decode: %w", err)
	}

	// Use the first unit key (USD for revenue, USD/shares for EPS).
	result := make(map[string]float64)
	filingDates  := make(map[string]string) // period-end → most recent filed date
	periodStarts := make(map[string]string) // period-end → period start date

	for _, entries := range raw.Units {
		for _, e := range entries {
			if e.Form != "10-Q" && e.Form != "10-K" {
				continue // skip non-quarterly forms (8-K, etc.)
			}
			// Skip YTD (cumulative) entries: only keep single-quarter periods.
			// A fiscal quarter spans 75–105 days; YTD entries span 150+ days.
			if e.Start != "" && e.End != "" {
				start, err1 := time.Parse("2006-01-02", e.Start)
				end, err2 := time.Parse("2006-01-02", e.End)
				if err1 == nil && err2 == nil {
					days := int(end.Sub(start).Hours() / 24)
					if days < 75 || days > 105 {
						continue
					}
				}
			}
			// Only accept filings where the filing date is within 150 days of the period end.
			// This prevents comparative prior-year data included in a later 10-Q from
			// overwriting the original filing date (e.g. LULU's Dec 2025 10-Q contains
			// tagged comparative data for Oct 2024, which would otherwise corrupt the date).
			endDate, endErr := time.Parse("2006-01-02", e.End)
			filedDate, filedErr := time.Parse("2006-01-02", e.Filed)
			if endErr == nil && filedErr == nil {
				if int(filedDate.Sub(endDate).Hours()/24) > 150 {
					continue // comparative re-filing; ignore
				}
			}
			// Among valid filings, keep the most recently filed (handles amendments within window).
			if prev, ok := filingDates[e.End]; !ok || e.Filed > prev {
				result[e.End] = e.Val
				filingDates[e.End] = e.Filed
				if e.Start != "" {
					periodStarts[e.End] = e.Start
				}
			}
		}
		break // only process the first unit type
	}

	return result, filingDates, periodStarts, nil
}

// ── Insider trading (Form 4) ──────────────────────────────────────────────────

// InsiderSummary holds aggregated open-market insider transaction data.
type InsiderSummary struct {
	BuyShares   int64
	BuyValue    float64 // USD total of open-market purchases
	SellShares  int64
	SellValue   float64 // USD total of open-market sales
	FilingCount int     // number of Form 4 filings processed
	Activity    string  // "Net Buyer" / "Net Seller" / "No Activity"
}

// secSubmissionsResponse is the relevant slice of the SEC submissions JSON.
type secSubmissionsResponse struct {
	Filings struct {
		Recent struct {
			Form            []string `json:"form"`
			FilingDate      []string `json:"filingDate"`
			AccessionNumber []string `json:"accessionNumber"`
			PrimaryDocument []string `json:"primaryDocument"`
		} `json:"recent"`
	} `json:"filings"`
}

// form4Doc is the XML structure of an SEC Form 4 filing.
type form4Doc struct {
	XMLName            xml.Name `xml:"ownershipDocument"`
	NonDerivativeTable struct {
		Transactions []form4Txn `xml:"nonDerivativeTransaction"`
	} `xml:"nonDerivativeTable"`
}

type form4Txn struct {
	Coding struct {
		Code string `xml:"transactionCode"`
	} `xml:"transactionCoding"`
	Amounts struct {
		Shares struct{ Value string `xml:"value"` } `xml:"transactionShares"`
		Price  struct{ Value string `xml:"value"` } `xml:"transactionPricePerShare"`
	} `xml:"transactionAmounts"`
}

// fetchSubmissions fetches the SEC submissions JSON for a given CIK.
// This contains all recent filings (Form 4, 8-K, 10-Q, etc.).
func (c *SECClient) fetchSubmissions(cik int) (*secSubmissionsResponse, error) {
	subsURL := fmt.Sprintf("https://data.sec.gov/submissions/CIK%010d.json", cik)
	req, _ := http.NewRequest("GET", subsURL, nil)
	req.Header.Set("User-Agent", secUserAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("submissions fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("submissions HTTP %d", resp.StatusCode)
	}
	var subs secSubmissionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&subs); err != nil {
		return nil, fmt.Errorf("submissions decode: %w", err)
	}
	return &subs, nil
}

// FetchEarningsAnnouncementDates returns a map of period-end-date → 8-K filing date
// for the given quarters. Companies file an 8-K (Item 2.02) the same day they
// release earnings, which precedes the 10-Q by several days. The 8-K date is the
// correct proxy for when the market first saw the results.
func (c *SECClient) FetchEarningsAnnouncementDates(symbol string, quarters []QuarterActual) (map[string]string, error) {
	cik, ok := c.tickerCIK[strings.ToUpper(symbol)]
	if !ok {
		return nil, fmt.Errorf("symbol %s not in SEC ticker map", symbol)
	}
	subs, err := c.fetchSubmissions(cik)
	if err != nil {
		return nil, err
	}

	// Collect original 8-K filing dates only (not 8-K/A amendments, which can be dated
	// months after the original and would corrupt the window lookup).
	var eightKDates []string
	r := subs.Filings.Recent
	for i, form := range r.Form {
		if form == "8-K" {
			eightKDates = append(eightKDates, r.FilingDate[i])
		}
	}
	sort.Strings(eightKDates) // oldest → newest

	result := make(map[string]string)
	for _, q := range quarters {
		if q.FilingDate == "" || q.Period == "" {
			continue
		}
		// Earnings 8-Ks are filed BEFORE (or on the same day as) the 10-Q.
		// Using filingDate+2 as windowEnd could pick up post-filing 8-Ks (investor
		// events, executive changes) filed in the days after the 10-Q, causing two
		// quarters to share the same wrong announcement date.
		windowStart := dateAddDays(q.Period, 14)
		windowEnd := q.FilingDate // 8-K must be filed on or before the 10-Q

		// Take the last 8-K in the window (closest to 10-Q filing = earnings release).
		var best string
		for _, d := range eightKDates {
			if d >= windowStart && d <= windowEnd {
				best = d
			}
		}
		if best != "" {
			result[q.Period] = best
		}
	}
	return result, nil
}

// dateAddDays adds n days to a YYYY-MM-DD string, returning the result as YYYY-MM-DD.
func dateAddDays(dateStr string, n int) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	return t.AddDate(0, 0, n).Format("2006-01-02")
}

// FetchInsiderActivity returns open-market buy/sell activity from Form 4 filings
// filed on or after `since`.
func (c *SECClient) FetchInsiderActivity(symbol string, since time.Time) (*InsiderSummary, error) {
	cik, ok := c.tickerCIK[strings.ToUpper(symbol)]
	if !ok {
		return nil, fmt.Errorf("symbol %s not in SEC ticker map", symbol)
	}

	// 1. Fetch submissions JSON to get list of recent Form 4 filings.
	subs, err := c.fetchSubmissions(cik)
	if err != nil {
		return nil, err
	}

	// 2. Filter to Form 4 / 4/A filed since the cutoff date.
	cutoff := since.Format("2006-01-02")
	type filing struct{ accNum, docFile string }
	var filings []filing
	r := subs.Filings.Recent
	for i, form := range r.Form {
		if (form == "4" || form == "4/A") && r.FilingDate[i] >= cutoff {
			filings = append(filings, filing{
				accNum:  strings.ReplaceAll(r.AccessionNumber[i], "-", ""),
				docFile: path.Base(r.PrimaryDocument[i]),
			})
		}
	}
	if len(filings) == 0 {
		return &InsiderSummary{Activity: "No Activity"}, nil
	}

	// 3. Fetch and parse each Form 4 XML concurrently (SEC rate limit: ≤10 req/s).
	type txnSummary struct {
		buyShares, sellShares int64
		buyValue, sellValue   float64
	}
	results := make([]txnSummary, len(filings))
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for idx, f := range filings {
		wg.Add(1)
		go func(i int, fil filing) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			xmlURL := fmt.Sprintf(
				"https://www.sec.gov/Archives/edgar/data/%d/%s/%s",
				cik, fil.accNum, fil.docFile,
			)
			xreq, _ := http.NewRequest("GET", xmlURL, nil)
			xreq.Header.Set("User-Agent", secUserAgent)
			xresp, err := c.httpClient.Do(xreq)
			if err != nil {
				return
			}
			defer xresp.Body.Close()
			if xresp.StatusCode != http.StatusOK {
				return
			}
			var doc form4Doc
			if err := xml.NewDecoder(xresp.Body).Decode(&doc); err != nil {
				return
			}
			for _, t := range doc.NonDerivativeTable.Transactions {
				code := strings.TrimSpace(t.Coding.Code)
				if code != "P" && code != "S" {
					continue // ignore awards, exercises, tax-withholding, etc.
				}
				shares, _ := strconv.ParseInt(strings.TrimSpace(t.Amounts.Shares.Value), 10, 64)
				price, _ := strconv.ParseFloat(strings.TrimSpace(t.Amounts.Price.Value), 64)
				value := float64(shares) * price
				if code == "P" {
					results[i].buyShares += shares
					results[i].buyValue += value
				} else {
					results[i].sellShares += shares
					results[i].sellValue += value
				}
			}
		}(idx, f)
	}
	wg.Wait()

	// 4. Aggregate and classify.
	sum := &InsiderSummary{FilingCount: len(filings)}
	for _, r := range results {
		sum.BuyShares += r.buyShares
		sum.BuyValue += r.buyValue
		sum.SellShares += r.sellShares
		sum.SellValue += r.sellValue
	}
	switch {
	case sum.BuyValue > sum.SellValue:
		sum.Activity = "Net Buyer"
	case sum.SellValue > sum.BuyValue:
		sum.Activity = "Net Seller"
	default:
		sum.Activity = "No Activity"
	}
	return sum, nil
}
