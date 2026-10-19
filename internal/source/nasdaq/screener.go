package nasdaq

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const screenerURL = "https://api.nasdaq.com/api/screener/stocks?tableonly=true&limit=10000&download=true"

// ScreenerStock is one US-listed common stock from Nasdaq's stock screener.
type ScreenerStock struct {
	Symbol     string // Yahoo/SEC form, e.g. "BRK-B"
	Name       string
	MarketCapB float64 // current market cap, billions USD
	Sector     string
	Industry   string
}

type screenerRow struct {
	Symbol    string `json:"symbol"`
	Name      string `json:"name"`
	MarketCap string `json:"marketCap"`
	Country   string `json:"country"`
	Sector    string `json:"sector"`
	Industry  string `json:"industry"`
}

// FetchLargestUS returns the n largest US-domiciled common stocks by current
// market cap (one request). Preferred shares and other "^" listings are
// dropped; class shares use "-" ("BRK/B" → "BRK-B") as Yahoo and SEC do.
// Today's ranking is used for every historical date, so a dataset built from
// it carries survivorship bias — companies delisted since are absent.
func FetchLargestUS(client *http.Client, n int) ([]ScreenerStock, error) {
	var raw struct {
		Data struct {
			Rows []screenerRow `json:"rows"`
		} `json:"data"`
	}
	if err := nasdaqGetJSON(client, screenerURL, &raw); err != nil {
		return nil, err
	}
	return largestUS(raw.Data.Rows, n), nil
}

// nonCommonPat matches screener names of securities that aren't common equity
// but are listed under their own symbols: preferreds, depositary shares for
// fractional preferred interests, notes, debentures and corporate/equity units
// (e.g. AGNCO, HBANM, STRF, TBB, SOJC). ADRs ("… representing one Ordinary
// Share") and MLP common units are kept.
var nonCommonPat = regexp.MustCompile(`(?i)preferred|depositary shares (?:each )?represent(?:ing|s) an? \d|\bnotes?\b|debentures?|subordinated|warrants?|\brights\b|corporate units|equity units?|tangible equity|%`)

func largestUS(rows []screenerRow, n int) []ScreenerStock {
	var out []ScreenerStock
	for _, r := range rows {
		sym := strings.TrimSpace(r.Symbol)
		if r.Country != "United States" || sym == "" || strings.Contains(sym, "^") || nonCommonPat.MatchString(r.Name) {
			continue
		}
		capUSD, err := strconv.ParseFloat(strings.TrimSpace(r.MarketCap), 64)
		if err != nil || capUSD <= 0 {
			continue
		}
		out = append(out, ScreenerStock{
			Symbol:     strings.ReplaceAll(sym, "/", "-"),
			Name:       strings.TrimSpace(r.Name),
			MarketCapB: capUSD / 1e9,
			Sector:     r.Sector,
			Industry:   r.Industry,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MarketCapB > out[j].MarketCapB })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
