package enrich

import (
	"context"
	"math"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"quarterly_results/internal/backtest"
	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/indicators"
	"quarterly_results/internal/logging"
	"quarterly_results/internal/macro"
	"quarterly_results/internal/peers"
	"quarterly_results/internal/provider"
	"quarterly_results/internal/recommend"
	"quarterly_results/internal/sector"
	"quarterly_results/internal/source/finnhub"
	"quarterly_results/internal/source/finviz"
	"quarterly_results/internal/source/llmagent"
	"quarterly_results/internal/source/sec"
	"quarterly_results/internal/source/stockanalysis"
	"quarterly_results/internal/source/yahoo"
)

// Config controls optional expensive features. Both default to enabled (false = not disabled).
// Override with env vars DISABLE_PEERS=1 / DISABLE_NEWS=1 or CLI flags --no-peers / --no-news.
type Config struct {
	DisablePeers bool // skip sector-peer analysis
	DisableNews  bool // skip material 8-K events analysis
	ComputeGEX   bool // compute dealer gamma exposure (requires --gex + --symbol)
	RunBacktest  bool // run walk-forward Tier-1 backtest of recommendation signals
	LLMRating    bool // call sibling llm-earnings-agent to attach an LLM rating
	// LLMAgents selects the agent's specialists (see llmagent.ParseAgents);
	// empty means llmagent.DefaultAgents.
	LLMAgents []string
}

// Summary is the result produced by EnrichStream for one stock.
type Summary struct {
	Symbol  string
	Summary *domain.FinancialSummary
}

// Enricher fetches and computes financial growth metrics for a list of earnings results.
type Enricher struct {
	secClient     *sec.Client
	httpClient    *http.Client
	finnhubClient *finnhub.Client

	yahoo *yahoo.Client

	Config    Config
	exCfg     exchange.Config
	providers provider.ExchangeProviders

	peerFinder *peers.Finder
}

func New() *Enricher {
	return NewForExchange(exchange.DefaultUSConfig)
}

func NewForExchange(cfg exchange.Config) *Enricher {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	secClient := sec.NewClient()
	finnhubClient := finnhub.NewClient()
	yc := yahoo.NewClient()
	providers := provider.NewProvidersForExchange(cfg, secClient, finnhubClient, httpClient)
	e := &Enricher{
		secClient:     secClient,
		finnhubClient: finnhubClient,
		httpClient:    httpClient,
		yahoo:         yc,
		Config: Config{
			DisablePeers: os.Getenv("DISABLE_PEERS") == "1",
			DisableNews:  os.Getenv("DISABLE_NEWS") == "1",
		},
		exCfg:      cfg,
		providers:  providers,
		peerFinder: peers.NewFinder(httpClient, yc.HTTP(), secClient, providers.Prices, peers.LoadOverrideStore(peers.OverridesPath())),
	}
	return e
}

// EnrichStream enriches all results concurrently and sends each completed summary
// on the returned channel in completion order (not input order). The channel is
// closed once all stocks have been processed. Callers that need a specific output
// order should sort after draining the channel.
func (e *Enricher) EnrichStream(results []domain.EarningsResult, calendarRows map[string]domain.CalendarRow, macroCal *macro.Calendar) <-chan Summary {
	ch := make(chan Summary, len(results))
	go func() {
		if err := e.secClient.LoadTickerMap(); err != nil {
			logging.Logf("Warning: could not load SEC ticker map: %v", err)
		}
		// 5 concurrent stocks — each now fans out its own goroutines internally,
		// so total concurrency is higher but still bounded per-stock.
		sem := make(chan struct{}, 5)
		var wg sync.WaitGroup
		for _, r := range results {
			wg.Add(1)
			go func(res domain.EarningsResult) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				ch <- Summary{
					Symbol:  res.Symbol,
					Summary: e.summarize(res, calendarRows[res.Symbol], macroCal),
				}
			}(r)
		}
		wg.Wait()
		close(ch)
	}()
	return ch
}

// EnrichAll collects all enriched summaries into a map. Use EnrichStream directly
// when you want to process results as they complete rather than waiting for all.
func (e *Enricher) EnrichAll(results []domain.EarningsResult, calendarRows map[string]domain.CalendarRow, macroCal *macro.Calendar) map[string]*domain.FinancialSummary {
	out := make(map[string]*domain.FinancialSummary, len(results))
	for es := range e.EnrichStream(results, calendarRows, macroCal) {
		out[es.Symbol] = es.Summary
	}
	return out
}

func (e *Enricher) buildSummary(res domain.EarningsResult, row domain.CalendarRow, macroCal *macro.Calendar) *domain.FinancialSummary {
	s := &domain.FinancialSummary{
		Symbol:        res.Symbol,
		EPSEstimate:   row.EPSForecast,
		EPSLastYear:   row.LastYearEPS,
		FiscalQuarter: row.FiscalQuarterEnding,
		Currency:      e.exCfg.Currency,
	}
	if s.Currency == "" {
		s.Currency = "USD"
	}

	// ── Phase 1: fire all independent fetches in parallel ────────────────────
	var (
		forwardEPS    []domain.ForwardQuarter
		sa            *stockanalysis.Estimates
		insider       *domain.InsiderSummary
		institutional *domain.InstitutionalData
		history       []domain.QuarterActual
		prices        []domain.PricePoint
		pricesErr     error
		vixPrices     []domain.PricePoint
		options       *domain.OptionsSnapshot
		peerList      []domain.PeerResult
		rawMatEvents  []domain.MaterialEvent
		mspr          float64
		msprSignal    string
		llmRating     *domain.LLMRating
	)

	since := time.Now().AddDate(0, -3, 0)

	var p1 sync.WaitGroup

	p1.Add(1)
	go func() {
		defer p1.Done()
		forwardEPS, _ = e.providers.Financials.FetchForwardEPS(res.Symbol)
	}()

	p1.Add(1)
	go func() {
		defer p1.Done()
		sa, _ = stockanalysis.FetchEstimates(e.httpClient, res.Symbol)
	}()

	p1.Add(1)
	go func() {
		defer p1.Done()
		var err error
		insider, err = e.providers.Financials.FetchInsiderActivity(res.Symbol, since)
		if err != nil {
			logging.Logf("Warning: insider data unavailable for %s: %v", res.Symbol, err)
		}
	}()

	if e.exCfg.Exchange == exchange.ExchangeUS || e.exCfg.Exchange == "" {
		p1.Add(1)
		go func() {
			defer p1.Done()
			var err error
			institutional, err = finviz.FetchInstitutionalData(e.httpClient, res.Symbol)
			if err != nil {
				logging.Logf("Warning: institutional data unavailable for %s: %v", res.Symbol, err)
			}
		}()
	}

	p1.Add(1)
	go func() {
		defer p1.Done()
		h, err := e.providers.Financials.FetchQuarterlyActuals(res.Symbol)
		// Fall back to Yahoo quoteSummary when the primary provider returns no
		// data. This is the path for US-listed Foreign Private Issuers (e.g.
		// NBIS — Nebius) which file 6-K interim reports instead of 10-Qs and
		// therefore have no us-gaap quarterly facts in the SEC XBRL API.
		usExchange := e.exCfg.Exchange == exchange.ExchangeUS || e.exCfg.Exchange == ""
		var yerr error
		if (err != nil || len(h) < 2) && usExchange {
			var yh []domain.QuarterActual
			yh, yerr = e.yahoo.FetchQuarterlyActuals(res.Symbol)
			if yerr == nil && len(yh) >= 2 {
				// Yahoo's quoteSummary caps at 4 trailing quarters, which leaves
				// YoY math empty on every history row. Try to backfill older
				// quarters from stockanalysis.com so we have ≥5 rows.
				if sa, saErr := stockanalysis.FetchQuarterlyHistory(e.yahoo.HTTP(), res.Symbol); saErr == nil && len(sa) > 0 {
					merged := stockanalysis.MergeHistoryWithYahoo(yh, sa)
					if len(merged) > len(yh) {
						logging.Logf("Note: %s has no SEC quarterly data — Yahoo (%d) + stockanalysis backfill (%d total quarters)", res.Symbol, len(yh), len(merged))
						history = merged
						return
					}
				}
				logging.Logf("Note: %s has no SEC quarterly data — using Yahoo fallback (%d quarters)", res.Symbol, len(yh))
				history = yh
				return
			}
		}
		if err != nil || len(h) < 2 {
			logging.Logf("Warning: quarterly history unavailable for %s (primary: %d quarters, err=%v; Yahoo fallback err=%v)",
				res.Symbol, len(h), err, yerr)
			return
		}
		if announceDates, err2 := e.providers.Financials.FetchEarningsAnnouncementDates(res.Symbol, h); err2 == nil {
			for i := range h {
				if ad, ok := announceDates[h[i].Period]; ok {
					h[i].FilingDate = ad
				}
			}
		}
		history = h
	}()

	p1.Add(1)
	go func() {
		defer p1.Done()
		prices, pricesErr = e.providers.Prices.FetchPriceHistory(res.Symbol)
	}()

	p1.Add(1)
	go func() {
		defer p1.Done()
		vixPrices, _ = e.providers.Prices.FetchPriceHistory("^VIX")
	}()

	p1.Add(1)
	go func() {
		defer p1.Done()
		var err error
		options, err = e.yahoo.FetchOptionsSnapshot(res.Symbol, res.EarningsDate, e.Config.ComputeGEX)
		if err != nil {
			logging.Logf("Warning: options data unavailable for %s: %v", res.Symbol, err)
		}
	}()

	isUS := e.exCfg.Exchange == exchange.ExchangeUS || e.exCfg.Exchange == ""

	// SIC code is shared by peers and sector momentum. Fetch once, broadcast.
	var (
		sicCode   int
		sicReady  = make(chan struct{})
		sectorMom *sector.Momentum
	)
	if isUS {
		p1.Add(1)
		go func() {
			defer p1.Done()
			sic, _, _ := e.providers.Financials.FetchEntitySIC(res.Symbol)
			sicCode = sic
			close(sicReady)
		}()
	} else {
		close(sicReady)
	}

	if isUS && !e.Config.DisablePeers {
		if qEnd, ok := parseFiscalQuarterEnd(row.FiscalQuarterEnding); ok {
			p1.Add(1)
			go func() {
				defer p1.Done()
				<-sicReady
				peerList = e.peerFinder.Find(res.Symbol, sicCode, qEnd, res.MarketCapB)
			}()
		}
	}

	if isUS {
		p1.Add(1)
		go func() {
			defer p1.Done()
			<-sicReady
			if sicCode > 0 {
				sectorMom = sector.FetchMomentum(sicCode, e.httpClient)
			}
		}()
	}

	if isUS && !e.Config.DisableNews {
		p1.Add(1)
		go func() {
			defer p1.Done()
			rawMatEvents, _ = e.providers.Financials.FetchMaterialEvents(res.Symbol, since)
		}()
	}

	p1.Add(1)
	go func() {
		defer p1.Done()
		var err error
		mspr, msprSignal, err = e.finnhubClient.FetchMSPR(res.Symbol)
		if err != nil {
			msprSignal = "N/A"
			logging.Logf("Note: MSPR unavailable for %s: %v", res.Symbol, err)
		}
	}()

	// Text-only agents don't need this stock's result, so they run alongside
	// the other fetches; payload-reading agents run after enrichment (llm.go).
	if e.Config.LLMRating && !llmagent.NeedsPayload(e.Config.LLMAgents) {
		p1.Add(1)
		go func() {
			defer p1.Done()
			r, err := llmagent.FetchRating(context.Background(), res.Symbol, llmagent.Options{Agents: e.Config.LLMAgents})
			if err != nil {
				logging.Logf("Warning: LLM rating unavailable for %s: %v", res.Symbol, err)
				return
			}
			llmRating = r
		}()
	}

	p1.Wait()

	// ── Phase 2: apply fetched data and compute all derived metrics ───────────

	// --- Forward EPS ---
	if len(forwardEPS) > 0 {
		s.ForwardEPS = forwardEPS
	}

	// --- Stockanalysis estimates + analyst ratings ---
	// stockanalysis correctly identifies the matching fiscal quarter across 10-Q and 10-K
	// filings (e.g. fiscal-year-end quarters that are only in 10-K, not 10-Q).
	if sa != nil {
		s.RevenueEstimate = &sa.RevenueEst
		if sa.RevenuePrevYear != 0 {
			s.RevenuePrevYear = &sa.RevenuePrevYear
		}
		if s.EPSEstimate == 0 && sa.EPSEst != 0 {
			s.EPSEstimate = sa.EPSEst
		}
		s.ConsensusRating = sa.ConsensusRating
		s.StrongBuy = sa.StrongBuy
		s.Buy = sa.Buy
		s.Hold = sa.Hold
		s.Sell = sa.Sell
		s.StrongSell = sa.StrongSell
		s.TotalRatings = sa.TotalRatings
		s.AvgPriceTarget = sa.AvgPriceTarget
	}

	// --- Insider / institutional ---
	s.Insider = insider
	s.Institutional = institutional

	// --- Historical actuals ---
	if len(history) >= 2 {
		s.History = history

		// RevenuePrevYear fallback: stockanalysis is preferred; history[0] as backup.
		oldest := history[0]
		if s.RevenuePrevYear == nil && oldest.Revenue != 0 {
			v := oldest.Revenue
			s.RevenuePrevYear = &v
		}

		last := history[len(history)-1]
		if last.EPS != 0 {
			v := last.EPS
			s.EPSPrevQtr = &v
		}
		if last.Revenue != 0 {
			v := last.Revenue
			s.RevenuePrevQtr = &v
		}

		// Override EPSLastYear with the authoritative SEC EDGAR value.
		// The Nasdaq calendar's lastYearEPS field is unreliable (e.g. off by 10×).
		if qEnd, ok := parseFiscalQuarterEnd(s.FiscalQuarter); ok {
			targetYearAgo := qEnd.AddDate(-1, 0, 0)
			bestDiff := time.Duration(math.MaxInt64)
			var bestEPS float64
			for _, q := range history {
				qPeriod, err2 := time.Parse("2006-01-02", q.Period)
				if err2 != nil || q.EPS == 0 {
					continue
				}
				diff := qPeriod.Sub(targetYearAgo)
				if diff < 0 {
					diff = -diff
				}
				if diff <= 46*24*time.Hour && diff < bestDiff {
					bestDiff = diff
					bestEPS = q.EPS
				}
			}
			if bestEPS != 0 {
				// Only override when Nasdaq has no value or the ratio is extreme (>4× or <0.25×).
				// GAAP vs non-GAAP adjustments normally differ by <30%, so trust Nasdaq within range.
				nasdaqEPS := s.EPSLastYear
				var absRatio float64
				if nasdaqEPS != 0 {
					absRatio = math.Abs(bestEPS / nasdaqEPS)
				}
				if nasdaqEPS == 0 || absRatio > 4 || absRatio < 0.25 {
					s.EPSLastYear = bestEPS
				}
			}
		}
	}

	// YoY EPS — computed once here, after EPSLastYear is finalised.
	if s.EPSLastYear != 0 && s.EPSEstimate != 0 {
		yoy := indicators.PctChange(s.EPSLastYear, s.EPSEstimate)
		s.EPSYoYPct = &yoy
	}

	// Prior-period YoY: growth rate of the most-recent reported quarter vs
	// same fiscal quarter four periods earlier. Needs ≥5 quarters of history.
	if len(s.History) >= 5 {
		latest := s.History[len(s.History)-1]
		yearAgo := s.History[len(s.History)-5]
		if yearAgo.Revenue != 0 && latest.Revenue != 0 {
			v := indicators.PctChange(yearAgo.Revenue, latest.Revenue)
			s.RevenueYoYPctPrev = &v
		}
		if yearAgo.EPS != 0 && latest.EPS != 0 {
			v := indicators.PctChange(yearAgo.EPS, latest.EPS)
			s.EPSYoYPctPrev = &v
		}
	}

	// YoY Revenue: estimate vs same fiscal quarter last year.
	if s.RevenuePrevYear != nil && *s.RevenuePrevYear != 0 && s.RevenueEstimate != nil && *s.RevenueEstimate != 0 {
		v := indicators.PctChange(*s.RevenuePrevYear, *s.RevenueEstimate)
		s.RevenueYoYPct = &v
	}

	// QoQ: current-quarter estimate vs most-recent actual quarter.
	if s.EPSPrevQtr != nil && *s.EPSPrevQtr != 0 && s.EPSEstimate != 0 {
		v := indicators.PctChange(*s.EPSPrevQtr, s.EPSEstimate)
		s.EPSQoQPct = &v
	}
	if s.RevenuePrevQtr != nil && *s.RevenuePrevQtr != 0 && s.RevenueEstimate != nil && *s.RevenueEstimate != 0 {
		v := indicators.PctChange(*s.RevenuePrevQtr, *s.RevenueEstimate)
		s.RevenueQoQPct = &v
	}

	// --- Valuation ratios (price-independent) ---
	if len(s.History) >= 4 && res.MarketCapB > 0 {
		ttmRev := 0.0
		for _, q := range s.History[len(s.History)-4:] {
			ttmRev += q.Revenue
		}
		if ttmRev > 0 {
			v := (res.MarketCapB * 1e9) / ttmRev
			s.PS = &v
		}
	}

	var ttmEPS float64
	if len(s.History) >= 4 {
		for _, q := range s.History[len(s.History)-4:] {
			ttmEPS += q.EPS
		}
	}

	var fwdEPS float64
	if len(s.ForwardEPS) >= 4 {
		for _, fq := range s.ForwardEPS[:4] {
			fwdEPS += fq.ConsensusEPS
		}
	} else if s.EPSEstimate != 0 {
		fwdEPS = s.EPSEstimate * 4
	}

	// --- Peers + industry-relative valuation medians ---
	if len(peerList) > 0 {
		s.Peers = peerList
		if mPE, mPS := peerValuationMedians(peerList); mPE > 0 || mPS > 0 {
			if mPE > 0 {
				v := mPE
				s.IndustryMedianPE = &v
			}
			if mPS > 0 {
				v := mPS
				s.IndustryMedianPS = &v
			}
		}
	}

	// --- Sector momentum ---
	if sectorMom != nil {
		s.SectorETF = sectorMom.ETF
		s.SectorRet1M = sectorMom.Ret1M
		s.SectorRet3M = sectorMom.Ret3M
	}

	// --- MSPR ---
	s.MSPR = mspr
	s.MSPRSignal = msprSignal
	if s.MSPRSignal == "" {
		s.MSPRSignal = "N/A"
	}

	// --- LLM rating (sibling Python agent) ---
	s.LLMRating = llmRating

	// No price history → return what we have so far.
	if pricesErr != nil || len(prices) == 0 {
		logging.Logf("Warning: price history unavailable for %s: %v", res.Symbol, pricesErr)
		return s
	}

	current := prices[len(prices)-1].Close
	now := prices[len(prices)-1].Date
	s.CurrentPrice = current

	if s.AvgPriceTarget > 0 {
		v := indicators.PctChange(current, s.AvgPriceTarget)
		s.PriceTargetUpside = &v
	}

	if len(s.History) >= 4 && ttmEPS != 0 {
		v := current / ttmEPS
		s.PE_TTM = &v
	}
	if fwdEPS != 0 {
		v := current / fwdEPS
		s.PE_Forward = &v
	}

	for _, lb := range []struct {
		days  int
		field **float64
	}{
		{7, &s.Ret1W},
		{30, &s.Ret1M},
		{182, &s.Ret6M},
		{365, &s.Ret1Y},
	} {
		target := now.AddDate(0, 0, -lb.days)
		if past, ok := indicators.ClosestPrice(prices, target); ok && past != 0 {
			v := indicators.PctChange(past, current)
			*lb.field = &v
		}
	}

	// ── Earnings reactions (last ≤4 historical quarters) ─────────────────────
	quarters := s.History
	if len(quarters) > 4 {
		quarters = quarters[len(quarters)-4:]
	}
	usedAnnounceDates := make(map[string]bool)
	for _, q := range quarters {
		if q.FilingDate == "" {
			continue
		}
		announceTime, err := time.Parse("2006-01-02", q.FilingDate)
		if err != nil {
			continue
		}
		periodEnd, perr := time.Parse("2006-01-02", q.Period)
		if perr == nil {
			days := int(announceTime.Sub(periodEnd).Hours() / 24)
			if days < 10 || days > 91 {
				logging.Logf("Warning: skipping reaction for %s period %s — announcement date %s is out of expected range (%d days after period end)", res.Symbol, q.Period, q.FilingDate, days)
				continue
			}
		}
		if usedAnnounceDates[q.FilingDate] {
			logging.Logf("Warning: skipping reaction for %s period %s — announcement date %s already used for another quarter", res.Symbol, q.Period, q.FilingDate)
			continue
		}
		usedAnnounceDates[q.FilingDate] = true
		nextDay := e.providers.Holidays.NextWorkingDay(announceTime)

		dayBeforeClose, okDayBefore := indicators.ClosestPrice(prices, announceTime.AddDate(0, 0, -1))
		announceDayOpen := indicators.OpenOnDate(prices, announceTime)
		announceDayClose, _ := indicators.ClosestPrice(prices, announceTime)
		nextDayOpen := indicators.OpenOnDate(prices, nextDay)

		isBMO := false
		if okDayBefore && dayBeforeClose > 0 && announceDayOpen > 0 && announceDayClose > 0 && nextDayOpen > 0 {
			gapAnnounce := math.Abs(announceDayOpen-dayBeforeClose) / dayBeforeClose
			gapNextDay := math.Abs(nextDayOpen-announceDayClose) / announceDayClose
			isBMO = gapAnnounce > gapNextDay
		}

		var reactionDay time.Time
		var priorClose float64
		var okPrior bool
		if isBMO {
			reactionDay = announceTime
			priorClose, okPrior = dayBeforeClose, okDayBefore
		} else {
			reactionDay = nextDay
			if announceDayClose > 0 {
				priorClose, okPrior = announceDayClose, true
			} else {
				priorClose, okPrior = dayBeforeClose, okDayBefore
			}
		}

		if reactionDay.After(now) {
			continue
		}
		reactionClose, okReact := indicators.ClosestPrice(prices, reactionDay)
		if !okPrior || !okReact || priorClose == 0 {
			continue
		}
		reactionOpen := indicators.OpenOnDate(prices, reactionDay)

		var pre7Ret *float64
		var pre7CloseVal float64
		if pre7Close, ok := indicators.ClosestPrice(prices, announceTime.AddDate(0, 0, -7)); ok && pre7Close != 0 {
			v := indicators.PctChange(pre7Close, priorClose)
			pre7Ret = &v
			pre7CloseVal = pre7Close
		}

		var post7Ret *float64
		var post7CloseVal float64
		post7Target := reactionDay.AddDate(0, 0, 7)
		if !post7Target.After(now) {
			if post7Close, ok := indicators.ClosestPrice(prices, post7Target); ok && post7Close != 0 {
				v := indicators.PctChange(reactionClose, post7Close)
				post7Ret = &v
				post7CloseVal = post7Close
			}
		}

		rxn := domain.EarningsReaction{
			Period:           q.Period,
			AnnouncementDate: q.FilingDate,
			ReactionDay:      reactionDay.Format("2006-01-02"),
			PriorClose:       priorClose,
			ReactionOpen:     reactionOpen,
			ReactionClose:    reactionClose,
			GapRetPct:        indicators.PctChange(priorClose, reactionOpen),
			RetPct:           indicators.PctChange(priorClose, reactionClose),
			EPSActual:        q.EPS,
			RevenueActual:    q.Revenue,
			Pre7Ret:          pre7Ret,
			Pre7Close:        pre7CloseVal,
			Post7Ret:         post7Ret,
			Post7Close:       post7CloseVal,
		}
		if vix, ok := indicators.ClosestPrice(vixPrices, reactionDay); ok && vix > 0 {
			rxn.VIX = vix
		}
		if macroCal != nil {
			nearby := macroCal.EventsNear(q.FilingDate, 2)
			rxn.MacroContext = macro.FormatContext(nearby, q.FilingDate)
		}
		s.EarningsReactions = append(s.EarningsReactions, rxn)
	}

	// ── Macro context for upcoming earnings date ──────────────────────────────
	if macroCal != nil {
		nearby := macroCal.EventsNear(res.EarningsDate, 2)
		s.MacroContext = macro.FormatContext(nearby, res.EarningsDate)
	}

	// ── Options: attach HistAvgAbsRxn now that reactions are known ───────────
	if options != nil {
		if len(s.EarningsReactions) > 0 {
			total := 0.0
			for _, rxn := range s.EarningsReactions {
				total += math.Abs(rxn.RetPct)
			}
			options.HistAvgAbsRxn = total / float64(len(s.EarningsReactions))
		}
		s.Options = options
	}

	// ── Enrich reactions with consensus EPS estimates (Nasdaq calendar) ──────
	if len(s.EarningsReactions) > 0 {
		type epsResult struct {
			period   string
			estimate float64
		}
		estCh := make(chan epsResult, len(s.EarningsReactions))
		for _, rxn := range s.EarningsReactions {
			go func(r domain.EarningsReaction) {
				t, err := time.Parse("2006-01-02", r.AnnouncementDate)
				if err != nil {
					estCh <- epsResult{r.Period, 0}
					return
				}
				est, err := e.providers.Prices.FetchHistoricalEPSEstimate(res.Symbol, t)
				if err != nil {
					est = 0
				}
				estCh <- epsResult{r.Period, est}
			}(rxn)
		}
		epsEstimates := make(map[string]float64, len(s.EarningsReactions))
		for range s.EarningsReactions {
			er := <-estCh
			if er.estimate != 0 {
				epsEstimates[er.period] = er.estimate
			}
		}
		for i := range s.EarningsReactions {
			rxn := &s.EarningsReactions[i]
			if est, ok := epsEstimates[rxn.Period]; ok {
				rxn.EPSEstimate = est
				v := indicators.PctChange(est, rxn.EPSActual)
				rxn.EPSBeatPct = &v
			}
		}
	}

	// ── Signal 2: 52-week high/low position + RSI(14) ────────────────────────
	yearAgoTarget := now.AddDate(-1, 0, 0)
	var hi52, lo52 float64
	for _, p := range prices {
		if p.Date.Before(yearAgoTarget) {
			continue
		}
		if hi52 == 0 || p.Close > hi52 {
			hi52 = p.Close
		}
		if lo52 == 0 || p.Close < lo52 {
			lo52 = p.Close
		}
	}
	if hi52 > 0 {
		s.Hi52 = hi52
		v := indicators.PctChange(hi52, current)
		s.PctFrom52Hi = &v
	}
	if lo52 > 0 {
		s.Lo52 = lo52
		v := indicators.PctChange(lo52, current)
		s.PctFrom52Lo = &v
	}
	if len(prices) >= 15 {
		rsi := indicators.RSI14(prices)
		s.RSI14 = &rsi
	}

	// ── Signal 3: implied move vs historical move ratio ───────────────────────
	if s.Options != nil && s.Options.ExpectedMovePct > 0 && s.Options.HistAvgAbsRxn > 0 {
		v := s.Options.ExpectedMovePct / s.Options.HistAvgAbsRxn
		s.ImpliedVsHistRatio = &v
	}

	// ── Signal 4: earnings beat consistency ──────────────────────────────────
	if len(s.EarningsReactions) > 0 {
		var beats, total int
		var sumBeatPct float64
		for _, rxn := range s.EarningsReactions {
			if rxn.EPSBeatPct == nil {
				continue
			}
			total++
			if *rxn.EPSBeatPct > 0 {
				beats++
			}
			sumBeatPct += *rxn.EPSBeatPct
		}
		if total > 0 {
			br := float64(beats) / float64(total)
			s.BeatRate = &br
			avg := sumBeatPct / float64(total)
			s.AvgBeatPct = &avg
		}
	}

	// ── Material 8-K events: annotate with price reaction ────────────────────
	if len(rawMatEvents) > 0 {
		vol30 := indicators.DailyVolatility(prices, 30)
		threshold := 1.5 * vol30
		for i := range rawMatEvents {
			evDate, perr := time.Parse("2006-01-02", rawMatEvents[i].Date)
			if perr != nil {
				continue
			}
			evClose, okClose := indicators.ClosestPrice(prices, evDate)
			evPrev, okPrev := indicators.ClosestPrice(prices, evDate.AddDate(0, 0, -1))
			if okClose && okPrev && evPrev != 0 {
				ret := indicators.PctChange(evPrev, evClose)
				rawMatEvents[i].RetPct = ret
				rawMatEvents[i].Abnormal = threshold > 0 && math.Abs(ret) > threshold
			}
		}
		s.MaterialEvents = rawMatEvents
	}

	// ── Recommendation: aggregate every signal into a single directional score ──
	s.Recommendation = recommend.ComputeRecommendation(s)

	// ── Optional walk-forward backtest of the Tier-1 signals ────────────────
	if e.Config.RunBacktest && len(s.EarningsReactions) > 0 {
		s.Backtest = backtest.RunBacktest(s, prices, sicCode, e.httpClient)
	}

	return s
}

// peerValuationMedians returns the median PE_TTM and PS across peers that
// reported usable values. Returns 0 for either if no peer had a positive value.
func peerValuationMedians(peerList []domain.PeerResult) (medianPE, medianPS float64) {
	var pes, pss []float64
	for _, p := range peerList {
		if p.PE_TTM > 0 {
			pes = append(pes, p.PE_TTM)
		}
		if p.PS > 0 {
			pss = append(pss, p.PS)
		}
	}
	return indicators.Median(pes), indicators.Median(pss)
}

// parseFiscalQuarterEnd converts a Nasdaq FiscalQuarterEnding string (e.g. "Mar/2026")
// to the last calendar day of that month. Returns false if the string cannot be parsed.
func parseFiscalQuarterEnd(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	// Parse as the first day of the month, then advance to the last day.
	t, err := time.Parse("Jan/2006", s)
	if err != nil {
		return time.Time{}, false
	}
	// Last day of the month = first day of the next month minus one day.
	lastDay := time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	return lastDay, true
}
