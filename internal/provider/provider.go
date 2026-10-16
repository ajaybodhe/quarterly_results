package provider

import (
	"net/http"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/source/finnhub"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/source/sec"
	"quarterly_results/internal/source/yahoo"
)

// CalendarProvider fetches an earnings calendar for a date range.
type CalendarProvider interface {
	FetchEarningsCalendar(from, to time.Time) ([]domain.EarningsEvent, map[string]domain.CalendarRow, error)
}

// PriceProvider fetches daily OHLC price history for a symbol.
type PriceProvider interface {
	// FetchPriceHistory returns ~18 months of daily closes, sorted oldest→newest.
	FetchPriceHistory(symbol string) ([]domain.PricePoint, error)
	// FetchHistoricalEPSEstimate returns the consensus EPS estimate for symbol on the
	// given past announcement date. Returns (0, nil) when unavailable.
	FetchHistoricalEPSEstimate(symbol string, announcementDate time.Time) (float64, error)
}

// FinancialsProvider fetches fundamental financial data for a symbol.
// Non-US implementations return nil/empty (never an error) for fields not applicable
// to their filing system (FetchMaterialEvents, FetchEntitySIC).
type FinancialsProvider interface {
	FetchQuarterlyActuals(symbol string) ([]domain.QuarterActual, error)
	FetchEarningsAnnouncementDates(symbol string, history []domain.QuarterActual) (map[string]string, error)
	FetchInsiderActivity(symbol string, since time.Time) (*domain.InsiderSummary, error)
	FetchMaterialEvents(symbol string, since time.Time) ([]domain.MaterialEvent, error)
	FetchEntitySIC(symbol string) (int, string, error)
	FetchForwardEPS(symbol string) ([]domain.ForwardQuarter, error)
}

// ExchangeProviders bundles concrete provider implementations for a specific exchange.
type ExchangeProviders struct {
	Financials FinancialsProvider
	Prices     PriceProvider
	Holidays   exchange.HolidayCalendar
}

// NewProvidersForExchange returns the correct provider bundle for the given exchange.
// US → SEC + Nasdaq price API. International → Finnhub financials + Yahoo price API.
func NewProvidersForExchange(cfg exchange.Config, secClient *sec.Client, finnhubClient *finnhub.Client, httpClient *http.Client) ExchangeProviders {
	if cfg.Exchange == exchange.ExchangeUS {
		return ExchangeProviders{
			Financials: &USFinancials{sec: secClient, httpClient: httpClient},
			Prices:     nasdaq.NewPriceProvider(httpClient),
			Holidays:   exchange.USHolidayCalendar{},
		}
	}
	return ExchangeProviders{
		Financials: finnhub.NewFinancialsProvider(finnhubClient, cfg),
		Prices:     yahoo.NewPriceProvider(httpClient, cfg),
		Holidays:   exchange.NewHolidayCalendar(cfg),
	}
}
