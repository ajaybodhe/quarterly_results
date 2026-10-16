package provider

import (
	"net/http"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/source/sec"
)

// USFinancials implements FinancialsProvider for US stocks.
// Filing data comes from SEC EDGAR; forward EPS from the Nasdaq forecast API.
type USFinancials struct {
	sec        *sec.Client
	httpClient *http.Client
}

func (p *USFinancials) FetchQuarterlyActuals(symbol string) ([]domain.QuarterActual, error) {
	return p.sec.FetchQuarterlyActuals(symbol)
}

func (p *USFinancials) FetchEarningsAnnouncementDates(symbol string, history []domain.QuarterActual) (map[string]string, error) {
	return p.sec.FetchEarningsAnnouncementDates(symbol, history)
}

func (p *USFinancials) FetchInsiderActivity(symbol string, since time.Time) (*domain.InsiderSummary, error) {
	return p.sec.FetchInsiderActivity(symbol, since)
}

func (p *USFinancials) FetchMaterialEvents(symbol string, since time.Time) ([]domain.MaterialEvent, error) {
	return p.sec.FetchMaterialEvents(symbol, since)
}

func (p *USFinancials) FetchEntitySIC(symbol string) (int, string, error) {
	return p.sec.FetchEntitySIC(symbol)
}

// FetchForwardEPS delegates to the Nasdaq analyst earnings-forecast endpoint.
func (p *USFinancials) FetchForwardEPS(symbol string) ([]domain.ForwardQuarter, error) {
	return nasdaq.FetchForwardEPS(p.httpClient, symbol)
}
