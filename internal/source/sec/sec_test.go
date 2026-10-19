package sec

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/testutil"
)

func TestDateAddDays(t *testing.T) {
	cases := []struct {
		date string
		n    int
		want string
	}{
		{"2025-01-01", 14, "2025-01-15"},
		{"2025-01-31", 1, "2025-02-01"},  // month boundary
		{"2024-02-28", 1, "2024-02-29"},  // leap year
		{"2025-02-28", 1, "2025-03-01"},  // non-leap year
		{"2025-03-01", -5, "2025-02-24"}, // negative days
		{"not-a-date", 5, "not-a-date"},  // parse error passthrough
		{"2025-12-31", 1, "2026-01-01"},  // year boundary
	}
	for _, tc := range cases {
		got := dateAddDays(tc.date, tc.n)
		if got != tc.want {
			t.Errorf("dateAddDays(%q, %d) = %q, want %q", tc.date, tc.n, got, tc.want)
		}
	}
}

// redirectCacheDir points os.UserCacheDir at a temporary directory so cache-
// related tests don't touch the real ~/Library/Caches or ~/.cache.
func redirectCacheDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// On darwin, os.UserCacheDir uses $HOME/Library/Caches. On linux, it honors
	// $XDG_CACHE_HOME first. Setting both covers both platforms.
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CACHE_HOME", path.Join(dir, ".cache"))
	return dir
}

// buildTickerJSON returns SEC company_tickers.json bytes containing the given
// symbol→CIK pairs.
func buildTickerJSON(pairs map[string]int) []byte {
	out := make(map[string]tickerEntry, len(pairs))
	i := 0
	for sym, cik := range pairs {
		out[fmt.Sprintf("%d", i)] = tickerEntry{CIK: cik, Ticker: sym}
		i++
	}
	b, _ := json.Marshal(out)
	return b
}

func TestLoadTickerMap_Success(t *testing.T) {
	redirectCacheDir(t)

	tr := testutil.NewMockTransport().On(
		"company_tickers.json", 200,
		string(buildTickerJSON(map[string]int{"AAPL": 320193, "MSFT": 789019})),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	if err := c.LoadTickerMap(); err != nil {
		t.Fatalf("LoadTickerMap: %v", err)
	}
	cik, err := c.lookupCIK("aapl")
	if err != nil || cik != 320193 {
		t.Errorf("lookupCIK(aapl) = %d, %v; want 320193, nil", cik, err)
	}

	// Second call must be a no-op (in-memory cache hit): no additional HTTP call.
	if err := c.LoadTickerMap(); err != nil {
		t.Fatalf("second LoadTickerMap: %v", err)
	}
	if n := tr.Count("company_tickers.json"); n != 1 {
		t.Errorf("expected 1 HTTP call to company_tickers.json, got %d", n)
	}
}

func TestLoadTickerMap_RateLimit(t *testing.T) {
	redirectCacheDir(t)

	tr := testutil.NewMockTransport().On("company_tickers.json", 429, "too many requests", "text/plain")
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	err := c.LoadTickerMap()
	if err == nil {
		t.Fatal("expected error for HTTP 429, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention 429: %v", err)
	}
	if !strings.Contains(err.Error(), "SEC_USER_AGENT") {
		t.Errorf("429 error should include SEC_USER_AGENT hint for operators: %v", err)
	}
}

func TestLoadTickerMap_DiskCacheRoundtrip(t *testing.T) {
	redirectCacheDir(t)

	// First run: fetch + write cache.
	tr1 := testutil.NewMockTransport().On("company_tickers.json", 200,
		string(buildTickerJSON(map[string]int{"NVDA": 1045810})),
		"application/json",
	)
	c1 := &Client{httpClient: testutil.NewMockClient(tr1)}
	if err := c1.LoadTickerMap(); err != nil {
		t.Fatalf("first load: %v", err)
	}
	if _, err := os.Stat(tickerCachePath()); err != nil {
		t.Fatalf("expected cache file at %s: %v", tickerCachePath(), err)
	}

	// Second run with a completely different transport: cache must be used,
	// no HTTP call should be made.
	tr2 := testutil.NewMockTransport().On("company_tickers.json", 500, "boom", "text/plain")
	c2 := &Client{httpClient: testutil.NewMockClient(tr2)}
	if err := c2.LoadTickerMap(); err != nil {
		t.Fatalf("second load (cache hit expected): %v", err)
	}
	cik, err := c2.lookupCIK("NVDA")
	if err != nil || cik != 1045810 {
		t.Errorf("cached lookupCIK(NVDA) = %d, %v; want 1045810, nil", cik, err)
	}
	if n := tr2.Count("company_tickers.json"); n != 0 {
		t.Errorf("expected cache hit (0 HTTP calls), got %d", n)
	}
}

func TestFetchConcept_QuarterlySelection(t *testing.T) {
	tr := testutil.NewMockTransport().On(
		"/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD",
			// valid Q1: 90 days, filed 40 days after period end
			testutil.ConceptEntryJSON("2025-01-01", "2025-03-31", "10-Q", "2025-05-10", 1_000),
			// valid Q2
			testutil.ConceptEntryJSON("2025-04-01", "2025-06-30", "10-Q", "2025-08-10", 1_100),
			// comparative re-filing: period Q1 2024, filed way later → must be rejected by 150-day cap
			testutil.ConceptEntryJSON("2024-01-01", "2024-03-31", "10-Q", "2025-05-10", 999_999),
			// amendment: same Q1 2025 period, later filing date → should win over original
			testutil.ConceptEntryJSON("2025-01-01", "2025-03-31", "10-Q", "2025-06-01", 1_050),
			// ignored: wrong form
			testutil.ConceptEntryJSON("2025-04-01", "2025-06-30", "8-K", "2025-08-11", 42),
			// ignored: YTD period (180 days, not quarterly)
			testutil.ConceptEntryJSON("2025-01-01", "2025-06-30", "10-Q", "2025-08-10", 2_100),
		),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	data, filed, starts, err := c.fetchConcept(320193, "us-gaap", "Revenues")
	if err != nil {
		t.Fatalf("fetchConcept: %v", err)
	}

	// 150-day cap excludes the Q1 2024 comparative re-filing.
	if _, ok := data["2024-03-31"]; ok {
		t.Errorf("expected Q1 2024 comparative filing to be dropped (150-day cap)")
	}
	// Amendment (later filing) wins for Q1 2025.
	if got := data["2025-03-31"]; got != 1050 {
		t.Errorf("Q1 2025 value = %v, want 1050 (amendment should win)", got)
	}
	if got := filed["2025-03-31"]; got != "2025-06-01" {
		t.Errorf("Q1 2025 filed = %q, want 2025-06-01 (amendment date)", got)
	}
	if got := starts["2025-03-31"]; got != "2025-01-01" {
		t.Errorf("Q1 2025 start = %q, want 2025-01-01", got)
	}
	if got := data["2025-06-30"]; got != 1100 {
		t.Errorf("Q2 2025 value = %v, want 1100", got)
	}
	if _, ok := data["2025-06-30 YTD"]; ok {
		t.Errorf("YTD rows should not appear as synthetic keys")
	}
	if len(data) != 2 {
		t.Errorf("expected 2 quarters, got %d: %v", len(data), data)
	}
}

func TestFetchConcept_Q4Derivation(t *testing.T) {
	tr := testutil.NewMockTransport().On(
		"/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD",
			// Three quarters within fiscal year 2024 (calendar year).
			testutil.ConceptEntryJSON("2024-01-01", "2024-03-31", "10-Q", "2024-05-01", 100),
			testutil.ConceptEntryJSON("2024-04-01", "2024-06-30", "10-Q", "2024-08-01", 110),
			testutil.ConceptEntryJSON("2024-07-01", "2024-09-30", "10-Q", "2024-11-01", 120),
			// Annual 10-K for fiscal 2024 filed early 2025; days 365 → triggers Q4 derivation.
			testutil.ConceptEntryJSON("2024-01-01", "2024-12-31", "10-K", "2025-02-15", 500),
		),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	data, filed, starts, err := c.fetchConcept(320193, "us-gaap", "Revenues")
	if err != nil {
		t.Fatalf("fetchConcept: %v", err)
	}
	q4, ok := data["2024-12-31"]
	if !ok {
		t.Fatal("expected derived Q4 at 2024-12-31")
	}
	if math.Abs(q4-170) > 1e-9 {
		t.Errorf("derived Q4 = %v, want 500 − (100+110+120) = 170", q4)
	}
	if filed["2024-12-31"] != "2025-02-15" {
		t.Errorf("derived Q4 filed date = %q, want 2025-02-15", filed["2024-12-31"])
	}
	// PeriodStart for derived Q4 = latest Q-end (2024-09-30) + 1 day = 2024-10-01.
	if starts["2024-12-31"] != "2024-10-01" {
		t.Errorf("derived Q4 period start = %q, want 2024-10-01", starts["2024-12-31"])
	}
}

func TestFetchConcept_ExplicitQ4WinsOverDerivation(t *testing.T) {
	tr := testutil.NewMockTransport().On(
		"/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON("2024-01-01", "2024-03-31", "10-Q", "2024-05-01", 100),
			testutil.ConceptEntryJSON("2024-04-01", "2024-06-30", "10-Q", "2024-08-01", 110),
			testutil.ConceptEntryJSON("2024-07-01", "2024-09-30", "10-Q", "2024-11-01", 120),
			// Explicit Q4 tagged directly as a 10-K quarterly (some filers do this).
			testutil.ConceptEntryJSON("2024-10-01", "2024-12-31", "10-K", "2025-02-15", 175),
			// Annual 10-K: should NOT overwrite the explicit Q4.
			testutil.ConceptEntryJSON("2024-01-01", "2024-12-31", "10-K", "2025-02-15", 500),
		),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	data, _, _, err := c.fetchConcept(320193, "us-gaap", "Revenues")
	if err != nil {
		t.Fatalf("fetchConcept: %v", err)
	}
	if got := data["2024-12-31"]; got != 175 {
		t.Errorf("explicit Q4 = %v, want 175 (should not be overwritten by derivation)", got)
	}
}

func TestFetchConcept_NotFound(t *testing.T) {
	tr := testutil.NewMockTransport().On("/us-gaap/Missing.json", 404, `{"error":"not found"}`, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr)}
	_, _, _, err := c.fetchConcept(320193, "us-gaap", "Missing")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got %v", err)
	}
}

func TestFetchBrokerGrossRevenue(t *testing.T) {
	// Use recent period (within the cutoff) so the recency check passes.
	recent := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	periodStart := time.Now().AddDate(0, -4, 0).Format("2006-01-02")

	tr := testutil.NewMockTransport()
	tr.On("/us-gaap/InterestIncomeOperating.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 800),
			// Period present only here — should be dropped in the merged result.
			testutil.ConceptEntryJSON("2024-01-01", "2024-03-31", "10-Q", "2024-05-01", 700),
		),
		"application/json",
	)
	tr.On("/us-gaap/NoninterestIncome.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 500),
		),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	cutoff := time.Now().AddDate(-1, -6, 0)
	got, _, _, ok := c.fetchBrokerGrossRevenue(320193, cutoff)
	if !ok {
		t.Fatal("expected ok=true, got false")
	}
	if v := got[recent]; v != 1300 {
		t.Errorf("broker gross for %s = %v, want 800+500=1300", recent, v)
	}
	if _, present := got["2024-03-31"]; present {
		t.Errorf("period missing from NoninterestIncome must be dropped")
	}
}

func TestFetchBrokerGrossRevenue_FallbackBank(t *testing.T) {
	// Banks use InterestAndDividendIncomeOperating instead of InterestIncomeOperating.
	recent := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	periodStart := time.Now().AddDate(0, -4, 0).Format("2006-01-02")

	tr := testutil.NewMockTransport()
	// First concept 404s, forcing fallback.
	tr.On("/us-gaap/InterestIncomeOperating.json", 404, `{"error":"not found"}`, "application/json")
	tr.On("/us-gaap/InterestAndDividendIncomeOperating.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 2_000),
		),
		"application/json",
	)
	tr.On("/us-gaap/NoninterestIncome.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 1_500),
		),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	got, _, _, ok := c.fetchBrokerGrossRevenue(320193, time.Now().AddDate(-1, -6, 0))
	if !ok {
		t.Fatal("expected fallback concept to satisfy broker lookup")
	}
	if v := got[recent]; v != 3500 {
		t.Errorf("bank gross = %v, want 2000+1500=3500", v)
	}
}

func TestFetchBrokerGrossRevenue_MissingNonInterest(t *testing.T) {
	recent := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	periodStart := time.Now().AddDate(0, -4, 0).Format("2006-01-02")

	tr := testutil.NewMockTransport()
	tr.On("/us-gaap/InterestIncomeOperating.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 800),
		),
		"application/json",
	)
	tr.On("/us-gaap/NoninterestIncome.json", 404, `{"error":"not found"}`, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	_, _, _, ok := c.fetchBrokerGrossRevenue(320193, time.Now().AddDate(-1, -6, 0))
	if ok {
		t.Error("expected ok=false when NoninterestIncome is missing")
	}
}

func TestFetchBrokerGrossRevenue_AllOld(t *testing.T) {
	// Both concepts present, but every period predates the cutoff.
	tr := testutil.NewMockTransport()
	tr.On("/us-gaap/InterestIncomeOperating.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON("2020-01-01", "2020-03-31", "10-Q", "2020-05-01", 10),
		),
		"application/json",
	)
	tr.On("/us-gaap/NoninterestIncome.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON("2020-01-01", "2020-03-31", "10-Q", "2020-05-01", 5),
		),
		"application/json",
	)
	c := &Client{httpClient: testutil.NewMockClient(tr)}

	_, _, _, ok := c.fetchBrokerGrossRevenue(320193, time.Now().AddDate(-1, -6, 0))
	if ok {
		t.Error("expected ok=false when no period is within the recency cutoff")
	}
}

func TestFetchEarningsAnnouncementDates(t *testing.T) {
	// Submissions JSON listing 8-Ks around two quarter-end dates.
	submissions := `{"filings":{"recent":{
		"form":["8-K","8-K","10-Q","8-K","8-K"],
		"filingDate":["2025-04-25","2025-05-01","2025-05-05","2025-07-24","2025-07-29"],
		"accessionNumber":["","","","",""],
		"primaryDocument":["","","","",""]
	}}}`
	tr := testutil.NewMockTransport().On("/submissions/CIK0000320193.json", 200, submissions, "application/json")
	c := &Client{
		httpClient: testutil.NewMockClient(tr),
		tickerCIK:  map[string]int{"AAPL": 320193},
	}
	quarters := []domain.QuarterActual{
		{Period: "2025-03-31", FilingDate: "2025-05-05"},
		{Period: "2025-06-30", FilingDate: "2025-07-30"},
	}
	got, err := c.FetchEarningsAnnouncementDates("AAPL", quarters)
	if err != nil {
		t.Fatalf("FetchEarningsAnnouncementDates: %v", err)
	}
	// For Q1 2025: window is (period+14, filingDate] = (2025-04-14, 2025-05-05].
	// Two 8-Ks fall in that window — the later one (2025-05-01) wins.
	if got["2025-03-31"] != "2025-05-01" {
		t.Errorf("Q1 2025 announcement = %q, want 2025-05-01", got["2025-03-31"])
	}
	// For Q2 2025: window is (2025-07-14, 2025-07-30]. 2025-07-29 wins.
	if got["2025-06-30"] != "2025-07-29" {
		t.Errorf("Q2 2025 announcement = %q, want 2025-07-29", got["2025-06-30"])
	}
}

func TestLookupCIK_Unknown(t *testing.T) {
	c := &Client{tickerCIK: map[string]int{"AAPL": 320193}}
	if _, err := c.lookupCIK("ZZZ"); err == nil {
		t.Error("expected error for unknown ticker")
	}
}

func TestFetchQuarterlyActuals_MergesEPSAndRevenue(t *testing.T) {
	recent := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	periodStart := time.Now().AddDate(0, -4, 0).Format("2006-01-02")

	tr := testutil.NewMockTransport()
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200,
		testutil.ConceptJSON("USD/shares",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 2.50),
		),
		"application/json",
	)
	tr.On("/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart, recent, "10-Q", recent, 100_000),
		),
		"application/json",
	)
	c := &Client{
		httpClient: testutil.NewMockClient(tr),
		tickerCIK:  map[string]int{"TEST": 1},
	}
	quarters, err := c.FetchQuarterlyActuals("TEST")
	if err != nil {
		t.Fatalf("FetchQuarterlyActuals: %v", err)
	}
	if len(quarters) != 1 {
		t.Fatalf("expected 1 quarter, got %d", len(quarters))
	}
	q := quarters[0]
	if q.Period != recent {
		t.Errorf("Period = %q, want %q", q.Period, recent)
	}
	if q.EPS != 2.50 {
		t.Errorf("EPS = %v, want 2.50", q.EPS)
	}
	if q.Revenue != 100_000 {
		t.Errorf("Revenue = %v, want 100000", q.Revenue)
	}
	if q.FilingDate != recent {
		t.Errorf("FilingDate = %q, want %q", q.FilingDate, recent)
	}
}

func TestFetchQuarterlyActuals_Sorted(t *testing.T) {
	// Three quarters, ordered out of sequence in the mock payload.
	recent1 := time.Now().AddDate(0, -1, 0).Format("2006-01-02")
	recent2 := time.Now().AddDate(0, -4, 0).Format("2006-01-02")
	recent3 := time.Now().AddDate(0, -7, 0).Format("2006-01-02")

	periodStart := func(p string) string {
		t, _ := time.Parse("2006-01-02", p)
		return t.AddDate(0, -3, 0).Format("2006-01-02")
	}

	tr := testutil.NewMockTransport()
	tr.On("/us-gaap/EarningsPerShareDiluted.json", 200,
		testutil.ConceptJSON("USD/shares",
			testutil.ConceptEntryJSON(periodStart(recent2), recent2, "10-Q", recent2, 2.0),
			testutil.ConceptEntryJSON(periodStart(recent3), recent3, "10-Q", recent3, 1.5),
			testutil.ConceptEntryJSON(periodStart(recent1), recent1, "10-Q", recent1, 2.5),
		),
		"application/json",
	)
	tr.On("/us-gaap/Revenues.json", 200,
		testutil.ConceptJSON("USD",
			testutil.ConceptEntryJSON(periodStart(recent1), recent1, "10-Q", recent1, 300),
			testutil.ConceptEntryJSON(periodStart(recent2), recent2, "10-Q", recent2, 200),
			testutil.ConceptEntryJSON(periodStart(recent3), recent3, "10-Q", recent3, 100),
		),
		"application/json",
	)
	c := &Client{
		httpClient: testutil.NewMockClient(tr),
		tickerCIK:  map[string]int{"TEST": 1},
	}
	quarters, err := c.FetchQuarterlyActuals("TEST")
	if err != nil {
		t.Fatalf("FetchQuarterlyActuals: %v", err)
	}
	// Result must be sorted by period (oldest → newest).
	if !sort.SliceIsSorted(quarters, func(i, j int) bool {
		return quarters[i].Period < quarters[j].Period
	}) {
		t.Errorf("quarters not sorted: %+v", quarters)
	}
}

func TestTickerCachePath(t *testing.T) {
	dir := redirectCacheDir(t)
	p := tickerCachePath()
	// Path must live under the redirected cache root, so the test never writes
	// to a real shared cache location.
	if !strings.HasPrefix(p, dir) {
		t.Errorf("cache path %q should live under redirected dir %q", p, dir)
	}
	if !strings.HasSuffix(p, "sec_company_tickers.json") {
		t.Errorf("cache path %q should end with sec_company_tickers.json", p)
	}
}

func TestReadTickerCache_StaleFile(t *testing.T) {
	redirectCacheDir(t)

	p := tickerCachePath()
	if err := os.MkdirAll(path.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buildTickerJSON(map[string]int{"X": 1}), 0o644); err != nil {
		t.Fatal(err)
	}
	// Backdate mtime to 48h ago — cache should be treated as stale.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}

	if _, ok := readTickerCache(); ok {
		t.Error("expected stale cache (>24h) to be rejected")
	}
}

// ── item8KLabel ───────────────────────────────────────────────────────────────

func TestItem8KLabel(t *testing.T) {
	cases := []struct {
		items string
		want  string
	}{
		{"2.02,9.01", "Earnings Release"},
		{"5.02", "Director/Officer Change"},
		{"1.01,9.01", "Material Agreement"},
		{"1.03", "Bankruptcy/Receivership"},
		{"9.01", "Financial Statements"},
		{"7.01", "Regulation FD Disclosure"},
		{"8.01", "Other Events"},
		{"5.01", "Change in Control"},
		{"3.01", "Rating Agency Action"},
		// Priority: 1.03 beats 2.02 when both present.
		{"2.02,1.03", "Bankruptcy/Receivership"},
		// Unknown item falls back to "Item X.XX" form.
		{"6.99", "Item 6.99"},
		{"", "8-K Filing"},
	}
	for _, tc := range cases {
		got := item8KLabel(tc.items)
		if got != tc.want {
			t.Errorf("item8KLabel(%q) = %q, want %q", tc.items, got, tc.want)
		}
	}
}

// ── FetchMaterialEvents ───────────────────────────────────────────────────────

func TestFetchMaterialEvents_Basic(t *testing.T) {
	// Submissions JSON with three 8-Ks and one 8-K/A (amendment) in the recent window.
	// One 8-K has only "9.01" and must be filtered out.
	submissions := `{"filings":{"recent":{
		"form":["8-K","8-K","8-K","8-K/A","10-Q"],
		"filingDate":["2026-03-15","2026-02-20","2026-01-10","2026-02-21","2026-03-01"],
		"accessionNumber":["","","","",""],
		"primaryDocument":["","","","",""],
		"items":["2.02,9.01","5.02","9.01","2.02",""],
		"primaryDocDescription":["EARNINGS","MGMT CHANGE","","",""]
	}}}`
	tr := testutil.NewMockTransport().On("/submissions/CIK0000320193.json", 200, submissions, "application/json")
	c := &Client{
		httpClient: testutil.NewMockClient(tr),
		tickerCIK:  map[string]int{"AAPL": 320193},
	}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events, err := c.FetchMaterialEvents("AAPL", since)
	if err != nil {
		t.Fatalf("FetchMaterialEvents: %v", err)
	}
	// 8-K/A amendment excluded, 9.01-only excluded, 10-Q excluded → 2 events.
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	// Most-recent first (submissions JSON is ordered newest → oldest).
	if events[0].Date != "2026-03-15" || events[0].Label != "Earnings Release" {
		t.Errorf("event[0] = %+v, want 2026-03-15 Earnings Release", events[0])
	}
	if events[1].Date != "2026-02-20" || events[1].Label != "Director/Officer Change" {
		t.Errorf("event[1] = %+v, want 2026-02-20 Director/Officer Change", events[1])
	}
}

func TestFetchMaterialEvents_SinceFilter(t *testing.T) {
	// One 8-K in range, one older than `since`.
	submissions := `{"filings":{"recent":{
		"form":["8-K","8-K"],
		"filingDate":["2026-04-01","2025-12-01"],
		"accessionNumber":["",""],
		"primaryDocument":["",""],
		"items":["1.01","1.01"],
		"primaryDocDescription":["",""]
	}}}`
	tr := testutil.NewMockTransport().On("/submissions/", 200, submissions, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"X": 1}}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events, _ := c.FetchMaterialEvents("X", since)
	if len(events) != 1 || events[0].Date != "2026-04-01" {
		t.Errorf("expected 1 in-range event at 2026-04-01, got %+v", events)
	}
}

func TestFetchMaterialEvents_UnknownSymbol(t *testing.T) {
	c := &Client{tickerCIK: map[string]int{}}
	_, err := c.FetchMaterialEvents("UNKNOWN", time.Now())
	if err == nil {
		t.Error("expected error for unknown symbol")
	}
}

// ── FetchInsiderActivity ──────────────────────────────────────────────────────

func TestFetchInsiderActivity_NoFilings(t *testing.T) {
	submissions := `{"filings":{"recent":{
		"form":["10-Q","8-K"],
		"filingDate":["2026-03-01","2026-02-01"],
		"accessionNumber":["",""],
		"primaryDocument":["",""]
	}}}`
	tr := testutil.NewMockTransport().On("/submissions/", 200, submissions, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"T": 1}}
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	result, err := c.FetchInsiderActivity("T", since)
	if err != nil {
		t.Fatalf("FetchInsiderActivity: %v", err)
	}
	if result.Activity != "No Activity" {
		t.Errorf("expected No Activity when no Form 4 filings, got %q", result.Activity)
	}
}

func TestFetchInsiderActivity_Form4Parsed(t *testing.T) {
	// Submissions with one Form 4 filing.
	submissions := `{"filings":{"recent":{
		"form":["4"],
		"filingDate":["2026-03-15"],
		"accessionNumber":["0001234567890001"],
		"primaryDocument":["form4.xml"]
	}}}`
	// Minimal Form 4 XML with one open-market purchase (code P).
	form4XML := `<?xml version="1.0"?>
<ownershipDocument>
  <nonDerivativeTable>
    <nonDerivativeTransaction>
      <transactionCoding><transactionCode>P</transactionCode></transactionCoding>
      <transactionAmounts>
        <transactionShares><value>1000</value></transactionShares>
        <transactionPricePerShare><value>100.00</value></transactionPricePerShare>
      </transactionAmounts>
    </nonDerivativeTransaction>
  </nonDerivativeTable>
</ownershipDocument>`

	tr := testutil.NewMockTransport()
	tr.On("/submissions/CIK0000000001.json", 200, submissions, "application/json")
	tr.On("/Archives/edgar/data/1/", 200, form4XML, "application/xml")

	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"INS": 1}}
	result, err := c.FetchInsiderActivity("INS", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchInsiderActivity: %v", err)
	}
	if result.BuyShares != 1000 {
		t.Errorf("BuyShares = %d, want 1000", result.BuyShares)
	}
	if result.Activity != "Net Buyer" {
		t.Errorf("Activity = %q, want Net Buyer", result.Activity)
	}
}

func TestNewSECClient(t *testing.T) {
	c := NewClient()
	if c == nil || c.httpClient == nil {
		t.Error("NewSECClient returned nil or missing httpClient")
	}
}

// Compile-time assertion that testutil.MockTransport still satisfies http.RoundTripper.
var _ http.RoundTripper = (*testutil.MockTransport)(nil)

func TestFetchEntitySIC_Valid(t *testing.T) {
	submissions := `{
		"name": "Apple Inc.",
		"sic": "3571",
		"sicDescription": "ELECTRONIC COMPUTERS",
		"filings": {"recent": {
			"form": [], "filingDate": [], "accessionNumber": [],
			"primaryDocument": [], "items": [], "primaryDocDescription": []
		}}
	}`
	tr := testutil.NewMockTransport().On("/submissions/CIK0000320193.json", 200, submissions, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"AAPL": 320193}}
	sic, desc, err := c.FetchEntitySIC("AAPL")
	if err != nil {
		t.Fatalf("FetchEntitySIC: %v", err)
	}
	if sic != 3571 {
		t.Errorf("SIC = %d, want 3571", sic)
	}
	if desc != "ELECTRONIC COMPUTERS" {
		t.Errorf("SICDescription = %q, want ELECTRONIC COMPUTERS", desc)
	}
}

func TestFetchEntitySIC_UnknownSymbol(t *testing.T) {
	c := &Client{tickerCIK: map[string]int{}}
	_, _, err := c.FetchEntitySIC("UNKNOWN")
	if err == nil {
		t.Error("expected error for unknown symbol")
	}
}

func TestItem8KLabel_MultipleItems_PriorityOrder(t *testing.T) {
	// 1.03 (Bankruptcy) must beat every other item in priority.
	cases := []struct {
		items string
		want  string
	}{
		{"2.02,1.03,9.01", "Bankruptcy/Receivership"},
		{"1.01,2.02", "Material Agreement"},
		{"8.01,7.01", "Regulation FD Disclosure"}, // 7.01 beats 8.01
		{"9.01,2.02", "Earnings Release"},         // 2.02 beats 9.01
	}
	for _, tc := range cases {
		got := item8KLabel(tc.items)
		if got != tc.want {
			t.Errorf("item8KLabel(%q) = %q, want %q", tc.items, got, tc.want)
		}
	}
}

func TestItem8KLabel_WhitespaceTrimmed(t *testing.T) {
	// Items may have spaces around commas.
	got := item8KLabel("2.02 , 9.01")
	if got != "Earnings Release" {
		t.Errorf("item8KLabel with spaces = %q, want Earnings Release", got)
	}
}

func TestFetchMaterialEvents_CapAt10(t *testing.T) {
	// Build submissions with 15 valid 8-Ks — result must be capped at 10.
	forms := make([]string, 15)
	dates := make([]string, 15)
	items := make([]string, 15)
	descs := make([]string, 15)
	for i := range forms {
		forms[i] = `"8-K"`
		dates[i] = `"2026-04-01"`
		items[i] = `"1.01"`
		descs[i] = `""`
	}
	submissions := `{"filings":{"recent":{
		"form":[` + strings.Join(forms, ",") + `],
		"filingDate":[` + strings.Join(dates, ",") + `],
		"accessionNumber":[` + strings.Repeat(`"",`, 14) + `""` + `],
		"primaryDocument":[` + strings.Repeat(`"",`, 14) + `""` + `],
		"items":[` + strings.Join(items, ",") + `],
		"primaryDocDescription":[` + strings.Join(descs, ",") + `]
	}}}`

	tr := testutil.NewMockTransport().On("/submissions/", 200, submissions, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"CAP": 1}}
	events, err := c.FetchMaterialEvents("CAP", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("FetchMaterialEvents: %v", err)
	}
	if len(events) != 10 {
		t.Errorf("expected 10 events (cap), got %d", len(events))
	}
}

func TestFetchMaterialEvents_EmptyItemsFiltered(t *testing.T) {
	// Items = "" must be excluded (same rule as "9.01" only).
	submissions := `{"filings":{"recent":{
		"form":["8-K","8-K"],
		"filingDate":["2026-03-01","2026-03-02"],
		"accessionNumber":["",""],
		"primaryDocument":["",""],
		"items":["","1.01"],
		"primaryDocDescription":["",""]
	}}}`
	tr := testutil.NewMockTransport().On("/submissions/", 200, submissions, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"EMP": 1}}
	events, _ := c.FetchMaterialEvents("EMP", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(events) != 1 || events[0].Items != "1.01" {
		t.Errorf("expected only the non-empty-items 8-K, got %+v", events)
	}
}

func TestFetchMaterialEvents_LabelPopulated(t *testing.T) {
	submissions := `{"filings":{"recent":{
		"form":["8-K"],
		"filingDate":["2026-04-10"],
		"accessionNumber":[""],
		"primaryDocument":[""],
		"items":["5.02"],
		"primaryDocDescription":[""]
	}}}`
	tr := testutil.NewMockTransport().On("/submissions/", 200, submissions, "application/json")
	c := &Client{httpClient: testutil.NewMockClient(tr), tickerCIK: map[string]int{"LBL": 1}}
	evts, _ := c.FetchMaterialEvents("LBL", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(evts) != 1 || evts[0].Label != "Director/Officer Change" {
		t.Errorf("expected label Director/Officer Change, got %+v", evts)
	}
}

func TestFetchEarningsReleases_ReadsOlderPagesAndFilters(t *testing.T) {
	recent := `{"name":"X","sic":"3571","filings":{"recent":{
		"form":["8-K","8-K/A","4","8-K","8-K"],
		"filingDate":["2026-07-30","2026-08-02","2026-07-01","2026-05-15","2026-04-30"],
		"acceptanceDateTime":["2026-07-31T00:30:28.000Z","","","2026-05-15T12:00:00.000Z","2026-04-30T20:31:00.000Z"],
		"reportDate":["2026-07-30","","","2026-05-15","2026-04-30"],
		"accessionNumber":["a1","a2","a3","a4","a5"],
		"primaryDocument":["d1.htm","d2.htm","d3.htm","d4.htm","d5.htm"],
		"items":["2.02,9.01","2.02","","5.07","2.02,9.01"]},
		"files":[
			{"name":"CIK0000000001-submissions-001.json","filingFrom":"2015-01-01","filingTo":"2026-01-31"},
			{"name":"CIK0000000001-submissions-002.json","filingFrom":"2001-01-01","filingTo":"2014-12-31"}]}}`
	page1 := `{"form":["8-K","8-K"],"filingDate":["2026-01-29","2015-10-28"],
		"acceptanceDateTime":["2026-01-29T21:35:00.000Z","2015-10-28T20:30:00.000Z"],
		"reportDate":["2026-01-29","2015-10-28"],"accessionNumber":["b1","b2"],
		"primaryDocument":["e1.htm","e2.htm"],"items":["2.02,9.01","2.02"]}`
	tr := testutil.NewMockTransport().
		On("submissions/CIK0000000001.json", 200, recent, "application/json").
		On("CIK0000000001-submissions-001.json", 200, page1, "application/json")
	c := NewClientWithHTTP(testutil.NewMockClient(tr), map[string]int{"X": 1})

	got, err := c.FetchEarningsReleases("X", "2016-01-01")
	if err != nil {
		t.Fatal(err)
	}
	var dates []string
	for _, r := range got {
		dates = append(dates, r.FilingDate)
	}
	// Item 2.02 originals only (no 8-K/A, no Form 4, no 5.07), since-filtered,
	// oldest first, including the older page; page 002 (pre-2015) never fetched.
	if want := "2026-01-29,2026-04-30,2026-07-30"; strings.Join(dates, ",") != want {
		t.Errorf("release dates = %v, want %s", dates, want)
	}
	if got[2].Accession != "a1" || got[2].PrimaryDoc != "d1.htm" ||
		!got[2].Accepted.Equal(time.Date(2026, 7, 31, 0, 30, 28, 0, time.UTC)) {
		t.Errorf("latest release = %+v", got[2])
	}
	if tr.Count("CIK0000000001-submissions-002.json") != 0 {
		t.Error("pages entirely before `since` must not be fetched")
	}
}

func TestGet_RetriesThrottlingThenSucceeds(t *testing.T) {
	calls := 0
	tr := testutil.NewMockTransport().OnFunc(
		func(r *http.Request) bool { return true },
		func(r *http.Request) *http.Response {
			calls++
			status, body := http.StatusTooManyRequests, "slow down"
			if calls == 3 {
				status, body = http.StatusOK, `{"name":"X"}`
			}
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}
		})
	c := NewClientWithHTTP(testutil.NewMockClient(tr), nil)
	c.retryBackoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	resp, err := c.get("https://data.sec.gov/submissions/CIK0000000001.json")
	if err != nil || resp.StatusCode != http.StatusOK || calls != 3 {
		t.Fatalf("get = %v, %v after %d calls", resp, err, calls)
	}
	resp.Body.Close()
}

func TestFetchQuarterlyHistory_ReportsFailuresNotMissingData(t *testing.T) {
	tr := testutil.NewMockTransport().On("companyconcept", http.StatusTooManyRequests, "rate limited", "text/plain")
	c := NewClientWithHTTP(testutil.NewMockClient(tr), map[string]int{"X": 1})
	_, err := c.FetchQuarterlyHistory("X")
	if err == nil || !strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "no EPS concept found") {
		t.Errorf("a throttled request must surface as a failure, got: %v", err)
	}
}

func TestFetchConcept_FallsBackToCompanyFactsOnEmptyUnits(t *testing.T) {
	// companyconcept serves the units as empty objects (seen for KO, ABT, PYPL, F).
	broken := `{"units":{"USD/shares":{},"pure":{}}}`
	facts := `{"facts":{"us-gaap":{"EarningsPerShareDiluted":{"units":{"USD/shares":[
		{"start":"2026-01-01","end":"2026-03-31","val":0.91,"form":"10-Q","filed":"2026-04-30"},
		{"start":"2025-10-01","end":"2025-12-31","val":0.70,"form":"10-K","filed":"2026-02-20"}]}}}}}`
	tr := testutil.NewMockTransport().
		On("companyconcept", 200, broken, "application/json").
		On("companyfacts/CIK0000021344.json", 200, facts, "application/json")
	c := NewClientWithHTTP(testutil.NewMockClient(tr), map[string]int{"KO": 21344})
	vals, _, _, err := c.fetchConcept(21344, "us-gaap", "EarningsPerShareDiluted")
	if err != nil || vals["2026-03-31"] != 0.91 {
		t.Fatalf("fallback values = %v, err %v", vals, err)
	}
	// A second concept for the same company reuses the cached document.
	c.fetchConcept(21344, "us-gaap", "Revenues")
	if n := tr.Count("companyfacts"); n != 1 {
		t.Errorf("companyfacts fetched %d times, want 1", n)
	}
}
