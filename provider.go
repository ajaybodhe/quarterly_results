package main

import (
	"net/http"
	"time"
)

// CalendarProvider fetches an earnings calendar for a date range.
type CalendarProvider interface {
	FetchEarningsCalendar(from, to time.Time) ([]EarningsEvent, map[string]CalendarRow, error)
}

// PriceProvider fetches daily OHLC price history for a symbol.
type PriceProvider interface {
	// FetchPriceHistory returns ~18 months of daily closes, sorted oldest→newest.
	FetchPriceHistory(symbol string) ([]pricePoint, error)
	// FetchHistoricalEPSEstimate returns the consensus EPS estimate for symbol on the
	// given past announcement date. Returns (0, nil) when unavailable.
	FetchHistoricalEPSEstimate(symbol string, announcementDate time.Time) (float64, error)
}

// FinancialsProvider fetches fundamental financial data for a symbol.
// Non-US implementations return nil/empty (never an error) for fields not applicable
// to their filing system (FetchMaterialEvents, FetchEntitySIC).
type FinancialsProvider interface {
	FetchQuarterlyActuals(symbol string) ([]QuarterActual, error)
	FetchEarningsAnnouncementDates(symbol string, history []QuarterActual) (map[string]string, error)
	FetchInsiderActivity(symbol string, since time.Time) (*InsiderSummary, error)
	FetchMaterialEvents(symbol string, since time.Time) ([]MaterialEvent, error)
	FetchEntitySIC(symbol string) (int, string, error)
	FetchForwardEPS(symbol string) ([]ForwardQuarter, error)
}

// HolidayCalendar determines whether a date is a trading day on a specific exchange.
type HolidayCalendar interface {
	IsWorkingDay(t time.Time) bool
	PrevWorkingDay(t time.Time) time.Time
	NextWorkingDay(t time.Time) time.Time
}

// ExchangeProviders bundles concrete provider implementations for a specific exchange.
type ExchangeProviders struct {
	Financials FinancialsProvider
	Prices     PriceProvider
	Holidays   HolidayCalendar
}

// NewProvidersForExchange returns the correct provider bundle for the given exchange.
// US → SEC + Nasdaq price API. International → Finnhub financials + Yahoo price API.
func NewProvidersForExchange(cfg ExchangeConfig, secClient *SECClient, finnhubClient *FinnhubClient, httpClient *http.Client) ExchangeProviders {
	if cfg.Exchange == ExchangeUS {
		return ExchangeProviders{
			Financials: &SECFinancialsProvider{sec: secClient, httpClient: httpClient},
			Prices:     &NasdaqPriceProvider{httpClient: httpClient},
			Holidays:   USHolidayCalendar{},
		}
	}
	return ExchangeProviders{
		Financials: NewFinnhubFinancialsProvider(finnhubClient, cfg),
		Prices:     &YahooPriceProvider{httpClient: httpClient, exCfg: cfg},
		Holidays:   NewHolidayCalendar(cfg),
	}
}
