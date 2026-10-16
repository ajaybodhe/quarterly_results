package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/enrich"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/logging"
	"quarterly_results/internal/macro"
	"quarterly_results/internal/provider"
	"quarterly_results/internal/report"
	"quarterly_results/internal/source/finnhub"
	"quarterly_results/internal/source/llmagent"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/source/yahoo"
	"quarterly_results/internal/tracking"
)

const defaultMinMarketCap = 10_000_000_000.0 // $10 billion

func main() {
	fromStr := flag.String("from", "", "Start of date range (YYYY-MM-DD); optional when --symbol is set")
	toStr := flag.String("to", "", "End of date range (YYYY-MM-DD); optional when --symbol is set")
	outputFmt := flag.String("output", "table", "Output format: table | csv | json")
	minCapB := flag.Float64("min-cap-b", defaultMinMarketCap/1e9, "Minimum market cap in billions USD (default 10)")
	maxCapB := flag.Float64("max-cap-b", 0, "Maximum market cap in billions USD (0 = no upper limit)")
	symbolFlag := flag.String("symbol", "", "Single stock symbol to analyse (skips market-cap filter; from/to optional)")
	noPeers := flag.Bool("no-peers", false, "Disable sector peer analysis (also: DISABLE_PEERS=1)")
	noNews := flag.Bool("no-news", false, "Disable material 8-K events analysis (also: DISABLE_NEWS=1)")
	gexFlag := flag.Bool("gex", false, "Compute dealer gamma exposure table (requires --symbol)")
	exchangeFlag := flag.String("exchange", "US", "Exchange: US | LSE | FSE | EURONEXT")
	timingFlag := flag.String("timing", "", "Filter by earnings timing: bmo (before market open) | amc (after market close) | \"\" (all)")
	backtestFlag := flag.Bool("backtest", false, "Run walk-forward Tier-1 backtest on each stock's prior reactions")
	llmRatingFlag := flag.Bool("llm-rating", false, "Attach an LLM rating from the sibling llm-earnings-agent project")
	llmAgentsFlag := flag.String("llm-agents", strings.Join(llmagent.DefaultAgents, ","),
		"LLM specialist agents for --llm-rating: comma-separated subset of "+strings.Join(llmagent.AllAgents, ",")+", or all")
	logPredsFlag := flag.Bool("log-predictions", false, "Append each upcoming report's Rating (and LLM rating) to --predictions-file")
	predFileFlag := flag.String("predictions-file", tracking.DefaultPath, "JSON-lines prediction log for --log-predictions / --score-predictions")
	scorePredsFlag := flag.Bool("score-predictions", false, "Score the prediction log against actual earnings reactions, then exit")
	flag.Parse()

	if *scorePredsFlag {
		if err := scorePredictions(*predFileFlag, *outputFmt); err != nil {
			log.Fatalf("--score-predictions: %v", err)
		}
		return
	}

	llmAgents, err := llmagent.ParseAgents(*llmAgentsFlag)
	if err != nil {
		log.Fatalf("--llm-agents: %v", err)
	}

	exCfg, err := exchange.ConfigByName(*exchangeFlag)
	if err != nil {
		log.Fatalf("--exchange: %v", err)
	}

	filterTiming := strings.ToLower(strings.TrimSpace(*timingFlag))
	if filterTiming != "" && filterTiming != "bmo" && filterTiming != "amc" {
		log.Fatalf("--timing must be \"bmo\", \"amc\", or empty (got %q)", filterTiming)
	}

	// Auto-append Yahoo suffix to bare tickers for non-US exchanges.
	if *symbolFlag != "" && exCfg.YahooSuffix != "" {
		*symbolFlag = exchange.ToYahooSymbol(*symbolFlag, exCfg)
	}
	// If no --exchange but symbol has a known suffix, infer the exchange.
	if *exchangeFlag == "US" && *symbolFlag != "" {
		inferred := exchange.ForTicker(*symbolFlag)
		if inferred.Exchange != exchange.ExchangeUS {
			exCfg = inferred
		}
	}

	// Resolve date range.
	// When --symbol is given without dates, default to today → today+90 days.
	if *fromStr == "" && *symbolFlag != "" {
		*fromStr = time.Now().Format("2006-01-02")
	}
	if *toStr == "" && *symbolFlag != "" {
		*toStr = time.Now().AddDate(0, 0, 90).Format("2006-01-02")
	}

	if *fromStr == "" || *toStr == "" {
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  %s --from YYYY-MM-DD --to YYYY-MM-DD [flags]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s --symbol TSLA [flags]\n\nFlags:\n", os.Args[0])
		flag.PrintDefaults()
		os.Exit(1)
	}

	from, err := time.Parse("2006-01-02", *fromStr)
	if err != nil {
		log.Fatalf("Invalid --from date: %v", err)
	}
	to, err := time.Parse("2006-01-02", *toStr)
	if err != nil {
		log.Fatalf("Invalid --to date: %v", err)
	}
	if to.Before(from) {
		log.Fatal("--to must not be before --from")
	}

	minCap := *minCapB * 1e9
	maxCap := *maxCapB * 1e9 // 0 means no upper limit
	filterSymbol := strings.ToUpper(strings.TrimSpace(*symbolFlag))

	// ── Step 1: Fetch earnings calendar ─────────────────────────────────────
	var calProvider provider.CalendarProvider
	if exCfg.Exchange == exchange.ExchangeUS {
		logging.Logf("Fetching earnings calendar from Nasdaq.com (%s → %s) ...", *fromStr, *toStr)
		calProvider = nasdaq.NewClient()
	} else {
		logging.Logf("Fetching earnings calendar from Finnhub (%s, %s → %s) ...", exCfg.Exchange, *fromStr, *toStr)
		calProvider = finnhub.NewCalendarClient(finnhub.NewClient(), exCfg)
	}
	events, calMap, err := calProvider.FetchEarningsCalendar(from, to)
	if err != nil {
		log.Fatalf("Failed to fetch earnings calendar: %v", err)
	}
	logging.Logf("Total events fetched: %d", len(events))

	// ── Step 2: Filter by symbol or market cap ────────────────────────────────
	var qualified []domain.EarningsEvent
	if filterSymbol != "" {
		for _, e := range events {
			if strings.ToUpper(e.Symbol) == filterSymbol {
				qualified = append(qualified, e)
				break
			}
		}
		if len(qualified) == 0 {
			// Symbol not found in calendar window — synthesise a minimal entry so
			// enrichment still runs (EarningsDate will be empty).
			logging.Logf("Symbol %s not found in earnings calendar for %s→%s; running enrichment without earnings date.", filterSymbol, *fromStr, *toStr)
			qualified = []domain.EarningsEvent{{Symbol: filterSymbol}}
		}
	} else {
		qualified = filterByMarketCap(events, minCap, maxCap)
		if maxCap > 0 {
			logging.Logf("Qualifying ($%.0fB–$%.0fB market cap): %d", *minCapB, *maxCapB, len(qualified))
		} else {
			logging.Logf("Qualifying (market cap > $%.0fB): %d", *minCapB, len(qualified))
		}
		if len(qualified) == 0 {
			return
		}
	}

	// ── Step 2a: Filter by earnings timing (BMO / AMC) ───────────────────────
	if filterTiming != "" {
		qualified = filterByTiming(qualified, filterTiming)
		logging.Logf("After --timing=%s filter: %d stocks", filterTiming, len(qualified))
		if len(qualified) == 0 {
			return
		}
	}

	// ── Step 2b: Load macro economic calendar ────────────────────────────────
	logging.Logf("Loading macro economic calendar ...")
	macroCal := macro.LoadCalendar(from, to, exCfg)

	// ── Step 3: Build preliminary results for enricher input ─────────────────
	preliminary := make([]domain.EarningsResult, 0, len(qualified))
	for _, e := range qualified {
		preliminary = append(preliminary, domain.EarningsResult{
			Symbol:       e.Symbol,
			CompanyName:  e.Name,
			MarketCapB:   e.MarketCap / 1e9,
			EarningsDate: e.Date,
			EarningsTime: e.Time,
			ResultDate:   exchange.ComputeResultDate(e.Date, e.Time, exchange.NewHolidayCalendar(exCfg)),
		})
	}
	prelimMap := make(map[string]domain.EarningsResult, len(preliminary))
	for _, r := range preliminary {
		prelimMap[r.Symbol] = r
	}

	// ── Step 4: Enrich and output ─────────────────────────────────────────────
	logging.Logf("Fetching financial summaries (EPS estimates, revenue history, trends) ...")
	enricher := enrich.NewForExchange(exCfg)
	if *noPeers {
		enricher.Config.DisablePeers = true
	}
	if *noNews {
		enricher.Config.DisableNews = true
	}
	if *gexFlag {
		if filterSymbol == "" {
			log.Fatal("--gex requires --symbol to be set")
		}
		enricher.Config.ComputeGEX = true
	}
	if *backtestFlag {
		enricher.Config.RunBacktest = true
	}
	if *llmRatingFlag {
		enricher.Config.LLMRating = true
	}
	enricher.Config.LLMAgents = llmAgents

	var predLog *tracking.Log
	if *logPredsFlag {
		predLog = tracking.NewLog(*predFileFlag)
	}

	// Assemble each stock as it finishes and forward to the output channel.
	resultCh := make(chan domain.EarningsResult, len(preliminary))
	go func() {
		logged := 0
		for es := range enricher.EnrichStream(preliminary, calMap, macroCal) {
			r := report.AssembleResult(prelimMap[es.Symbol], es.Summary)
			if predLog != nil {
				logged += logPrediction(predLog, r, exCfg)
			}
			resultCh <- r
		}
		if predLog != nil {
			logging.Logf("Logged %d prediction(s) to %s", logged, predLog.Path())
		}
		close(resultCh)
	}()

	switch strings.ToLower(*outputFmt) {
	case "json":
		// JSON requires a complete sorted list before it can be written.
		var results []domain.EarningsResult
		for r := range resultCh {
			results = append(results, r)
		}
		sort.Slice(results, func(i, j int) bool {
			if results[i].ResultDate != results[j].ResultDate {
				return results[i].ResultDate < results[j].ResultDate
			}
			if results[i].MarketCapB != results[j].MarketCapB {
				return results[i].MarketCapB > results[j].MarketCapB
			}
			return results[i].Symbol < results[j].Symbol
		})
		logging.Logf("Done.\n")
		report.WriteJSON(os.Stdout, results)
	case "csv":
		// Header is written immediately; rows stream as each stock completes.
		report.WriteCSVStream(os.Stdout, resultCh)
		logging.Logf("Done.\n")
	default:
		// Collect all results, sort by result-date then market-cap, then print.
		var results []domain.EarningsResult
		for r := range resultCh {
			results = append(results, r)
		}
		sort.Slice(results, func(i, j int) bool {
			if results[i].ResultDate != results[j].ResultDate {
				return results[i].ResultDate < results[j].ResultDate
			}
			if results[i].MarketCapB != results[j].MarketCapB {
				return results[i].MarketCapB > results[j].MarketCapB
			}
			return results[i].Symbol < results[j].Symbol
		})
		logging.Logf("Done.\n")
		report.WriteStockCards(os.Stdout, results)
	}
}

// filterByTiming returns only events whose timing matches the given code ("bmo" or "amc").
// Events with no timing ("") are excluded when a filter is active.
func filterByTiming(events []domain.EarningsEvent, timing string) []domain.EarningsEvent {
	var out []domain.EarningsEvent
	for _, e := range events {
		if e.Time == timing {
			out = append(out, e)
		}
	}
	return out
}

// filterByMarketCap returns the subset of events whose MarketCap is in
// [minCap, maxCap]. maxCap == 0 means no upper bound.
func filterByMarketCap(events []domain.EarningsEvent, minCap, maxCap float64) []domain.EarningsEvent {
	var out []domain.EarningsEvent
	for _, e := range events {
		if e.MarketCap < minCap {
			continue
		}
		if maxCap > 0 && e.MarketCap > maxCap {
			continue
		}
		out = append(out, e)
	}
	return out
}

// logPrediction appends r to the prediction log when it is a pre-earnings
// call and reports how many records were written (0 or 1).
func logPrediction(l *tracking.Log, r domain.EarningsResult, cfg exchange.Config) int {
	rec, ok := tracking.NewRecord(r, cfg, time.Now())
	if !ok {
		return 0
	}
	if err := l.Append(rec); err != nil {
		logging.Logf("Warning: could not log prediction for %s: %v", r.Symbol, err)
		return 0
	}
	return 1
}

// scorePredictions scores the prediction log against the reactions that
// followed and prints the track record (table, or JSON with --output json).
func scorePredictions(path, outputFmt string) error {
	records, err := tracking.Load(path)
	if err != nil {
		return err
	}
	logging.Logf("Scoring %d logged prediction(s) from %s ...", len(records), path)
	client := &http.Client{Timeout: 30 * time.Second}
	rep := tracking.Score(records, func(symbol string) ([]domain.PricePoint, error) {
		return yahoo.FetchChartHistory(client, symbol)
	}, time.Now())
	if strings.ToLower(outputFmt) == "json" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	fmt.Print(tracking.FormatReport(rep, path))
	return nil
}
