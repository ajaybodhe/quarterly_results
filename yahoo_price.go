package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

func decodeJSON(r io.Reader, dst any) error {
	return json.NewDecoder(r).Decode(dst)
}

// YahooPriceProvider implements PriceProvider for international stocks via the
// Yahoo Finance chart API. It works for any Yahoo-suffix ticker (e.g. "VOD.L",
// "BMW.DE", "AIR.PA") and also for US tickers.
type YahooPriceProvider struct {
	httpClient *http.Client
	exCfg      ExchangeConfig
}

// yahooChartResponse is the raw shape from the Yahoo Finance v8 chart API.
type yahooChartResponse struct {
	Chart struct {
		Result []struct {
			Timestamps []int64 `json:"timestamp"`
			Indicators struct {
				Quote []struct {
					Open  []float64 `json:"open"`
					Close []float64 `json:"close"`
				} `json:"quote"`
			} `json:"indicators"`
		} `json:"result"`
		Error *struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"error"`
	} `json:"chart"`
}

// FetchPriceHistory fetches ~2 years of daily OHLC data from Yahoo Finance.
func (p *YahooPriceProvider) FetchPriceHistory(symbol string) ([]pricePoint, error) {
	url := fmt.Sprintf(
		"https://query1.finance.yahoo.com/v8/finance/chart/%s?interval=1d&range=2y",
		symbol,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Yahoo Finance HTTP %d for %s", resp.StatusCode, symbol)
	}

	var raw yahooChartResponse
	if err := decodeJSON(resp.Body, &raw); err != nil {
		return nil, err
	}
	if raw.Chart.Error != nil {
		return nil, fmt.Errorf("Yahoo Finance error: %s", raw.Chart.Error.Description)
	}
	if len(raw.Chart.Result) == 0 {
		return nil, fmt.Errorf("no price data for %s", symbol)
	}

	res := raw.Chart.Result[0]
	if len(res.Indicators.Quote) == 0 {
		return nil, fmt.Errorf("no quote data for %s", symbol)
	}
	quotes := res.Indicators.Quote[0]

	var out []pricePoint
	for i, ts := range res.Timestamps {
		if i >= len(quotes.Close) || i >= len(quotes.Open) {
			break
		}
		c := quotes.Close[i]
		o := quotes.Open[i]
		if c <= 0 {
			continue
		}
		out = append(out, pricePoint{
			Date:  time.Unix(ts, 0).UTC(),
			Open:  o,
			Close: c,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no valid price data for %s", symbol)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, nil
}

// FetchHistoricalEPSEstimate returns 0, nil for international stocks — Finnhub
// free tier does not provide historical consensus EPS estimates per announcement date.
func (p *YahooPriceProvider) FetchHistoricalEPSEstimate(symbol string, date time.Time) (float64, error) {
	return 0, nil
}
