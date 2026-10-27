package tracking

import (
	"errors"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
)

var ny, _ = time.LoadLocation("America/New_York")

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, ny)
	if err != nil {
		panic(err)
	}
	return t
}

// bars builds daily closes dated at midnight UTC, the convention the price
// providers use.
func bars(closes map[string]float64) []domain.PricePoint {
	var out []domain.PricePoint
	for d, c := range closes {
		t, _ := time.Parse("2006-01-02", d)
		out = append(out, domain.PricePoint{Date: t, Open: c, Close: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

func fetcher(m map[string][]domain.PricePoint) PriceFetcher {
	return func(sym string) ([]domain.PricePoint, error) {
		if p, ok := m[sym]; ok {
			return p, nil
		}
		return nil, errors.New("no data")
	}
}

func rec(sym, date, timing, logged, label string) Record {
	return Record{
		LoggedAt:     at(logged),
		Symbol:       sym,
		Exchange:     "US",
		EarningsDate: date,
		EarningsTime: timing,
		Rating:       &Prediction{Label: label, Score: 30, Confidence: 0.6},
	}
}

// ── NewRecord ────────────────────────────────────────────────────────────────

func TestNewRecord_UpcomingWithBothRatings(t *testing.T) {
	r := domain.EarningsResult{
		Symbol: "AAPL", EarningsDate: "2026-11-02", EarningsTime: "amc",
		Recommendation: &domain.Recommendation{Label: "Negative", Score: -20, Confidence: 0.5},
		LLMRating: &domain.LLMRating{
			Rating:     domain.LLMRatingScore{Label: "Positive", Score: 18, Confidence: 0.45},
			SubRatings: domain.LLMSubRatings{Transcript: &domain.LLMSubAnalysis{Score: 0.6, Confidence: 0.5}},
			Metadata:   domain.LLMMetadata{Model: "m", Agents: []string{"transcript", "news"}},
		},
	}
	got, ok := NewRecord(r, exchange.DefaultUSConfig, at("2026-10-16 10:00"))
	if !ok {
		t.Fatal("expected an upcoming report to be logged")
	}
	if got.Rating.Label != "Negative" || got.LLM.Label != "Positive" || got.Exchange != "US" {
		t.Errorf("unexpected record: %+v", got)
	}
	if sub := got.LLM.Sub["transcript"]; sub.Score != 0.6 || len(got.LLM.Sub) != 1 {
		t.Errorf("sub-ratings not copied: %+v", got.LLM.Sub)
	}
}

func TestNewRecord_SkipsPastOrUnrated(t *testing.T) {
	now := at("2026-10-16 10:00")
	rated := &domain.Recommendation{Label: "Neutral"}
	cases := []domain.EarningsResult{
		{Symbol: "A", EarningsDate: "2026-10-15", Recommendation: rated}, // fallback to a past announcement
		{Symbol: "B", EarningsDate: "", Recommendation: rated},
		{Symbol: "C", EarningsDate: "2026-11-01"}, // no rating at all
	}
	for _, r := range cases {
		if _, ok := NewRecord(r, exchange.DefaultUSConfig, now); ok {
			t.Errorf("%s should not be logged", r.Symbol)
		}
	}
	if _, ok := NewRecord(domain.EarningsResult{Symbol: "D", EarningsDate: "2026-10-16", Recommendation: rated}, exchange.DefaultUSConfig, now); !ok {
		t.Error("a report due today should be logged")
	}
}

// ── Log ──────────────────────────────────────────────────────────────────────

func TestLog_AppendLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.jsonl")
	l := NewLog(path)
	for _, r := range []Record{rec("AAPL", "2026-11-02", "amc", "2026-10-16 10:00", "Positive"), rec("MSFT", "2026-10-28", "amc", "2026-10-16 10:00", "Neutral")} {
		if err := l.Append(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Symbol != "AAPL" || !got[0].LoggedAt.Equal(at("2026-10-16 10:00")) || got[1].Rating.Label != "Neutral" {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

// ── Score ────────────────────────────────────────────────────────────────────

func TestScore_AMCUsesAnnounceCloseToNextClose(t *testing.T) {
	prices := bars(map[string]float64{"2026-10-29": 100, "2026-10-30": 110, "2026-11-02": 104.5})
	// Logged at noon on the announcement day: before the 16:00 cutoff.
	rep := Score([]Record{rec("AAPL", "2026-10-30", "amc", "2026-10-30 12:00", "Negative")},
		fetcher(map[string][]domain.PricePoint{"AAPL": prices}), at("2026-11-03 09:00"))
	if rep.Scored != 1 {
		t.Fatalf("scored = %d, report %+v", rep.Scored, rep)
	}
	o := rep.Outcomes[0]
	// Prior close = announce-day close (110), reaction = next trading day (Mon 11-02).
	if o.ReactionDay != "2026-11-02" || math.Abs(o.RetPct-(-5)) > 1e-9 || o.Actual != "Negative" {
		t.Errorf("outcome = %+v", o)
	}
	if st := rep.Predictors[0]; st.Name != "rating" || st.Directional != 1 || st.Hits != 1 || st.HitRate != 1 {
		t.Errorf("rating stats = %+v", st)
	}
}

func TestScore_BMOUsesPriorCloseToSameDayClose(t *testing.T) {
	prices := bars(map[string]float64{"2026-10-29": 100, "2026-10-30": 103})
	rep := Score([]Record{rec("X", "2026-10-30", "bmo", "2026-10-29 20:00", "Positive")},
		fetcher(map[string][]domain.PricePoint{"X": prices}), at("2026-10-31 09:00"))
	if rep.Scored != 1 || rep.Outcomes[0].ReactionDay != "2026-10-30" || math.Abs(rep.Outcomes[0].RetPct-3) > 1e-9 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestScore_PendingAndLateAndMissingPrices(t *testing.T) {
	prices := bars(map[string]float64{"2026-10-29": 100, "2026-10-30": 101})
	records := []Record{
		rec("FUT", "2026-11-20", "amc", "2026-10-16 10:00", "Positive"),  // not reported yet
		rec("LATE", "2026-10-30", "amc", "2026-10-30 17:00", "Positive"), // logged after the AMC release
		rec("NOPX", "2026-10-29", "amc", "2026-10-28 10:00", "Positive"), // fetch fails
		rec("WAIT", "2026-10-30", "amc", "2026-10-30 10:00", "Positive"), // next-day bar not in yet
	}
	rep := Score(records, fetcher(map[string][]domain.PricePoint{"LATE": prices, "WAIT": prices}), at("2026-10-31 09:00"))
	if rep.Scored != 0 || rep.Pending != 2 || rep.NoPrediction != 1 || rep.NoPrices != 1 {
		t.Errorf("report counts = %+v", rep)
	}
}

func TestScore_UsesLatestPreReleasePredictionAndLatestDate(t *testing.T) {
	prices := bars(map[string]float64{"2026-11-04": 100, "2026-11-05": 100, "2026-11-06": 95})
	records := []Record{
		rec("AAPL", "2026-10-30", "amc", "2026-10-10 10:00", "Positive"), // early estimate of the date
		rec("AAPL", "2026-11-05", "amc", "2026-11-01 10:00", "Negative"), // date confirmed later
		rec("AAPL", "2026-11-05", "amc", "2026-11-05 18:00", "Positive"), // after the release: ignored
	}
	rep := Score(records, fetcher(map[string][]domain.PricePoint{"AAPL": prices}), at("2026-11-07 09:00"))
	if rep.Scored != 1 {
		t.Fatalf("expected one report for the moved date, got %+v", rep)
	}
	o := rep.Outcomes[0]
	if o.EarningsDate != "2026-11-05" || o.Rating.Label != "Negative" || !o.PredictedAt.Equal(at("2026-11-01 10:00")) {
		t.Errorf("outcome = %+v", o)
	}
}

func TestScore_LLMAndSubAgentStats(t *testing.T) {
	prices := bars(map[string]float64{"2026-10-29": 100, "2026-10-30": 100, "2026-11-02": 104})
	r := rec("AAPL", "2026-10-30", "amc", "2026-10-30 09:00", "Neutral")
	r.LLM = &LLMPrediction{
		Prediction: Prediction{Label: "Positive", Score: 20, Confidence: 0.5},
		Sub: map[string]SubPrediction{
			"transcript": {Score: 0.8, Confidence: 0.5}, // 40 → Positive
			"news":       {Score: 0.9, Confidence: 0.3}, // below confidence floor → Neutral
		},
	}
	rep := Score([]Record{r}, fetcher(map[string][]domain.PricePoint{"AAPL": prices}), at("2026-11-03 09:00"))
	got := map[string]PredictorStats{}
	for _, s := range rep.Predictors {
		got[s.Name] = s
	}
	if got["rating"].Directional != 0 || got["llm"].Hits != 1 || got["llm.transcript"].Hits != 1 || got["llm.news"].Directional != 0 {
		t.Errorf("predictor stats = %+v", rep.Predictors)
	}
	if _, ok := got["rating (LLM-covered stocks)"]; !ok {
		t.Error("expected like-for-like rating row")
	}
	if out := FormatReport(rep, "p.jsonl"); !strings.Contains(out, "llm.transcript") || !strings.Contains(out, "Baseline") {
		t.Errorf("unexpected report text:\n%s", out)
	}
}

func TestSummarize_WilsonInterval(t *testing.T) {
	calls := []Call{{"Positive", 2}, {"Positive", 3}, {"Negative", 1}, {"Neutral", 5}}
	st := Summarize("x", calls)
	if st.N != 4 || st.Directional != 3 || st.Hits != 2 {
		t.Fatalf("stats = %+v", st)
	}
	// 2/3 with n=3: Wilson 95% ≈ [0.21, 0.94].
	if math.Abs(st.HitRateLow-0.208) > 0.01 || math.Abs(st.HitRateHigh-0.939) > 0.01 {
		t.Errorf("Wilson interval = [%.3f, %.3f]", st.HitRateLow, st.HitRateHigh)
	}
	if Baseline([]string{"Positive", "Positive", "Negative"}) != 2.0/3 {
		t.Error("baseline should be the most common label's share")
	}
}

// ── drift horizon ────────────────────────────────────────────────────────────

// dailyBars builds n consecutive daily bars from start with closes from f(i).
func dailyBars(start string, n int, f func(i int) float64) []domain.PricePoint {
	t0, _ := time.Parse("2006-01-02", start)
	out := make([]domain.PricePoint, n)
	for i := range out {
		out[i] = domain.PricePoint{Date: t0.AddDate(0, 0, i), Close: f(i)}
	}
	return out
}

func TestNewDriftRecord(t *testing.T) {
	r := domain.EarningsResult{
		Symbol: "AAPL", EarningsTime: "amc",
		DriftFeatures:       &domain.DriftFeatures{AnnouncementDate: "2026-10-29", ReactionDay: "2026-10-30", SectorETF: "XLK"},
		DriftRecommendation: &domain.Recommendation{Label: "Positive", Score: 30, Confidence: 0.7},
		LLMRating:           &domain.LLMRating{Rating: domain.LLMRatingScore{Label: "Negative"}, Metadata: domain.LLMMetadata{PromptVersion: "v2"}},
	}
	rec, ok := NewDriftRecord(r, exchange.DefaultUSConfig, at("2026-10-30 17:00"))
	if !ok || rec.Horizon != HorizonDrift || rec.ReactionDay != "2026-10-30" || rec.SectorETF != "XLK" ||
		rec.Rating.Label != "Positive" || rec.LLM.Label != "Negative" || rec.LLM.PromptVersion != "v2" {
		t.Errorf("drift record = %+v", rec)
	}
	if _, ok := NewDriftRecord(domain.EarningsResult{Symbol: "X"}, exchange.DefaultUSConfig, at("2026-10-30 17:00")); ok {
		t.Error("no drift prediction → no record")
	}
}

func TestScore_DriftUsesEarliestRecordAndSectorAdjusts(t *testing.T) {
	// Stock +1/bar from 100; ETF flat except +2% by bar 5 from the start.
	stock := dailyBars("2026-10-28", 40, func(i int) float64 { return 100 + float64(i) })
	etf := dailyBars("2026-10-28", 40, func(i int) float64 {
		if i >= 7 {
			return 51
		}
		return 50
	})
	early := Record{LoggedAt: at("2026-10-30 17:00"), Horizon: HorizonDrift, Symbol: "AAPL", Exchange: "US",
		ReactionDay: "2026-10-30", SectorETF: "XLK", Rating: &Prediction{Label: "Positive"}}
	late := early
	late.LoggedAt = at("2026-11-03 10:00")
	late.Rating = &Prediction{Label: "Negative"}
	rep := Score([]Record{late, early}, fetcher(map[string][]domain.PricePoint{"AAPL": stock, "XLK": etf}), at("2026-12-31 10:00"))

	if len(rep.DriftOutcomes) != 2 || rep.DriftPending != 0 {
		t.Fatalf("drift outcomes = %+v pending %d", rep.DriftOutcomes, rep.DriftPending)
	}
	d5 := rep.DriftOutcomes[0]
	// Earliest record counts; window starts at the 10-30 close (bar 2 = 102) → bar 7 = 107.
	if d5.Bars != 5 || d5.Start != "2026-10-30" || d5.Rating.Label != "Positive" {
		t.Errorf("drift5 outcome = %+v", d5)
	}
	if math.Abs(d5.RetPct-(5.0/102*100)) > 1e-9 || !d5.Adjusted || math.Abs(d5.AdjRetPct-(d5.RetPct-2)) > 1e-9 {
		t.Errorf("drift5 returns = %+v", d5)
	}
	if got := rep.DriftPredictors[0]; got.Name != "drift5: rules" || got.Hits != 1 {
		t.Errorf("drift5 stats = %+v", got)
	}
	if !strings.Contains(FormatReport(rep, "p.jsonl"), "drift20: rules") {
		t.Error("report should include the drift table")
	}
}

func TestScore_DriftPendingUntilWindowCompletes(t *testing.T) {
	stock := dailyBars("2026-10-28", 8, func(i int) float64 { return 100 })
	r := Record{LoggedAt: at("2026-10-30 17:00"), Horizon: HorizonDrift, Symbol: "AAPL", Exchange: "US",
		ReactionDay: "2026-10-30", Rating: &Prediction{Label: "Positive"}}
	rep := Score([]Record{r}, fetcher(map[string][]domain.PricePoint{"AAPL": stock}), at("2026-11-05 10:00"))
	// 5 bars after the start exist (bar 2 → bar 7) but not 20.
	if len(rep.DriftOutcomes) != 1 || rep.DriftPending != 1 || rep.DriftOutcomes[0].Adjusted {
		t.Errorf("outcomes %+v pending %d", rep.DriftOutcomes, rep.DriftPending)
	}
}

// ── post-release signals ─────────────────────────────────────────────────────

func postReleaseResult() domain.EarningsResult {
	hit := 0.54
	return domain.EarningsResult{
		Symbol: "PGR", EarningsTime: "bmo",
		PostRelease: &domain.PostRelease{
			Symbol: "PGR", AnnouncementDate: "2026-10-14", ReactionDay: "2026-10-14",
			Signal1: &domain.ReleaseSignal{SurprisePct: 3.1, PUp: 0.40, N: 2424, Label: "Negative", SurpriseSource: "web (Zacks)"},
			Signal2: &domain.OpenSignal{PredictedPct: 1.2, Decile: 10, Lean: "LONG", HistMeanPct: 1.06, HistHit: &hit},
		},
	}
}

func TestNewPostReleaseRecords(t *testing.T) {
	now := at("2026-10-14 09:45")
	rel, ok := NewReleaseRecord(postReleaseResult(), exchange.DefaultUSConfig, now)
	if !ok || rel.Horizon != HorizonRelease || rel.Rating.Label != "Negative" || rel.EarningsDate != "2026-10-14" || rel.Source != "web (Zacks)" {
		t.Errorf("release record = %+v", rel)
	}
	in, ok := NewIntradayRecord(postReleaseResult(), exchange.DefaultUSConfig, now)
	if !ok || in.Horizon != HorizonIntraday || in.Rating.Label != "Positive" || in.Decile != 10 || in.Source != "web (Zacks)" {
		t.Errorf("intraday record = %+v", in)
	}
	noOpen := postReleaseResult()
	noOpen.PostRelease.Signal2 = nil
	if _, ok := NewIntradayRecord(noOpen, exchange.DefaultUSConfig, now); ok {
		t.Error("no Signal 2 → no intraday record")
	}
}

func TestScore_PostReleaseSignals(t *testing.T) {
	day := func(d string, o, c float64) domain.PricePoint {
		t, _ := time.Parse("2006-01-02", d)
		return domain.PricePoint{Date: t, Open: o, Close: c}
	}
	pgr := []domain.PricePoint{day("2026-10-13", 200, 200), day("2026-10-14", 190, 195)} // −2.5% reaction, +2.63% open→close
	spy := []domain.PricePoint{day("2026-10-13", 500, 500), day("2026-10-14", 500, 505)} // +1% open→close
	now := at("2026-10-14 09:45")
	rel, _ := NewReleaseRecord(postReleaseResult(), exchange.DefaultUSConfig, now)
	in, _ := NewIntradayRecord(postReleaseResult(), exchange.DefaultUSConfig, now)
	late, _ := NewIntradayRecord(postReleaseResult(), exchange.DefaultUSConfig, at("2026-10-14 17:00"))
	late.Symbol = "LATE"

	rep := Score([]Record{rel, in, late}, fetcher(map[string][]domain.PricePoint{"PGR": pgr, "LATE": pgr, "SPY": spy}), at("2026-10-15 09:00"))
	if len(rep.PostReleaseOutcomes) != 2 || rep.PostReleaseLate != 1 {
		t.Fatalf("outcomes %+v late %d pending %d", rep.PostReleaseOutcomes, rep.PostReleaseLate, rep.PostReleasePending)
	}
	got := map[string]PostReleaseOutcome{}
	for _, o := range rep.PostReleaseOutcomes {
		got[o.Horizon] = o
	}
	if math.Abs(got[HorizonRelease].RetPct-(-2.5)) > 1e-9 {
		t.Errorf("signal 1 reaction = %v", got[HorizonRelease].RetPct)
	}
	if want := (195.0/190-1)*100 - 1; math.Abs(got[HorizonIntraday].RetPct-want) > 1e-9 {
		t.Errorf("signal 2 open→close−SPY = %v, want %v", got[HorizonIntraday].RetPct, want)
	}
	if s := rep.PostReleasePredictors; len(s) != 2 || s[0].Hits != 1 || s[1].Hits != 1 {
		t.Errorf("post-release stats = %+v", s)
	}
	if !strings.Contains(FormatReport(rep, "p.jsonl"), "signal 2: open→close−SPY") {
		t.Error("report should include the post-release table")
	}
}
