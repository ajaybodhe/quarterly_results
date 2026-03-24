# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
# Build
go build ./...

# Run (requires --from and --to)
go run . --from 2026-03-10 --to 2026-03-14
go run . --from 2026-03-10 --to 2026-03-14 --output csv
go run . --from 2026-03-10 --to 2026-03-14 --output json
go run . --from 2026-03-10 --to 2026-03-14 --min-cap-b 50

# Test all
go test ./...

# Test a single function
go test -run TestComputeMaxPain
go test -run TestParseSAEstimates
```

## Architecture

Single `main` package. The program fetches the Nasdaq earnings calendar for a date range, filters by market cap, enriches each stock with financial data from multiple APIs, then outputs tables/CSV/JSON.

**Data flow:**
1. `nasdaq.go` → fetches earnings calendar (symbol, date, time, market cap)
2. `main.go` → filters by `--min-cap-b`, builds preliminary `[]EarningsResult`
3. `enricher.go` → `EnrichAll()` runs 5 concurrent goroutines, each calling `buildSummary()` which chains all data fetches for one stock
4. `main.go` → assembles `EarningsResult` fields from `FinancialSummary`, sorts, outputs

**Key files:**

| File | Responsibility |
|---|---|
| `main.go` | Entry point, `EarningsResult` / `EarningsEvent` types, CLI flags, final assembly loop |
| `enricher.go` | `FinancialSummary` struct, `Enricher`, `buildSummary()`, price history + PE/PS ratios |
| `sec.go` | `SECClient`: XBRL quarterly actuals (revenue/EPS), 8-K announcement dates, Form 4 insider filings |
| `nasdaq.go` | Nasdaq calendar, historical prices, forward EPS forecasts, current quote |
| `stockanalysis.go` | Revenue/EPS consensus estimates and analyst ratings (scraped from stockanalysis.com) |
| `options.go` | Yahoo Finance options: crumb auth, IV, Expected Move, P/C ratio, Skew, Max Pain |
| `finviz.go` | Institutional ownership (scrapes Finviz) |
| `workday.go` | NYSE market holiday calendar, `isWorkingDay`, `nextWorkingDay`, `prevWorkingDay` |
| `format.go` | Pure math/string helpers: `pctChange`, `closestPrice`, `fmtPct`, `fmtDollars`, `fmtRatio`, etc. |
| `output.go` | All `write*` functions: `writeTable`, `writeCSV`, `writeJSON`, `writeOptionsTable`, etc. |

## Important Implementation Details

**SEC XBRL comparative data:** When a company files a 10-Q, the XBRL data includes comparative prior-year figures tagged with the current filing date. `fetchConcept` in `sec.go` applies a 150-day cap (`filed - periodEnd <= 150 days`) to reject these comparative re-filings. Within the window, the most recently filed date wins (handles amendments).

**Yahoo Finance auth:** Yahoo requires a crumb token tied to a cookie session. `ensureYahooCrumb()` in `options.go` hits `fc.yahoo.com` (returns 404 but sets the `A3` cookie), then fetches `query2.finance.yahoo.com/v1/test/getcrumb`. All Yahoo API calls use `yahooClient` (has a cookie jar) with `?crumb=` appended.

**Price history:** Nasdaq API caps at ~300 rows. `fetchPriceHistory` makes two sequential calls covering 9-month halves then merges/deduplicates, providing ~18 months needed for 4-quarter reaction history with pre-earnings lookback.

**Earnings reactions:** Computed in `buildSummary` using the 8-K filing date as announcement date (more accurate than 10-Q filing date). Sanity check: announcement must be 10–91 days after period end, else the 8-K lookup returned a wrong filing.

**Valuation ratios:** PE(ttm) and PE(forward) use current price from Nasdaq historical API (last row). PS uses `MarketCapB` from the Nasdaq calendar (no price fetch needed).
