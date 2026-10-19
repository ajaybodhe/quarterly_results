package events

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/source/sec"
	"quarterly_results/internal/source/yahoo"
)

func quarters(eps ...float64) []domain.QuarterActual {
	// Calendar quarter ends from 2023-03-31 onward.
	ends := []string{"03-31", "06-30", "09-30", "12-31"}
	out := make([]domain.QuarterActual, len(eps))
	for i, e := range eps {
		y := 2023 + i/4
		out[i] = domain.QuarterActual{Period: time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006") + "-" + ends[i%4], EPS: e}
	}
	return out
}

func TestSUE(t *testing.T) {
	// Year-over-year differences for quarters 4..7 are 1, 2, 1, 2 (σ ≈ 0.577);
	// quarter 8's is 4 − 2 = 2 (its year-ago quarter is quarter 4).
	h := quarters(1, 1, 1, 1, 2, 3, 2, 3, 4)
	got := SUE(h, nil, 8)
	if got == nil {
		t.Fatal("SUE should be defined with 4 earlier differences")
	}
	want := 2 / math.Sqrt(1.0/3) // sample σ of {2,1,2,1} = sqrt(1/3)
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("SUE = %v, want %v", *got, want)
	}
	if SUE(h, nil, 7) != nil {
		t.Error("only 3 earlier differences → nil")
	}
	if SUE(quarters(1, 1, 1, 1, 2, 2, 2, 2, 3), nil, 8) != nil {
		t.Error("σ = 0 → nil")
	}
}

func TestMatchRelease(t *testing.T) {
	q := domain.QuarterActual{Period: "2026-06-30", FilingDate: "2026-08-01"}
	rels := []sec.EarningsRelease{
		{FilingDate: "2026-07-05", Accession: "too-early"},
		{FilingDate: "2026-07-23", Accession: "release"},
		{FilingDate: "2026-08-05", Accession: "after-10q"},
	}
	if r, ok := matchRelease(q, rels, map[string]bool{}); !ok || r.Accession != "release" {
		t.Errorf("match = %+v %v", r, ok)
	}
	if _, ok := matchRelease(q, rels, map[string]bool{"release": true}); ok {
		t.Error("a release already used by another quarter must not match again")
	}
	// A later restating filing date must not stretch the window into the
	// next quarter's release (AAPL June-2020 quarter vs the October release).
	restated := domain.QuarterActual{Period: "2026-06-30", FilingDate: "2026-10-30"}
	rels2 := append(rels, sec.EarningsRelease{FilingDate: "2026-10-29", Accession: "next-quarter"})
	if r, ok := matchRelease(restated, rels2, map[string]bool{}); !ok || r.Accession != "after-10q" {
		t.Errorf("an implausible 10-Q date falls back to +80 days; got %+v", r)
	}
}

func TestReleaseTiming(t *testing.T) {
	cases := []struct {
		accepted, filed, want string
	}{
		{"2026-07-31T00:30:28Z", "2026-07-30", "amc"}, // Apple: filed 20:30 ET
		{"2026-10-13T14:30:38Z", "2026-10-13", "bmo"}, // JPM: filed 10:30 ET
		{"2026-01-13T16:41:09Z", "2026-01-13", "bmo"}, // 11:41 EST
		{"2026-04-30T20:31:00Z", "2026-04-30", "amc"}, // 16:31 EDT
	}
	for _, c := range cases {
		at, _ := time.Parse(time.RFC3339, c.accepted)
		if got := ReleaseTiming(sec.EarningsRelease{Accepted: at, FilingDate: c.filed}); got != c.want {
			t.Errorf("ReleaseTiming(%s) = %s, want %s", c.accepted, got, c.want)
		}
	}
	if ReleaseTiming(sec.EarningsRelease{FilingDate: "2026-01-01"}) != "" {
		t.Error("unknown acceptance time → \"\"")
	}
}

func TestWithEPS(t *testing.T) {
	h := []domain.QuarterActual{{Period: "a", EPS: 1}, {Period: "b", Revenue: 5}, {Period: "c", EPS: -0.2}}
	if got := withEPS(h); len(got) != 2 || got[1].Period != "c" {
		t.Errorf("withEPS = %+v", got)
	}
}

func TestUniverseRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "u.csv")
	in := []nasdaq.ScreenerStock{{Symbol: "AAPL", Name: "Apple, Inc.", MarketCapB: 3000}, {Symbol: "BRK-B", MarketCapB: 1000}}
	if err := WriteUniverse(path, in); err != nil {
		t.Fatal(err)
	}
	got, err := ReadUniverse(path)
	if err != nil || len(got) != 2 || got[0] != "AAPL" || got[1] != "BRK-B" {
		t.Errorf("ReadUniverse = %v %v", got, err)
	}
}

func TestReadDone_RetriesFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.done")
	os.WriteFile(path, []byte("AAPL\tok\t320193\t40\t\nGOOG\tduplicate\t1652044\t0\t\nXYZ\tfailed\t0\t0\tHTTP 500\n"), 0o644)
	syms, ciks, err := readDone(path)
	if err != nil {
		t.Fatal(err)
	}
	if !syms["AAPL"] || !syms["GOOG"] || syms["XYZ"] {
		t.Errorf("done symbols = %v (failed ones must be retried)", syms)
	}
	if !ciks[320193] || ciks[1652044] {
		t.Errorf("done CIKs = %v (only built companies reserve their CIK)", ciks)
	}
}

func TestSplitAdjust(t *testing.T) {
	split := []yahoo.Split{{Date: time.Date(2020, 8, 31, 0, 0, 0, 0, time.UTC), Ratio: 4}}
	h := []domain.QuarterActual{
		{Period: "2019-03-30", FilingDate: "2019-05-01", EPS: 2.46}, // pre-split filing → ÷4
		{Period: "2020-03-28", FilingDate: "2020-05-01", EPS: 2.55}, // pre-split filing → ÷4, >100d before the split
		{Period: "2020-06-27", FilingDate: "2020-10-30", EPS: 0.65}, // restated in a post-split filing → as is; split window
		{Period: "2021-06-26", FilingDate: "2021-07-28", EPS: 1.30}, // within a year after → split window
		{Period: "2021-12-25", FilingDate: "2022-01-28", EPS: 2.10}, // post-split, clean
	}
	adj, tainted := splitAdjust(h, split)
	if math.Abs(adj[0].EPS-0.615) > 1e-9 || math.Abs(adj[1].EPS-0.6375) > 1e-9 || adj[2].EPS != 0.65 || adj[4].EPS != 2.10 {
		t.Errorf("adjusted EPS = %v %v %v %v", adj[0].EPS, adj[1].EPS, adj[2].EPS, adj[4].EPS)
	}
	if tainted[0] || tainted[1] || !tainted[2] || !tainted[3] || tainted[4] {
		t.Errorf("tainted = %v", tainted)
	}
	if h[0].EPS != 2.46 {
		t.Error("splitAdjust must not modify its input")
	}
}

func TestSUE_SkipsTaintedQuarters(t *testing.T) {
	h := quarters(1, 1, 1, 1, 2, 3, 2, 3, 4)
	tainted := make([]bool, len(h))
	tainted[4] = true // quarter 8's year-ago quarter
	if SUE(h, tainted, 8) != nil {
		t.Error("a tainted year-ago quarter makes SUE undefined")
	}
}
