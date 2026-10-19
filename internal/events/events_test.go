package events

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/tracking"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// series builds consecutive daily bars starting 2026-01-01 (calendar days are
// fine here: the code counts bars, not weekdays). Volume is 100 except where
// overridden.
func series(closes []float64, vol map[int]float64) []domain.PricePoint {
	out := make([]domain.PricePoint, len(closes))
	for i, c := range closes {
		v := 100.0
		if x, ok := vol[i]; ok {
			v = x
		}
		out[i] = domain.PricePoint{Date: day("2026-01-01").AddDate(0, 0, i), Open: c, Close: c, Volume: v}
	}
	return out
}

func flat(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func approx(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil || math.Abs(*got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestFeaturesAndLabels(t *testing.T) {
	// 30 flat bars at 100, reaction day (index 30, 2026-01-31) closes 110 on
	// 3x volume, then the stock drifts to 121 at +5 and 132 at +20.
	closes := append(flat(30, 100), 110)
	closes = append(closes, 112, 114, 116, 118, 121) // +1…+5
	closes = append(closes, flat(14, 125)...)        // +6…+19
	closes = append(closes, 132)                     // +20
	prices := series(closes, map[int]float64{30: 300, 31: 200, 32: 200, 33: 200, 34: 200, 35: 200})
	etf := series(append(flat(30, 50), append(flat(5, 50), append(flat(15, 50), 55)...)...), nil) // +10% over the 20-bar window

	announce, rx := day("2026-01-30"), day("2026-01-31")
	surprise := 8.0
	f, ok := Features(announce, rx, 100, &surprise, prices, etf, "XLK")
	if !ok {
		t.Fatal("features not computed")
	}
	if f.ReactionRetPct != 10 || f.ReactionDay != "2026-01-31" || *f.EPSSurprisePct != 8 {
		t.Errorf("features = %+v", f)
	}
	approx(t, "abnormal volume", f.AbnormalVolume, 3)
	approx(t, "sector 1M", f.SectorRet1M, 0)

	l := LabelsAt(announce, rx, prices, etf, etf)
	approx(t, "drift5", l.Drift5, 10)          // 110 → 121
	approx(t, "drift20", l.Drift20, 20)        // 110 → 132
	approx(t, "drift5_adj", l.Drift5Adj, 10)   // ETF flat over +5
	approx(t, "drift20_adj", l.Drift20Adj, 10) // ETF +10% over +20
	approx(t, "drift20_mkt", l.Drift20Mkt, 10) // same series used as the market here
	approx(t, "abnormal volume 1-5", l.AbnormalVolume1to5, 2)
	approx(t, "pre7 placebo", l.Pre7Ret, 0)
}

func TestLabels_IncompleteWindowsStayNil(t *testing.T) {
	prices := series(append(flat(30, 100), 110, 111, 112), nil)
	l := LabelsAt(day("2026-01-30"), day("2026-01-31"), prices, nil, nil)
	if l.Drift5 != nil || l.Drift20 != nil || l.Drift5Adj != nil {
		t.Errorf("windows past the data must be nil: %+v", l)
	}
}

func TestLiveFeatures_OnlyWhileFresh(t *testing.T) {
	// AMC release Thu 2026-01-29 (index 28) → reaction Fri 2026-01-30 (index 29).
	prices := series(append(flat(29, 100), 104, 105), nil)
	if f, ok := LiveFeatures("2026-01-29", "amc", nil, prices, nil, ""); !ok || f.ReactionRetPct != 4 || f.ReactionDay != "2026-01-30" {
		t.Fatalf("fresh reaction should produce features, got %+v %v", f, ok)
	}
	// Ten bars later the reaction is stale: no live drift prediction.
	stale := series(append(flat(29, 100), append([]float64{104}, flat(10, 105)...)...), nil)
	if _, ok := LiveFeatures("2026-01-29", "amc", nil, stale, nil, ""); ok {
		t.Error("stale reaction must not produce a live prediction")
	}
	// Not yet reported (reaction day after the last bar).
	early := series(flat(29, 100), nil)
	if _, ok := LiveFeatures("2026-01-29", "amc", nil, early, nil, ""); ok {
		t.Error("future reaction must not produce features")
	}
}

func f64(v float64) *float64 { return &v }

func TestLoadDedupesAndEvaluate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	old := Event{Symbol: "AAA", ExportedAt: day("2026-02-01"),
		Features: domain.DriftFeatures{AnnouncementDate: "2026-01-30", ReactionRetPct: 5, EPSSurprisePct: f64(4)},
		Rules:    tracking.Prediction{Label: "Positive"}}
	newer := old
	newer.ExportedAt = day("2026-03-01")
	newer.Labels = Labels{Drift5Adj: f64(3), Drift20Adj: f64(-2)}
	other := Event{Symbol: "BBB", ExportedAt: day("2026-03-01"),
		Features: domain.DriftFeatures{AnnouncementDate: "2026-02-10", ReactionRetPct: -4},
		Rules:    tracking.Prediction{Label: "Negative"},
		Labels:   Labels{Drift5Adj: f64(-2)}}
	if err := Append(path, []Event{old, newer, other}); err != nil {
		t.Fatal(err)
	}
	evs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Labels.Drift5Adj == nil {
		t.Fatalf("expected the newer AAA export to win: %+v", evs)
	}

	reps := Evaluate(evs)
	d5 := reps[0]
	if d5.N != 2 {
		t.Fatalf("drift5 N = %d", d5.N)
	}
	rules := d5.Predictors[0]
	if rules.Directional != 2 || rules.Hits != 2 {
		t.Errorf("rules on drift5 = %+v", rules)
	}
	if surprise := d5.Predictors[2]; surprise.N != 1 { // BBB has no surprise
		t.Errorf("surprise-only should skip events without a surprise: %+v", surprise)
	}
	if d20 := reps[1]; d20.N != 1 || d20.Predictors[0].Hits != 0 {
		t.Errorf("drift20 = %+v", d20)
	}
	if out := FormatEvaluation(reps, len(evs)); len(out) == 0 {
		t.Error("empty report")
	}
}
