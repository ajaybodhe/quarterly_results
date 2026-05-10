package main

import (
	"net/http"
	"sync"
	"time"
)

// SectorMomentum holds a stock's sector-ETF reference returns at a single
// point in time. Used as a coarse "is this sector in favour?" gauge by the
// recommendation system.
type SectorMomentum struct {
	ETF   string   // ticker symbol, e.g. "XLK"
	Ret1M *float64 // calendar-30-day return ending today, %
	Ret3M *float64 // calendar-90-day return ending today, %
}

// sicSectorETF maps the broad sector buckets defined in sicSectorGroup() to a
// representative SPDR (or industry) ETF. Returns "" for unknown buckets so the
// caller can skip the fetch cleanly.
//
// Mapping rationale: prefer the most liquid, broadly-traded sector ETF so the
// 1M return is a fair proxy for "is sector capital flowing in or out?". For
// niche groups (e.g. semiconductors) we use a focused ETF rather than the
// parent broad-tech ETF so chip-specific moves register.
func sicSectorETF(sic int) string {
	switch sicSectorGroup(sic) {
	case 1: // Agriculture
		return "MOO"
	case 2, 6: // Mining / Petroleum Refining → Energy
		return "XLE"
	case 3: // Construction → Industrials
		return "XLI"
	case 4: // Food / Beverage / Apparel → Consumer Staples
		return "XLP"
	case 5: // Chemicals / Pharma → Health Care
		return "XLV"
	case 7: // Industrial Manufacturing
		return "XLI"
	case 8: // Computers & Electronics — semis use SOXX, others XLK
		// We don't have the exact SIC here, only the bucket. Use XLK as the
		// broader-tech default; semiconductor-specific tickers (SIC 3674) are
		// handled by the explicit override below.
		return "XLK"
	case 9: // Transportation Equipment / Autos
		return "XLI"
	case 10: // Transportation / Airlines
		return "IYT"
	case 11: // Utilities
		return "XLU"
	case 12: // Wholesale & Retail Trade
		return "XLY"
	case 13: // Finance / Banking / Insurance / Real Estate
		return "XLF"
	case 14: // Hotels / Entertainment
		return "XLY"
	case 15: // Business Services
		return "XLK"
	case 16: // Healthcare / Professional Services
		return "XLV"
	default:
		return "SPY" // unknown → broad market
	}
}

// sicSectorETFOverride applies finer-grain ETF choices for SIC codes whose
// sector bucket is too coarse for a meaningful momentum read.
func sicSectorETFOverride(sic int) string {
	switch {
	case sic == 3674: // Semiconductors
		return "SOXX"
	case sic >= 7370 && sic < 7380: // Computer programming / data processing → broad tech
		return "XLK"
	case sic >= 2830 && sic < 2840: // Pharmaceuticals
		return "XBI"
	case sic >= 6020 && sic < 6100: // Banks specifically
		return "KBE"
	}
	return ""
}

// sectorETFForSIC returns the best ETF for a given 4-digit SIC code, preferring
// fine-grained overrides over the broad sector bucket default.
func sectorETFForSIC(sic int) string {
	if etf := sicSectorETFOverride(sic); etf != "" {
		return etf
	}
	return sicSectorETF(sic)
}

// sectorMomentumCache memoises ETF return calculations across the run so that
// 200 stocks fetching XLK only hit the network once. Key: ETF ticker.
type sectorMomentumCache struct {
	mu      sync.Mutex
	results map[string]*SectorMomentum
}

var globalSectorCache = &sectorMomentumCache{results: map[string]*SectorMomentum{}}

// FetchSectorMomentum returns the sector-ETF momentum for the given SIC code.
// Uses Yahoo Finance (works for all SPDR/index ETFs and is rate-limit-friendly
// at the volumes we run at). Cached per ETF for the lifetime of the process.
func FetchSectorMomentum(sic int, httpClient *http.Client) *SectorMomentum {
	etf := sectorETFForSIC(sic)
	if etf == "" {
		return nil
	}

	globalSectorCache.mu.Lock()
	if cached, ok := globalSectorCache.results[etf]; ok {
		globalSectorCache.mu.Unlock()
		return cached
	}
	globalSectorCache.mu.Unlock()

	// Use the Yahoo provider directly (works for ETFs; Nasdaq's API doesn't).
	yp := &YahooPriceProvider{httpClient: httpClient}
	prices, err := yp.FetchPriceHistory(etf)
	if err != nil || len(prices) == 0 {
		return &SectorMomentum{ETF: etf}
	}

	now := prices[len(prices)-1]
	out := &SectorMomentum{ETF: etf}

	if past, ok := closestPrice(prices, now.Date.AddDate(0, 0, -30)); ok && past != 0 {
		v := pctChange(past, now.Close)
		out.Ret1M = &v
	}
	if past, ok := closestPrice(prices, now.Date.AddDate(0, 0, -90)); ok && past != 0 {
		v := pctChange(past, now.Close)
		out.Ret3M = &v
	}

	globalSectorCache.mu.Lock()
	globalSectorCache.results[etf] = out
	globalSectorCache.mu.Unlock()
	return out
}

// SectorMomentumAt returns the sector-ETF return for a *historical* point in
// time, ending at `asOf`. Used by the backtest harness to score signals as
// they would have been at a past announcement date — no caching, since the
// `asOf` parameter varies per call.
func SectorMomentumAt(sic int, asOf time.Time, httpClient *http.Client) *SectorMomentum {
	etf := sectorETFForSIC(sic)
	if etf == "" {
		return nil
	}
	yp := &YahooPriceProvider{httpClient: httpClient}
	prices, err := yp.FetchPriceHistory(etf)
	if err != nil || len(prices) == 0 {
		return &SectorMomentum{ETF: etf}
	}
	asOfClose, ok := closestPrice(prices, asOf)
	if !ok || asOfClose == 0 {
		return &SectorMomentum{ETF: etf}
	}
	out := &SectorMomentum{ETF: etf}
	if past, ok := closestPrice(prices, asOf.AddDate(0, 0, -30)); ok && past != 0 {
		v := pctChange(past, asOfClose)
		out.Ret1M = &v
	}
	if past, ok := closestPrice(prices, asOf.AddDate(0, 0, -90)); ok && past != 0 {
		v := pctChange(past, asOfClose)
		out.Ret3M = &v
	}
	return out
}
