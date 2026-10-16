package peers

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/indicators"
	"quarterly_results/internal/logging"
	"quarterly_results/internal/provider"
	"quarterly_results/internal/sector"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/source/sec"
)

// reactionDayFor returns the correct reaction day and prior-close price given:
//   - announceDate: the date the company released earnings (8-K or calendar date)
//   - earningsTime: "bmo" (before market open) or "amc"/"" (after close or unknown)
//   - prices: sorted price history (oldest → newest)
//
// BMO: earnings known before market opens → market reacts that same day.
//
//	priorClose = close on (announceDate − 1), reactionDay = announceDate.
//
// AMC / unknown: earnings known after close → market reacts next working day.
//
//	priorClose = close on announceDate, reactionDay = exchange.NextWorkingDay(announceDate).
//
// Falls back to price-gap heuristic when earningsTime is "" and both days have data.
func reactionDayFor(announceDate time.Time, earningsTime string, prices []domain.PricePoint) (
	reactionDay time.Time, priorClose float64, ok bool,
) {
	dayBeforeClose, okDayBefore := indicators.ClosestPrice(prices, announceDate.AddDate(0, 0, -1))
	announceDayClose, okAnnounce := indicators.ClosestPrice(prices, announceDate)

	switch earningsTime {
	case "bmo":
		if !okDayBefore || dayBeforeClose == 0 {
			return time.Time{}, 0, false
		}
		return announceDate, dayBeforeClose, true

	case "amc":
		if !okAnnounce || announceDayClose == 0 {
			return time.Time{}, 0, false
		}
		return exchange.NextWorkingDay(announceDate), announceDayClose, true

	default:
		// Heuristic: compare absolute gap at announce-day open vs next-day open.
		// Whichever side has the larger gap is likely the reaction day.
		announceDayOpen := indicators.OpenOnDate(prices, announceDate)
		nextDay := exchange.NextWorkingDay(announceDate)
		nextDayOpen := indicators.OpenOnDate(prices, nextDay)

		if okDayBefore && dayBeforeClose > 0 && announceDayOpen > 0 &&
			okAnnounce && announceDayClose > 0 && nextDayOpen > 0 {
			gapAnnounce := math.Abs(announceDayOpen-dayBeforeClose) / dayBeforeClose
			gapNextDay := math.Abs(nextDayOpen-announceDayClose) / announceDayClose
			if gapAnnounce > gapNextDay {
				return announceDate, dayBeforeClose, okDayBefore
			}
			return nextDay, announceDayClose, okAnnounce
		}
		// Not enough price data — prefer AMC assumption (safer default).
		if okAnnounce && announceDayClose > 0 {
			return exchange.NextWorkingDay(announceDate), announceDayClose, true
		}
		return time.Time{}, 0, false
	}
}

// Finder resolves sector peers for a target stock. It owns the data
// sources the peer search needs so it can be wired independently of the Enricher.
type Finder struct {
	httpClient *http.Client // Nasdaq calendar lookups
	yahooHTTP  *http.Client // Yahoo similar-symbol lookups (shared cookie session)
	sec        *sec.Client
	prices     provider.PriceProvider
	overrides  *OverrideStore // nil disables the override path
}

func NewFinder(httpClient, yahooHTTP *http.Client, secClient *sec.Client, prices provider.PriceProvider, overrides *OverrideStore) *Finder {
	return &Finder{httpClient: httpClient, yahooHTTP: yahooHTTP, sec: secClient, prices: prices, overrides: overrides}
}

// Find finds sector peers that already reported results for the same
// fiscal quarter as targetPeriodEnd. It:
//  1. Fetches the Nasdaq calendar for ±75 days around targetPeriodEnd to find
//     companies that reported around the same calendar quarter.
//  2. Filters to market cap > $10B and excludes targetSymbol.
//  3. Takes the top 15 by market cap, then fetches their SIC codes concurrently.
//  4. Keeps those in the same broad sector as targetSIC (0 = skip sector filter).
//  5. For each matching peer: fetches quarterly actuals + price history, then
//     finds the quarter whose period end is within ±70 days of targetPeriodEnd.
//  6. Returns up to 8 peers sorted by market cap (largest first).
func (f *Finder) Find(
	targetSymbol string,
	targetSIC int,
	targetPeriodEnd time.Time,
	targetCapB float64,
) []domain.PeerResult {
	const (
		calWindowDays   = 75 // ± days around targetPeriodEnd to scan for peer reporters
		periodMatchDays = 45 // max |days| between peer period end and target period end
		maxCandidates   = 15 // how many top-cap candidates to enrich
		maxPeers        = 8  // maximum peers to return

		// Relative market cap bounds: peer must be within 0.05×–30× of target.
		// Floor of $1B avoids data-sparse micro-caps.
		peerCapMinRatio = 0.05
		peerCapMaxRatio = 30.0
		absMinPeerCapB  = 1.0
	)

	// Derive absolute peer cap bounds from the target's own market cap.
	minPeerCapB := math.Max(absMinPeerCapB, targetCapB*peerCapMinRatio)
	maxPeerCapB := targetCapB * peerCapMaxRatio

	// ── Override path: curated / Yahoo-cached / LLM-cached peer list takes
	// precedence over SIC sector matching. SIC groups are too coarse for many
	// modern tickers (e.g. NBIS lands with MU/AMD/INTC instead of CRWV/NET).
	var overridePeers []string
	if f.overrides != nil {
		var src string
		overridePeers, src = resolvePeerOverrides(context.Background(), f.overrides, targetSymbol, f.yahooHTTP)
		if len(overridePeers) > 0 {
			logging.Logf("Using %d %s peers for %s: %s", len(overridePeers), src, targetSymbol, strings.Join(overridePeers, ", "))
		}
	}
	overrideMode := len(overridePeers) > 0
	overrideSet := map[string]bool{}
	for _, p := range overridePeers {
		overrideSet[strings.ToUpper(p)] = true
	}

	nc := nasdaq.NewClientWithHTTP(f.httpClient)
	from := targetPeriodEnd.AddDate(0, 0, -calWindowDays)
	to := time.Now() // only past reporters

	// Cap `to` to avoid fetching future calendar dates (no results, just noise).
	if cap := targetPeriodEnd.AddDate(0, 0, calWindowDays); cap.Before(to) {
		to = cap
	}

	events, calMap, _ := nc.FetchEarningsCalendar(from, to)

	// Filter: past only, within relative cap band, not the target symbol.
	// In override mode, skip the cap-band filter and take only the override
	// tickers (cap is implicit in the human-curated list).
	today := time.Now().Format("2006-01-02")
	type candidate struct {
		sym   string
		name  string
		capB  float64
		date  string
		time_ string
	}
	var candidates []candidate
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Symbol == targetSymbol || seen[ev.Symbol] {
			continue
		}
		if ev.Date > today {
			continue // hasn't reported yet
		}
		capB := ev.MarketCap / 1e9
		if overrideMode {
			if !overrideSet[strings.ToUpper(ev.Symbol)] {
				continue
			}
		} else {
			if capB < minPeerCapB || capB > maxPeerCapB {
				continue
			}
		}
		seen[ev.Symbol] = true
		candidates = append(candidates, candidate{
			sym:   ev.Symbol,
			name:  ev.Name,
			capB:  capB,
			date:  ev.Date,
			time_: ev.Time,
		})
		_ = calMap // calendar row data available if needed later
	}

	// Sort by market cap descending and cap at maxCandidates (override mode
	// skips this cap because the user-curated list is already short).
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].capB > candidates[j].capB
	})
	if !overrideMode && len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	targetSectorGroup := sector.SICGroup(targetSIC)

	// Enrich each candidate concurrently.
	results := make([]domain.PeerResult, 0, len(candidates))
	var mu sync.Mutex
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, cand := range candidates {
		wg.Add(1)
		go func(c candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// ── 1. SIC check (skipped in override mode — the override list
			//    is human/Yahoo/LLM-curated and authoritative).
			if !overrideMode && targetSIC != 0 {
				peerSIC, _, err := f.sec.FetchEntitySIC(c.sym)
				if err != nil || sector.SICGroup(peerSIC) != targetSectorGroup {
					return // different sector — skip
				}
			}

			// ── 2. Quarterly actuals ──────────────────────────────────────────
			history, err := f.sec.FetchQuarterlyActuals(c.sym)
			if err != nil || len(history) == 0 {
				return
			}

			// Find the quarter whose period end is closest to targetPeriodEnd.
			var bestQ *domain.QuarterActual
			bestDiff := time.Duration(math.MaxInt64)
			for i := range history {
				qEnd, perr := time.Parse("2006-01-02", history[i].Period)
				if perr != nil {
					continue
				}
				diff := qEnd.Sub(targetPeriodEnd)
				if diff < 0 {
					diff = -diff
				}
				maxDiff := time.Duration(periodMatchDays) * 24 * time.Hour
				if diff <= maxDiff && diff < bestDiff {
					bestDiff = diff
					bestQ = &history[i]
				}
			}
			if bestQ == nil {
				return // no matching quarter
			}

			// Use 8-K announcement date when available, fall back to calendar date.
			announceDate := c.date
			if bestQ.FilingDate != "" {
				// Re-fetch announcement dates using 8-K lookup (same as main flow).
				if annDates, err2 := f.sec.FetchEarningsAnnouncementDates(c.sym, []domain.QuarterActual{*bestQ}); err2 == nil {
					if ad, ok := annDates[bestQ.Period]; ok {
						announceDate = ad
					}
				}
			}
			annTime, err := time.Parse("2006-01-02", announceDate)
			if err != nil {
				return
			}

			// ── 3. Price history ──────────────────────────────────────────────
			prices, err := f.prices.FetchPriceHistory(c.sym)
			if err != nil || len(prices) == 0 {
				return
			}

			// ── 4. EPS estimate at announcement time ─────────────────────────
			epsEst, _ := f.prices.FetchHistoricalEPSEstimate(c.sym, annTime)

			// ── 5. Price reaction (BMO/AMC-aware) ─────────────────────────────
			rxnDay, priorClose, okRxn := reactionDayFor(annTime, c.time_, prices)
			if !okRxn || priorClose == 0 {
				return
			}
			// Skip if reaction day hasn't happened yet (shouldn't happen due to
			// the `date > today` filter, but be defensive).
			now := prices[len(prices)-1].Date
			if rxnDay.After(now) {
				return
			}
			rxnClose, okClose := indicators.ClosestPrice(prices, rxnDay)
			if !okClose {
				return
			}
			rxnOpen := indicators.OpenOnDate(prices, rxnDay)

			pr := domain.PeerResult{
				Symbol:           c.sym,
				CompanyName:      c.name,
				MarketCapB:       c.capB,
				EarningsDate:     c.date,
				EarningsTime:     c.time_,
				Period:           bestQ.Period,
				EPSActual:        bestQ.EPS,
				RevActual:        bestQ.Revenue,
				EPSEstimate:      epsEst,
				AnnouncementDate: announceDate,
				ReactionDay:      rxnDay.Format("2006-01-02"),
				PriorClose:       priorClose,
				ReactionOpen:     rxnOpen,
				ReactionClose:    rxnClose,
				GapRetPct:        indicators.PctChange(priorClose, rxnOpen),
				DayRetPct:        indicators.PctChange(priorClose, rxnClose),
			}
			if epsEst != 0 {
				v := indicators.PctChange(epsEst, bestQ.EPS)
				pr.EPSBeatPct = &v
			}

			// Valuation ratios — computed from the peer's own data so that
			// the target stock's industry-median PE/PS can be derived from
			// genuine peer comparables. PE_TTM uses current price ÷ TTM EPS;
			// PS uses current market cap ÷ TTM revenue.
			if len(history) >= 4 {
				idx := -1
				for i := range history {
					if history[i].Period == bestQ.Period {
						idx = i
						break
					}
				}
				if idx >= 3 {
					var ttmEPS, ttmRev float64
					for j := idx - 3; j <= idx; j++ {
						ttmEPS += history[j].EPS
						ttmRev += history[j].Revenue
					}
					if currentPx := prices[len(prices)-1].Close; currentPx > 0 && ttmEPS > 0 {
						pr.PE_TTM = currentPx / ttmEPS
					}
					if c.capB > 0 && ttmRev > 0 {
						pr.PS = (c.capB * 1e9) / ttmRev
					}
				}
			}

			mu.Lock()
			results = append(results, pr)
			mu.Unlock()
		}(cand)
	}
	wg.Wait()

	// Sort by market cap descending, cap at maxPeers.
	sort.Slice(results, func(i, j int) bool {
		return results[i].MarketCapB > results[j].MarketCapB
	})
	if len(results) > maxPeers {
		results = results[:maxPeers]
	}
	return results
}
