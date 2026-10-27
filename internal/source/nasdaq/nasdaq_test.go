package nasdaq

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseEPS(t *testing.T) {
	cases := []struct {
		input string
		want  float64
	}{
		{"", 0},
		{"N/A", 0},
		{"-", 0},
		{"$4.51", 4.51},
		{"-$0.12", -0.12},
		{"($0.61)", -0.61},
		{"($12.34)", -12.34},
		{"$0", 0},
		{"$1,234.56", 1234.56},
		{"  $2.00  ", 2.00}, // leading/trailing whitespace
	}
	for _, tc := range cases {
		got := parseEPS(tc.input)
		if math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("parseEPS(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestParseMarketCap(t *testing.T) {
	cases := []struct {
		input   string
		want    float64
		wantErr bool
	}{
		{"", 0, false},
		{"N/A", 0, false},
		{"$84,902,848,858", 84_902_848_858, false},
		{"$10,000,000,000", 10_000_000_000, false},
		{"$1,000", 1000, false},
		{"not-a-number", 0, true},
	}
	for _, tc := range cases {
		got, err := parseMarketCap(tc.input)
		if tc.wantErr && err == nil {
			t.Errorf("parseMarketCap(%q): expected error, got nil", tc.input)
			continue
		}
		if !tc.wantErr && err != nil {
			t.Errorf("parseMarketCap(%q): unexpected error: %v", tc.input, err)
			continue
		}
		if math.Abs(got-tc.want) > 1 {
			t.Errorf("parseMarketCap(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestNormalizeNasdaqTime(t *testing.T) {
	cases := []struct {
		input, want string
	}{
		{"time-pre-market", "bmo"},
		{"time-after-hours", "amc"},
		{"time-not-supplied", ""},
		{"", ""},
		{"anything-else", ""},
	}
	for _, tc := range cases {
		got := normalizeNasdaqTime(tc.input)
		if got != tc.want {
			t.Errorf("normalizeNasdaqTime(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestNasdaqGetJSONRetriesWAFResponses exercises the WAF-detection retry
// loop: an HTTP 200 with text/html body (BOM-prefixed <html>) and an HTTP
// 403 both must be retried, and a subsequent JSON response must decode
// cleanly. This is the regression test for the two Akamai Bot Manager
// failure modes we saw against api.nasdaq.com.
func TestNasdaqGetJSONRetriesWAFResponses(t *testing.T) {
	// Shrink backoffs so the test runs in milliseconds.
	orig := nasdaqRetryBackoffs
	nasdaqRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	defer func() { nasdaqRetryBackoffs = orig }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		switch n {
		case 1:
			// HTTP 200 + BOM-prefixed HTML — the sneaky WAF challenge.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("\xef\xbb\xbf\r\n<html><head><title>Access Denied</title></head></html>"))
		case 2:
			// Plain 403 — the loud WAF block.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte("<html>Access Denied</html>"))
		default:
			// Success on the third attempt.
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"ok":true,"n":42}`)
		}
	}))
	defer srv.Close()

	var out struct {
		OK bool `json:"ok"`
		N  int  `json:"n"`
	}
	if err := nasdaqGetJSON(srv.Client(), srv.URL+"/api/test", &out); err != nil {
		t.Fatalf("expected success after retries, got error: %v", err)
	}
	if !out.OK || out.N != 42 {
		t.Fatalf("decoded body mismatch: %+v", out)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 server calls (WAF-html, 403, JSON), got %d", got)
	}
}

// TestNasdaqGetJSONGivesUpAfterMaxRetries confirms the loop returns the
// last WAF error (not a decode error) once every attempt has been used.
func TestNasdaqGetJSONGivesUpAfterMaxRetries(t *testing.T) {
	orig := nasdaqRetryBackoffs
	nasdaqRetryBackoffs = []time.Duration{1 * time.Millisecond, 1 * time.Millisecond, 1 * time.Millisecond}
	defer func() { nasdaqRetryBackoffs = orig }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte("<html>Access Denied</html>"))
	}))
	defer srv.Close()

	var out map[string]interface{}
	err := nasdaqGetJSON(srv.Client(), srv.URL+"/api/test", &out)
	if err == nil {
		t.Fatal("expected error after exhausted retries, got nil")
	}
	if !strings.Contains(err.Error(), "WAF") {
		t.Fatalf("expected WAF error, got: %v", err)
	}
}

func TestParseSurprise(t *testing.T) {
	for in, want := range map[string]float64{"3.37": 3.37, "-12.5": -12.5, " 1,234.5% ": 1234.5} {
		if got := parseSurprise(in); got == nil || *got != want {
			t.Errorf("parseSurprise(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"", "N/A", "-", "abc"} {
		if got := parseSurprise(in); got != nil {
			t.Errorf("parseSurprise(%q) = %v, want nil", in, *got)
		}
	}
}

func TestSurprisePctFallback(t *testing.T) {
	cases := []struct {
		surprise, eps, forecast string
		want                    *float64
	}{
		{"3.37", "$1.51", "$1.61", ptr(3.37)}, // Nasdaq's value wins
		{"N/A", "$1.22", "$1.22", ptr(0)},     // in-line: Nasdaq leaves it blank
		{"N/A", "$1.51", "$1.61", nil},        // CDNS 2026-10-26: actual was last year's placeholder
		{"N/A", "N/A", "$1.61", nil},          // not reported yet
		{"N/A", "$0.00", "$0.00", nil},        // no forecast
	}
	for _, c := range cases {
		got := surprisePct(c.surprise, c.eps, c.forecast)
		if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
			t.Errorf("surprisePct(%q, %q, %q) = %v, want %v", c.surprise, c.eps, c.forecast, got, c.want)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func TestLargestUS(t *testing.T) {
	rows := []screenerRow{
		{Symbol: "AAPL", MarketCap: "3000000000000", Country: "United States"},
		{Symbol: "TSM", MarketCap: "2000000000000", Country: "Taiwan"},
		{Symbol: "BRK/B", MarketCap: "1000000000000", Country: "United States"},
		{Symbol: "BAC^K", MarketCap: "900000000000", Country: "United States"},
		{Symbol: "SMMT", MarketCap: "12000000000", Country: "United States"},
		{Symbol: "NOCAP", MarketCap: "", Country: "United States"},
	}
	got := largestUS(rows, 2)
	if len(got) != 2 || got[0].Symbol != "AAPL" || got[1].Symbol != "BRK-B" || got[1].MarketCapB != 1000 {
		t.Errorf("largestUS = %+v", got)
	}
	if all := largestUS(rows, 10); len(all) != 3 {
		t.Errorf("expected AAPL, BRK-B, SMMT only, got %+v", all)
	}
}

func TestLargestUS_DropsNonCommonSecurities(t *testing.T) {
	names := map[string]bool{ // name → kept
		"AGNC Investment Corp. Common Stock":                                                       true,
		"Liberty Media Corporation Series C Common Stock":                                          true,
		"Sea Limited American Depositary Shares each representing one Class A Ordinary Share":      true,
		"MPLX LP Common Units Representing Limited Partner Interests":                              true,
		"AGNC Investment Corp. Depositary Shares Each Representing a 1/1000th Interest in a Share": false,
		"Strategy Inc 10.00% Series A Perpetual Strife Preferred Stock":                            false,
		"AT&T Inc. 5.350% Global Notes due 2066":                                                   false,
		"Duke Energy Corporation 5.625% Junior Subordinated Debentures due 2078":                   false,
		"Southern Company (The) 2025 Series A Corporate Units":                                     false,
		"BrightSpring Health Services Inc. Tangible Equity Unit":                                   false,
	}
	for name, kept := range names {
		got := largestUS([]screenerRow{{Symbol: "X", Name: name, MarketCap: "1e10", Country: "United States"}}, 10)
		if (len(got) == 1) != kept {
			t.Errorf("%q kept=%v, want %v", name, len(got) == 1, kept)
		}
	}
}
