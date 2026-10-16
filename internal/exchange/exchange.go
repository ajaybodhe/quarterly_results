package exchange

import (
	"fmt"
	"strings"
)

// Exchange identifies a stock exchange.
type Exchange string

const (
	ExchangeUS       Exchange = "US"
	ExchangeLSE      Exchange = "LSE"
	ExchangeFSE      Exchange = "FSE"
	ExchangeEuronext Exchange = "EURONEXT"
)

// Config holds all exchange-specific configuration.
type Config struct {
	Exchange      Exchange
	Currency      string // "USD", "GBP", "EUR"
	YahooSuffix   string // ".L", ".DE", ".PA", etc. (empty for US)
	FinnhubExch   string // Finnhub exchange code for /stock/symbol (e.g. "L", "DE", "PA")
	Timezone      string // IANA timezone name
	ReportingFreq string // "quarterly" or "semi-annual"
}

var DefaultUSConfig = Config{
	Exchange:      ExchangeUS,
	Currency:      "USD",
	YahooSuffix:   "",
	FinnhubExch:   "",
	Timezone:      "America/New_York",
	ReportingFreq: "quarterly",
}

// ConfigByName maps the --exchange flag value to an Config.
func ConfigByName(name string) (Config, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "US", "":
		return DefaultUSConfig, nil
	case "LSE":
		return Config{
			Exchange:      ExchangeLSE,
			Currency:      "GBP",
			YahooSuffix:   ".L",
			FinnhubExch:   "L",
			Timezone:      "Europe/London",
			ReportingFreq: "semi-annual",
		}, nil
	case "FSE":
		return Config{
			Exchange:      ExchangeFSE,
			Currency:      "EUR",
			YahooSuffix:   ".DE",
			FinnhubExch:   "DE",
			Timezone:      "Europe/Berlin",
			ReportingFreq: "quarterly",
		}, nil
	case "EURONEXT":
		return Config{
			Exchange:      ExchangeEuronext,
			Currency:      "EUR",
			YahooSuffix:   ".PA", // default to Paris; overridden by ForTicker per suffix
			FinnhubExch:   "PA",
			Timezone:      "Europe/Paris",
			ReportingFreq: "semi-annual",
		}, nil
	default:
		return Config{}, fmt.Errorf("unknown exchange %q — choose US, LSE, FSE, or EURONEXT", name)
	}
}

// ForTicker infers the Config from a Yahoo Finance–style ticker suffix.
// "VOD.L" → LSE, "BMW.DE" → FSE, "AIR.PA"/"ASML.AS"/"ABI.BR" → Euronext.
// Tickers without a recognised suffix default to US.
func ForTicker(symbol string) Config {
	dot := strings.LastIndex(symbol, ".")
	if dot < 0 {
		return DefaultUSConfig
	}
	suffix := strings.ToUpper(symbol[dot:])
	switch suffix {
	case ".L":
		cfg, _ := ConfigByName("LSE")
		return cfg
	case ".DE":
		cfg, _ := ConfigByName("FSE")
		return cfg
	case ".PA":
		return Config{Exchange: ExchangeEuronext, Currency: "EUR", YahooSuffix: ".PA", FinnhubExch: "PA", Timezone: "Europe/Paris", ReportingFreq: "semi-annual"}
	case ".AS":
		return Config{Exchange: ExchangeEuronext, Currency: "EUR", YahooSuffix: ".AS", FinnhubExch: "AS", Timezone: "Europe/Amsterdam", ReportingFreq: "semi-annual"}
	case ".BR":
		return Config{Exchange: ExchangeEuronext, Currency: "EUR", YahooSuffix: ".BR", FinnhubExch: "BR", Timezone: "Europe/Brussels", ReportingFreq: "semi-annual"}
	case ".LS":
		return Config{Exchange: ExchangeEuronext, Currency: "EUR", YahooSuffix: ".LS", FinnhubExch: "LS", Timezone: "Europe/Lisbon", ReportingFreq: "semi-annual"}
	default:
		return DefaultUSConfig
	}
}

// ToYahooSymbol ensures the ticker has the correct Yahoo Finance suffix.
// "VOD" + LSEConfig → "VOD.L"; "VOD.L" + LSEConfig → "VOD.L" (unchanged).
func ToYahooSymbol(ticker string, cfg Config) string {
	if cfg.YahooSuffix == "" {
		return ticker
	}
	if strings.HasSuffix(strings.ToUpper(ticker), strings.ToUpper(cfg.YahooSuffix)) {
		return ticker
	}
	return ticker + cfg.YahooSuffix
}

// BareTickerFromSymbol strips the Yahoo Finance exchange suffix if present.
// "VOD.L" → "VOD", "BMW.DE" → "BMW", "AAPL" → "AAPL".
func BareTickerFromSymbol(symbol string) string {
	dot := strings.LastIndex(symbol, ".")
	if dot < 0 {
		return symbol
	}
	suffix := strings.ToUpper(symbol[dot:])
	switch suffix {
	case ".L", ".DE", ".PA", ".AS", ".BR", ".LS", ".MI", ".MC", ".SW", ".TO", ".AX", ".HK":
		return symbol[:dot]
	}
	return symbol
}

// ToFinnhubSymbol maps a ticker to Finnhub's symbol format.
// US tickers are used bare; international tickers use the bare ticker only
// (Finnhub's earnings calendar and financial endpoints accept bare tickers when
// the exchange is specified via the FinnhubExch field).
func ToFinnhubSymbol(ticker string, cfg Config) string {
	return BareTickerFromSymbol(ticker)
}

// CurrencySymbol returns the currency sign for a given ISO code.
func CurrencySymbol(currency string) string {
	switch currency {
	case "GBP":
		return "£"
	case "EUR":
		return "€"
	default:
		return "$"
	}
}
