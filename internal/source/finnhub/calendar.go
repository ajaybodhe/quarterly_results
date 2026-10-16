package finnhub

import (
	"fmt"
	"strings"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
)

// CalendarClient implements CalendarProvider for international exchanges
// using the Finnhub global earnings calendar API.
type CalendarClient struct {
	client *Client
	cfg    exchange.Config
}

func NewCalendarClient(client *Client, cfg exchange.Config) *CalendarClient {
	return &CalendarClient{client: client, cfg: cfg}
}

// finnhubEarningsEntry is one row from /calendar/earnings.
type finnhubEarningsEntry struct {
	Symbol  string  `json:"symbol"`
	Date    string  `json:"date"` // YYYY-MM-DD
	Hour    string  `json:"hour"` // "bmo" / "amc" / ""
	EPSEst  float64 `json:"epsEstimate"`
	EPSAct  float64 `json:"epsActual"`
	RevEst  float64 `json:"revenueEstimate"`
	Quarter float64 `json:"quarter"`
	Year    int     `json:"year"`
}

// FetchEarningsCalendar implements CalendarProvider.
// It calls the Finnhub earnings calendar for the date range, filters by exchange,
// and returns EarningsEvent list + CalendarRow map keyed by bare symbol.
func (c *CalendarClient) FetchEarningsCalendar(from, to time.Time) ([]domain.EarningsEvent, map[string]domain.CalendarRow, error) {
	url := fmt.Sprintf(
		"https://finnhub.io/api/v1/calendar/earnings?from=%s&to=%s&token=%s",
		from.Format("2006-01-02"), to.Format("2006-01-02"), c.client.apiKey,
	)

	var raw struct {
		EarningsCalendar []finnhubEarningsEntry `json:"earningsCalendar"`
	}
	if err := c.client.getJSON(url, &raw); err != nil {
		return nil, nil, fmt.Errorf("finnhub earnings calendar: %w", err)
	}

	var events []domain.EarningsEvent
	calMap := make(map[string]domain.CalendarRow)

	for _, e := range raw.EarningsCalendar {
		if !c.matchesExchange(e.Symbol) {
			continue
		}

		bare := exchange.BareTickerFromSymbol(e.Symbol)
		yahooSym := exchange.ToYahooSymbol(bare, c.cfg)

		events = append(events, domain.EarningsEvent{
			Symbol:    yahooSym,
			Date:      e.Date,
			Time:      normalizeFinnhubTime(e.Hour),
			MarketCap: 0, // Finnhub free tier doesn't include market cap in this endpoint
			Name:      bare,
		})

		calMap[yahooSym] = domain.CalendarRow{
			Symbol:      yahooSym,
			Name:        bare,
			EPSForecast: e.EPSEst,
			LastYearEPS: 0,
		}
	}

	return events, calMap, nil
}

// matchesExchange returns true when the Finnhub symbol belongs to the configured exchange.
// Finnhub uses suffixes like ".L" (LSE), ".DE" (XETRA), ".PA"/".AS"/".BR"/".LS" (Euronext).
func (c *CalendarClient) matchesExchange(symbol string) bool {
	dot := strings.LastIndex(symbol, ".")
	if dot < 0 {
		return c.cfg.Exchange == exchange.ExchangeUS
	}
	suffix := strings.ToUpper(symbol[dot:])
	switch c.cfg.Exchange {
	case exchange.ExchangeLSE:
		return suffix == ".L"
	case exchange.ExchangeFSE:
		return suffix == ".DE"
	case exchange.ExchangeEuronext:
		switch suffix {
		case ".PA", ".AS", ".BR", ".LS":
			return true
		}
		return false
	default:
		return dot < 0
	}
}

// normalizeFinnhubTime maps Finnhub timing strings to the internal format.
func normalizeFinnhubTime(h string) string {
	switch strings.ToLower(h) {
	case "bmo", "before market open":
		return "bmo"
	case "amc", "after market close":
		return "amc"
	default:
		return ""
	}
}
