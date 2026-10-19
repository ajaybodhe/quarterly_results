package events

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/source/nasdaq"
)

func TestBackfillConsensus(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ev.jsonl")
	ev := func(sym, date string) Event {
		return Event{EventID: sym + "|" + date, Symbol: sym, ExportedAt: day("2026-10-01"),
			Features: domain.DriftFeatures{AnnouncementDate: date, ReactionRetPct: 3}}
	}
	if err := Append(path, []Event{ev("AAPL", "2026-07-30"), ev("BRK-B", "2026-08-02"), ev("OLD", "2019-01-30"), ev("MISS", "2026-07-30")}); err != nil {
		t.Fatal(err)
	}
	f64 := func(v float64) *float64 { return &v }
	calls := 0
	fetch := func(d time.Time) ([]nasdaq.ReportedEarnings, error) {
		calls++
		switch d.Format("2006-01-02") {
		case "2026-07-30":
			return []nasdaq.ReportedEarnings{{Symbol: "AAPL", Time: "amc", EPSActual: f64(1.57), EPSForecast: 1.43, SurprisePct: f64(9.79)}}, nil
		case "2026-08-02":
			return []nasdaq.ReportedEarnings{{Symbol: "BRK/B", SurprisePct: f64(-2)}}, nil
		}
		return nil, errors.New("unexpected date")
	}
	stats, err := BackfillConsensus(path, "2021-01-01", fetch, filepath.Join(dir, "cache"), 0, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Dates != 2 || stats.Fetched != 2 || stats.Matched != 2 || stats.Unmatched != 1 {
		t.Errorf("stats = %+v", stats)
	}
	evs, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Event{}
	for _, e := range evs {
		got[e.Symbol] = e
	}
	a := got["AAPL"]
	if a.Features.EPSSurprisePct == nil || *a.Features.EPSSurprisePct != 9.79 || *a.EPSConsensus != 1.43 || *a.EPSStreet != 1.57 || a.NasdaqTiming != "amc" {
		t.Errorf("AAPL = %+v", a)
	}
	if got["BRK-B"].Features.EPSSurprisePct == nil {
		t.Error("class shares must match Nasdaq's slash form")
	}
	if got["OLD"].Features.EPSSurprisePct != nil || got["MISS"].Features.EPSSurprisePct != nil {
		t.Error("events before `from` or without a row must stay unset")
	}

	// A second run is served entirely from the cache.
	calls = 0
	stats, err = BackfillConsensus(path, "2021-01-01", fetch, filepath.Join(dir, "cache"), 0, func(string, ...any) {})
	if err != nil || calls != 0 || stats.Cached != 2 {
		t.Errorf("rerun: calls=%d stats=%+v err=%v", calls, stats, err)
	}
}
