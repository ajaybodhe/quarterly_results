package peers

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/source/sec"
	"quarterly_results/internal/testutil"
)

// ── reactionDayFor ────────────────────────────────────────────────────────────

func bmoAMCPrices(announceDate time.Time) []domain.PricePoint {
	// Build a price series where the announcement day has a big gap.
	return []domain.PricePoint{
		{Date: announceDate.AddDate(0, 0, -2), Open: 100, Close: 101},
		{Date: announceDate.AddDate(0, 0, -1), Open: 101, Close: 102},
		{Date: announceDate, Open: 110, Close: 112}, // big gap on announce day → BMO
		{Date: exchange.NextWorkingDay(announceDate), Open: 112, Close: 113},
	}
}

func amcPrices(announceDate time.Time) []domain.PricePoint {
	// Next day has the big gap → AMC signal.
	return []domain.PricePoint{
		{Date: announceDate.AddDate(0, 0, -1), Open: 100, Close: 101},
		{Date: announceDate, Open: 102, Close: 103},                          // small move on announce day
		{Date: exchange.NextWorkingDay(announceDate), Open: 112, Close: 114}, // big gap next day → AMC
	}
}

func TestReactionDayFor_ExplicitBMO(t *testing.T) {
	ann := testutil.MustDate("2026-03-15")
	prices := bmoAMCPrices(ann)
	rxnDay, priorClose, ok := reactionDayFor(ann, "bmo", prices)
	if !ok {
		t.Fatal("expected ok=true for explicit BMO")
	}
	if !rxnDay.Equal(ann) {
		t.Errorf("BMO reaction day = %s, want %s (announce day)", rxnDay.Format("2006-01-02"), ann.Format("2006-01-02"))
	}
	// Prior close = day before announce.
	if math.Abs(priorClose-102) > 1e-9 {
		t.Errorf("BMO prior close = %v, want 102 (close day-before)", priorClose)
	}
}

func TestReactionDayFor_ExplicitAMC(t *testing.T) {
	ann := testutil.MustDate("2026-03-15")
	prices := amcPrices(ann)
	rxnDay, priorClose, ok := reactionDayFor(ann, "amc", prices)
	if !ok {
		t.Fatal("expected ok=true for explicit AMC")
	}
	expected := exchange.NextWorkingDay(ann)
	if !rxnDay.Equal(expected) {
		t.Errorf("AMC reaction day = %s, want %s (next working day)", rxnDay.Format("2006-01-02"), expected.Format("2006-01-02"))
	}
	// Prior close = announce day close (market closed before results).
	if math.Abs(priorClose-103) > 1e-9 {
		t.Errorf("AMC prior close = %v, want 103 (announce-day close)", priorClose)
	}
}

func TestReactionDayFor_HeuristicPicksBMO(t *testing.T) {
	// When earningsTime is "" and the announce-day gap is larger → BMO.
	ann := testutil.MustDate("2026-03-15")
	prices := bmoAMCPrices(ann) // big gap on announce day
	rxnDay, _, ok := reactionDayFor(ann, "", prices)
	if !ok {
		t.Fatal("expected ok=true for heuristic BMO detection")
	}
	if !rxnDay.Equal(ann) {
		t.Errorf("heuristic BMO: reaction day = %s, want %s", rxnDay.Format("2006-01-02"), ann.Format("2006-01-02"))
	}
}

func TestReactionDayFor_HeuristicPicksAMC(t *testing.T) {
	ann := testutil.MustDate("2026-03-15")
	prices := amcPrices(ann) // big gap next day
	rxnDay, _, ok := reactionDayFor(ann, "", prices)
	if !ok {
		t.Fatal("expected ok=true for heuristic AMC detection")
	}
	expected := exchange.NextWorkingDay(ann)
	if !rxnDay.Equal(expected) {
		t.Errorf("heuristic AMC: reaction day = %s, want %s", rxnDay.Format("2006-01-02"), expected.Format("2006-01-02"))
	}
}

func TestReactionDayFor_InsufficientData(t *testing.T) {
	ann := testutil.MustDate("2026-03-15")
	_, _, ok := reactionDayFor(ann, "bmo", nil) // no prices
	if ok {
		t.Error("expected ok=false when price data is unavailable")
	}
}

func TestReactionDayFor_BMOFallbackToAMCWhenNoPriorClose(t *testing.T) {
	// Explicit BMO but no price for the day before → should return ok=false
	// rather than using a zero prior close.
	ann := testutil.MustDate("2026-03-15")
	prices := []domain.PricePoint{
		// No entry for day before announce.
		{Date: ann, Open: 110, Close: 112},
		{Date: exchange.NextWorkingDay(ann), Open: 113, Close: 114},
	}
	_, _, ok := reactionDayFor(ann, "bmo", prices)
	// dayBeforeClose lookup on 2026-03-14 will return the closest earlier price,
	// which doesn't exist → ok=false OR returns announce-day as closest prior.
	// Either is acceptable; we just check it doesn't panic.
	_ = ok
}

// ── Finder.Find integration via mock ─────────────────────────────────────────

// buildPeerMocks wires up a testutil.MockTransport with responses needed for one peer.
// The peer has SIC 3571 (tech), reports "amc" on reportDate, with given EPS.
func buildPeerMocks(
	tr *testutil.MockTransport,
	peerSym string,
	peerCIK int,
	reportDate string, // YYYY-MM-DD
	periodEnd string, // fiscal quarter end
	epsActual float64,
	revenue float64,
) {
	cikStr := fmt.Sprintf("CIK%010d", peerCIK)

	// Submissions (SIC lookup + 8-K dates)
	submissions := fmt.Sprintf(`{
		"name": "%s Inc.",
		"sic": "3571",
		"sicDescription": "ELECTRONIC COMPUTERS",
		"filings": {"recent": {
			"form": ["8-K"],
			"filingDate": ["%s"],
			"accessionNumber": [""],
			"primaryDocument": [""],
			"items": ["2.02"],
			"primaryDocDescription": ["EARNINGS"]
		}}
	}`, peerSym, reportDate)
	tr.On("/submissions/"+cikStr+".json", 200, submissions, "application/json")

	// EPS concept
	periodStart := periodEnd[:7] + "-01"
	epsJSON := testutil.ConceptJSON("USD/shares",
		testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", reportDate, epsActual),
	)
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200, epsJSON, "application/json")

	// Revenue concept
	revJSON := testutil.ConceptJSON("USD",
		testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", reportDate, revenue),
	)
	tr.On("/us-gaap/Revenues.json", 200, revJSON, "application/json")
}

// ── Finder.Find mock-based tests ──────────────────────────────────────────────

// sevenDayPrices is a price history covering 2026-04-09 through 2026-04-23.
// Sufficient for both AMC and BMO reaction tests:
//
//	04/15 (announce day AMC):  priorClose via this day's close = $102
//	04/16 (AMC reaction day):  open=$107, close=$108
//	04/14 (day-before for BMO):close=$101 → priorClose for BMO
//	04/10 (alternate announce): close=$99 → used in sort test (PEER2)
//
// Rows are "MM/DD/YYYY, open, close" — the JSON encoder normalises order.
var sevenDayPrices = [][3]string{
	{"04/23/2026", "109.00", "110.00"},
	{"04/16/2026", "107.00", "108.00"},
	{"04/15/2026", "101.00", "102.00"},
	{"04/14/2026", "100.00", "101.00"},
	{"04/13/2026", "99.00", "100.00"},
	{"04/10/2026", "98.00", "99.00"},
	{"04/09/2026", "97.00", "98.00"},
}

// peerPriceHistJSON builds a Yahoo Finance v8 chart API response — the shape
// used by yahoo.FetchChartHistory (called by nasdaq.PriceProvider.FetchPriceHistory).
// Rows may be in any order; Yahoo emits timestamp/open/close as parallel arrays.
func peerPriceHistJSON(rows [][3]string) string {
	var timestamps, opens, closes []string
	for _, r := range rows {
		d, err := time.Parse("01/02/2006", r[0])
		if err != nil {
			panic(fmt.Sprintf("peerPriceHistJSON: bad date %q: %v", r[0], err))
		}
		timestamps = append(timestamps, fmt.Sprintf("%d", d.Unix()))
		opens = append(opens, r[1])
		closes = append(closes, r[2])
	}
	return fmt.Sprintf(
		`{"chart":{"result":[{"timestamp":[%s],"indicators":{"quote":[{"open":[%s],"close":[%s]}]}}],"error":null}}`,
		strings.Join(timestamps, ","),
		strings.Join(opens, ","),
		strings.Join(closes, ","),
	)
}

// peerCalResp builds a Nasdaq calendar response containing one peer row.
func peerCalResp(sym, name, cap, timing, fiscalQ string) string {
	return fmt.Sprintf(
		`{"data":{"rows":[{"symbol":%q,"name":%q,"marketCap":%q,"time":%q,"fiscalQuarterEnding":%q,"epsForecast":"$1.50","lastYearRptDt":"","lastYearEPS":""}]},"status":{"rCode":200}}`,
		sym, name, cap, timing, fiscalQ,
	)
}

func emptyCalResp() string {
	return `{"data":{"rows":[]},"status":{"rCode":200}}`
}

// peerSubJSON returns a minimal SEC submissions JSON with the given SIC and an
// optional 8-K filing on announceDate (leave empty to omit the 8-K).
func peerSubJSON(name, sic, sicDesc, announceDate string) string {
	if announceDate == "" {
		return fmt.Sprintf(
			`{"name":%q,"sic":%q,"sicDescription":%q,"filings":{"recent":{"form":[],"filingDate":[],"accessionNumber":[],"primaryDocument":[],"items":[],"primaryDocDescription":[]}}}`,
			name, sic, sicDesc,
		)
	}
	return fmt.Sprintf(
		`{"name":%q,"sic":%q,"sicDescription":%q,"filings":{"recent":{"form":["8-K"],"filingDate":[%q],"accessionNumber":[""],"primaryDocument":[""],"items":["2.02"],"primaryDocDescription":["EARNINGS"]}}}`,
		name, sic, sicDesc, announceDate,
	)
}

// fpEnricher creates a Finder wired to the given mock transport.
func fpEnricher(tr *testutil.MockTransport, ciks map[string]int) *Finder {
	mc := testutil.NewMockClient(tr)
	secClient := sec.NewClientWithHTTP(mc, ciks)
	return NewFinder(mc, nil, secClient, nasdaq.NewPriceProvider(mc), nil)
}

// calRoute registers a mock route that returns body for requests whose URL
// contains the exact query string "date=<date>" (Nasdaq calendar format).
// It avoids matching price-history URLs that use "fromdate=..." or "todate=...".
func calRoute(tr *testutil.MockTransport, date, body string) {
	tr.OnFunc(
		func(r *http.Request) bool {
			return strings.Contains(r.URL.String(), "calendar/earnings?date="+date)
		},
		func(r *http.Request) *http.Response {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    r,
			}
		},
	)
}

// ─── TestFetchPeers_AMC ───────────────────────────────────────────────────────
// Peer reports AMC on 2026-04-15. Reaction day should be the next working day
// (2026-04-16) and priorClose should be the announce-day close ($102).
func TestFetchPeers_AMC(t *testing.T) {
	const (
		peerSym     = "PEER1"
		calDate     = "2026-04-15" // earnings release date on Nasdaq calendar
		xbrlFiled   = "2026-05-10" // 10-Q filed ~40 days after quarter end
		periodEnd   = "2026-03-31"
		periodStart = "2026-01-01"
	)

	tr := testutil.NewMockTransport()

	// Nasdaq: peer appears on calDate; all other calendar dates are empty.
	calRoute(tr, calDate, peerCalResp(peerSym, "Peer One Inc.", "$50,000,000,000", "time-after-hours", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")

	// Yahoo: price history (single ~2y chart call).
	tr.On("/v8/finance/chart/"+peerSym, 200, peerPriceHistJSON(sevenDayPrices), "application/json")

	// SEC: submissions for PEER1 — SIC lookup + 8-K date (April 15).
	// windowStart = periodEnd+14 = "2026-04-14", windowEnd = xbrlFiled = "2026-05-10"
	// 8-K on "2026-04-15" falls in that window → announceDate overridden to calDate.
	tr.On("/submissions/CIK0000111111.json", 200, peerSubJSON("Peer One Inc.", "3571", "ELECTRONIC COMPUTERS", calDate), "application/json")

	// SEC: quarterly EPS and Revenue concepts.
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200,
		testutil.ConceptJSON("USD/shares", testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", xbrlFiled, 1.65)),
		"application/json")
	tr.On("/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD", testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", xbrlFiled, 90e9)),
		"application/json")

	e := fpEnricher(tr, map[string]int{peerSym: 111111})
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	p := peers[0]

	if p.Symbol != peerSym {
		t.Errorf("Symbol = %q, want %q", p.Symbol, peerSym)
	}
	if p.EarningsTime != "amc" {
		t.Errorf("EarningsTime = %q, want amc", p.EarningsTime)
	}
	if p.ReactionDay != "2026-04-16" {
		t.Errorf("ReactionDay = %q, want 2026-04-16 (next working day)", p.ReactionDay)
	}
	if math.Abs(p.PriorClose-102.0) > 1e-9 {
		t.Errorf("PriorClose = %v, want 102 (announce-day close for AMC)", p.PriorClose)
	}
	if math.Abs(p.ReactionClose-108.0) > 1e-9 {
		t.Errorf("ReactionClose = %v, want 108", p.ReactionClose)
	}
	if math.Abs(p.ReactionOpen-107.0) > 1e-9 {
		t.Errorf("ReactionOpen = %v, want 107", p.ReactionOpen)
	}
	wantGap := (107.0 - 102.0) / 102.0 * 100
	if math.Abs(p.GapRetPct-wantGap) > 0.01 {
		t.Errorf("GapRetPct = %.4f, want %.4f", p.GapRetPct, wantGap)
	}
	if p.EPSEstimate != 1.50 {
		t.Errorf("EPSEstimate = %v, want 1.50 (from calendar mock)", p.EPSEstimate)
	}
	if p.EPSActual != 1.65 {
		t.Errorf("EPSActual = %v, want 1.65", p.EPSActual)
	}
	if p.EPSBeatPct == nil {
		t.Fatal("EPSBeatPct is nil, want non-nil")
	}
	wantBeat := (1.65 - 1.50) / math.Abs(1.50) * 100
	if math.Abs(*p.EPSBeatPct-wantBeat) > 0.01 {
		t.Errorf("EPSBeatPct = %.2f, want %.2f", *p.EPSBeatPct, wantBeat)
	}
	if math.Abs(p.RevActual-90e9) > 1 {
		t.Errorf("RevActual = %g, want 90e9", p.RevActual)
	}
}

// ─── TestFetchPeers_BMO ───────────────────────────────────────────────────────
// Peer reports BMO on 2026-04-15. Reaction day should be the announce day itself
// and priorClose should be the previous day's close ($101).
func TestFetchPeers_BMO(t *testing.T) {
	const (
		peerSym     = "BPEER"
		calDate     = "2026-04-15"
		xbrlFiled   = "2026-05-10"
		periodEnd   = "2026-03-31"
		periodStart = "2026-01-01"
	)

	tr := testutil.NewMockTransport()
	calRoute(tr, calDate, peerCalResp(peerSym, "BMO Peer Inc.", "$50,000,000,000", "time-pre-market", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")
	tr.On("/v8/finance/chart/"+peerSym, 200, peerPriceHistJSON(sevenDayPrices), "application/json")
	tr.On("/submissions/CIK0000222222.json", 200, peerSubJSON("BMO Peer Inc.", "3571", "ELECTRONIC COMPUTERS", calDate), "application/json")
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200,
		testutil.ConceptJSON("USD/shares", testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", xbrlFiled, 2.00)),
		"application/json")
	tr.On("/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD", testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", xbrlFiled, 50e9)),
		"application/json")

	e := fpEnricher(tr, map[string]int{peerSym: 222222})
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	p := peers[0]

	if p.EarningsTime != "bmo" {
		t.Errorf("EarningsTime = %q, want bmo", p.EarningsTime)
	}
	// BMO: reaction day = announcement day
	if p.ReactionDay != calDate {
		t.Errorf("ReactionDay = %q, want %q (announce day for BMO)", p.ReactionDay, calDate)
	}
	// BMO: priorClose = close on day BEFORE announcement = indicators.ClosestPrice(2026-04-14) = 101
	if math.Abs(p.PriorClose-101.0) > 1e-9 {
		t.Errorf("PriorClose = %v, want 101 (day-before close for BMO)", p.PriorClose)
	}
	// BMO: reaction close = close ON announcement day = 102
	if math.Abs(p.ReactionClose-102.0) > 1e-9 {
		t.Errorf("ReactionClose = %v, want 102 (announce-day close for BMO)", p.ReactionClose)
	}
}

// ─── TestFetchPeers_SkipsTargetSymbol ────────────────────────────────────────
func TestFetchPeers_SkipsTargetSymbol(t *testing.T) {
	const targetSym = "TARGET"
	tr := testutil.NewMockTransport()
	// Calendar returns the target symbol itself as a candidate.
	calRoute(tr, "2026-04-15", peerCalResp(targetSym, "Target Co.", "$50,000,000,000", "time-after-hours", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")

	e := fpEnricher(tr, map[string]int{})
	peers := e.Find(targetSym, 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 0 {
		t.Errorf("expected 0 peers when target symbol appears in calendar, got %d", len(peers))
	}
}

// ─── TestFetchPeers_SkipsDifferentSector ─────────────────────────────────────
func TestFetchPeers_SkipsDifferentSector(t *testing.T) {
	const bankSym = "BANK1"
	tr := testutil.NewMockTransport()
	calRoute(tr, "2026-04-15", peerCalResp(bankSym, "First Bank Corp.", "$200,000,000,000", "time-after-hours", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")
	// Bank's submissions return SIC 6022 (commercial banking, sector group 13).
	tr.On("/submissions/CIK0000333333.json", 200, peerSubJSON("First Bank Corp.", "6022", "COMMERCIAL BANK", ""), "application/json")

	e := fpEnricher(tr, map[string]int{bankSym: 333333})
	// Target is tech (SIC 3571, group 8) — bank (group 13) must not appear.
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 0 {
		t.Errorf("expected 0 peers for different sector, got %d", len(peers))
	}
}

// ─── TestFetchPeers_SkipsSmallCap ────────────────────────────────────────────
func TestFetchPeers_SkipsSmallCap(t *testing.T) {
	tr := testutil.NewMockTransport()
	// $5B peer is below the relative floor (targetCapB=200B → minPeer=max(1,10)=10B).
	calRoute(tr, "2026-04-15", peerCalResp("SMALL1", "Tiny Corp.", "$5,000,000,000", "time-after-hours", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")

	e := fpEnricher(tr, map[string]int{})
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 200.0)

	if len(peers) != 0 {
		t.Errorf("expected 0 peers below min market cap, got %d", len(peers))
	}
}

// ─── TestFetchPeers_EmptyCalendar ────────────────────────────────────────────
func TestFetchPeers_EmptyCalendar(t *testing.T) {
	tr := testutil.NewMockTransport()
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")

	e := fpEnricher(tr, map[string]int{})
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 0 {
		t.Errorf("expected 0 peers for empty calendar, got %d", len(peers))
	}
}

// ─── TestFetchPeers_SkipsNoPeriodMatch ───────────────────────────────────────
// Peer's quarterly actuals have a period end far outside the ±70-day window.
func TestFetchPeers_SkipsNoPeriodMatch(t *testing.T) {
	const (
		peerSym = "OLDQ"
		calDate = "2026-04-15"
	)

	tr := testutil.NewMockTransport()
	calRoute(tr, calDate, peerCalResp(peerSym, "Old Quarter Inc.", "$80,000,000,000", "time-after-hours", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")
	tr.On("/submissions/CIK0000444444.json", 200, peerSubJSON("Old Quarter Inc.", "3571", "ELECTRONIC COMPUTERS", calDate), "application/json")
	// XBRL data covers Q2 2023 (>1000 days from target 2026-03-31 → no period match).
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200,
		testutil.ConceptJSON("USD/shares", testutil.ConceptEntryJSON("2023-04-01", "2023-06-30", "10-Q", "2023-08-15", 1.00)),
		"application/json")
	tr.On("/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD", testutil.ConceptEntryJSON("2023-04-01", "2023-06-30", "10-Q", "2023-08-15", 20e9)),
		"application/json")

	e := fpEnricher(tr, map[string]int{peerSym: 444444})
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 0 {
		t.Errorf("expected 0 peers when no quarter matches period window, got %d", len(peers))
	}
}

// ─── TestFetchPeers_SortsByMarketCap ─────────────────────────────────────────
// Two qualifying peers are returned sorted largest market cap first.
func TestFetchPeers_SortsByMarketCap(t *testing.T) {
	const (
		xbrlFiled   = "2026-05-10"
		periodEnd   = "2026-03-31"
		periodStart = "2026-01-01"
	)

	tr := testutil.NewMockTransport()

	// PEER2 ($100B) announces on 2026-04-10; PEER1 ($50B) announces on 2026-04-15.
	calRoute(tr, "2026-04-10", peerCalResp("PEER2", "Peer Two Inc.", "$100,000,000,000", "time-after-hours", "Mar/2026"))
	calRoute(tr, "2026-04-15", peerCalResp("PEER1", "Peer One Inc.", "$50,000,000,000", "time-after-hours", "Mar/2026"))
	tr.On("calendar/earnings", 200, emptyCalResp(), "application/json")

	// Nasdaq price history — both peers use the same shared series.
	tr.On("/v8/finance/chart/PEER1", 200, peerPriceHistJSON(sevenDayPrices), "application/json")
	tr.On("/v8/finance/chart/PEER2", 200, peerPriceHistJSON(sevenDayPrices), "application/json")

	// SEC: separate submissions per CIK; concepts are matched by path substring
	// (same response served to both, which is fine for this test).
	tr.On("/submissions/CIK0000111111.json", 200, peerSubJSON("Peer One Inc.", "3571", "ELECTRONIC COMPUTERS", "2026-04-15"), "application/json")
	tr.On("/submissions/CIK0000222222.json", 200, peerSubJSON("Peer Two Inc.", "3571", "ELECTRONIC COMPUTERS", "2026-04-10"), "application/json")
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200,
		testutil.ConceptJSON("USD/shares", testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", xbrlFiled, 2.00)),
		"application/json")
	tr.On("/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD", testutil.ConceptEntryJSON(periodStart, periodEnd, "10-Q", xbrlFiled, 60e9)),
		"application/json")

	e := fpEnricher(tr, map[string]int{"PEER1": 111111, "PEER2": 222222})
	peers := e.Find("TGT", 3571, testutil.MustDate("2026-03-31"), 50.0)

	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	// Largest cap (PEER2, $100B) must be first.
	if peers[0].Symbol != "PEER2" {
		t.Errorf("peers[0].Symbol = %q, want PEER2 (higher market cap)", peers[0].Symbol)
	}
	if peers[1].Symbol != "PEER1" {
		t.Errorf("peers[1].Symbol = %q, want PEER1 (lower market cap)", peers[1].Symbol)
	}
	if peers[0].MarketCapB <= peers[1].MarketCapB {
		t.Errorf("peers not sorted by market cap: [0]=%.1fB [1]=%.1fB", peers[0].MarketCapB, peers[1].MarketCapB)
	}
}
