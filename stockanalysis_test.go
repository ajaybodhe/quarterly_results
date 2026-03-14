package main

import (
	"math"
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
