package events

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/indicators"
	"quarterly_results/internal/recommend"
	"quarterly_results/internal/sector"
	"quarterly_results/internal/source/sec"
	"quarterly_results/internal/source/yahoo"
	"quarterly_results/internal/tracking"
)

// marketBenchmark is the market-adjusted drift benchmark.
const marketBenchmark = "SPY"

var newYork = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}()

// Builder assembles a multi-year event history for one stock from SEC filings
// and Yahoo prices only — no consensus estimates, options, scrapers or LLM —
// so a 1,000-stock, 10-year universe can be built in one run.
type Builder struct {
	sec        *sec.Client
	httpClient *http.Client
	since      time.Time
	priceRange string

	mu    sync.Mutex
	bench map[string][]domain.PricePoint // sector ETF / SPY histories, fetched once
}

// NewBuilder covers releases filed in the last `years` years.
func NewBuilder(secClient *sec.Client, httpClient *http.Client, years int, now time.Time) *Builder {
	return &Builder{
		sec:        secClient,
		httpClient: httpClient,
		since:      now.AddDate(-years, 0, 0),
		priceRange: fmt.Sprintf("%dy", years),
		bench:      map[string][]domain.PricePoint{},
	}
}

// Build returns one event per quarterly earnings release since b.since.
func (b *Builder) Build(symbol string, now time.Time) ([]Event, error) {
	history, err := b.sec.FetchQuarterlyHistory(symbol)
	if err != nil {
		return nil, fmt.Errorf("SEC history: %w", err)
	}
	history = withEPS(history)
	if len(history) == 0 {
		return nil, fmt.Errorf("no quarterly EPS in SEC XBRL")
	}
	releases, err := b.sec.FetchEarningsReleases(symbol, b.since.Format("2006-01-02"))
	if err != nil {
		return nil, fmt.Errorf("SEC releases: %w", err)
	}
	if len(releases) == 0 {
		return nil, fmt.Errorf("no Item 2.02 8-Ks since %s", b.since.Format("2006-01-02"))
	}
	cik, _ := b.sec.CIK(symbol)
	sic, _, _ := b.sec.FetchEntitySIC(symbol)
	etfName := sector.ETFForSIC(sic)

	prices, splits, err := yahoo.FetchChartWithSplits(b.httpClient, symbol, b.priceRange)
	if err != nil {
		return nil, fmt.Errorf("prices: %w", err)
	}
	reported := history
	history, tainted := splitAdjust(history, splits)
	etf := b.benchmark(etfName)
	market := b.benchmark(marketBenchmark)

	used := map[string]bool{}
	var out []Event
	for i, q := range history {
		rel, ok := matchRelease(q, releases, used)
		if !ok {
			continue
		}
		used[rel.Accession] = true
		announce, err := time.Parse("2006-01-02", rel.FilingDate)
		if err != nil || announce.Before(b.since) {
			continue
		}
		timing := ReleaseTiming(rel)
		reactionDay, priorClose, ok := indicators.ReactionDay(announce, timing, prices)
		if !ok {
			continue
		}
		f, ok := Features(announce, reactionDay, priorClose, nil, prices, etf, etfName)
		if !ok {
			continue
		}
		f.SUE = SUE(history, tainted, i)
		f.ReleaseTiming = timing
		if v, ok := indicators.ReturnBetween(market, reactionDay.AddDate(0, 0, -30), reactionDay); ok {
			f.MarketRet1M = &v
		}
		rules := recommend.ComputeDrift(f, nil)
		out = append(out, Event{
			EventID:     strings.ToUpper(symbol) + "|" + f.AnnouncementDate,
			Symbol:      symbol,
			Exchange:    "US",
			CIK:         cik,
			SIC:         sic,
			Period:      q.Period,
			EPS:         q.EPS, // on today's share basis (split-adjusted)
			EPSReported: reported[i].EPS,
			SplitWindow: tainted[i],
			Accession:   rel.Accession,
			PrimaryDoc:  rel.PrimaryDoc,
			ExportedAt:  now.UTC().Truncate(time.Second),
			Features:    *f,
			Rules:       tracking.Prediction{Label: rules.Label, Score: rules.Score, Confidence: rules.Confidence},
			Labels:      LabelsAt(announce, reactionDay, prices, etf, market),
		})
	}
	return out, nil
}

// benchmark returns a benchmark's price history, fetched once per Builder.
func (b *Builder) benchmark(sym string) []domain.PricePoint {
	if sym == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if p, ok := b.bench[sym]; ok {
		return p
	}
	p, _ := yahoo.FetchChartHistoryRange(b.httpClient, sym, b.priceRange) // nil on error: labels stay unadjusted
	b.bench[sym] = p
	return p
}

// withEPS drops periods that carry only revenue: FetchQuarterlyHistory merges
// EPS and revenue by period, and a revenue-only period has EPS 0.
func withEPS(history []domain.QuarterActual) []domain.QuarterActual {
	out := history[:0:0]
	for _, q := range history {
		if q.EPS != 0 {
			out = append(out, q)
		}
	}
	return out
}

// matchRelease finds a quarter's earnings release: the last unused Item 2.02
// 8-K filed from 10 days after the period end up to the 10-Q filing (the
// release precedes or accompanies the 10-Q; "last" skips an earlier
// preliminary release, e.g. at a January investor conference). When the 10-Q
// date is missing or implausible — XBRL can attribute a period to a later
// filing that restated it — the window ends 80 days after the period end.
func matchRelease(q domain.QuarterActual, releases []sec.EarningsRelease, used map[string]bool) (sec.EarningsRelease, bool) {
	period, err := time.Parse("2006-01-02", q.Period)
	if err != nil {
		return sec.EarningsRelease{}, false
	}
	start := period.AddDate(0, 0, 10).Format("2006-01-02")
	end := period.AddDate(0, 0, 80).Format("2006-01-02")
	if q.FilingDate >= start && q.FilingDate <= end {
		end = q.FilingDate
	}
	var best sec.EarningsRelease
	found := false
	for _, r := range releases {
		if r.FilingDate >= start && r.FilingDate <= end && !used[r.Accession] {
			best, found = r, true
		}
	}
	return best, found
}

// ReleaseTiming infers the session from the 8-K's EDGAR acceptance time.
// Used for history (--build-dataset) and live, so features match training.
// Companies release outside market hours and often file hours later (Apple
// releases 16:30 ET and files ~20:30; JPM releases ~07:00 and files ~10:30),
// so acceptance before 16:00 ET means a pre-open release and later means
// after the close. "" (unknown) leaves the reaction day to the price-gap
// heuristic in indicators.ReactionDay.
func ReleaseTiming(r sec.EarningsRelease) string {
	if r.Accepted.IsZero() {
		return ""
	}
	et := r.Accepted.In(newYork)
	if et.Format("2006-01-02") > r.FilingDate || et.Hour() >= 16 {
		return "amc"
	}
	return "bmo"
}

// splitAdjust restates EPS on today's share basis. Each XBRL value is on the
// basis of the filing it came from (q.FilingDate — possibly a later filing
// that already restated it), so it is divided by every split after that date.
// tainted marks quarters ending from 100 days before to a year after a split:
// SEC derives Q4 as annual − (Q1+Q2+Q3), and when the fiscal year (or its
// 10-K filing) spans the split the parts are on different share bases —
// e.g. GOOGL's derived Q4 2022 EPS of −22.33 after its July 2022 20:1 split.
// Those values can't be trusted for SUE.
func splitAdjust(history []domain.QuarterActual, splits []yahoo.Split) ([]domain.QuarterActual, []bool) {
	out := make([]domain.QuarterActual, len(history))
	tainted := make([]bool, len(history))
	for i, q := range history {
		out[i] = q
		basis := q.FilingDate
		period, perr := time.Parse("2006-01-02", q.Period)
		if basis == "" && perr == nil {
			basis = period.AddDate(0, 0, 45).Format("2006-01-02")
		}
		for _, sp := range splits {
			if sp.Date.Format("2006-01-02") > basis {
				out[i].EPS /= sp.Ratio
			}
			if perr == nil && !period.Before(sp.Date.AddDate(0, 0, -100)) && !period.After(sp.Date.AddDate(1, 0, 0)) {
				tainted[i] = true
			}
		}
	}
	return out, tainted
}

// SUE is quarter i's standardized unexpected earnings under a seasonal random
// walk: (EPS_i − EPS a year earlier) ÷ the sample σ of up to 8 earlier such
// differences. Only quarters before i are used, so it is known at quarter i's
// release. EPS must be on one share basis (see splitAdjust); quarters marked
// tainted are never used. nil with fewer than 4 earlier differences or σ ≈ 0.
func SUE(history []domain.QuarterActual, tainted []bool, i int) *float64 {
	bad := func(j int) bool { return j < len(tainted) && tainted[j] }
	k, ok := yearAgo(history, i)
	if !ok || bad(i) || bad(k) {
		return nil
	}
	cur := history[i].EPS - history[k].EPS
	var diffs []float64
	for j := i - 1; j >= 0 && len(diffs) < 8; j-- {
		if kk, ok := yearAgo(history, j); ok && !bad(j) && !bad(kk) {
			diffs = append(diffs, history[j].EPS-history[kk].EPS)
		}
	}
	if len(diffs) < 4 {
		return nil
	}
	var mean float64
	for _, d := range diffs {
		mean += d
	}
	mean /= float64(len(diffs))
	var ss float64
	for _, d := range diffs {
		ss += (d - mean) * (d - mean)
	}
	sigma := math.Sqrt(ss / float64(len(diffs)-1))
	if sigma < 1e-9 {
		return nil
	}
	v := cur / sigma
	return &v
}

// yearAgo finds the quarter whose period end is ~1 year before quarter j's.
func yearAgo(history []domain.QuarterActual, j int) (int, bool) {
	pj, err := time.Parse("2006-01-02", history[j].Period)
	if err != nil {
		return 0, false
	}
	for k := j - 1; k >= 0; k-- {
		pk, err := time.Parse("2006-01-02", history[k].Period)
		if err != nil {
			continue
		}
		if days := pj.Sub(pk).Hours() / 24; days >= 340 && days <= 390 {
			return k, true
		}
	}
	return 0, false
}
