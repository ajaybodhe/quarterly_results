package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// FinnhubClient fetches insider sentiment from Finnhub (free tier, 60 calls/min).
// Requires FINNHUB_API_KEY env var — free registration at finnhub.io.
type FinnhubClient struct {
	apiKey     string
	httpClient *http.Client
}

const defaultFinnhubAPIKey = "d6kt2lhr01qmopd22780d6kt2lhr01qmopd2278g"

func NewFinnhubClient() *FinnhubClient {
	key := os.Getenv("FINNHUB_API_KEY")
	if key == "" {
		key = defaultFinnhubAPIKey
	}
	return &FinnhubClient{
		apiKey:     key,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

type finnhubSentimentResponse struct {
	Data []struct {
		Symbol string  `json:"symbol"`
		Year   int     `json:"year"`
		Month  int     `json:"month"`
		Change float64 `json:"change"` // net shares bought
		MSPR   float64 `json:"mspr"`   // Monthly Share Purchase Ratio (0–1)
	} `json:"data"`
	Symbol string `json:"symbol"`
}

// FetchMSPR returns the average MSPR over the last 3 months and its directional signal.
// MSPR = purchases / (purchases + sales) per month; 0.0–1.0 range.
// Signal: "Bullish" (>0.6), "Neutral" (0.4–0.6), "Bearish" (<0.4).
func (c *FinnhubClient) FetchMSPR(symbol string) (mspr float64, signal string, err error) {
	if c.apiKey == "" {
		return 0, "N/A", fmt.Errorf("FINNHUB_API_KEY not set")
	}

	to := time.Now()
	from := to.AddDate(0, -3, 0)
	url := fmt.Sprintf(
		"https://finnhub.io/api/v1/stock/insider-sentiment?symbol=%s&from=%s&to=%s&token=%s",
		symbol, from.Format("2006-01-02"), to.Format("2006-01-02"), c.apiKey,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, "N/A", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "N/A", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		n := len(body)
		if n > 80 {
			n = 80
		}
		return 0, "N/A", fmt.Errorf("finnhub HTTP %d: %s", resp.StatusCode, string(body[:n]))
	}

	var raw finnhubSentimentResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return 0, "N/A", err
	}

	if len(raw.Data) == 0 {
		return 0, "N/A", fmt.Errorf("no insider sentiment data for %s", symbol)
	}

	var sum float64
	var count int
	for _, d := range raw.Data {
		if d.MSPR >= 0 {
			sum += d.MSPR
			count++
		}
	}
	if count == 0 {
		return 0, "N/A", fmt.Errorf("no valid MSPR entries for %s", symbol)
	}

	avg := sum / float64(count)
	return avg, msprSignal(avg), nil
}

// getJSON performs a GET request and JSON-decodes the response body into dst.
func (c *FinnhubClient) getJSON(url string, dst any) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		n := len(body)
		if n > 80 {
			n = 80
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body[:n]))
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func msprSignal(mspr float64) string {
	switch {
	case mspr > 0.6:
		return "Bullish"
	case mspr < 0.4:
		return "Bearish"
	default:
		return "Neutral"
	}
}
