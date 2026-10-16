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
