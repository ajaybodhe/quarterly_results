package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"quarterly_results/internal/recommend"
	"quarterly_results/internal/source/nasdaq"
	"quarterly_results/internal/tracking"
)

// ConsensusStats summarises a BackfillConsensus run.
type ConsensusStats struct {
	Dates     int // distinct announcement dates in range
	Fetched   int // dates requested from Nasdaq this run
	Cached    int // dates served from the on-disk cache
	FetchErrs int
	Matched   int // events that received a consensus surprise
	Unmatched int // events in range with no Nasdaq row (symbol/date mismatch or no estimate)
}

// BackfillConsensus adds Nasdaq's consensus EPS surprise to every event
// announced on or after from (YYYY-MM-DD): one calendar request per distinct
// announcement date, cached as JSON in cacheDir so re-runs and later
// datasets reuse it. The events file is rewritten in place (atomically) with
// features.eps_surprise_pct set and the rule-based prediction recomputed.
// pace is the extra wait between network requests (Nasdaq bans bursts).
func BackfillConsensus(path, from string, fetch func(time.Time) ([]nasdaq.ReportedEarnings, error),
	cacheDir string, pace time.Duration, progress func(string, ...any)) (ConsensusStats, error) {
	var stats ConsensusStats
	evs, err := Load(path)
	if err != nil {
		return stats, err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return stats, err
	}

	var dates []string
	seen := map[string]bool{}
	for _, e := range evs {
		d := e.Features.AnnouncementDate
		if d >= from && !seen[d] {
			seen[d] = true
			dates = append(dates, d)
		}
	}
	sort.Strings(dates)
	stats.Dates = len(dates)

	byDate := map[string]map[string]nasdaq.ReportedEarnings{}
	for i, d := range dates {
		rows, cached, err := calendarDay(d, cacheDir, fetch)
		switch {
		case err != nil:
			stats.FetchErrs++
			progress("[%d/%d] %s: %v", i+1, len(dates), d, err)
		case cached:
			stats.Cached++
		default:
			stats.Fetched++
			if stats.Fetched%50 == 0 {
				progress("[%d/%d] fetched %s (%d rows)", i+1, len(dates), d, len(rows))
			}
			time.Sleep(pace)
		}
		m := make(map[string]nasdaq.ReportedEarnings, len(rows))
		for _, r := range rows {
			m[r.Symbol] = r
		}
		byDate[d] = m
	}

	for i := range evs {
		e := &evs[i]
		d := e.Features.AnnouncementDate
		if d < from {
			continue
		}
		r, ok := byDate[d][nasdaqSymbol(e.Symbol)]
		if !ok || r.SurprisePct == nil {
			stats.Unmatched++
			continue
		}
		stats.Matched++
		s, fc := *r.SurprisePct, r.EPSForecast
		e.Features.EPSSurprisePct = &s
		e.EPSConsensus = &fc
		e.EPSStreet = r.EPSActual
		e.NasdaqTiming = r.Time
		rules := recommend.ComputeDrift(&e.Features, nil)
		e.Rules = tracking.Prediction{Label: rules.Label, Score: rules.Score, Confidence: rules.Confidence}
	}
	return stats, rewrite(path, evs)
}

// nasdaqSymbol maps a Yahoo/SEC class-share ticker to Nasdaq's ("BRK-B" → "BRK/B").
func nasdaqSymbol(sym string) string { return strings.ReplaceAll(strings.ToUpper(sym), "-", "/") }

// calendarDay returns one date's calendar rows from cacheDir, fetching and
// caching them when absent.
func calendarDay(date, cacheDir string, fetch func(time.Time) ([]nasdaq.ReportedEarnings, error)) ([]nasdaq.ReportedEarnings, bool, error) {
	path := filepath.Join(cacheDir, date+".json")
	if b, err := os.ReadFile(path); err == nil {
		var rows []nasdaq.ReportedEarnings
		if json.Unmarshal(b, &rows) == nil {
			return rows, true, nil
		}
	}
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, false, err
	}
	rows, err := fetch(t)
	if err != nil {
		return nil, false, err
	}
	b, err := json.Marshal(rows)
	if err != nil {
		return nil, false, err
	}
	return rows, false, os.WriteFile(path, b, 0o644)
}

// rewrite replaces path with evs, one JSON line each, via a temp file + rename.
func rewrite(path string, evs []Event) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range evs {
		line, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return fmt.Errorf("encode %s: %w", e.EventID, err)
		}
		w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
