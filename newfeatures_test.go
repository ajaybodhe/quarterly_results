package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// ── --max-cap-b / filterByMarketCap ──────────────────────────────────────────

func events(caps ...float64) []EarningsEvent {
	out := make([]EarningsEvent, len(caps))
	for i, c := range caps {
		out[i] = EarningsEvent{Symbol: "X", MarketCap: c}
	}
	return out
}

func TestFilterByMarketCap_MinOnly(t *testing.T) {
	// minCap=50B, no upper bound (maxCap=0).
	in := events(10e9, 50e9, 100e9, 200e9)
	got := filterByMarketCap(in, 50e9, 0)
	if len(got) != 3 {
		t.Errorf("expected 3 results (≥$50B), got %d", len(got))
	}
}

func TestFilterByMarketCap_MinAndMax(t *testing.T) {
	in := events(5e9, 20e9, 50e9, 100e9, 500e9)
	// $10B–$100B window → 20B, 50B, 100B qualify.
	got := filterByMarketCap(in, 10e9, 100e9)
	if len(got) != 3 {
		t.Errorf("expected 3 results ($10B–$100B), got %d: %+v", len(got), got)
	}
}

func TestFilterByMarketCap_MaxZeroMeansNoUpperBound(t *testing.T) {
	in := events(1e9, 999e9)
	got := filterByMarketCap(in, 1, 0) // minCap=$1, no max
	if len(got) != 2 {
		t.Errorf("maxCap=0 should include all ≥minCap, got %d", len(got))
	}
}

func TestFilterByMarketCap_BoundaryInclusion(t *testing.T) {
	// Exactly on the boundaries — both endpoints are inclusive.
	in := events(10e9, 50e9)
	got := filterByMarketCap(in, 10e9, 50e9)
	if len(got) != 2 {
		t.Errorf("boundary values should be included, got %d", len(got))
	}
}

func TestFilterByMarketCap_NoneQualify(t *testing.T) {
	in := events(1e9, 2e9, 3e9)
	got := filterByMarketCap(in, 100e9, 0)
	if len(got) != 0 {
		t.Errorf("expected 0 results, got %d", len(got))
	}
}

func TestFilterByMarketCap_EmptyInput(t *testing.T) {
	got := filterByMarketCap(nil, 10e9, 100e9)
	if len(got) != 0 {
		t.Errorf("expected 0 results for nil input, got %d", len(got))
	}
}

func TestFilterByMarketCap_MaxBelowMin(t *testing.T) {
	// maxCap < minCap → nothing passes both filters.
	in := events(10e9, 20e9, 30e9)
	got := filterByMarketCap(in, 50e9, 10e9) // 50B min, 10B max: impossible range
	if len(got) != 0 {
		t.Errorf("impossible range should return 0 results, got %d", len(got))
	}
}

// ── item8KLabel (already tested in sec_test.go; add edge cases) ──────────────

func TestItem8KLabel_MultipleItems_PriorityOrder(t *testing.T) {
	// 1.03 (Bankruptcy) must beat every other item in priority.
	cases := []struct {
		items string
		want  string
	}{
		{"2.02,1.03,9.01", "Bankruptcy/Receivership"},
		{"1.01,2.02", "Material Agreement"},
		{"8.01,7.01", "Regulation FD Disclosure"}, // 7.01 beats 8.01
		{"9.01,2.02", "Earnings Release"},          // 2.02 beats 9.01
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

// ── FetchMaterialEvents edge cases ────────────────────────────────────────────

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
		"accessionNumber":[` + strings.Repeat(`"",`, 14) + `""`+`],
		"primaryDocument":[` + strings.Repeat(`"",`, 14) + `""`+`],
		"items":[` + strings.Join(items, ",") + `],
		"primaryDocDescription":[` + strings.Join(descs, ",") + `]
	}}}`

	tr := newMockTransport().on("/submissions/", 200, submissions, "application/json")
	c := &SECClient{httpClient: newMockClient(tr), tickerCIK: map[string]int{"CAP": 1}}
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
	tr := newMockTransport().on("/submissions/", 200, submissions, "application/json")
	c := &SECClient{httpClient: newMockClient(tr), tickerCIK: map[string]int{"EMP": 1}}
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
	tr := newMockTransport().on("/submissions/", 200, submissions, "application/json")
	c := &SECClient{httpClient: newMockClient(tr), tickerCIK: map[string]int{"LBL": 1}}
	evts, _ := c.FetchMaterialEvents("LBL", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if len(evts) != 1 || evts[0].Label != "Director/Officer Change" {
		t.Errorf("expected label Director/Officer Change, got %+v", evts)
	}
}

// ── dailyVolatility date-cutoff path ─────────────────────────────────────────

func TestDailyVolatility_OnlyRecentPricesUsed(t *testing.T) {
	// Prices older than the window should be excluded from the vol calculation.
	// Construct two groups: a flat block far in the past and a volatile block
	// within the 5-day window. If the old prices were included, vol would be 0;
	// if only recent prices are used, vol should be positive.
	base := time.Now().AddDate(0, 0, -60)
	var pts []pricePoint
	// 50 flat days far in the past.
	for i := 0; i < 50; i++ {
		pts = append(pts, pricePoint{Date: base.AddDate(0, 0, i), Close: 100})
	}
	// 5 volatile days within the last 5 calendar days.
	recent := time.Now().AddDate(0, 0, -4)
	prices := []float64{100, 110, 95, 108, 102}
	for i, p := range prices {
		pts = append(pts, pricePoint{Date: recent.AddDate(0, 0, i), Close: p})
	}
	vol := dailyVolatility(pts, 5)
	if vol == 0 {
		t.Error("expected non-zero vol from recent volatile prices; old flat prices should not dominate")
	}
}

// ── Material events in writeStockCard output ─────────────────────────────────

func TestWriteStockCard_MaterialEventsAbnormalFlag(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("MAT")
	r.MaterialEvents = []MaterialEvent{
		{Date: "2026-04-10", Items: "2.02,9.01", Label: "Earnings Release", RetPct: 8.5, Abnormal: true},
		{Date: "2026-03-15", Items: "5.02", Label: "Director/Officer Change", RetPct: -0.3, Abnormal: false},
	}
	writeStockCard(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "◀ abnormal") {
		t.Error("expected abnormal flag for high-return event")
	}
	if strings.Count(out, "◀ abnormal") != 1 {
		t.Errorf("expected exactly 1 abnormal flag, got %d", strings.Count(out, "◀ abnormal"))
	}
	if !strings.Contains(out, "Director/Officer Change") {
		t.Error("normal event label should still appear")
	}
	if !strings.Contains(out, "+8.5%") {
		t.Error("positive return should be formatted with + sign")
	}
	if !strings.Contains(out, "-0.3%") {
		t.Error("negative return should appear with - sign")
	}
}

func TestWriteStockCard_MaterialEventsZeroReturn(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("ZERO")
	r.MaterialEvents = []MaterialEvent{
		// RetPct == 0 means price data was unavailable — display "—" not "+0.0%".
		{Date: "2026-04-01", Items: "1.01", Label: "Material Agreement", RetPct: 0, Abnormal: false},
	}
	writeStockCard(&buf, r)
	out := buf.String()
	if strings.Contains(out, "+0.0%") {
		t.Error("zero RetPct (unavailable) should render as — not +0.0%")
	}
	if !strings.Contains(out, "—") {
		t.Error("zero RetPct should render as — (em-dash)")
	}
}

func TestWriteStockCard_NoMaterialEvents(t *testing.T) {
	var buf bytes.Buffer
	r := minimalResult("NONE")
	r.MaterialEvents = nil
	writeStockCard(&buf, r)
	if strings.Contains(buf.String(), "Material Events") {
		t.Error("Material Events section should be absent when slice is empty")
	}
}

func TestWriteStockCard_ForwardEPS(t *testing.T) {
	// ForwardEPS is shown inside the Quarterly History block only when History
	// is also present.
	var buf bytes.Buffer
	r := minimalResult("FWD")
	r.History = []QuarterActual{
		{Period: "2025-12-31", PeriodStart: "2025-10-01", Revenue: 10e9, EPS: 3.0},
	}
	r.ForwardEPS = []ForwardQuarter{
		{FiscalEnd: "Mar/2026", ConsensusEPS: 3.50, LowEPS: 3.20, HighEPS: 3.80, NumberOfEstimates: 20},
		{FiscalEnd: "Jun/2026", ConsensusEPS: 3.70, LowEPS: 3.40, HighEPS: 4.00, NumberOfEstimates: 18},
	}
	writeStockCard(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "Forward EPS") {
		t.Error("expected Forward EPS section when ForwardEPS is populated")
	}
	if !strings.Contains(out, "Mar/2026") {
		t.Error("expected first forward quarter period in output")
	}
}

func TestWriteStockCard_AnalystCountZero(t *testing.T) {
	// When AnalystTotal == 0, bullish/neutral/bearish should show "N/A",
	// not a divide-by-zero panic or percentage string.
	var buf bytes.Buffer
	r := minimalResult("NOANAL")
	r.AnalystTotal = 0
	r.AnalystBullish = 0
	r.AnalystNeutral = 0
	r.AnalystBearish = 0
	writeStockCard(&buf, r)
	out := buf.String()
	if !strings.Contains(out, "N/A") {
		t.Error("expected N/A for analyst counts when total is 0")
	}
}
