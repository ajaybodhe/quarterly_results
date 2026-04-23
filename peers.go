package main

import (
	"math"
	"sort"
	"sync"
	"time"
)

// PeerResult holds earnings results for one sector peer that already reported
// the same fiscal quarter.
type PeerResult struct {
	Symbol       string  `json:"symbol"`
	CompanyName  string  `json:"company_name"`
	MarketCapB   float64 `json:"market_cap_b"`
	EarningsDate string  `json:"earnings_date"`  // YYYY-MM-DD
	EarningsTime string  `json:"earnings_time"`  // "bmo" / "amc" / ""
	Period       string  `json:"period"`         // fiscal quarter end matched to target

	// Consensus EPS estimate vs actual
	EPSEstimate float64  `json:"eps_estimate"`
	EPSActual   float64  `json:"eps_actual"`
	EPSBeatPct  *float64 `json:"eps_beat_pct,omitempty"` // (actual−est)/|est|×100

	// Revenue actual (consensus revenue estimate not available historically)
	RevActual float64 `json:"rev_actual"`

	// Price reaction — reaction day is BMO/AMC-aware (see comment on reactionDayFor)
	AnnouncementDate string  `json:"announcement_date"` // 8-K date or earnings date
	ReactionDay      string  `json:"reaction_day"`      // YYYY-MM-DD
	PriorClose       float64 `json:"prior_close"`       // close before market saw results
	ReactionOpen     float64 `json:"reaction_open"`     // first open after results known
	ReactionClose    float64 `json:"reaction_close"`    // close on reaction day
	GapRetPct        float64 `json:"gap_ret_pct"`       // (open − prior) / prior × 100
	DayRetPct        float64 `json:"day_ret_pct"`       // (close − prior) / prior × 100
}

// sicSectorGroup maps a 4-digit SIC code to a coarse sector so that, for
// example, AAPL (3571), MSFT (7372), and GOOG (7370) all land in "Technology".
// Returns an int used only for equality comparison.
func sicSectorGroup(sic int) int {
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

// reactionDayFor returns the correct reaction day and prior-close price given:
//   - announceDate: the date the company released earnings (8-K or calendar date)
//   - earningsTime: "bmo" (before market open) or "amc"/"" (after close or unknown)
//   - prices: sorted price history (oldest → newest)
//
// BMO: earnings known before market opens → market reacts that same day.
//   priorClose = close on (announceDate − 1), reactionDay = announceDate.
//
// AMC / unknown: earnings known after close → market reacts next working day.
//   priorClose = close on announceDate, reactionDay = nextWorkingDay(announceDate).
//
// Falls back to price-gap heuristic when earningsTime is "" and both days have data.
func reactionDayFor(announceDate time.Time, earningsTime string, prices []pricePoint) (
	reactionDay time.Time, priorClose float64, ok bool,
) {
	dayBeforeClose, okDayBefore := closestPrice(prices, announceDate.AddDate(0, 0, -1))
	announceDayClose, okAnnounce := closestPrice(prices, announceDate)

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
		return nextWorkingDay(announceDate), announceDayClose, true

	default:
		// Heuristic: compare absolute gap at announce-day open vs next-day open.
		// Whichever side has the larger gap is likely the reaction day.
		announceDayOpen := openOnDate(prices, announceDate)
		nextDay := nextWorkingDay(announceDate)
		nextDayOpen := openOnDate(prices, nextDay)

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
			return nextWorkingDay(announceDate), announceDayClose, true
		}
		return time.Time{}, 0, false
	}
}

// fetchPeers finds sector peers that already reported results for the same
// fiscal quarter as targetPeriodEnd. It:
//  1. Fetches the Nasdaq calendar for ±75 days around targetPeriodEnd to find
//     companies that reported around the same calendar quarter.
//  2. Filters to market cap > $10B and excludes targetSymbol.
//  3. Takes the top 15 by market cap, then fetches their SIC codes concurrently.
//  4. Keeps those in the same broad sector as targetSIC (0 = skip sector filter).
//  5. For each matching peer: fetches quarterly actuals + price history, then
//     finds the quarter whose period end is within ±70 days of targetPeriodEnd.
//  6. Returns up to 8 peers sorted by market cap (largest first).
func (e *Enricher) fetchPeers(
	targetSymbol string,
	targetSIC int,
	targetPeriodEnd time.Time,
) []PeerResult {
	const (
		calWindowDays  = 75  // ± days around targetPeriodEnd to scan for peer reporters
		periodMatchDays = 70  // max |days| between peer period end and target period end
		minPeerCapB    = 10.0 // $10B minimum market cap
		maxCandidates  = 15  // how many top-cap candidates to enrich
		maxPeers       = 8   // maximum peers to return
	)

	nc := &NasdaqClient{httpClient: e.httpClient}
	from := targetPeriodEnd.AddDate(0, 0, -calWindowDays)
	to := time.Now() // only past reporters

	// Cap `to` to avoid fetching future calendar dates (no results, just noise).
	if cap := targetPeriodEnd.AddDate(0, 0, calWindowDays); cap.Before(to) {
		to = cap
	}

	events, calMap := nc.FetchEarningsCalendar(from, to)

	// Filter: past only, mktcap > $10B, not the target symbol.
	today := time.Now().Format("2006-01-02")
	type candidate struct {
		sym    string
		name   string
		capB   float64
		date   string
		time_  string
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
		if ev.MarketCap < minPeerCapB*1e9 {
			continue
		}
		seen[ev.Symbol] = true
		candidates = append(candidates, candidate{
			sym:   ev.Symbol,
			name:  ev.Name,
			capB:  ev.MarketCap / 1e9,
			date:  ev.Date,
			time_: ev.Time,
		})
		_ = calMap // calendar row data available if needed later
	}

	// Sort by market cap descending and cap at maxCandidates.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].capB > candidates[j].capB
	})
	if len(candidates) > maxCandidates {
		candidates = candidates[:maxCandidates]
	}

	targetSectorGroup := sicSectorGroup(targetSIC)

	// Enrich each candidate concurrently.
	results := make([]PeerResult, 0, len(candidates))
	var mu sync.Mutex
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, cand := range candidates {
		wg.Add(1)
		go func(c candidate) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// ── 1. SIC check ─────────────────────────────────────────────────
			if targetSIC != 0 {
				peerSIC, _, err := e.secClient.FetchEntitySIC(c.sym)
				if err != nil || sicSectorGroup(peerSIC) != targetSectorGroup {
					return // different sector — skip
				}
			}

			// ── 2. Quarterly actuals ──────────────────────────────────────────
			history, err := e.secClient.FetchQuarterlyActuals(c.sym)
			if err != nil || len(history) == 0 {
				return
			}

			// Find the quarter whose period end is closest to targetPeriodEnd.
			var bestQ *QuarterActual
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
				if annDates, err2 := e.secClient.FetchEarningsAnnouncementDates(c.sym, []QuarterActual{*bestQ}); err2 == nil {
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
			prices, err := e.fetchPriceHistory(c.sym)
			if err != nil || len(prices) == 0 {
				return
			}

			// ── 4. EPS estimate at announcement time ─────────────────────────
			epsEst, _ := e.fetchNasdaqEPSEstimate(c.sym, annTime)

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
			rxnClose, okClose := closestPrice(prices, rxnDay)
			if !okClose {
				return
			}
			rxnOpen := openOnDate(prices, rxnDay)

			pr := PeerResult{
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
				GapRetPct:        pctChange(priorClose, rxnOpen),
				DayRetPct:        pctChange(priorClose, rxnClose),
			}
			if epsEst != 0 {
				v := pctChange(epsEst, bestQ.EPS)
				pr.EPSBeatPct = &v
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
