package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// OptionsSnapshot holds pre-earnings options analytics for one stock.
// All computed from the nearest options expiry that falls on or after the earnings date,
// so the straddle price fully captures the expected earnings move.
type OptionsSnapshot struct {
	Expiry           string  // YYYY-MM-DD of the expiry used
	ExpectedMove     float64 // ATM straddle mid-price ($): call_mid + put_mid
	ExpectedMovePct  float64 // ExpectedMove / stock_price × 100
	IVAtm            float64 // average IV of ATM call + put (in %, e.g. 84.2)
	PCVol            float64 // total put volume / total call volume
	PCoi             float64 // total put OI / total call OI
	Skew             float64 // IV(5%-OTM put) − IV(5%-OTM call), in %
	MaxPain          float64 // strike price that minimises option-buyer payouts ($)
	MaxPainVsCurrent float64 // (MaxPain − stock_price) / stock_price × 100
	TotalCallOI      int
	TotalPutOI       int
	TotalCallVol     int
	TotalPutVol      int
	HistAvgAbsRxn    float64 // average |RXN_RET| across historical quarters (%) — for comparison
}

// yahooOption is one contract row from the Yahoo Finance options API.
type yahooOption struct {
	Strike            float64 `json:"strike"`
	LastPrice         float64 `json:"lastPrice"`
	Bid               float64 `json:"bid"`
	Ask               float64 `json:"ask"`
	ImpliedVolatility float64 `json:"impliedVolatility"` // 0–1 scale (e.g. 0.45 = 45%)
	OpenInterest      int     `json:"openInterest"`
	Volume            int     `json:"volume"`
}

// yahooOptionsResponse is the minimal shape of the Yahoo Finance v7 options API.
type yahooOptionsResponse struct {
	OptionChain struct {
		Result []struct {
			ExpirationDates []int64 `json:"expirationDates"` // Unix timestamps, sorted ascending
			Quote           struct {
				RegularMarketPrice float64 `json:"regularMarketPrice"`
			} `json:"quote"`
			Options []struct {
				ExpirationDate int64         `json:"expirationDate"`
				Calls          []yahooOption `json:"calls"`
				Puts           []yahooOption `json:"puts"`
			} `json:"options"`
		} `json:"result"`
	} `json:"optionChain"`
}

// fetchOptionsSnapshot fetches options analytics for the nearest expiry on or after earningsDate.
func (e *Enricher) fetchOptionsSnapshot(symbol, earningsDateStr string) (*OptionsSnapshot, error) {
	earningsDate, err := time.Parse("2006-01-02", earningsDateStr)
	if err != nil {
		return nil, fmt.Errorf("invalid earnings date: %w", err)
	}

	// First call — no date param — returns the nearest expiry chain + full expiry list.
	raw, err := e.fetchYahooOptions(symbol, 0)
	if err != nil {
		return nil, err
	}
	if len(raw.OptionChain.Result) == 0 {
		return nil, fmt.Errorf("no options data")
	}
	res := raw.OptionChain.Result[0]
	currentPrice := res.Quote.RegularMarketPrice
	if currentPrice <= 0 {
		return nil, fmt.Errorf("invalid current price")
	}

	// Find the first expiry on or after the earnings date.
	earningsTs := earningsDate.Unix()
	var targetTs int64
	for _, ts := range res.ExpirationDates {
		if ts >= earningsTs {
			targetTs = ts
			break
		}
	}
	if targetTs == 0 {
		return nil, fmt.Errorf("no expiry found after %s", earningsDateStr)
	}
	expiryDate := time.Unix(targetTs, 0).UTC().Format("2006-01-02")

	// Use already-fetched chain if it matches, otherwise re-fetch.
	var calls, puts []yahooOption
	if len(res.Options) > 0 && res.Options[0].ExpirationDate == targetTs {
		calls = res.Options[0].Calls
		puts = res.Options[0].Puts
	} else {
		raw2, err := e.fetchYahooOptions(symbol, targetTs)
		if err != nil {
			return nil, err
		}
		if len(raw2.OptionChain.Result) == 0 || len(raw2.OptionChain.Result[0].Options) == 0 {
			return nil, fmt.Errorf("empty chain for expiry %s", expiryDate)
		}
		calls = raw2.OptionChain.Result[0].Options[0].Calls
		puts = raw2.OptionChain.Result[0].Options[0].Puts
	}
	if len(calls) == 0 || len(puts) == 0 {
		return nil, fmt.Errorf("empty calls or puts for expiry %s", expiryDate)
	}

	snap := &OptionsSnapshot{Expiry: expiryDate}

	// ── Aggregate totals ──────────────────────────────────────────────────────
	for _, c := range calls {
		snap.TotalCallOI += c.OpenInterest
		snap.TotalCallVol += c.Volume
	}
	for _, p := range puts {
		snap.TotalPutOI += p.OpenInterest
		snap.TotalPutVol += p.Volume
	}
	if snap.TotalCallVol > 0 {
		snap.PCVol = float64(snap.TotalPutVol) / float64(snap.TotalCallVol)
	}
	if snap.TotalCallOI > 0 {
		snap.PCoi = float64(snap.TotalPutOI) / float64(snap.TotalCallOI)
	}

	// ── ATM straddle: expected move ───────────────────────────────────────────
	atmCall := closestStrike(calls, currentPrice)
	atmPut := closestStrike(puts, currentPrice)
	callMid := contractMid(atmCall)
	putMid := contractMid(atmPut)
	snap.ExpectedMove = callMid + putMid
	if currentPrice > 0 {
		snap.ExpectedMovePct = snap.ExpectedMove / currentPrice * 100
	}

	// ATM IV: average of call and put, converted to % (Yahoo stores as 0–1)
	callIV := cleanIV(atmCall.ImpliedVolatility)
	putIV := cleanIV(atmPut.ImpliedVolatility)
	if callIV > 0 && putIV > 0 {
		snap.IVAtm = (callIV + putIV) / 2 * 100
	}

	// ── Skew: IV(5%-OTM put) − IV(5%-OTM call) ───────────────────────────────
	otmPut := closestStrike(puts, currentPrice*0.95)
	otmCall := closestStrike(calls, currentPrice*1.05)
	otmPutIV := cleanIV(otmPut.ImpliedVolatility)
	otmCallIV := cleanIV(otmCall.ImpliedVolatility)
	if otmPutIV > 0 && otmCallIV > 0 {
		snap.Skew = (otmPutIV - otmCallIV) * 100
	}

	// ── Max pain ──────────────────────────────────────────────────────────────
	snap.MaxPain = computeMaxPain(calls, puts)
	if snap.MaxPain > 0 {
		snap.MaxPainVsCurrent = (snap.MaxPain - currentPrice) / currentPrice * 100
	}

	return snap, nil
}

// ensureYahooCrumb fetches a Yahoo Finance session cookie + crumb (done once, cached).
// Yahoo Finance requires a valid crumb tied to a cookie session for all API requests.
//
// Flow:
//  1. GET https://fc.yahoo.com — returns 404 but sets the A3 session cookie via HTTP headers.
//     This cookie is required by the crumb and options endpoints.
//  2. GET https://query2.finance.yahoo.com/v1/test/getcrumb — returns the crumb string.
//
// Both calls share yahooClient (which has a cookie jar), so the A3 cookie is automatically
// sent with the crumb request and all subsequent options API calls.
func (e *Enricher) ensureYahooCrumb() error {
	e.yahooCrumbOnce.Do(func() {
		ua := "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

		// Step 1: hit fc.yahoo.com to receive the A3 session cookie via Set-Cookie headers.
		// fc.yahoo.com returns HTTP 404 but the cookie is still set — this is intentional.
		req1, _ := http.NewRequest("GET", "https://fc.yahoo.com", nil)
		req1.Header.Set("User-Agent", ua)
		resp1, err := e.yahooClient.Do(req1)
		if err != nil {
			e.yahooCrumbErr = fmt.Errorf("yahoo A3 cookie: %w", err)
			return
		}
		resp1.Body.Close()

		// Step 2: fetch the crumb using the A3 cookie now in the jar.
		req2, _ := http.NewRequest("GET", "https://query2.finance.yahoo.com/v1/test/getcrumb", nil)
		req2.Header.Set("User-Agent", ua)
		req2.Header.Set("Accept", "text/plain")
		req2.Header.Set("Referer", "https://finance.yahoo.com/")
		resp2, err := e.yahooClient.Do(req2)
		if err != nil {
			e.yahooCrumbErr = fmt.Errorf("yahoo crumb: %w", err)
			return
		}
		defer resp2.Body.Close()
		body, _ := io.ReadAll(resp2.Body)
		crumb := strings.TrimSpace(string(body))
		if resp2.StatusCode != http.StatusOK || crumb == "" {
			e.yahooCrumbErr = fmt.Errorf("yahoo crumb failed (HTTP %d): %s", resp2.StatusCode, crumb)
			return
		}
		e.yahooCrumb = crumb
	})
	return e.yahooCrumbErr
}

// fetchYahooOptions calls the Yahoo Finance v7 options API.
// ts==0 fetches the nearest expiry; ts>0 fetches the chain for that Unix timestamp.
func (e *Enricher) fetchYahooOptions(symbol string, ts int64) (*yahooOptionsResponse, error) {
	if err := e.ensureYahooCrumb(); err != nil {
		return nil, fmt.Errorf("yahoo crumb: %w", err)
	}

	url := fmt.Sprintf("https://query2.finance.yahoo.com/v7/finance/options/%s?crumb=%s", symbol, e.yahooCrumb)
	if ts > 0 {
		url += fmt.Sprintf("&date=%d", ts)
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://finance.yahoo.com/")

	resp, err := e.yahooClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yahoo options: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("yahoo options HTTP %d: %s", resp.StatusCode, string(body)[:min(120, len(body))])
	}
	var out yahooOptionsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("yahoo options decode: %w", err)
	}
	return &out, nil
}

// closestStrike returns the contract whose strike is closest to target.
func closestStrike(contracts []yahooOption, target float64) yahooOption {
	if len(contracts) == 0 {
		return yahooOption{}
	}
	best := contracts[0]
	for _, c := range contracts[1:] {
		if math.Abs(c.Strike-target) < math.Abs(best.Strike-target) {
			best = c
		}
	}
	return best
}

// contractMid returns the mid-price ((bid+ask)/2), falling back to lastPrice.
func contractMid(c yahooOption) float64 {
	if c.Bid > 0 && c.Ask > 0 {
		return (c.Bid + c.Ask) / 2
	}
	return c.LastPrice
}

// cleanIV returns 0 for invalid IV values (Inf, NaN, zero, or absurdly large).
func cleanIV(iv float64) float64 {
	if math.IsInf(iv, 0) || math.IsNaN(iv) || iv <= 0 || iv > 20 {
		return 0
	}
	return iv
}

// computeMaxPain returns the strike price at which total option-buyer payouts are minimised.
// For each candidate strike K as the expiry price:
//
//	pain(K) = Σ_calls OI_i × max(K − strike_i, 0)  +  Σ_puts OI_i × max(strike_i − K, 0)
//
// We return the K that minimises pain (= where option sellers owe the least).
func computeMaxPain(calls, puts []yahooOption) float64 {
	// Collect all unique strikes as candidate expiry prices.
	strikeSet := make(map[float64]bool, len(calls)+len(puts))
	for _, c := range calls {
		strikeSet[c.Strike] = true
	}
	for _, p := range puts {
		strikeSet[p.Strike] = true
	}

	callOI := make(map[float64]int, len(calls))
	putOI := make(map[float64]int, len(puts))
	for _, c := range calls {
		callOI[c.Strike] += c.OpenInterest
	}
	for _, p := range puts {
		putOI[p.Strike] += p.OpenInterest
	}

	minPain := math.MaxFloat64
	var maxPainStrike float64
	for k := range strikeSet {
		pain := 0.0
		for s, oi := range callOI {
			if k > s {
				pain += float64(oi) * (k - s)
			}
		}
		for s, oi := range putOI {
			if k < s {
				pain += float64(oi) * (s - k)
			}
		}
		if pain < minPain {
			minPain = pain
			maxPainStrike = k
		}
	}
	return maxPainStrike
}
