# Quarterly Results Analyser

A CLI that fetches the Nasdaq earnings calendar for a date range, enriches each stock with data from multiple sources, and outputs a per-stock analysis card (or CSV/JSON) designed to help you assess how a stock might react on its earnings day.

---

## Quick Start

```bash
# Build
go build ./...

# All large-caps reporting next week (market cap > $10B by default)
go run . --from 2026-05-04 --to 2026-05-09

# Single stock, any date range
go run . --symbol NVDA

# International stocks — exchange is auto-detected from the Yahoo Finance suffix
go run . --symbol VOD.L          # LSE (London)
go run . --symbol BMW.DE         # FSE / XETRA (Frankfurt)
go run . --symbol AIR.PA         # Euronext Paris

# Or pass exchange explicitly with a bare ticker
go run . --symbol VOD --exchange LSE
go run . --symbol BMW --exchange FSE
go run . --symbol AIR --exchange EURONEXT

# LSE calendar scan with market-cap filter
go run . --from 2026-05-04 --to 2026-05-09 --exchange LSE --min-cap-b 5

# Filter by market cap range (US)
go run . --from 2026-05-04 --to 2026-05-09 --min-cap-b 50 --max-cap-b 500

# CSV or JSON output
go run . --from 2026-05-04 --to 2026-05-09 --output csv > results.csv
go run . --symbol AAPL --output json > aapl.json

# Deep options analysis: compute dealer gamma exposure (see GEX section)
go run . --symbol AAPL --gex
```

---

## CLI Flags

| Flag | Default | Description |
|---|---|---|
| `--from YYYY-MM-DD` | required* | Start of earnings date range to scan |
| `--to YYYY-MM-DD` | required* | End of earnings date range to scan |
| `--symbol TICKER` | — | Analyse a single stock (skips market-cap filter; `--from`/`--to` optional, defaults to today + 90 days) |
| `--exchange US\|LSE\|FSE\|EURONEXT` | `US` | Exchange to scan. Auto-detected from ticker suffix (`.L`/`.DE`/`.PA` etc.) when `--symbol` is set. |
| `--output table\|csv\|json` | `table` | Output format |
| `--min-cap-b N` | `10` | Minimum market cap in billions USD |
| `--max-cap-b N` | `0` | Maximum market cap in billions USD (0 = no upper limit) |
| `--no-peers` | off | Disable sector peer analysis (faster; US-only feature) |
| `--no-news` | off | Disable material 8-K event fetching (faster; US-only feature) |
| `--gex` | off | Compute dealer gamma exposure table — **requires `--symbol`** |

*`--from`/`--to` are optional when `--symbol` is set.

### Ticker Conventions

| Market | Example ticker | Yahoo suffix | Notes |
|---|---|---|---|
| US (default) | `AAPL` | _(none)_ | NYSE/Nasdaq; SEC EDGAR data |
| London (LSE) | `VOD.L` | `.L` | Prices in GBP pence (p); reported as GBP |
| Frankfurt (XETRA) | `BMW.DE` | `.DE` | Prices in EUR |
| Euronext Paris | `AIR.PA` | `.PA` | Prices in EUR |
| Euronext Amsterdam | `ASML.AS` | `.AS` | Prices in EUR |
| Euronext Brussels | `ABI.BR` | `.BR` | Prices in EUR |
| Euronext Lisbon | `EDP.LS` | `.LS` | Prices in EUR |

The exchange is auto-detected from the suffix when `--symbol` contains one. Use `--exchange` to specify it explicitly with a bare ticker.

### Environment Variables

| Variable | Description |
|---|---|
| `FINNHUB_API_KEY` | Override the default Finnhub API key. A working key is already hardcoded; only set this if you want to use your own account. |
| `SEC_USER_AGENT` | Override the SEC EDGAR User-Agent header. Format: `"Your Name your@email.com"`. Default is `ajaybodhe@gmail.com`. |
| `DISABLE_PEERS=1` | Disable sector peer analysis (same as `--no-peers`) |
| `DISABLE_NEWS=1` | Disable material 8-K event fetching (same as `--no-news`) |

---

## Data Sources

### 1. Nasdaq API (`us_calendar.go`) — US only
**What it provides:**
- Earnings calendar: symbol, company name, earnings date, timing (BMO/AMC), market cap
- Current EPS consensus estimate and last-year EPS for the upcoming quarter
- Daily price history (OHLC) — fetched in two parallel 9-month segments to work around the API's ~300-row cap
- Forward EPS estimates (next 3–4 quarters, consensus/high/low)

**Rate limiting:** No auth required; uses browser-like User-Agent headers. Parallel fetches per stock.

---

### 2. SEC EDGAR (`sec.go`) — US only
**What it provides:**
- **XBRL quarterly actuals** — reported revenue and EPS for the last 8 quarters, sourced from 10-Q/10-K XBRL filings
- **8-K announcement dates** — the date each quarterly earnings press release was filed (more accurate than 10-Q filing dates for computing earnings reactions)
- **Form 4 insider filings** — buy/sell transactions by insiders in the last 3 months
- **Material 8-K events** — significant corporate events filed in the last 90 days: M&A, guidance changes, management changes, restructurings, etc., with a text snippet and sentiment signal

**Auth:** Requires a valid `User-Agent` header per EDGAR policy (email address). Defaults to `ajaybodhe@gmail.com`; override with `SEC_USER_AGENT`.

**Rate limiting:** SEC enforces 10 req/s. The code uses an 8 req/s token-bucket limiter plus an in-memory submissions cache to avoid redundant CIK lookups.

---

### 3. Finnhub (`finnhub.go` / `finnhub_financials.go` / `intl_calendar.go`) — US + International
**What it provides:**
- **MSPR** (Monthly Share Purchase Ratio) — insider buy/sell balance over the last 3 months (all exchanges)
- **International earnings calendar** — `GET /calendar/earnings` filtered by exchange suffix (LSE/FSE/Euronext)
- **International quarterly actuals** — revenue + EPS via `GET /stock/financials-reported` (semi-annual for LSE/Euronext, quarterly for FSE)
- **International insider transactions** — `GET /stock/insider-transactions`
- **Forward EPS estimates** — `GET /stock/eps-estimates` (next quarters, consensus/high/low)

**Auth:** Free API key hardcoded as default; override with `FINNHUB_API_KEY`. Free tier: 60 calls/min.

---

### 4. Yahoo Finance (`options.go` / `yahoo_price.go`)
**What it provides:**
- **Options chain** (US-focused) — full chain for nearest post-earnings expiry; IV, OI, volume per contract
- **International price history** — `GET /v8/finance/chart/{symbol}?interval=1d&range=2y` for non-US tickers (`.L`, `.DE`, `.PA` etc.)
- **VIX history** — `^VIX` fetched via same endpoint for US reaction enrichment

**Auth:** Yahoo requires a session cookie + crumb token for the options API. The code fetches these automatically.

---

### 5. StockAnalysis.com (`stockanalysis.go`) — US + some international
**What it provides:**
- Revenue consensus estimate for the upcoming quarter
- Same-quarter-last-year revenue (for YoY computation)
- Analyst ratings: Strong Buy / Buy / Hold / Sell / Strong Sell counts, consensus rating string
- Average analyst price target

**Method:** HTML scrape (no API key required). 404s for unlisted international tickers are handled gracefully.

---

### 6. Finviz (`finviz.go`) — US only
**What it provides:**
- Institutional ownership percentage (% of shares held by institutions)
- Quarter-over-quarter change in institutional ownership
- Short float (short interest as % of float)
- Short ratio / days-to-cover

**Method:** HTML scrape of the Finviz snapshot table. Skipped automatically for non-US exchanges.

---

### 7. Macro Calendar (`macro.go`)
**What it provides:**
- **US:** FOMC rate decisions, FOMC minutes, Non-Farm Payrolls, CPI, PPI releases (2025–2026)
- **LSE:** BoE Monetary Policy Committee rate decisions (2025–2026)
- **FSE / Euronext:** ECB Governing Council rate decisions (2025–2026)
- US macro events (FOMC/NFP/CPI) are always included even for non-US exchanges — they move global markets
- Shown as context when an event falls within ±2 days of the earnings date or a historical reaction day

**Method:** Hardcoded (BLS blocks programmatic scraping via WAF). Update the date slices in `macro.go` each January.

---

## Data Availability by Exchange

| Feature | US (NYSE/Nasdaq) | LSE | FSE (XETRA) | Euronext |
|---|---|---|---|---|
| Earnings calendar | Nasdaq API | Finnhub | Finnhub | Finnhub |
| Price history | Nasdaq API | Yahoo Finance | Yahoo Finance | Yahoo Finance |
| Quarterly actuals (rev/EPS) | SEC EDGAR | Finnhub (semi-annual) | Finnhub (quarterly) | Finnhub (semi-annual) |
| Forward EPS estimates | Nasdaq forecast API | Finnhub | Finnhub | Finnhub |
| Revenue estimates + ratings | StockAnalysis.com | StockAnalysis.com* | StockAnalysis.com* | StockAnalysis.com* |
| Insider activity | SEC Form 4 + Finnhub | Finnhub | Finnhub | Finnhub |
| Institutional / short data | Finviz | ❌ N/A | ❌ N/A | ❌ N/A |
| Material 8-K events | SEC EDGAR | ❌ N/A | ❌ N/A | ❌ N/A |
| Sector peers | SEC SIC codes | ❌ N/A | ❌ N/A | ❌ N/A |
| Options analytics (IV, GEX) | Yahoo Finance | Limited | ❌ N/A | ❌ N/A |
| Macro context | FOMC / NFP / CPI | BoE + FOMC | ECB + FOMC | ECB + FOMC |
| Holiday calendar | NYSE/Nasdaq | LSE bank holidays | XETRA | Euronext |

\* StockAnalysis.com coverage is variable for international tickers; unavailable tickers return N/A gracefully.

---

## Output Fields — Full Reference

The table output is a per-stock "card". Every section is described below.

---

### Header

```
══════════════════════════════════════════════════════════
 AAPL  Apple Inc.  ·  $3100.0B  ·  Result: 2026-05-01 (BMO)  ·  Mar/2026
══════════════════════════════════════════════════════════
```

| Field | Meaning |
|---|---|
| **Result date** | Date the market can first react. BMO = prior working day's close → today's open is the reaction. AMC = today's close → tomorrow's open is the reaction. |
| **Fiscal quarter** | The quarter being reported (e.g. `Mar/2026` = quarter ending March 2026). |

---

### EPS & Revenue

```
EPS    Est $1.65    Prev Qtr $2.11   (QoQ -21.8%)  Last Year $1.53   (YoY +7.8%)
Rev    Est $94.50B  Prev Qtr $124.30B (QoQ -24.0%) Last Year $90.75B (YoY +4.1%)
```

| Field | Source | Meaning |
|---|---|---|
| **Est** | Nasdaq/StockAnalysis | Consensus estimate for the upcoming quarter |
| **Prev Qtr** | SEC XBRL | Actual from the most recently reported quarter |
| **QoQ** | Computed | `(Estimate − Prev Qtr) / |Prev Qtr| × 100`. Negative when estimate is below the most recent actual (common for seasonal businesses). |
| **Last Year** | SEC XBRL (overrides Nasdaq) | Same fiscal quarter, one year ago. SEC value is used unless the Nasdaq value is within 30% (GAAP vs non-GAAP delta). |
| **YoY** | Computed | `(Estimate − Last Year) / |Last Year| × 100`. Primary growth signal. |

---

### Valuation & Price

```
Val    PE(TTM) 28.3x   PE(Fwd) 24.1x   PS 6.8x
Price  $185.50   1W +2.3%   1M -5.1%   6M +12.4%   1Y +18.7%
```

| Field | Source | Meaning |
|---|---|---|
| **PE(TTM)** | Computed | Current price / sum of EPS from the last 4 reported quarters. High PE = market expects growth; low PE = value or distress. |
| **PE(Fwd)** | Computed | Current price / sum of next 4 quarters' consensus EPS. More forward-looking than TTM. |
| **PS** | Computed | Market cap / trailing 12-month revenue. Useful for unprofitable companies where PE is meaningless. |
| **1W/1M/6M/1Y** | Nasdaq price history | Total return over the period ending at the most recent close. |

---

### Macro Context

```
Macro  ⚠ FOMC same-day | CPI -1d
```

Flags scheduled high-impact macro events within ±2 days of the earnings date. A FOMC or CPI release on the same day can overwhelm the earnings signal — the stock may move on macro rather than fundamentals. Also shown on each historical earnings reaction row.

Events tracked: **FOMC** (rate decision), **FOMC Minutes**, **NFP** (Non-Farm Payrolls), **CPI**, **PPI**.

---

### Quarterly History

```
  Quarterly History:
    PERIOD                REVENUE_B  EPS    REV_QoQ   EPS_QoQ  REV_YoY  EPS_YoY
    2025-06-30            85.78      1.40   +2.1%     +8.5%    —        —
    2025-09-30            94.93      1.64   +10.6%    +17.1%   —        —
    ...
```

Last 5 quarters of actual reported revenue and EPS from SEC XBRL filings. The oldest 3+ quarters serve as YoY anchors but aren't displayed. Use this section to spot trend breaks: accelerating/decelerating growth, margin expansion/compression, seasonality.

```
  Forward EPS:  Jun/2026 $1.72 ($1.58–$1.89, 14 ests)  Sep/2026 $1.68 ...
```

Next 3 quarters of consensus EPS estimates from Nasdaq, with analyst range (low–high) and estimate count.

---

### Analyst Ratings

```
Analyst  Strong Buy    PT $215.00 (upside +16.0%)  Bullish 28 (62%)  Neutral 15 (33%)  Bearish 2 (5%)
```

| Field | Source | Meaning |
|---|---|---|
| **Consensus** | StockAnalysis | Summary label: Strong Buy / Buy / Hold / Sell / Strong Sell |
| **PT** | StockAnalysis | Average analyst 12-month price target |
| **Upside** | Computed | `(Target − Current) / Current × 100`. Negative = analysts think stock is overpriced. |
| **Bullish / Neutral / Bearish** | StockAnalysis | Count (and %) of Strong Buy+Buy / Hold / Sell+Strong Sell ratings |

---

### Institutional & Insider

```
Inst     Activity Net Buyer     Own 81.2%   QoQ +1.3%   Short 0.61%   DaysCover 0.5d
Insider  Activity Net Seller    Buy $0      Sell $12.4M  Net -$12.4M  Filings 3   MSPR 0.21 (Bearish)
```

#### Institutional (Finviz)

| Field | Meaning |
|---|---|
| **Activity** | Net Buyer / Net Seller / No Activity — derived from QoQ change in institutional ownership |
| **Own** | % of total shares outstanding held by institutions (mutual funds, ETFs, hedge funds). > 70% is typical for large-caps. |
| **QoQ** | Quarter-over-quarter change in institutional ownership %. Positive = net buying last 13F quarter; negative = net selling. A large negative (e.g. −5%) before earnings can indicate informed selling. |
| **Short** | Short float — short interest as % of the stock's float. > 10% is elevated; > 20% = heavily shorted. High short float + beat = squeeze risk (sharp upside). |
| **DaysCover** | Short ratio = short interest / average daily volume. How many days it would take all short-sellers to cover at normal volume. > 5 days is elevated. |

#### Insider (SEC Form 4 + Finnhub)

| Field | Meaning |
|---|---|
| **Activity** | Net Buyer / Net Seller / No Activity — based on dollar value of buys vs sells in last 3 months |
| **Buy / Sell / Net** | Dollar value of insider open-market purchases, sales, and net position. Note: option exercises and plan-based sales (10b5-1) are included; not all sales indicate negative sentiment. |
| **Filings** | Count of Form 4 filings (individual transactions) in the last 3 months |
| **MSPR** | Monthly Share Purchase Ratio from Finnhub. `purchases / (purchases + sales)` averaged over the last 3 months. Range 0.0–1.0. |
| | > 0.6 = **Bullish** (insiders net buying) |
| | 0.4–0.6 = **Neutral** |
| | < 0.4 = **Bearish** (insiders net selling) |

---

### Derived Signals

```
Range    52W-Hi $198.23  52W-Lo $142.11  Pct-from-Hi -6.4%   Pct-from-Lo +30.4%   RSI14 58.3
Signals  IV/HistRatio 1.24x   BeatRate 75%   AvgBeat +4.2%
```

| Field | Meaning |
|---|---|
| **52W-Hi / 52W-Lo** | Highest and lowest closing prices over the trailing 52 weeks |
| **Pct-from-Hi** | `(Current − 52W-Hi) / 52W-Hi × 100`. Negative value (e.g. −6%) means 6% below the high — stock is pulling back but not far. Large negative (e.g. −40%) suggests extended weakness. |
| **Pct-from-Lo** | `(Current − 52W-Lo) / 52W-Lo × 100`. Positive value. How far the stock has recovered off its yearly low. |
| **RSI14** | 14-period Wilder RSI of daily closes. < 30 = oversold, > 70 = overbought, 40–60 = neutral. RSI entering earnings near 70 can mean the stock has already priced in a beat. |
| **IV/HistRatio** | `Options expected move % / historical avg |reaction| %`. > 1.0 = options pricing more than history suggests (expensive hedging, or high uncertainty). < 1.0 = options underpricing relative to history (potentially cheap vol). |
| **BeatRate** | Fraction of the last ≤4 quarters where reported EPS exceeded consensus. 75% = beat in 3 of 4. High beat rate going into earnings is a positive setup. |
| **AvgBeat** | Average EPS beat percentage across those quarters. +4.2% = company typically beats by 4.2%. Companies with a history of sandbagging estimates tend to beat consistently. |

---

### Options Setup

```
Options  Exp 2026-05-02  Move ±$8.50 (±4.6%)  IV 62.3%  P/C_Vol 0.84  P/C_OI 0.91  Skew +3.2%  MaxPain $180.00 (-2.7%)  HistAvg ±5.1%
```

All values computed from the options chain for the **nearest expiry on or after the earnings date**, so the straddle fully prices in the earnings move.

| Field | Meaning |
|---|---|
| **Exp** | Expiry date of the chain used |
| **Move ±$N (±X%)** | ATM straddle mid-price = `(call_mid + put_mid)`. This is the market's expected move in either direction. If the stock moves less than this, option buyers lose; more, they win. |
| **IV** | At-the-money implied volatility averaged from the ATM call and put. Higher IV = more uncertainty priced in = more expensive options. Compare to historical vol to judge if options are cheap or expensive. |
| **P/C_Vol** | Put/Call volume ratio = `total put volume / total call volume`. > 1.0 = more put activity (bearish or hedging). < 1.0 = more call activity (bullish or speculation). Context matters — high P/C can be hedging, not directional. |
| **P/C_OI** | Put/Call open interest ratio. Similar to P/C_Vol but based on standing positions (OI) rather than same-day activity. More stable signal. |
| **Skew** | `IV(5%-OTM put) − IV(5%-OTM call)`. Positive skew = puts are more expensive than calls = market fears downside. Negative skew = calls are pricier = market expects upside. Large positive skew (> 5%) before earnings often indicates institutional hedging. |
| **MaxPain** | The strike price where total option-buyer payouts are minimised — i.e. where option *sellers* (typically market makers) lose the least. Some traders believe price gravitates toward max pain at expiry. Shows as a % offset from current price. |
| **HistAvg** | Average absolute day-return across the last ≤4 earnings reactions. Use alongside expected move: if HistAvg is ±5% and Move is ±4.6%, options are fairly priced. If Move is ±8%, vol is rich. |

---

### Dealer Gamma Exposure (GEX) — `--gex` only

```
  Dealer Gamma Exposure  Net GEX: $2.34B  [Positive (dampening)]
    STRIKE    CALL_GEX    PUT_GEX     NET_GEX
    $175.00   $312.4M     $88.1M      $224.3M
    $180.00   $450.2M     $120.5M     $329.7M
    ...
```

GEX measures the net dollar value of delta-hedging flows dealers must execute per 1% move in the stock. Computed per contract as:

```
GEX = Σ (γ_call × OI_call − γ_put × OI_put) × 100 × spot_price
```

Gamma (γ) is not provided by Yahoo Finance — it is computed via Black-Scholes from each contract's IV and time-to-expiry.

| Signal | Meaning |
|---|---|
| **Positive GEX** (dampening) | Dealers are net long gamma. When price rises, dealers *sell* to hedge; when price falls, dealers *buy*. This creates a stabilising effect — smaller gaps, mean-reverting moves. |
| **Negative GEX** (amplifying) | Dealers are net short gamma. When price rises, dealers *buy* more; when price falls, they *sell*. This amplifies directional moves — large gaps become more likely. |
| **GEX near zero** | Inflection point. The market can move sharply in either direction without dealer flow acting as a brake. |

The by-strike table shows the top 15 strikes by |GEX| magnitude, sorted by strike. Large positive NET_GEX at a specific strike = dealers will aggressively buy if price falls to that strike (acts as a floor). Large negative = dealers will sell if price drops there (accelerant).

---

### Past Earnings Reactions

```
  Past Earnings Reactions:
    QUARTER     ANNOUNCED   RXN_DAY     PRIOR_CLS  RXN_OPEN  GAP_RET  RXN_CLS  DAY_RET  PRE7_CLS  PRE7    POST7_CLS  POST7   EPS_EST  EPS_ACT  EPS_BEAT  REV_ACT    VIX   MACRO
    2025-09-30  2025-10-31  2025-11-03  $221.50    $228.40   +3.1%    $226.80  +2.4%    $215.30   +2.9%  $231.10    +1.9%  $1.60    $1.68    +5.0%     $94.93B    18.2  —
```

The last ≤4 quarters of actual earnings reactions, reconstructed from price history and SEC 8-K filing dates.

| Field | Meaning |
|---|---|
| **QUARTER** | Fiscal quarter-end date (the period being reported) |
| **ANNOUNCED** | Date the 8-K earnings press release was filed with the SEC. Used as the announcement date (more accurate than 10-Q filing date). |
| **RXN_DAY** | The first full trading session where the market could react: next working day after AMC release, or same day for BMO. |
| **PRIOR_CLS** | Closing price the day before the announcement (for AMC) or the day before the reaction day (for BMO). This is the baseline for return calculations. |
| **RXN_OPEN** | Opening price on the reaction day — captures the overnight gap. |
| **GAP_RET** | `(RXN_OPEN − PRIOR_CLS) / PRIOR_CLS × 100`. The overnight gap. Large positive = market liked results. |
| **RXN_CLS** | Closing price on the reaction day. |
| **DAY_RET** | `(RXN_CLS − PRIOR_CLS) / PRIOR_CLS × 100`. Full-day move including the gap and intraday action. |
| **PRE7_CLS / PRE7** | Closing price ~7 calendar days before announcement, and return from there to PRIOR_CLS. Positive PRE7 = stock ran into earnings (already priced in). |
| **POST7_CLS / POST7** | Closing price ~7 calendar days after the reaction day, and return from RXN_CLS to there. Positive POST7 = beat sustained; negative = reaction faded. |
| **EPS_EST / EPS_ACT / EPS_BEAT** | Consensus estimate at announcement time, actual reported EPS, and beat percentage `(Actual − Estimate) / |Estimate| × 100`. |
| **REV_ACT** | Actual reported revenue for that quarter (from SEC XBRL). |
| **VIX** | CBOE VIX closing level on the reaction day. High VIX (> 25) means macro noise may have distorted the earnings-driven reaction. |
| **MACRO** | Any FOMC/CPI/NFP/PPI event within ±2 days of the announcement date. |

---

### Material Events (8-K filings, last 90 days)

```
  Material Events (last 90 days):
    2026-03-15  Item 2.02: Results of Operations (press release)  +3.2% ◀ abnormal  [Positive]
                Revenue beat driven by cloud segment growth; raised full-year guidance...
```

Significant SEC 8-K filings in the 90 days before the earnings date.

| Field | Meaning |
|---|---|
| **Date** | 8-K filing date |
| **Label** | SEC item number and description (e.g. `Item 1.01: Entry into Material Agreement`, `Item 5.02: Departure of Directors`) |
| **Ret%** | Stock return on that filing date vs the prior close. Helps gauge how the market reacted to the event at the time. |
| **◀ abnormal** | Flagged when the return exceeds 1.5× the stock's 30-day daily volatility — an unusual move that probably relates to the filing. |
| **[Positive/Negative/Neutral]** | Keyword-based sentiment from the 8-K document text. Positive words: beat, growth, raised, record, expanded, etc. Negative: miss, loss, reduced, impairment, terminated, etc. |
| **Snippet** | First ~120 chars of relevant text from the 8-K document (SEC cover page boilerplate is skipped). |

---

### Sector Peers (same quarter, already reported)

```
  Sector Peers (same quarter, already reported):
    SYMBOL  COMPANY              CAP_B   TIME  EPS_EST  EPS_ACT  EPS_BEAT  REV_ACT_B  RXN_DAY    PRIOR_CLS  RXN_OPEN  GAP     RXN_CLS  DAY_RET
    TSM     Taiwan Semiconductor  $800B  AMC   $2.10    $2.24    +6.7%     $25.60B    2026-04-17  $182.30    $188.50   +3.4%   $187.20  +2.7%
```

Stocks in the same SIC sector that have **already reported the same fiscal quarter**, showing how the sector trended before this stock reports. Useful for context: if peers beat and gapped up, the setup for this stock is constructive.

**Peer selection criteria:**
- Same broad SIC sector group
- Fiscal quarter end within ±45 days (to capture companies with slightly different fiscal calendars)
- Market cap between `max($1B, 5% of target)` and `30× target market cap` (avoids NVIDIA showing as a peer to a $5B chip company)
- Already reported (8-K filed and 10-Q/10-K actuals available)

---

## Output Formats

### Table (default)
Human-readable stock cards, sorted by result date (ascending), then market cap (descending). Each card shows all sections above.

### CSV (`--output csv`)
One row per stock. Streams to stdout as each stock completes (no sorting). Includes all scalar fields; omits nested tables (reactions, peers, events — use JSON for those).

### JSON (`--output json`)
Full structured output including nested arrays (EarningsReactions, MaterialEvents, Peers, History, ForwardEPS, GEX). Sorted same as table output. Suitable for piping into `jq` or further analysis.

```bash
# Examples with jq
go run . --symbol AAPL --output json | jq '.[] | {symbol, beat_rate: .beat_rate, mspr: .mspr_signal}'
go run . --symbol AAPL --output json | jq '.[] | .earnings_reactions[] | {period: .Period, gap: .GapRetPct}'
```

---

## Performance Notes

- **Concurrency:** Up to 5 stocks are enriched in parallel. Within each stock, 10+ independent API calls fire as goroutines (Phase 1 fan-out), so total wall time is dominated by the slowest single call rather than the sum.
- **SEC rate limiting:** SEC EDGAR enforces 10 req/s. The code uses an 8 req/s token-bucket limiter and caches per-CIK submissions data in memory to avoid repeated lookups. If you still see HTTP 429 errors, wait 30 seconds.
- **`--no-peers` and `--no-news`** skip two of the most expensive fetches (peer data requires multiple SEC lookups per candidate; news fetches and parses full 8-K HTML). Use these for faster iteration when you don't need those sections.
- **Price history:** Two parallel Nasdaq API calls cover a combined ~18-month window (needed for 4-quarter reaction history + 7-day pre-earnings drift lookback).

---

## Maintenance

- **Macro calendar:** Update the date slices in `macro.go` each January with the new year's FOMC, CPI, PPI, and NFP dates. Sources: [federalreserve.gov](https://www.federalreserve.gov/monetarypolicy/fomccalendars.htm) and [bls.gov/schedule](https://www.bls.gov/schedule/).
- **SEC ticker map cache:** Cached in `~/.cache/quarterly_results/sec_tickers.json`. Delete this file to force a refresh if you see "symbol not in SEC ticker map" errors.
- **Finviz HTML changes:** If institutional data stops working, the CSS class names in the `finvizFieldRe` regex in `finviz.go` may need updating.
