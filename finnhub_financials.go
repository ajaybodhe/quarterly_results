package main

import (
	"fmt"
	"strings"
	"time"
)

// FinnhubFinancialsProvider implements FinancialsProvider for international stocks
// using the Finnhub free-tier API.
type FinnhubFinancialsProvider struct {
	client *FinnhubClient
	cfg    ExchangeConfig
}

func NewFinnhubFinancialsProvider(client *FinnhubClient, cfg ExchangeConfig) *FinnhubFinancialsProvider {
	return &FinnhubFinancialsProvider{client: client, cfg: cfg}
}

// FetchQuarterlyActuals fetches historical income statement data via Finnhub.
// Returns quarterly entries sorted oldest → newest.
func (p *FinnhubFinancialsProvider) FetchQuarterlyActuals(symbol string) ([]QuarterActual, error) {
	bare := BareTickerFromSymbol(symbol)
	mic := p.cfg.FinnhubExch

	var raw struct {
		FinancialsAsReported struct {
			Data []struct {
				Year     int    `json:"year"`
				Quarter  int    `json:"quarter"`
				Form     string `json:"form"`
				FiledDate string `json:"filedDate"`
				StartDate string `json:"startDate"`
				EndDate   string `json:"endDate"`
				Report    struct {
					IC []struct {
						Concept string  `json:"concept"`
						Value   float64 `json:"value"`
					} `json:"ic"`
				} `json:"report"`
			} `json:"data"`
		} `json:"financialsAsReported"`
	}

	url := fmt.Sprintf(
		"https://finnhub.io/api/v1/stock/financials-reported?symbol=%s&exchange=%s&freq=quarterly&token=%s",
		bare, mic, p.client.apiKey,
	)
	if err := p.client.getJSON(url, &raw); err != nil {
		return nil, fmt.Errorf("finnhub financials for %s: %w", symbol, err)
	}

	var out []QuarterActual
	for _, d := range raw.FinancialsAsReported.Data {
		if d.EndDate == "" {
			continue
		}
		var revenue, eps float64
		for _, ic := range d.Report.IC {
			switch strings.ToLower(ic.Concept) {
			case "revenues", "revenue", "totalrevenue", "netsales", "netturnover":
				if ic.Value != 0 {
					revenue = ic.Value
				}
			case "basicepsfromdiscontinuedops", "basiceps", "dilutedeps", "earningspersharediluted":
				if ic.Value != 0 {
					eps = ic.Value
				}
			}
		}
		out = append(out, QuarterActual{
			PeriodStart: d.StartDate,
			Period:      d.EndDate,
			Revenue:     revenue,
			EPS:         eps,
			FilingDate:  d.FiledDate,
		})
	}

	// Sort oldest → newest.
	for i := 0; i < len(out)-1; i++ {
		for j := i + 1; j < len(out); j++ {
			if out[i].Period > out[j].Period {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// FetchEarningsAnnouncementDates maps each QuarterActual's FilingDate as the
// announcement date — Finnhub provides this directly in the financial data.
func (p *FinnhubFinancialsProvider) FetchEarningsAnnouncementDates(symbol string, history []QuarterActual) (map[string]string, error) {
	m := make(map[string]string, len(history))
	for _, q := range history {
		if q.FilingDate != "" {
			m[q.Period] = q.FilingDate
		}
	}
	return m, nil
}

// FetchInsiderActivity fetches insider transactions from Finnhub.
func (p *FinnhubFinancialsProvider) FetchInsiderActivity(symbol string, since time.Time) (*InsiderSummary, error) {
	bare := BareTickerFromSymbol(symbol)
	url := fmt.Sprintf(
		"https://finnhub.io/api/v1/stock/insider-transactions?symbol=%s&from=%s&token=%s",
		bare, since.Format("2006-01-02"), p.client.apiKey,
	)

	var raw struct {
		Data []struct {
			Name          string  `json:"name"`
			Share         float64 `json:"share"`
			Change        float64 `json:"change"`
			TransactionDate string `json:"transactionDate"`
			TransactionCode string `json:"transactionCode"` // "P"=buy, "S"=sell
			Value         float64 `json:"value"`
		} `json:"data"`
	}
	if err := p.client.getJSON(url, &raw); err != nil {
		return nil, err
	}

	sum := &InsiderSummary{}
	for _, t := range raw.Data {
		val := t.Value
		if val == 0 && t.Share != 0 {
			val = t.Share // fallback if value not provided
		}
		switch t.TransactionCode {
		case "P":
			sum.BuyShares += int64(t.Change)
			sum.BuyValue += val
			sum.FilingCount++
		case "S":
			sum.SellShares += int64(-t.Change)
			sum.SellValue += val
			sum.FilingCount++
		}
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

// FetchMaterialEvents returns nil — SEC 8-K events are US-only.
func (p *FinnhubFinancialsProvider) FetchMaterialEvents(symbol string, since time.Time) ([]MaterialEvent, error) {
	return nil, nil
}

// FetchEntitySIC returns 0,"",nil — SIC codes are SEC-specific.
func (p *FinnhubFinancialsProvider) FetchEntitySIC(symbol string) (int, string, error) {
	return 0, "", nil
}

// FetchForwardEPS fetches next-quarter EPS estimates from Finnhub.
func (p *FinnhubFinancialsProvider) FetchForwardEPS(symbol string) ([]ForwardQuarter, error) {
	bare := BareTickerFromSymbol(symbol)
	url := fmt.Sprintf(
		"https://finnhub.io/api/v1/stock/eps-estimates?symbol=%s&freq=quarterly&token=%s",
		bare, p.client.apiKey,
	)

	var raw struct {
		EpsEstimates []struct {
			Period           string  `json:"period"`
			EpsAvg           float64 `json:"epsAvg"`
			EpsHigh          float64 `json:"epsHigh"`
			EpsLow           float64 `json:"epsLow"`
			NumberAnalysts   int     `json:"numberAnalysts"`
		} `json:"epsEstimates"`
	}
	if err := p.client.getJSON(url, &raw); err != nil {
		return nil, err
	}

	var out []ForwardQuarter
	for _, e := range raw.EpsEstimates {
		out = append(out, ForwardQuarter{
			FiscalEnd:         e.Period,
			ConsensusEPS:      e.EpsAvg,
			HighEPS:           e.EpsHigh,
			LowEPS:            e.EpsLow,
			NumberOfEstimates: e.NumberAnalysts,
		})
	}
	return out, nil
}
