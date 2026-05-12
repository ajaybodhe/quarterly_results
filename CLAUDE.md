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

# LLM rating from the sibling llm-earnings-agent Python project (off by default)
go run . --symbol AAPL --llm-rating

# Test all
go test ./...

# Test a single function
go test -run TestComputeMaxPain
go test -run TestExchangeForTicker
go test -run TestComputeRecommendation
```

## Architecture

Single `main` package. Fetches an earnings calendar for a date range, filters by market cap and (optionally) reporting time, enriches each stock through provider-interface implementations, then outputs tables/CSV/JSON.

Data sources are abstracted behind interfaces in `provider.go` (`CalendarProvider`, `PriceProvider`, `FinancialsProvider`, `HolidayCalendar`). `NewProvidersForExchange(cfg)` returns a US bundle (Nasdaq + SEC) or international bundle (Finnhub + Yahoo) — the enrichment pipeline is identical for both.

**Data flow:**
1. `main.go` → parses `--exchange` (or infers from ticker suffix), builds `ExchangeConfig`, picks calendar provider
2. Calendar provider (`us_calendar.go` for US, `intl_calendar.go` for LSE/FSE/Euronext) → fetches earnings calendar
3. `main.go` → filters by `--min-cap-b` and `--timing`, builds preliminary `[]EarningsResult`
4. `enricher.go` → `EnrichAll()` runs 5 concurrent goroutines; `buildSummary()` chains all data fetches via `e.providers.*`
5. `main.go` → assembles `EarningsResult` from `FinancialSummary`, sorts, outputs with currency-aware formatting

**Key files:**

| File | Responsibility |
|---|---|
| `main.go` | Entry point, `EarningsResult` / `EarningsEvent` types, CLI flags, exchange parsing, timing filter, final assembly |
| `exchange.go` | `Exchange` enum, `ExchangeConfig`, ticker↔Yahoo↔Finnhub symbol conversion, currency helpers |
| `provider.go` | Provider interfaces + `NewProvidersForExchange` factory bundling concrete implementations |
| `enricher.go` | `FinancialSummary` struct, `Enricher`, `buildSummary()`, US-only guards for institutional/peers/events |
| `us_calendar.go` | Nasdaq earnings calendar, US prices, forward EPS forecasts (formerly `nasdaq.go`) |
| `intl_calendar.go` | Finnhub earnings calendar for LSE/FSE/Euronext |
| `yahoo_price.go` | Yahoo Finance v8 chart API for international price history |
| `yahoo_financials.go` | Yahoo `quoteSummary` fallback for quarterly EPS + revenue + announcement date when SEC XBRL returns nothing (e.g. US-listed Foreign Private Issuers like NBIS that file 6-K instead of 10-Q) |
| `finnhub.go` | Finnhub MSPR (insider sentiment) — 12-month window |
| `finnhub_financials.go` | Finnhub-based `FinancialsProvider` for international stocks |
| `sec.go` | `SECClient`: XBRL quarterly actuals, 8-K announcement dates, Form 4 insiders, SIC peers (US only) |
| `stockanalysis.go` | Revenue/EPS consensus + analyst ratings (scraped) |
| `options.go` | Yahoo Finance options: crumb auth, IV, Expected Move, P/C ratio, Skew, Max Pain |
| `finviz.go` | Institutional ownership scraper (US only — guarded) |
| `peers.go` | Sector peers via SEC SIC codes (US only — guarded); also computes peer PE_TTM / PS for industry medians. Consults `peer_overrides.go` first |
| `peer_overrides.go` | File-backed peer cache (`peer_overrides.json`) with three resolution layers: manual entries → Yahoo `recommendationsbysymbol` → LLM via `claude -p`. Yahoo and LLM results are cached back to disk so each ticker is resolved once |
| `sector_momentum.go` | SIC → sector ETF map (XLK, SOXX, XLF, XLV, etc.); fetches 1M / 3M ETF returns via Yahoo with per-process cache |
| `signals.go` | 11 directional signals (`signalValuationVsGrowth`, `signalGrowthTrajectory`, `signalPEvsIndustry`, `signalPosition`, `signalInsider`, `signalInstitutional`, `signalOptionsSentiment`, `signalBeatHistory`, `signalReactionTendency`, `signalPeerReactions`, `signalSectorMomentum`); each returns `(score, confidence, reason)` |
| `score.go` | `Recommendation` type, `ComputeRecommendation` aggregator (weight × score × confidence), label thresholds, top-reasons ranking |
| `backtest.go` | Walk-forward Tier-1 backtest using `BacktestSignals` subset; `RunBacktest` / `BacktestSummary` / `FormatBacktestSummary` |
| `llm_rating.go` | `LLMRating` struct + `FetchLLMRating` subprocess wrapper around the sibling `llm-earnings-agent` Python CLI; rendered by `writeLLMRating`. Result is parallel to `Recommendation` — it is **not** mixed into `ComputeRecommendation` |
| `workday.go` | `HolidayCalendar` interface + NYSE/LSE/XETRA/Euronext implementations, `NewHolidayCalendar` factory |
| `macro.go` | FOMC/CPI/NFP (US), ECB Rate (FSE/Euronext), BoE Rate (LSE); `LoadMacroCalendar(from, to, cfg)` |
| `format.go` | Math/string helpers: `pctChange`, `fmtCurrency`, `fmtCurrencyB`, `computeResultDate`, etc. |
| `output.go` | All `write*` functions: `writeTable`, `writeCSV`, `writeJSON`, `writeOptionsTable`, etc. |

## Important Implementation Details

**Exchange inference:** Yahoo-style suffix on the ticker (`.L`, `.DE`, `.PA`, `.AS`, `.BR`, `.LS`) determines the exchange. `--exchange` overrides and auto-appends the suffix. Bare ticker with no flag defaults to US — international users must pass either a suffix or `--exchange`.

**Timing filter:** `--timing bmo|amc` is applied in `main.go` *before* enrichment fires, so excluded stocks consume zero API calls. Empty `--timing` keeps all stocks.

**International data gaps:** Institutional ownership (Finviz), sector peers (SEC SIC), and material 8-K events are US-only. The enricher's US-only guard (`isUS := e.exCfg.Exchange == ExchangeUS || e.exCfg.Exchange == ""`) skips these provider calls for non-US exchanges; output renders "N/A" for the corresponding sections.

**SEC XBRL comparative data:** When a company files a 10-Q, the XBRL data includes comparative prior-year figures tagged with the current filing date. `fetchConcept` in `sec.go` applies a 150-day cap (`filed - periodEnd <= 150 days`) to reject these comparative re-filings. Within the window, the most recently filed date wins (handles amendments).

**Foreign Private Issuers (FPI) on US exchanges:** Tickers like NBIS (Nebius), TSEM (Tower Semi), TAK (Takeda) file 6-K interim reports and 20-F annuals instead of 10-Q/10-K, so the SEC XBRL companyconcept API returns no quarterly facts and the History/EarningsReactions blocks would otherwise be empty. The `buildSummary` history goroutine detects this (primary provider returns <2 quarters for a US-exchange ticker) and falls through to `fetchYahooQuarterlyActuals` in `yahoo_financials.go`, which queries Yahoo's `quoteSummary` for `earnings.earningsChart.quarterly` (periodEnd + reportedDate + EPS) merged with `incomeStatementHistoryQuarterly.incomeStatementHistory` (revenue). Yahoo's `reportedDate` doubles as the announcement date, so `EarningsReactions` rebuild correctly without needing the SEC 8-K lookup.

**Yahoo Finance auth:** Yahoo requires a crumb token tied to a cookie session. `ensureYahooCrumb()` in `options.go` hits `fc.yahoo.com` (returns 404 but sets the `A3` cookie), then fetches `query2.finance.yahoo.com/v1/test/getcrumb`. All Yahoo API calls use `yahooClient` (has a cookie jar) with `?crumb=` appended.

**Price history (US):** Nasdaq API caps at ~300 rows. `us_calendar.go`'s `fetchPriceHistory` makes two sequential calls covering 9-month halves then merges/deduplicates, providing ~18 months needed for 4-quarter reaction history with pre-earnings lookback.

**Price history (international):** Yahoo `chart` endpoint with `range=2y` returns ~500 rows in one call.

**Earnings reactions:** Computed in `buildSummary` using the 8-K filing date as announcement date for US (more accurate than 10-Q filing date). Sanity check: announcement must be 10–91 days after period end. International uses Finnhub's reported announcement date directly.

**Valuation ratios:** PE(ttm) and PE(forward) use current price from the price history (last row). PS uses `MarketCapB` from the calendar (no price fetch needed).

**MSPR sparsity:** `finnhub.go` queries a 12-month window for insider sentiment because large-cap insiders trade infrequently — a 3-month window returned no data for many tickers. MSPR-unavailable is logged as "Note" rather than "Warning" since it's expected.

**Currency:** `FinancialSummary` and `EarningsResult` carry a `Currency` field (USD/GBP/EUR). `fmtCurrency` and `fmtCurrencyB` apply the right symbol (£/€/$). Insider transaction values stay in USD (Form 4 reports in USD; Finnhub returns local currency for international, displayed as-is).

**Recommendation pipeline:** After all per-stock data is collected, `buildSummary` calls `ComputeRecommendation(s)` which runs every entry in `DefaultSignals` and aggregates them as `Σ weight × score × confidence / Σ weight × 100`. Confidence acts as a multiplier inside the sum: a low-confidence signal pulls the score toward zero rather than swinging it. Label thresholds (`labelThresholdScore=15`, `labelThresholdConfidence=0.4`) are intentionally conservative — when in doubt, output is "Neutral". The `TopReasons` field surfaces the three highest-impact `|weight × score × confidence|` reason strings for human-readable output.

**SIC fetch fan-out:** Both `peers.go` and `sector_momentum.go` need the SIC code. `buildSummary` fetches it once into `sicCode` behind a `sicReady` channel; the peers and sector-momentum goroutines both `<-sicReady` before proceeding. This avoids two SEC ticker-map lookups per stock.

**Peer resolution order:** `fetchPeers` in `peers.go` consults `peer_overrides.go` before falling back to SIC matching. The override store (`peer_overrides.json` at repo root, override path with `PEER_OVERRIDES_FILE`) holds three kinds of entries: `"manual"` (curated, e.g. NBIS→[CRWV,NET,AKAM]), `"yahoo"` (auto-populated from Yahoo's `recommendationsbysymbol` v6 endpoint), and `"llm"` (auto-populated from `claude -p` asking for product/market competitors). When an override is present, the cap-band filter and SIC sector check are both skipped — the curated list is treated as authoritative and only restricted by "must have reported in the ±75-day calendar window." Yahoo and LLM responses are written back to disk on first resolution so subsequent runs hit the cache. Delete an entry from `peer_overrides.json` to force re-resolution.

**LLM rating (opt-in via `--llm-rating`):** when the flag is set, `buildSummary` runs `FetchLLMRating(ctx, symbol)` as a Phase 1 goroutine alongside the others. It shells out to the sibling `llm-earnings-agent analyze --symbol X --output json` binary (override path with `LLM_AGENT_BIN`) with a 5-minute timeout. Errors are logged and swallowed so a failing LLM call never blocks the rest of the pipeline. The result is rendered by `writeLLMRating` directly below `writeRecommendation` in the stock card and is **not** fed back into `ComputeRecommendation` — the LLM rating and the deterministic Rating stay independent until track record warrants mixing.

**Industry medians:** After the peers list is populated, `peerValuationMedians()` derives median PE_TTM and PS across peers that reported usable values. These feed `signalPEvsIndustry`. Peers compute their own PE/PS in `peers.go` from each peer's TTM EPS / TTM Revenue at the matching quarter, divided by current price / market cap.

**Backtest scope (Tier-1 only):** `RunBacktest` evaluates only `BacktestSignals` (`position`, `reaction_tendency`, `peer_reactions`, `sector_momentum`) — the strict subset reconstructable at a historical point in time without lookahead. The eight signals that depend on *current* fundamentals (PE/PS at date X, MSPR at date X, options snapshot, current consensus estimates) are excluded because we don't have the historical snapshots. Walk-forward: for each prior reaction, prices are truncated to bars `< announcement_date`, prior reactions are restricted to the strict prefix, and `SectorMomentumAt` recomputes the ETF return ending at that date. The harness compares predicted label to actual signed return (±1% deadband), reports hit rate vs the always-guess-most-common-label baseline.
