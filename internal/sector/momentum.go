package sector

import (
	"net/http"
	"sync"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/indicators"
	"quarterly_results/internal/source/yahoo"
)

// Momentum holds a stock's sector-ETF reference returns at a single
// point in time. Used as a coarse "is this sector in favour?" gauge by the
// recommendation system.
type Momentum struct {
	ETF   string   // ticker symbol, e.g. "XLK"
	Ret1M *float64 // calendar-30-day return ending today, %
	Ret3M *float64 // calendar-90-day return ending today, %
}

// sicSectorETF maps the broad sector buckets defined in SICGroup() to a
// representative SPDR (or industry) ETF. Returns "" for unknown buckets so the
// caller can skip the fetch cleanly.
//
// Mapping rationale: prefer the most liquid, broadly-traded sector ETF so the
// 1M return is a fair proxy for "is sector capital flowing in or out?". For
// niche groups (e.g. semiconductors) we use a focused ETF rather than the
// parent broad-tech ETF so chip-specific moves register.
func sicSectorETF(sic int) string {
	switch SICGroup(sic) {
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
	case sic == 2836: // Biological products → biotech
		return "XBI"
	case sic >= 2830 && sic < 2840: // Pharmaceutical preparations (JNJ, PFE, MRK) → health care
		return "XLV"
	case sic == 6324: // Hospital & medical service plans (UNH, ELV, CI) → health care
		return "XLV"
	case sic >= 6020 && sic < 6100: // Banks specifically
		return "KBE"
	case sic == 6798 || (sic >= 6500 && sic < 6560): // REITs and real estate operators
		return "XLRE"
	case sic >= 4800 && sic < 4900: // Telephone, cable and other communications
		return "XLC"
	case sic == 5331 || sic == 5411 || sic == 5912: // Variety, grocery and drug stores → staples
		return "XLP"
	case sic >= 8700 && sic < 8750: // Engineering, accounting, research, management services
		return "XLI"
	}
	return ""
}

// sectorETFForSIC returns the best ETF for a given 4-digit SIC code, preferring
// fine-grained overrides over the broad sector bucket default.
// ETFForSIC returns the sector ETF used as the benchmark for a SIC code
// ("SPY" when the code is unknown).
func ETFForSIC(sic int) string { return sectorETFForSIC(sic) }

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
	results map[string]*Momentum
}

var globalSectorCache = &sectorMomentumCache{results: map[string]*Momentum{}}

// etfPriceCache memoises ETF daily price histories for the process, so
// momentum, the backtest and drift labels share one download per ETF.
var etfPriceCache = struct {
	mu     sync.Mutex
	prices map[string][]domain.PricePoint
}{prices: map[string][]domain.PricePoint{}}

// ETFPrices returns the daily price history for a sector ETF (oldest →
// newest), fetched once per process.
func ETFPrices(etf string, httpClient *http.Client) ([]domain.PricePoint, error) {
	etfPriceCache.mu.Lock()
	cached, ok := etfPriceCache.prices[etf]
	etfPriceCache.mu.Unlock()
	if ok {
		return cached, nil
	}
	// Use the Yahoo provider directly (works for ETFs; Nasdaq's API doesn't).
	prices, err := yahoo.NewPriceProvider(httpClient, exchange.Config{}).FetchPriceHistory(etf)
	if err != nil {
		return nil, err
	}
	etfPriceCache.mu.Lock()
	etfPriceCache.prices[etf] = prices
	etfPriceCache.mu.Unlock()
	return prices, nil
}

// FetchMomentum returns the sector-ETF momentum for the given SIC code.
// Uses Yahoo Finance (works for all SPDR/index ETFs and is rate-limit-friendly
// at the volumes we run at). Cached per ETF for the lifetime of the process.
func FetchMomentum(sic int, httpClient *http.Client) *Momentum {
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

	prices, err := ETFPrices(etf, httpClient)
	if err != nil || len(prices) == 0 {
		return &Momentum{ETF: etf}
	}

	now := prices[len(prices)-1]
	out := &Momentum{ETF: etf}

	if past, ok := indicators.ClosestPrice(prices, now.Date.AddDate(0, 0, -30)); ok && past != 0 {
		v := indicators.PctChange(past, now.Close)
		out.Ret1M = &v
	}
	if past, ok := indicators.ClosestPrice(prices, now.Date.AddDate(0, 0, -90)); ok && past != 0 {
		v := indicators.PctChange(past, now.Close)
		out.Ret3M = &v
	}

	globalSectorCache.mu.Lock()
	globalSectorCache.results[etf] = out
	globalSectorCache.mu.Unlock()
	return out
}

// MomentumAt returns the sector-ETF return for a *historical* point in
// time, ending at `asOf`. Used by the backtest harness to score signals as
// they would have been at a past announcement date. The result isn't cached
// because `asOf` varies per call; the ETF price history is (see ETFPrices).
func MomentumAt(sic int, asOf time.Time, httpClient *http.Client) *Momentum {
	etf := sectorETFForSIC(sic)
	if etf == "" {
		return nil
	}
	prices, err := ETFPrices(etf, httpClient)
	if err != nil || len(prices) == 0 {
		return &Momentum{ETF: etf}
	}
	asOfClose, ok := indicators.ClosestPrice(prices, asOf)
	if !ok || asOfClose == 0 {
		return &Momentum{ETF: etf}
	}
	out := &Momentum{ETF: etf}
	if past, ok := indicators.ClosestPrice(prices, asOf.AddDate(0, 0, -30)); ok && past != 0 {
		v := indicators.PctChange(past, asOfClose)
		out.Ret1M = &v
	}
	if past, ok := indicators.ClosestPrice(prices, asOf.AddDate(0, 0, -90)); ok && past != 0 {
		v := indicators.PctChange(past, asOfClose)
		out.Ret3M = &v
	}
	return out
}

// SICGroup maps a 4-digit SIC code to a coarse sector so that, for
// example, AAPL (3571), MSFT (7372), and GOOG (7370) all land in "Technology".
// Returns an int used only for equality comparison.
func SICGroup(sic int) int {
	switch {
	case sic >= 100 && sic < 1000:
		return 1 // Agriculture
	case sic >= 1000 && sic < 1500:
		return 2 // Mining / Oil & Gas Exploration
	case sic >= 1500 && sic < 1800:
		return 3 // Construction
	case sic >= 2000 && sic < 2800:
		return 4 // Food / Beverage / Tobacco / Apparel
	case sic >= 2800 && sic < 2900:
		return 5 // Chemicals / Pharma (SIC 28xx)
	case sic >= 2900 && sic < 3000:
		return 6 // Petroleum Refining
	case sic >= 3000 && sic < 3500:
		return 7 // Industrial Manufacturing
	case sic >= 3500 && sic < 3700:
		return 8 // Computers & Electronics (hardware, semiconductors, software)
	case sic >= 3700 && sic < 4000:
		return 9 // Transportation Equipment / Autos
	case sic >= 4000 && sic < 4900:
		return 10 // Transportation / Airlines / Shipping
	case sic >= 4900 && sic < 5000:
		return 11 // Utilities
	case sic >= 5000 && sic < 6000:
		return 12 // Wholesale & Retail Trade
	case sic >= 6000 && sic < 6800:
		return 13 // Finance / Banking / Insurance / Real Estate
	// 73xx spans Hotels/Entertainment, Business Services, and Computer Services.
	// 7370–7379 (Computer Programming, Data Processing) belongs with tech (group 8).
	// Must be checked before the broader 7300–7400 range.
	case sic >= 7370 && sic < 7380:
		return 8 // Computer Services / Software → same group as hardware
	case sic >= 7000 && sic < 7400:
		return 14 // Hotels / Entertainment / Amusements
	case sic >= 7400 && sic < 8000:
		return 15 // Business Services
	case sic >= 8000 && sic < 9000:
		return 16 // Healthcare / Professional Services
	default:
		return 0 // Unknown / Government
	}
}
