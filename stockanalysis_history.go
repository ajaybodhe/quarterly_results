package main

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// stockanalysis_history.go extends the Yahoo quoteSummary fallback (which is
// capped at ~4 trailing quarters) by scraping stockanalysis.com's quarterly
// financials page. Yahoo remains the source of truth for the anchor quarters
// (period-end, EPS in per-ADR display units, announcement date). We only use
// stockanalysis to backfill older-only quarters so YoY math has ≥5 rows.
//
// Currency/scale handling: stockanalysis.com reports revenue and EPS in the
// company's *native* reporting currency, per common share. For a US-listed
// FPI like TSM, that is TWD per common share, while Yahoo returns USD per ADR.
// To reconcile, we compute a per-metric scale factor from overlapping periods
// (median of yahoo_val / sa_val across quarters present in both) and apply it
// to sa-only quarters before appending. If the two sources already share units
// (typical for US-domicile issuers reporting in USD, e.g. NBIS), the median
// ratio comes out to 1.0 and the operation is a no-op.

// saDatekeyArrRe extracts the datekey period-end array from the quarterly
// financials page. Format: financialData:{datekey:["2026-03-31", ...
var saDatekeyArrRe = regexp.MustCompile(`financialData:\{datekey:\[(.*?)\]`)

// saFinRevRe / saFinEPSDilutedRe extract revenue and EPS diluted from the
// same block. Anchor them within a bounded window after the financialData
// start marker so we don't accidentally match a similarly-named field
// elsewhere on the page.
var (
	saFinRevRe        = regexp.MustCompile(`(?:^|,)revenue:\[(.*?)\]`)
	saFinEPSDilutedRe = regexp.MustCompile(`(?:^|,)epsDiluted:\[(.*?)\]`)
)

// fetchSAQuarterlyHistory returns quarterly (period, revenue, EPS) rows for
// the given symbol from stockanalysis.com. Results are sorted oldest→newest.
// Rows with both revenue == 0 and eps == 0 (paywalled or missing) are dropped.
func fetchSAQuarterlyHistory(client *http.Client, symbol string) ([]QuarterActual, error) {
	sym := strings.ToLower(symbol)
	url := fmt.Sprintf("https://stockanalysis.com/stocks/%s/financials/?p=quarterly", sym)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	html := string(body)

	// Locate the financialData block and take a bounded slice as the search
	// window for revenue / EPS arrays.
	dm := saDatekeyArrRe.FindStringSubmatchIndex(html)
	if dm == nil {
		return nil, fmt.Errorf("stockanalysis financialData block not found")
	}
	dates := saParseStrings(html[dm[2]:dm[3]])
	end := dm[1] + 20000
	if end > len(html) {
		end = len(html)
	}
	window := html[dm[1]:end]

	var rev, eps []float64
	if m := saFinRevRe.FindStringSubmatch(window); m != nil {
		rev = saParseFloats(m[1])
	}
	if m := saFinEPSDilutedRe.FindStringSubmatch(window); m != nil {
		eps = saParseFloats(m[1])
	}

	var out []QuarterActual
	for i, d := range dates {
		if d == "" || len(d) != 10 {
			continue
		}
		if _, perr := time.Parse("2006-01-02", d); perr != nil {
			continue
		}
		var r, e float64
		if i < len(rev) {
			r = rev[i]
		}
		if i < len(eps) {
			e = eps[i]
		}
		if r == 0 && e == 0 {
			continue // paywalled or unavailable
		}
		out = append(out, QuarterActual{Period: d, Revenue: r, EPS: e})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out, nil
}

// mergeSAHistoryWithYahoo backfills older quarters into yahooHist using
// stockanalysis data. Yahoo entries are trusted verbatim; sa entries for
// periods older than the earliest Yahoo period are scaled and prepended.
//
// Scale factors are the median of (yahoo_val / sa_val) across periods present
// in both sources. If fewer than 2 overlapping periods produce a usable ratio,
// scaling is skipped and Yahoo is returned unchanged (safer than injecting
// values with the wrong currency).
func mergeSAHistoryWithYahoo(yahooHist, saHist []QuarterActual) []QuarterActual {
	if len(yahooHist) == 0 || len(saHist) == 0 {
		return yahooHist
	}

	yahooByPeriod := make(map[string]QuarterActual, len(yahooHist))
	for _, q := range yahooHist {
		yahooByPeriod[q.Period] = q
	}
	earliestYahoo := yahooHist[0].Period

	var revRatios, epsRatios []float64
	for _, sa := range saHist {
		yq, ok := yahooByPeriod[sa.Period]
		if !ok {
			continue
		}
		if sa.Revenue > 0 && yq.Revenue > 0 {
			revRatios = append(revRatios, yq.Revenue/sa.Revenue)
		}
		if sa.EPS != 0 && yq.EPS != 0 {
			epsRatios = append(epsRatios, yq.EPS/sa.EPS)
		}
	}
	if len(revRatios) < 2 && len(epsRatios) < 2 {
		return yahooHist
	}
	revScale := medianFloat(revRatios)
	epsScale := medianFloat(epsRatios)

	// Sanity: reject nonsensical scales (e.g. one metric had a single wildly
	// off overlap). Fall back to no scaling for that metric.
	if revScale == 0 || math.IsNaN(revScale) || math.IsInf(revScale, 0) {
		revScale = 1
	}
	if epsScale == 0 || math.IsNaN(epsScale) || math.IsInf(epsScale, 0) {
		epsScale = 1
	}

	// Prepend sa quarters strictly older than the earliest Yahoo period.
	var older []QuarterActual
	for _, sa := range saHist {
		if sa.Period >= earliestYahoo {
			continue
		}
		older = append(older, QuarterActual{
			Period:  sa.Period,
			Revenue: sa.Revenue * revScale,
			EPS:     sa.EPS * epsScale,
		})
	}
	if len(older) == 0 {
		return yahooHist
	}
	merged := append(older, yahooHist...)
	return merged
}

