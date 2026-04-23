package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// saEstimates holds the current-quarter consensus estimates from stockanalysis.com.
type saEstimates struct {
	EPSEst          float64
	RevenueEst      float64 // upcoming quarter consensus revenue estimate
	RevenuePrevYear float64 // same fiscal quarter one year ago (from stats.quarterly.revenueThis.last)
	Analysts        int
	ReportDate      string

	// Analyst ratings (from most-recent monthly snapshot in the ratings history)
	ConsensusRating string
	StrongBuy       int
	Buy             int
	Hold            int
	Sell            int
	StrongSell      int
	TotalRatings    int

	// Average price target (mean of pt_now across individual analyst rows)
	AvgPriceTarget float64
}

// saQuarterlyRe extracts the four parallel arrays from the SvelteKit table payload.
var saQuarterlyRe = regexp.MustCompile(
	`quarterly:\{eps:\[(.*?)\],dates:\[(.*?)\],revenue:\[(.*?)\],analysts:\[(.*?)\]`,
)

// saStatsRe extracts the stats.quarterly block which holds year-ago values.
// Structure: stats:{...,quarterly:{epsNext:{...},epsThis:{...},revenueNext:{last:X,this:Y,...},revenueThis:{last:A,this:B,...}}}
var saRevenueNextRe = regexp.MustCompile(`revenueNext:\{last:([0-9.]+),this:([0-9.]+)`)
var saRevenueThisRe = regexp.MustCompile(`revenueThis:\{last:([0-9.]+),this:([0-9.]+)`)

// saRatingsSnapshotRe matches one monthly analyst ratings snapshot entry.
// Structure: {buy:N,date:"...",hold:N,sell:N,month:"...",score:N,total:N,updated:"...",consensus:"...",strongBuy:N,strongSell:N}
var saRatingsSnapshotRe = regexp.MustCompile(
	`\{buy:(\d+),date:"[^"]+",hold:(\d+),sell:(\d+),month:"[^"]+",score:[0-9.]+,total:(\d+),updated:"[^"]+",consensus:"([^"]+)",strongBuy:(\d+),strongSell:(\d+)\}`,
)

// saPriceTargetRe extracts pt_now from individual analyst rating rows.
var saPriceTargetRe = regexp.MustCompile(`pt_now:([0-9]+(?:\.[0-9]+)?)`)

// fetchSAEstimates fetches the forward quarterly estimates for symbol from
// stockanalysis.com/stocks/{symbol}/forecast/.
func (e *Enricher) fetchSAEstimates(symbol string) (*saEstimates, error) {
	url := fmt.Sprintf(
		"https://stockanalysis.com/stocks/%s/forecast/",
		strings.ToLower(symbol),
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stockanalysis HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return parseSAEstimates(string(body))
}

func parseSAEstimates(html string) (*saEstimates, error) {
	// ── 1. Quarterly table: find the first row with analysts != null ───────────
	m := saQuarterlyRe.FindStringSubmatch(html)
	if m == nil {
		return nil, fmt.Errorf("quarterly estimates block not found")
	}

	eps      := saParseFloats(m[1])
	dates    := saParseStrings(m[2])
	revenues := saParseFloats(m[3])
	analysts := saParseInts(m[4])

	var epsEst, revEstFromTable float64
	var reportDate string
	var analysts0 int
	n := len(dates)
	for i := 0; i < n; i++ {
		if i >= len(analysts) || analysts[i] <= 0 {
			continue
		}
		if i < len(eps) {
			epsEst = eps[i]
		}
		if i < len(revenues) {
			revEstFromTable = revenues[i]
		}
		reportDate = dates[i]
		analysts0 = analysts[i]
		break
	}
	if reportDate == "" {
		return nil, fmt.Errorf("no forward estimate row found")
	}

	// ── 2. Revenue estimate + year-ago values ─────────────────────────────────
	//
	// Revenue estimate for the upcoming reporting quarter comes from the same
	// quarterly-table row selected above (first row with analysts > 0). stockanalysis
	// labels that row as the fiscal quarter that most recently ended / is about to be
	// reported — which matches what the market cares about pre-earnings.
	//
	// The stats block `revenueThis.last` gives the same-quarter-one-year-ago actual,
	// used for YoY comparison. `revenueNext.this` is the quarter AFTER the upcoming
	// one, not what we want for the current report — kept only as a secondary
	// fallback in case the table cell is paywalled.
	var revEst, revPrevYear float64
	revEst = revEstFromTable

	qStatsIdx := strings.Index(html, `quarterly:{eps`)
	if qStatsIdx == -1 {
		qStatsIdx = 0
	}
	qStatsBlock := html[qStatsIdx:]

	if nm := saRevenueThisRe.FindStringSubmatch(qStatsBlock); nm != nil {
		revPrevYear, _ = strconv.ParseFloat(nm[1], 64) // revenueThis.last = year-ago same quarter
	}

	if revEst == 0 {
		// Table cell was paywalled or missing — fall back to stats block. Note that
		// revenueNext.this is one quarter too far out, but it's better than nothing.
		if nm := saRevenueNextRe.FindStringSubmatch(qStatsBlock); nm != nil {
			revEst, _ = strconv.ParseFloat(nm[2], 64)
		}
	}
	if revEst == 0 {
		return nil, fmt.Errorf("revenue estimate not found")
	}

	est := &saEstimates{
		EPSEst:          epsEst,
		RevenueEst:      revEst,
		RevenuePrevYear: revPrevYear,
		Analysts:        analysts0,
		ReportDate:      reportDate,
	}

	// ── 3. Analyst ratings: use the most recent monthly snapshot ─────────────
	// The page embeds a time-series array of monthly snapshots (oldest → newest).
	// We take the last match as the current month's data.
	snapshots := saRatingsSnapshotRe.FindAllStringSubmatch(html, -1)
	if len(snapshots) > 0 {
		s := snapshots[len(snapshots)-1]
		est.Buy, _ = strconv.Atoi(s[1])
		est.Hold, _ = strconv.Atoi(s[2])
		est.Sell, _ = strconv.Atoi(s[3])
		est.TotalRatings, _ = strconv.Atoi(s[4])
		est.ConsensusRating = s[5]
		est.StrongBuy, _ = strconv.Atoi(s[6])
		est.StrongSell, _ = strconv.Atoi(s[7])
	}

	// ── 4. Average price target: mean of pt_now across individual analyst rows ─
	ptMatches := saPriceTargetRe.FindAllStringSubmatch(html, -1)
	if len(ptMatches) > 0 {
		var sum float64
		for _, m := range ptMatches {
			v, err := strconv.ParseFloat(m[1], 64)
			if err == nil && v > 0 {
				sum += v
			}
		}
		est.AvgPriceTarget = sum / float64(len(ptMatches))
	}

	return est, nil
}

// ── array parsers ─────────────────────────────────────────────────────────────

func saParseFloats(s string) []float64 {
	var out []float64
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(strings.Trim(tok, `"`))
		if saIsNull(tok) {
			out = append(out, 0)
			continue
		}
		v, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			out = append(out, 0)
		} else {
			out = append(out, v)
		}
	}
	return out
}

func saParseStrings(s string) []string {
	var out []string
	for _, tok := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(strings.Trim(tok, `"`)))
	}
	return out
}

func saParseInts(s string) []int {
	var out []int
	for _, tok := range strings.Split(s, ",") {
		tok = strings.TrimSpace(strings.Trim(tok, `"`))
		if saIsNull(tok) {
			out = append(out, 0)
			continue
		}
		f, err := strconv.ParseFloat(tok, 64)
		if err != nil {
			out = append(out, 0)
		} else {
			out = append(out, int(f))
		}
	}
	return out
}

func saIsNull(tok string) bool {
	return tok == "null" || tok == "void 0" || strings.Contains(tok, "PRO") || tok == ""
}
