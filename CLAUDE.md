# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build ./...

# Run (US default)
go run . --from 2026-03-10 --to 2026-03-14
go run . --from 2026-03-10 --to 2026-03-14 --output csv
go run . --from 2026-03-10 --to 2026-03-14 --output json
go run . --from 2026-03-10 --to 2026-03-14 --min-cap-b 50

# Single ticker (suffix auto-detects exchange)
go run . --symbol AAPL
go run . --symbol VOD.L            # LSE
go run . --symbol BMW.DE           # FSE / XETRA
go run . --symbol AIR.PA           # Euronext Paris

# Explicit exchange (no suffix needed)
go run . --symbol AIR --exchange EURONEXT
go run . --from 2026-05-04 --to 2026-05-09 --exchange LSE

# Filter by reporting time (skips enrichment for excluded stocks)
go run . --from 2026-03-10 --to 2026-03-14 --timing bmo
go run . --from 2026-03-10 --to 2026-03-14 --timing amc

# Walk-forward backtest of the recommendation signals on prior reactions
go run . --symbol AAPL --backtest

# LLM rating from the sibling llm-earnings-agent Python project (off by default).
# Default agents: transcript,news (the inputs this repo doesn't have).
go run . --symbol AAPL --llm-rating
go run . --symbol AAPL --llm-rating --llm-agents all   # all five; payload sent on stdin

# Prediction track record: log upcoming reports' ratings, score them once they report
go run . --from 2026-10-19 --to 2026-10-23 --llm-rating --log-predictions
go run . --score-predictions                  # table; add --output json for raw outcomes

# Test all (some tests hit the network; the full run takes ~4–5 min)
go test -timeout 600s ./...

# Test a single package / function
go test ./internal/source/yahoo -run TestComputeMaxPain
go test ./internal/exchange -run TestExchangeForTicker
go test ./internal/recommend -run TestComputeRecommendation

# Live-API integration tests (build tag), one file per package
go test -tags integration -run TestIntegration -timeout 300s ./...
```

## Architecture

Thin CLI (`main.go`) over packages in `internal/`, split by domain responsibility. Fetches an earnings calendar for a date range, filters by market cap and (optionally) reporting time, enriches each stock through provider-interface implementations, then outputs tables/CSV/JSON.

Data sources are abstracted behind interfaces in `internal/provider` (`CalendarProvider`, `PriceProvider`, `FinancialsProvider`; `HolidayCalendar` lives in `internal/exchange`). `provider.NewProvidersForExchange(cfg, …)` returns a US bundle (SEC + Nasdaq + Yahoo prices) or international bundle (Finnhub + Yahoo) — the enrichment pipeline is identical for both.

**Layering** (imports only point downward; `domain` imports nothing internal):

```
main.go
  └─ enrich ──┬─ peers, sector, recommend, backtest      (analysis)
              ├─ provider ── source/{sec,nasdaq,finnhub,yahoo}
              ├─ source/{stockanalysis,finviz,llmagent}  (scrapers / external tools)
              ├─ report (AssembleResult, to build the LLM payload)
              └─ macro, exchange, indicators, logging
  └─ report (presentation)
  └─ tracking (prediction log + scoring)
domain  ← shared data model used by every layer
```

**Data flow:**
1. `main.go` → parses `--exchange` (or infers from ticker suffix via `exchange.ForTicker`), builds `exchange.Config`, picks calendar provider (`nasdaq.Client` for US, `finnhub.CalendarClient` for LSE/FSE/Euronext)
2. Calendar provider → fetches earnings calendar (`[]domain.EarningsEvent` + `map[string]domain.CalendarRow`)
3. `main.go` → filters by `--min-cap-b` and `--timing`, builds preliminary `[]domain.EarningsResult`
4. `enrich.Enricher.EnrichStream()` runs 5 concurrent stocks; `buildSummary()` chains all data fetches via `e.providers.*` and the source clients
5. `report.AssembleResult` maps `domain.FinancialSummary` → `domain.EarningsResult`; `main.go` sorts; `report.Write*` outputs with currency-aware formatting

**Packages:**

| Package | Responsibility |
|---|---|
| `main.go` (root) | Entry point: CLI flags, exchange parsing, market-cap / timing filters, output-format dispatch |
| `internal/domain` | Shared data model, types only: `EarningsEvent`, `CalendarRow`, `QuarterActual`, `ForwardQuarter`, `EarningsReaction`, `PricePoint`, `FinancialSummary`, `EarningsResult`, `InsiderSummary`, `MaterialEvent`, `InstitutionalData`, `OptionsSnapshot`/`GEX*`, `PeerResult`, `Recommendation`, `SignalResult`, `BacktestSummary`, `LLMRating` |
| `internal/exchange` | `Exchange` enum, `Config`, ticker↔Yahoo↔Finnhub symbol conversion, `CurrencySymbol`; `holidays.go`: `HolidayCalendar` interface + NYSE/LSE/XETRA/Euronext calendars, `NewHolidayCalendar`, `ComputeResultDate` |
| `internal/macro` | FOMC/CPI/NFP (US), ECB Rate (FSE/Euronext), BoE Rate (LSE); `LoadCalendar(from, to, cfg)`, `Calendar.EventsNear`, `FormatContext` |
| `internal/indicators` | Price math: `PctChange`, `ClosestPrice`, `OpenOnDate`, `DailyVolatility`, `RSI14`, `Median`, `ReactionDay` (BMO/AMC-aware reaction day + prior close) |
| `internal/logging` | `Logf` (stderr progress/warnings) |
| `internal/provider` | Provider interfaces + `NewProvidersForExchange` factory; `us.go`: `USFinancials` (SEC filings + Nasdaq forward EPS) |
| `internal/source/sec` | `sec.Client`: XBRL quarterly actuals, 8-K announcement dates + material events, Form 4 insiders, SIC codes (US only) |
| `internal/source/nasdaq` | Nasdaq earnings calendar (`Client`), `PriceProvider` (delegates bars to Yahoo chart, per-date EPS estimates from Nasdaq), `FetchForwardEPS` |
| `internal/source/finnhub` | Finnhub client + MSPR (insider sentiment, 12-month window), `CalendarClient` for LSE/FSE/Euronext, `FinancialsProvider` for international stocks |
| `internal/source/yahoo` | `client.go`: cookie + crumb session (`Client`); `prices.go`: v8 chart `PriceProvider` + `FetchChartHistory`; `financials.go`: `quoteSummary` fallback for quarterly actuals (FPIs); `options.go`: IV, Expected Move, P/C, Skew, Max Pain, GEX |
| `internal/source/stockanalysis` | Revenue/EPS consensus + analyst ratings (`FetchEstimates`), quarterly history backfill (`FetchQuarterlyHistory`, `MergeHistoryWithYahoo`) |
| `internal/source/finviz` | Institutional ownership scraper (US only — guarded) |
| `internal/source/llmagent` | `FetchRating` subprocess wrapper around the sibling `llm-earnings-agent` Python CLI; agent selection (`ParseAgents`, `DefaultAgents`, `NeedsPayload`) |
| `internal/peers` | `Finder.Find`: sector peers via SEC SIC codes (US only — guarded), peer reactions, peer PE_TTM / PS for industry medians. `overrides.go`: file-backed peer cache (`peer_overrides.json`) with three layers: manual → Yahoo `recommendationsbysymbol` → LLM via `claude -p`; Yahoo/LLM results are cached back to disk |
| `internal/sector` | SIC → sector group (`SICGroup`) and sector ETF map (XLK, SOXX, XLF, XLV, …); `FetchMomentum` / `MomentumAt` (1M / 3M ETF returns, per-process cache) |
| `internal/recommend` | `signals.go`: 11 directional signals (`signalValuationVsGrowth`, `signalGrowthTrajectory`, `signalPEvsIndustry`, `signalPosition`, `signalInsider`, `signalInstitutional`, `signalOptionsSentiment`, `signalBeatHistory`, `signalReactionTendency`, `signalPeerReactions`, `signalSectorMomentum`), each returning `(score, confidence, reason)`; `score.go`: `ComputeRecommendation` aggregator, label thresholds, top-reasons ranking |
| `internal/backtest` | Walk-forward Tier-1 backtest using `recommend.BacktestSignals`; `RunBacktest` / `FormatBacktestSummary` |
| `internal/enrich` | `Enricher` (`Config`, `EnrichStream`, `EnrichAll`), `buildSummary()`, US-only guards for institutional/peers/events, `peerValuationMedians` |
| `internal/report` | `AssembleResult`, all `Write*` output (table/stock cards, CSV, JSON, LLM rating block), `fmt*` formatting helpers |
| `internal/tracking` | Prediction log (`predictions.jsonl`, JSON lines) and scoring against actual reactions: `NewRecord`, `Log`, `Load`, `Score`, `FormatReport` |
| `internal/testutil` | `MockTransport` / `NewMockClient` HTTP mocks and shared test fixtures (`MustDate`, `ConceptJSON`) |

## Important Implementation Details

**Exchange inference:** Yahoo-style suffix on the ticker (`.L`, `.DE`, `.PA`, `.AS`, `.BR`, `.LS`) determines the exchange. `--exchange` overrides and auto-appends the suffix. Bare ticker with no flag defaults to US — international users must pass either a suffix or `--exchange`.

**Timing filter:** `--timing bmo|amc` is applied in `main.go` *before* enrichment fires, so excluded stocks consume zero API calls. Empty `--timing` keeps all stocks.

**International data gaps:** Institutional ownership (Finviz), sector peers (SEC SIC), and material 8-K events are US-only. The enricher's US-only guard (`isUS := e.exCfg.Exchange == exchange.ExchangeUS || e.exCfg.Exchange == ""` in `internal/enrich`) skips these provider calls for non-US exchanges; output renders "N/A" for the corresponding sections.

**SEC XBRL comparative data:** When a company files a 10-Q, the XBRL data includes comparative prior-year figures tagged with the current filing date. `fetchConcept` in `internal/source/sec` applies a 150-day cap (`filed - periodEnd <= 150 days`) to reject these comparative re-filings. Within the window, the most recently filed date wins (handles amendments).

**Foreign Private Issuers (FPI) on US exchanges:** Tickers like NBIS (Nebius), TSEM (Tower Semi), TAK (Takeda) file 6-K interim reports and 20-F annuals instead of 10-Q/10-K, so the SEC XBRL companyconcept API returns no quarterly facts and the History/EarningsReactions blocks would otherwise be empty. The `buildSummary` history goroutine detects this (primary provider returns <2 quarters for a US-exchange ticker) and falls through to `yahoo.Client.FetchQuarterlyActuals` (`internal/source/yahoo/financials.go`), backfilled with `stockanalysis.FetchQuarterlyHistory` when Yahoo returns only a few quarters, which queries Yahoo's `quoteSummary` for `earnings.earningsChart.quarterly` (periodEnd + reportedDate + EPS) merged with `incomeStatementHistoryQuarterly.incomeStatementHistory` (revenue). Yahoo's `reportedDate` doubles as the announcement date, so `EarningsReactions` rebuild correctly without needing the SEC 8-K lookup.

**Yahoo Finance auth:** Yahoo requires a crumb token tied to a cookie session. `yahoo.Client.ensureCrumb()` in `internal/source/yahoo/client.go` hits `fc.yahoo.com` (returns 404 but sets the `A3` cookie), then fetches `query2.finance.yahoo.com/v1/test/getcrumb`. All crumb-authenticated Yahoo calls go through the shared `yahoo.Client` (its `http.Client` has a cookie jar; `HTTP()` exposes it to other Yahoo endpoints) with `?crumb=` appended.

**Price history (US):** `nasdaq.PriceProvider.FetchPriceHistory` delegates to Yahoo's chart API (`yahoo.FetchChartHistory`, ~500 daily bars in one call) to keep Nasdaq traffic to the calendar plus per-date EPS lookups (Nasdaq's Akamai bot manager bans bursty clients).

**Price history (international):** Yahoo `chart` endpoint with `range=2y` returns ~500 rows in one call.

**Price bar dates:** every `PricePoint.Date` is midnight UTC of the exchange-local trading day, matching how the code parses `YYYY-MM-DD` strings. Yahoo stamps daily bars with the session open (13:30 UTC for US), so `yahoo.parseChart` re-dates each bar via the chart's `exchangeTimezoneName`. Without this, `ClosestPrice(prices, date)` returns the *previous* day's close and every earnings reaction is shifted a day early.

**Earnings reactions:** Computed in `buildSummary` using the 8-K filing date as announcement date for US (more accurate than 10-Q filing date). Sanity check: announcement must be 10–91 days after period end. International uses Finnhub's reported announcement date directly.

**Valuation ratios:** PE(ttm) and PE(forward) use current price from the price history (last row). PS uses `MarketCapB` from the calendar (no price fetch needed).

**MSPR sparsity:** `finnhub.Client.FetchMSPR` (`internal/source/finnhub`) queries a 12-month window for insider sentiment because large-cap insiders trade infrequently — a 3-month window returned no data for many tickers. MSPR-unavailable is logged as "Note" rather than "Warning" since it's expected.

**Currency:** `domain.FinancialSummary` and `domain.EarningsResult` carry a `Currency` field (USD/GBP/EUR). `fmtCurrency` and `fmtCurrencyB` (`internal/report`) apply the right symbol (£/€/$). Insider transaction values stay in USD (Form 4 reports in USD; Finnhub returns local currency for international, displayed as-is).

**Recommendation pipeline:** After all per-stock data is collected, `buildSummary` calls `recommend.ComputeRecommendation(s)` which runs every entry in `recommend.DefaultSignals` and aggregates them as `Σ weight × score × confidence / Σ weight × 100`. Confidence acts as a multiplier inside the sum: a low-confidence signal pulls the score toward zero rather than swinging it. Label thresholds (`labelThresholdScore=15`, `labelThresholdConfidence=0.4`) are intentionally conservative — when in doubt, output is "Neutral". The `TopReasons` field surfaces the three highest-impact `|weight × score × confidence|` reason strings for human-readable output.

**SIC fetch fan-out:** Both `internal/peers` and `internal/sector` need the SIC code. `buildSummary` fetches it once into `sicCode` behind a `sicReady` channel; the peers and sector-momentum goroutines both `<-sicReady` before proceeding. This avoids two SEC ticker-map lookups per stock.

**Peer resolution order:** `peers.Finder.Find` consults the override store (`internal/peers/overrides.go`) before falling back to SIC matching. The override store (`peer_overrides.json` at repo root, override path with `PEER_OVERRIDES_FILE`) holds three kinds of entries: `"manual"` (curated, e.g. NBIS→[CRWV,NET,AKAM]), `"yahoo"` (auto-populated from Yahoo's `recommendationsbysymbol` v6 endpoint), and `"llm"` (auto-populated from `claude -p` asking for product/market competitors). When an override is present, the cap-band filter and SIC sector check are both skipped — the curated list is treated as authoritative and only restricted by "must have reported in the ±75-day calendar window." Yahoo and LLM responses are written back to disk on first resolution so subsequent runs hit the cache. Delete an entry from `peer_overrides.json` to force re-resolution.

**LLM rating (opt-in via `--llm-rating`):** shells out to the sibling `llm-earnings-agent analyze --symbol X --output json --agents …` (binary: `LLM_AGENT_BIN`, else `llm-earnings-agent` on PATH, else `../llm_earnings_agent/.venv/bin/llm-earnings-agent`) with a 5-minute timeout. `--llm-agents` picks the specialists; the default `transcript,news` covers only inputs this repo lacks (latest earnings press release, news headlines), while `fundamentals`, `macro` and `dynamic` re-score fields the deterministic signals already use. Text-only agents run as a Phase 1 goroutine in `buildSummary`. If any payload agent is selected, `Enricher.summarize` (`internal/enrich/llm.go`) runs the call *after* enrichment and sends `report.AssembleResult(...)` JSON on stdin (`--payload -`), so the Python side never re-runs quarterly_results. Errors are logged and swallowed so a failing LLM call never blocks the pipeline. The result is rendered by `report.WriteLLMRating` below the deterministic Rating and is **not** fed into `ComputeRecommendation` — the two stay independent until the prediction log shows the LLM adds value.

**Prediction log (`--log-predictions`, `--score-predictions`):** `main.go` appends one `tracking.Record` per stock with an upcoming earnings date (deterministic Rating, LLM rating, LLM sub-agent scores) to `--predictions-file` (default `predictions.jsonl`, gitignored). Re-running before a report adds more records. `tracking.Score` groups records per report (earnings dates within 45 days are the same report, so a moved date is handled) and uses the latest record logged before the release cutoff: 06:00 local for `bmo`, 16:00 for `amc`, midnight of the earnings date if the timing is unknown. It measures the reaction exactly like `EarningsReaction.RetPct`, via `indicators.ReactionDay` on Yahoo prices, labels it with `backtest.LabelFromReturn` (±1%), and reports hits / directional calls for `rating`, `rating (LLM-covered stocks)` (like-for-like), `llm`, and each `llm.<agent>`. Sub-agent calls use the aggregator's thresholds (score × confidence ≥ 15 on the 100-point scale, confidence ≥ 0.4).

**Industry medians:** After the peers list is populated, `peerValuationMedians()` derives median PE_TTM and PS across peers that reported usable values. These feed `signalPEvsIndustry`. Peers compute their own PE/PS in `internal/peers` from each peer's TTM EPS / TTM Revenue at the matching quarter, divided by current price / market cap.

**Backtest scope (Tier-1 only):** `backtest.RunBacktest` evaluates only `recommend.BacktestSignals` (`position`, `reaction_tendency`, `peer_reactions`, `sector_momentum`) — the strict subset reconstructable at a historical point in time without lookahead. The eight signals that depend on *current* fundamentals (PE/PS at date X, MSPR at date X, options snapshot, current consensus estimates) are excluded because we don't have the historical snapshots. Walk-forward: for each prior reaction, prices are truncated to bars `< announcement_date`, prior reactions are restricted to the strict prefix, and `sector.MomentumAt` recomputes the ETF return ending at that date. The harness compares predicted label to actual signed return (±1% deadband), reports hit rate vs the always-guess-most-common-label baseline.
