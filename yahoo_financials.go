package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// yahoo_financials.go provides a fallback for quarterly actuals when the SEC
// XBRL companyconcept API returns nothing. The primary use case is Foreign
// Private Issuers (FPIs) listed on US exchanges — e.g. Nebius Group (NBIS),
// which files 6-K interim reports instead of 10-Qs and therefore has no
// us-gaap quarterly facts in EDGAR. Yahoo's quoteSummary returns the same
// data via `earnings.earningsChart.quarterly` (period + reportedDate + EPS)
// merged with `incomeStatementHistoryQuarterly.incomeStatementHistory`
// (period + totalRevenue).

// fetchYahooQuarterlyActuals returns up to ~4 quarters of EPS + revenue +
// announcement date for the given symbol, sorted oldest → newest. Requires a
// valid Yahoo crumb (cookie + token), which is handled by ensureYahooCrumb.
func (e *Enricher) fetchYahooQuarterlyActuals(symbol string) ([]QuarterActual, error) {
	if err := e.ensureYahooCrumb(); err != nil {
		return nil, fmt.Errorf("yahoo crumb: %w", err)
	}

	url := fmt.Sprintf(
		"https://query2.finance.yahoo.com/v10/finance/quoteSummary/%s?modules=earnings,incomeStatementHistoryQuarterly&crumb=%s",
		symbol, e.yahooCrumb,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://finance.yahoo.com/")

	resp, err := e.yahooClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yahoo quoteSummary HTTP %d", resp.StatusCode)
	}

	var top struct {
		QuoteSummary struct {
			Result []struct {
				Earnings struct {
					EarningsChart struct {
						Quarterly []struct {
							PeriodEndDate struct {
								Raw int64 `json:"raw"`
							} `json:"periodEndDate"`
							ReportedDate struct {
								Raw int64 `json:"raw"`
							} `json:"reportedDate"`
							Actual struct {
								Raw float64 `json:"raw"`
							} `json:"actual"`
							Estimate struct {
								Raw float64 `json:"raw"`
							} `json:"estimate"`
						} `json:"quarterly"`
					} `json:"earningsChart"`
				} `json:"earnings"`
				IncomeStatementHistoryQuarterly struct {
					IncomeStatementHistory []struct {
						EndDate struct {
							Raw int64 `json:"raw"`
						} `json:"endDate"`
						TotalRevenue struct {
							Raw float64 `json:"raw"`
						} `json:"totalRevenue"`
					} `json:"incomeStatementHistory"`
				} `json:"incomeStatementHistoryQuarterly"`
			} `json:"result"`
			Error *struct {
				Description string `json:"description"`
			} `json:"error"`
		} `json:"quoteSummary"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("decode yahoo quoteSummary: %w", err)
	}
	if top.QuoteSummary.Error != nil {
		return nil, fmt.Errorf("yahoo quoteSummary error: %s", top.QuoteSummary.Error.Description)
	}
	if len(top.QuoteSummary.Result) == 0 {
		return nil, fmt.Errorf("yahoo quoteSummary: no result")
	}
	r := top.QuoteSummary.Result[0]

	// Index revenue by period-end date (YYYY-MM-DD) for merge.
	revByPeriod := make(map[string]float64)
	for _, is := range r.IncomeStatementHistoryQuarterly.IncomeStatementHistory {
		if is.EndDate.Raw == 0 {
			continue
		}
		key := time.Unix(is.EndDate.Raw, 0).UTC().Format("2006-01-02")
		revByPeriod[key] = is.TotalRevenue.Raw
	}

	// Walk earnings chart (has period + announcement date + EPS). Merge revenue.
	var out []QuarterActual
	for _, q := range r.Earnings.EarningsChart.Quarterly {
		if q.PeriodEndDate.Raw == 0 {
			continue
		}
		periodEnd := time.Unix(q.PeriodEndDate.Raw, 0).UTC().Format("2006-01-02")
		filing := ""
		if q.ReportedDate.Raw > 0 {
			filing = time.Unix(q.ReportedDate.Raw, 0).UTC().Format("2006-01-02")
		}
		qa := QuarterActual{
			Period:     periodEnd,
			EPS:        q.Actual.Raw,
			Revenue:    revByPeriod[periodEnd], // 0 if not matched
			FilingDate: filing,
		}
		out = append(out, qa)
	}

	// Sort oldest → newest.
	sort.Slice(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out, nil
}
