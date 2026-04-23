package main

import (
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

func TestSaIsNull(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"null", true},
		{"void 0", true},
		{"PRO", true},
		{"PRO_ONLY", true},
		{"", true},
		{"1.23", false},
		{"0", false},
		{"null1", false}, // not equal to "null"
		{"2.5", false},
	}
	for _, tc := range cases {
		got := saIsNull(tc.input)
		if got != tc.want {
			t.Errorf("saIsNull(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestSaParseFloats(t *testing.T) {
	cases := []struct {
		input string
		want  []float64
	}{
		{"1.5,2.0,null,3.7", []float64{1.5, 2.0, 0, 3.7}},
		{"null,null", []float64{0, 0}},
		{"1.0", []float64{1.0}},
		{"bad_value", []float64{0}},
	}
	for _, tc := range cases {
		got := saParseFloats(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("saParseFloats(%q) len=%d, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i, v := range tc.want {
			if math.Abs(got[i]-v) > 1e-9 {
				t.Errorf("saParseFloats(%q)[%d] = %v, want %v", tc.input, i, got[i], v)
			}
		}
	}
}

func TestSaParseStrings(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{`"2025-03","2025-06"`, []string{"2025-03", "2025-06"}},
		{`"a","b","c"`, []string{"a", "b", "c"}},
		{`"solo"`, []string{"solo"}},
	}
	for _, tc := range cases {
		got := saParseStrings(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("saParseStrings(%q) len=%d, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i, v := range tc.want {
			if got[i] != v {
				t.Errorf("saParseStrings(%q)[%d] = %q, want %q", tc.input, i, got[i], v)
			}
		}
	}
}

func TestSaParseInts(t *testing.T) {
	cases := []struct {
		input string
		want  []int
	}{
		{"3,null,5", []int{3, 0, 5}},
		{"null", []int{0}},
		{"1,2,3", []int{1, 2, 3}},
	}
	for _, tc := range cases {
		got := saParseInts(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("saParseInts(%q) len=%d, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i, v := range tc.want {
			if got[i] != v {
				t.Errorf("saParseInts(%q)[%d] = %d, want %d", tc.input, i, got[i], v)
			}
		}
	}
}

func TestParseSAEstimates(t *testing.T) {
	t.Run("missing quarterly block returns error", func(t *testing.T) {
		_, err := parseSAEstimates("<html>no data here</html>")
		if err == nil {
			t.Error("expected error for HTML without quarterly block")
		}
	})

	t.Run("valid synthetic HTML", func(t *testing.T) {
		// Construct minimal HTML matching the actual regex patterns in stockanalysis.go:
		//   saQuarterlyRe:       quarterly:{eps:[...],dates:[...],revenue:[...],analysts:[...]}
		//   saRevenueNextRe:     revenueNext:{last:N,this:N
		//   saRevenueThisRe:     revenueThis:{last:N,this:N
		//   saRatingsSnapshotRe: {buy:N,date:"...",hold:N,sell:N,month:"...",score:N,total:N,updated:"...",consensus:"...",strongBuy:N,strongSell:N}
		//   saPriceTargetRe:     pt_now:N
		html := `quarterly:{eps:[3.1,3.2],dates:["2025-03","2025-06"],revenue:[1000000000,1100000000],analysts:[5,6]},` +
			`revenueNext:{last:900000000,this:1000000000},` +
			`revenueThis:{last:800000000,this:900000000},` +
			`{buy:5,date:"2025-01",hold:4,sell:2,month:"2025-01",score:3.5,total:15,updated:"2025-01-01",consensus:"Buy",strongBuy:3,strongSell:1},` +
			`pt_now:150 pt_now:160`
		est, err := parseSAEstimates(html)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if est == nil {
			t.Fatal("expected non-nil result")
		}
		// The function picks the first forward quarter where analysts > 0 and epsEst != 0.
		// That would be the first entry: epsEst=3.10, analysts=5.
		if math.Abs(est.EPSEst-3.10) > 1e-6 {
			t.Errorf("EPSEst = %v, want 3.10", est.EPSEst)
		}
		// Revenue estimate: first future revEst (non-null)
		if est.RevenueEst == 0 {
			t.Error("RevenueEst should be non-zero")
		}
		// Analyst ratings: StrongBuy maps to strongBuy:3 in the regex capture
		if est.StrongBuy != 3 {
			t.Errorf("StrongBuy = %d, want 3", est.StrongBuy)
		}
		if est.Hold != 4 {
			t.Errorf("Hold = %d, want 4", est.Hold)
		}
		// Price target: average of all pt_now values (150+160)/2 = 155
		if math.Abs(est.AvgPriceTarget-155) > 1e-6 {
			t.Errorf("AvgPriceTarget = %v, want 155", est.AvgPriceTarget)
		}
	})
}

// saHTMLBuilder helps compose synthetic stockanalysis HTML fragments for tests.
type saHTMLBuilder struct {
	eps, revenues, analysts string
	dates                   string
	revenueNext, revenueThis string
	extras                  []string
}

func (b saHTMLBuilder) String() string {
	return "quarterly:{eps:[" + b.eps +
		"],dates:[" + b.dates +
		"],revenue:[" + b.revenues +
		"],analysts:[" + b.analysts + "]}," +
		b.revenueNext + "," + b.revenueThis +
		strings.Join(b.extras, ",")
}

// TestParseSAEstimates_RevenueRowSelection pins the IBM/TSLA regression: when
// the first quarterly-table row with analysts>0 has a revenue cell populated,
// that value must be used as the forward estimate — NOT revenueNext.this from
// the stats block (which is the quarter *after* the upcoming report).
func TestParseSAEstimates_RevenueRowSelection(t *testing.T) {
	b := saHTMLBuilder{
		eps:      "3.1,3.2,3.3",
		dates:    `"2026-03","2026-06","2026-09"`,
		revenues: "15640000000,17890000000,19000000000", // IBM-like: row0 < row1
		analysts: "18,15,10",
		// revenueNext.this = 17.89B matches row 1, the row one quarter too far.
		// revenueThis.this = 15.64B matches row 0, the upcoming report.
		revenueNext: "revenueNext:{last:15640000000,this:17890000000}",
		revenueThis: "revenueThis:{last:14000000000,this:15640000000}",
	}
	est, err := parseSAEstimates(b.String())
	if err != nil {
		t.Fatalf("parseSAEstimates: %v", err)
	}
	// Must use the table row (15.64B), not revenueNext.this (17.89B).
	if math.Abs(est.RevenueEst-15_640_000_000) > 1 {
		t.Errorf("RevenueEst = %.0f, want 15_640_000_000 (table row, not revenueNext.this)", est.RevenueEst)
	}
	if math.Abs(est.RevenuePrevYear-14_000_000_000) > 1 {
		t.Errorf("RevenuePrevYear = %.0f, want 14_000_000_000 (revenueThis.last)", est.RevenuePrevYear)
	}
	if est.ReportDate != "2026-03" {
		t.Errorf("ReportDate = %q, want 2026-03", est.ReportDate)
	}
	if est.Analysts != 18 {
		t.Errorf("Analysts = %d, want 18", est.Analysts)
	}
}

// TestParseSAEstimates_PaywallFallback: if the quarterly-table revenue cell is
// null/0 (paywalled), fall back to revenueNext.this from the stats block.
func TestParseSAEstimates_PaywallFallback(t *testing.T) {
	b := saHTMLBuilder{
		eps:      "3.1,3.2",
		dates:    `"2026-03","2026-06"`,
		revenues: "null,17000000000", // paywalled row 0
		analysts: "20,15",
		revenueNext: "revenueNext:{last:14000000000,this:15500000000}",
		revenueThis: "revenueThis:{last:14000000000,this:15500000000}",
	}
	est, err := parseSAEstimates(b.String())
	if err != nil {
		t.Fatalf("parseSAEstimates: %v", err)
	}
	if math.Abs(est.RevenueEst-15_500_000_000) > 1 {
		t.Errorf("fallback RevenueEst = %.0f, want revenueNext.this = 15_500_000_000", est.RevenueEst)
	}
}

// TestParseSAEstimates_NoForwardRow: when every row has analysts == 0 (future
// rows only), parseSAEstimates must return an error rather than a zeroed struct.
func TestParseSAEstimates_NoForwardRow(t *testing.T) {
	b := saHTMLBuilder{
		eps:      "null,null",
		dates:    `"2026-12","2027-03"`,
		revenues: "null,null",
		analysts: "0,0",
		revenueNext: "revenueNext:{last:1,this:1}",
		revenueThis: "revenueThis:{last:1,this:1}",
	}
	_, err := parseSAEstimates(b.String())
	if err == nil {
		t.Error("expected error when no row has analysts > 0")
	}
}

// TestParseSAEstimates_LatestRatingSnapshotWins: with multiple monthly rating
// snapshots in the page, the *last* one represents the current month and must
// be the one surfaced.
func TestParseSAEstimates_LatestRatingSnapshotWins(t *testing.T) {
	b := saHTMLBuilder{
		eps:      "3.1",
		dates:    `"2026-03"`,
		revenues: "15000000000",
		analysts: "10",
		revenueNext: "revenueNext:{last:14000000000,this:15000000000}",
		revenueThis: "revenueThis:{last:13000000000,this:14000000000}",
		extras: []string{
			// Older snapshot.
			`{buy:1,date:"2025-01",hold:1,sell:1,month:"2025-01",score:2.0,total:5,updated:"2025-01-01",consensus:"Hold",strongBuy:1,strongSell:1}`,
			// Latest snapshot — should win.
			`{buy:9,date:"2026-03",hold:3,sell:1,month:"2026-03",score:4.2,total:20,updated:"2026-03-01",consensus:"Strong Buy",strongBuy:6,strongSell:1}`,
		},
	}
	est, err := parseSAEstimates(b.String())
	if err != nil {
		t.Fatalf("parseSAEstimates: %v", err)
	}
	if est.ConsensusRating != "Strong Buy" {
		t.Errorf("ConsensusRating = %q, want Strong Buy (latest snapshot)", est.ConsensusRating)
	}
	if est.Buy != 9 || est.StrongBuy != 6 || est.TotalRatings != 20 {
		t.Errorf("latest snapshot counts mismatch: Buy=%d StrongBuy=%d Total=%d", est.Buy, est.StrongBuy, est.TotalRatings)
	}
}

func TestFetchSAEstimates_HTTPError(t *testing.T) {
	tr := newMockTransport().on("stockanalysis.com", 503, "maintenance", "text/html")
	e := &Enricher{httpClient: newMockClient(tr)}
	_, err := e.fetchSAEstimates("AAPL")
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("expected 503 error, got %v", err)
	}
}

func TestFetchSAEstimates_LowercaseSymbolInURL(t *testing.T) {
	// stockanalysis URLs require lowercase tickers; fetchSAEstimates must lowercase.
	var seenURL string
	tr := newMockTransport().onFunc(
		func(r *http.Request) bool { seenURL = r.URL.String(); return true },
		func(r *http.Request) *http.Response {
			// Return a minimally valid HTML so the caller does not error on content.
			b := saHTMLBuilder{
				eps:      "1.0",
				dates:    `"2026-03"`,
				revenues: "100",
				analysts: "5",
				revenueNext: "revenueNext:{last:50,this:100}",
				revenueThis: "revenueThis:{last:50,this:100}",
			}
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(b.String())),
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Request:    r,
			}
		},
	)
	e := &Enricher{httpClient: newMockClient(tr)}
	if _, err := e.fetchSAEstimates("AAPL"); err != nil {
		t.Fatalf("fetchSAEstimates: %v", err)
	}
	if !strings.Contains(seenURL, "/stocks/aapl/forecast") {
		t.Errorf("URL should lowercase ticker: %s", seenURL)
	}
}
