package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
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
// SEC requires a descriptive User-Agent with a real contact email per their
// fair-access policy — requests with placeholder emails like "example.com" are
// rate-limited (HTTP 429). Override with the SEC_USER_AGENT env var.
var secUserAgent = func() string {
	if v := os.Getenv("SEC_USER_AGENT"); v != "" {
		return v
	}
	return "quarterly-results-tool ajaybodhe@gmail.com"
}()

type SECClient struct {
	httpClient *http.Client
	tickerCIK  map[string]int // upper-case ticker → CIK int
	mu         sync.Mutex
}

func NewSECClient() *SECClient {
	return &SECClient{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// lookupCIK returns the SEC CIK for the given ticker, or an error if not found.
func (c *SECClient) lookupCIK(symbol string) (int, error) {
	c.mu.Lock()
	cik, ok := c.tickerCIK[strings.ToUpper(symbol)]
	c.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("symbol %s not in SEC ticker map", symbol)
	}
	return cik, nil
}

// tickerEntry is one entry from SEC company_tickers.json.
type tickerEntry struct {
	CIK    int    `json:"cik_str"`
	Ticker string `json:"ticker"`
}

// LoadTickerMap fetches the SEC ticker→CIK mapping.
// It is safe to call concurrently. If a previous call already succeeded, it
// returns immediately. If the previous call failed (transient error), it retries.
// The mapping is also cached on disk (~24h) to avoid hitting SEC on every run;
// the fetch endpoint is aggressively rate-limited by IP.
func (c *SECClient) LoadTickerMap() error {
	c.mu.Lock()
	if c.tickerCIK != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	// Try disk cache first.
	if m, ok := readTickerCache(); ok {
		c.mu.Lock()
		c.tickerCIK = m
		c.mu.Unlock()
		return nil
	}

	req, _ := http.NewRequest("GET", "https://www.sec.gov/files/company_tickers.json", nil)
	req.Header.Set("User-Agent", secUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("SEC ticker map fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode == http.StatusTooManyRequests {
			hint = " (rate-limited by SEC — wait a few minutes, or set SEC_USER_AGENT='your-name your-email@domain')"
		}
		return fmt.Errorf("SEC ticker map HTTP %d%s", resp.StatusCode, hint)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("SEC ticker map read: %w", err)
	}

	var raw map[string]tickerEntry
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("SEC ticker map decode: %w", err)
	}

	m := make(map[string]int, len(raw))
	for _, e := range raw {
		m[strings.ToUpper(e.Ticker)] = e.CIK
	}

	c.mu.Lock()
	c.tickerCIK = m
	c.mu.Unlock()

	writeTickerCache(body)
	return nil
}

// tickerCachePath returns the on-disk location for the ticker map cache.
func tickerCachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return path.Join(dir, "quarterly_results", "sec_company_tickers.json")
}

// readTickerCache returns the cached ticker map if the file exists and is
// fresher than 24 hours. Returns (nil, false) on any miss or read error.
func readTickerCache() (map[string]int, bool) {
	p := tickerCachePath()
	info, err := os.Stat(p)
	if err != nil || time.Since(info.ModTime()) > 24*time.Hour {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var raw map[string]tickerEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	m := make(map[string]int, len(raw))
	for _, e := range raw {
		m[strings.ToUpper(e.Ticker)] = e.CIK
	}
	return m, true
}

// writeTickerCache persists the SEC ticker map JSON to disk. Errors are ignored
// — caching is best-effort and a failure just means the next run will refetch.
func writeTickerCache(data []byte) {
	p := tickerCachePath()
	if err := os.MkdirAll(path.Dir(p), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(p, data, 0o644)
}

// FetchQuarterlyActuals returns the last 5 quarters of EPS (diluted) and revenue
// for a stock symbol, sourced directly from SEC XBRL filings.
func (c *SECClient) FetchQuarterlyActuals(symbol string) ([]QuarterActual, error) {
	cik, err := c.lookupCIK(symbol)
	if err != nil {
		return nil, err
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
		// Many companies switched from "Revenues" to "RevenueFromContractWithCustomer..."
		// when ASC 606 took effect (~2018). A concept is only accepted if it has data
		// within the last 18 months — otherwise the old concept name silently wins
		// and hides the current one (e.g. BSX, MCO).
		recentCutoff := time.Now().AddDate(-1, -6, 0)
		for _, name := range []string{
			"Revenues",
			"RevenuesNetOfInterestExpense", // broker-dealers, banks (e.g. IBKR, GS, MS)
			"RevenueFromContractWithCustomerExcludingAssessedTax",
			"RevenueFromContractWithCustomerIncludingAssessedTax",
			"SalesRevenueNet",
			"SalesRevenueGoodsNet",
		} {
			// Broker-dealers and banks: RevenuesNetOfInterestExpense is *net* revenue
			// (gross interest income minus interest expense paid to customers, plus
			// non-interest income). Yahoo Finance and most screeners report the *gross*
			// figure — interest income + non-interest income — which produces a smaller
			// P/S. Compute the gross sum per period and use it when available so our
			// ratio matches what users see on comparison sites.
			if name == "RevenuesNetOfInterestExpense" {
				if d, fd, ps, ok := c.fetchBrokerGrossRevenue(cik, recentCutoff); ok {
					revCh <- conceptResult{d, fd, ps, nil}
					return
				}
			}

			d, fd, ps, err := c.fetchConcept(cik, name)
			if err != nil || len(d) == 0 {
				continue
			}
			// Only use this concept if it has at least one period within 18 months.
			hasRecent := false
			for period := range d {
				t, parseErr := time.Parse("2006-01-02", period)
				if parseErr == nil && t.After(recentCutoff) {
					hasRecent = true
					break
				}
			}
			if hasRecent {
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

	type annualRec struct {
		start  time.Time
		end    time.Time
		filed  string
		val    float64
	}
	var annuals []annualRec

	for _, entries := range raw.Units {
		for _, e := range entries {
			if e.Form != "10-Q" && e.Form != "10-K" {
				continue
			}
			if e.Start == "" || e.End == "" {
				continue
			}
			start, err1 := time.Parse("2006-01-02", e.Start)
			end, err2 := time.Parse("2006-01-02", e.End)
			if err1 != nil || err2 != nil {
				continue
			}
			days := int(end.Sub(start).Hours() / 24)

			// Only accept filings where the filing date is within 150 days of the
			// period end (prevents comparative re-filings from corrupting dates).
			filedDate, ferr := time.Parse("2006-01-02", e.Filed)
			if ferr != nil {
				continue
			}
			if int(filedDate.Sub(end).Hours()/24) > 150 {
				continue
			}

			if days >= 75 && days <= 105 {
				// Single-quarter entry: keep the most recently filed version.
				if prev, ok := filingDates[e.End]; !ok || e.Filed > prev {
					result[e.End] = e.Val
					filingDates[e.End] = e.Filed
					periodStarts[e.End] = e.Start
				}
			} else if days >= 350 && days <= 380 && e.Form == "10-K" {
				// Full-year 10-K entry: collect for Q4 derivation below.
				annuals = append(annuals, annualRec{start, end, e.Filed, e.Val})
			}
		}
		break // only process the first unit type
	}

	// Derive Q4 = Annual − (Q1 + Q2 + Q3) for each fiscal year where Q4 is
	// not already present as a directly-tagged quarterly period.
	// This is necessary for calendar-year reporters (e.g. NFLX, MSFT) whose
	// Q4 appears only as an annual total in the 10-K, never as a 10-Q.
	for _, ann := range annuals {
		yearEnd := ann.end.Format("2006-01-02")
		if _, exists := result[yearEnd]; exists {
			continue // Q4 was explicitly tagged; no derivation needed
		}
		// Find the three quarters (Q1, Q2, Q3) that fall within this fiscal year:
		// their start must be >= fiscal year start and their end must be < fiscal year end.
		var withinYear []struct {
			endDate string
			val     float64
		}
		for endStr, val := range result {
			psStr := periodStarts[endStr]
			if psStr == "" {
				continue
			}
			ps, err1 := time.Parse("2006-01-02", psStr)
			pe, err2 := time.Parse("2006-01-02", endStr)
			if err1 != nil || err2 != nil {
				continue
			}
			if (ps.Equal(ann.start) || ps.After(ann.start)) && pe.Before(ann.end) {
				withinYear = append(withinYear, struct {
					endDate string
					val     float64
				}{endStr, val})
			}
		}
		if len(withinYear) != 3 {
			continue // need exactly Q1+Q2+Q3 to derive Q4
		}

		var sumQ1Q2Q3 float64
		var latestQEnd time.Time
		for _, q := range withinYear {
			sumQ1Q2Q3 += q.val
			qe, _ := time.Parse("2006-01-02", q.endDate)
			if qe.After(latestQEnd) {
				latestQEnd = qe
			}
		}
		result[yearEnd] = ann.val - sumQ1Q2Q3
		filingDates[yearEnd] = ann.filed
		periodStarts[yearEnd] = latestQEnd.AddDate(0, 0, 1).Format("2006-01-02")
	}

	return result, filingDates, periodStarts, nil
}

// fetchBrokerGrossRevenue returns per-quarter gross revenue for broker-dealers/banks,
// computed as InterestIncomeOperating + NoninterestIncome (falling back to
// InterestAndDividendIncomeOperating for banks that use that tag). Only periods
// present in both underlying concepts are returned. `ok` is false if either
// concept is missing, unavailable, or has no period within the recency cutoff.
func (c *SECClient) fetchBrokerGrossRevenue(cik int, recentCutoff time.Time) (map[string]float64, map[string]string, map[string]string, bool) {
	var ii map[string]float64
	var iiFD, iiPS map[string]string
	for _, name := range []string{"InterestIncomeOperating", "InterestAndDividendIncomeOperating"} {
		d, fd, ps, err := c.fetchConcept(cik, name)
		if err == nil && len(d) > 0 {
			ii, iiFD, iiPS = d, fd, ps
			break
		}
	}
	if len(ii) == 0 {
		return nil, nil, nil, false
	}

	ni, niFD, _, err := c.fetchConcept(cik, "NoninterestIncome")
	if err != nil || len(ni) == 0 {
		return nil, nil, nil, false
	}

	merged := make(map[string]float64)
	mergedFD := make(map[string]string)
	mergedPS := make(map[string]string)
	for period, iv := range ii {
		nv, ok := ni[period]
		if !ok {
			continue
		}
		merged[period] = iv + nv
		// Use the later of the two filing dates for this period.
		if iiFD[period] > niFD[period] {
			mergedFD[period] = iiFD[period]
		} else {
			mergedFD[period] = niFD[period]
		}
		mergedPS[period] = iiPS[period]
	}
	if len(merged) == 0 {
		return nil, nil, nil, false
	}

	hasRecent := false
	for period := range merged {
		t, perr := time.Parse("2006-01-02", period)
		if perr == nil && t.After(recentCutoff) {
			hasRecent = true
			break
		}
	}
	if !hasRecent {
		return nil, nil, nil, false
	}
	return merged, mergedFD, mergedPS, true
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
// The top-level fields (Name, SIC, SICDescription) come from the entity metadata
// returned alongside the filings list.
type secSubmissionsResponse struct {
	Name           string `json:"name"`           // legal entity name, e.g. "Apple Inc."
	SIC            string `json:"sic"`            // 4-digit SIC code as string, e.g. "3571"
	SICDescription string `json:"sicDescription"` // e.g. "ELECTRONIC COMPUTERS"
	Filings struct {
		Recent struct {
			Form                []string `json:"form"`
			FilingDate          []string `json:"filingDate"`
			AccessionNumber     []string `json:"accessionNumber"`
			PrimaryDocument     []string `json:"primaryDocument"`
			Items               []string `json:"items"`               // e.g. "2.02,9.01"
			PrimaryDocDesc      []string `json:"primaryDocDescription"` // e.g. "EARNINGS RELEASE"
		} `json:"recent"`
	} `json:"filings"`
}

// FetchEntitySIC returns the SIC code (numeric) and its description for the given
// symbol. Used for sector-based peer matching.
func (c *SECClient) FetchEntitySIC(symbol string) (sic int, sicDesc string, err error) {
	cik, err := c.lookupCIK(symbol)
	if err != nil {
		return 0, "", err
	}
	subs, err := c.fetchSubmissions(cik)
	if err != nil {
		return 0, "", err
	}
	v, _ := strconv.Atoi(subs.SIC)
	return v, subs.SICDescription, nil
}

// MaterialEvent is a significant 8-K filing with optional stock-price context.
type MaterialEvent struct {
	Date     string  // YYYY-MM-DD (8-K filing date)
	Items    string  // raw item string, e.g. "2.02,9.01"
	Label    string  // human-readable label for the most significant item
	RetPct   float64 // stock return on that date (0 = unavailable)
	Abnormal bool    // |RetPct| > 1.5× 30-day rolling daily vol
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
	cik, err := c.lookupCIK(symbol)
	if err != nil {
		return nil, err
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

// item8KLabel returns a human-readable label for the most significant item in a
// comma-separated SEC 8-K items string. Items are evaluated in priority order
// so that a filing with both "2.02,9.01" surfaces as "Earnings Release" rather
// than the less-informative "Financial Statements".
func item8KLabel(items string) string {
	priority := []struct {
		item  string
		label string
	}{
		{"1.03", "Bankruptcy/Receivership"},
		{"1.01", "Material Agreement"},
		{"1.02", "Agreement Terminated"},
		{"2.01", "Asset Acquisition/Disposal"},
		{"3.01", "Rating Agency Action"},
		{"4.01", "Auditor Change"},
		{"5.01", "Change in Control"},
		{"5.02", "Director/Officer Change"},
		{"5.03", "Charter Amendment"},
		{"5.07", "Stockholder Vote"},
		{"2.03", "Off-Balance-Sheet Arrangement"},
		{"2.04", "Triggering Events"},
		{"2.02", "Earnings Release"},
		{"7.01", "Regulation FD Disclosure"},
		{"8.01", "Other Events"},
		{"9.01", "Financial Statements"},
	}
	for _, p := range priority {
		for _, part := range strings.Split(items, ",") {
			if strings.TrimSpace(part) == p.item {
				return p.label
			}
		}
	}
	if items != "" {
		return "Item " + strings.TrimSpace(strings.SplitN(items, ",", 2)[0])
	}
	return "8-K Filing"
}

// FetchMaterialEvents returns material 8-K events filed in the last 90 days
// (or since `since`, whichever is later). Excludes 8-K/A amendments and
// filings whose only item is "9.01" (standalone financial exhibit attachments).
// Returns at most 10 events (most-recent first).
func (c *SECClient) FetchMaterialEvents(symbol string, since time.Time) ([]MaterialEvent, error) {
	cik, err := c.lookupCIK(symbol)
	if err != nil {
		return nil, err
	}
	subs, err := c.fetchSubmissions(cik)
	if err != nil {
		return nil, err
	}

	cutoff := since.Format("2006-01-02")
	r := subs.Filings.Recent
	var events []MaterialEvent
	for i, form := range r.Form {
		if form != "8-K" {
			continue // skip 8-K/A amendments and other forms
		}
		if i >= len(r.FilingDate) {
			break
		}
		date := r.FilingDate[i]
		if date < cutoff {
			continue
		}
		items := ""
		if i < len(r.Items) {
			items = strings.TrimSpace(r.Items[i])
		}
		// Skip filings that only have "9.01" (financial exhibit with no material event).
		if items == "9.01" || items == "" {
			continue
		}
		events = append(events, MaterialEvent{
			Date:  date,
			Items: items,
			Label: item8KLabel(items),
		})
		if len(events) >= 10 {
			break
		}
	}
	return events, nil
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
	cik, err := c.lookupCIK(symbol)
	if err != nil {
		return nil, err
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
