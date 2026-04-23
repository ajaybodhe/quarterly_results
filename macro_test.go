package main

import (
	"strings"
	"testing"
	"time"
)

func TestLoadMacroCalendar_AggregatesAllSources(t *testing.T) {
	from, _ := time.Parse("2006-01-02", "2025-01-01")
	to, _ := time.Parse("2006-01-02", "2026-12-31")
	mc := LoadMacroCalendar(from, to)

	// Should include at least one event from each of FOMC / NFP / CPI / PPI tables.
	// Exact count is brittle (tables get updated yearly); check presence by name.
	want := map[string]bool{"FOMC": false, "NFP": false, "CPI": false, "PPI": false, "FOMC Minutes": false}
	for _, e := range mc.events {
		if _, ok := want[e.Name]; ok {
			want[e.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("LoadMacroCalendar missing %q events", name)
		}
	}
}

func TestEventsNear(t *testing.T) {
	mc := &MacroCalendar{events: []MacroEvent{
		{"2026-03-18", "FOMC", "Fed decision", "high"},
		{"2026-03-19", "CPI", "Inflation", "high"},
		{"2026-03-25", "NFP", "Jobs", "high"},
		{"2026-04-15", "PPI", "Producer prices", "medium"},
	}}
	// Window = ±2 days around earnings day 2026-03-20 → FOMC (-2d) and CPI (-1d) qualify; NFP (+5d) does not.
	got := mc.EventsNear("2026-03-20", 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 events within ±2 days of 2026-03-20, got %d: %+v", len(got), got)
	}
	names := map[string]bool{got[0].Name: true, got[1].Name: true}
	if !names["FOMC"] || !names["CPI"] {
		t.Errorf("expected FOMC+CPI, got %+v", got)
	}
}

func TestEventsNear_InvalidDate(t *testing.T) {
	mc := &MacroCalendar{events: []MacroEvent{{"2026-03-18", "FOMC", "x", "high"}}}
	if got := mc.EventsNear("not-a-date", 2); got != nil {
		t.Errorf("expected nil for invalid date, got %+v", got)
	}
}

func TestFormatMacroContext(t *testing.T) {
	evts := []MacroEvent{
		{"2026-03-20", "FOMC", "", "high"},   // same day
		{"2026-03-21", "CPI", "", "high"},    // +1d
		{"2026-03-19", "NFP", "", "high"},    // -1d
		{"2026-03-22", "PPI", "", "medium"},  // +2d
	}
	got := FormatMacroContext(evts, "2026-03-20")
	if !strings.HasPrefix(got, "⚠ ") {
		t.Errorf("expected warning prefix, got %q", got)
	}
	for _, want := range []string{"FOMC same-day", "CPI +1d", "NFP -1d", "PPI +2d"} {
		if !strings.Contains(got, want) {
			t.Errorf("FormatMacroContext missing %q in %q", want, got)
		}
	}
}

func TestFormatMacroContext_Empty(t *testing.T) {
	if got := FormatMacroContext(nil, "2026-03-20"); got != "" {
		t.Errorf("expected empty string for no events, got %q", got)
	}
}

func TestFormatMacroContext_Dedupe(t *testing.T) {
	evts := []MacroEvent{
		{"2026-03-20", "FOMC", "", "high"},
		{"2026-03-20", "FOMC", "", "high"}, // duplicate — should be collapsed
	}
	got := FormatMacroContext(evts, "2026-03-20")
	if strings.Count(got, "FOMC") != 1 {
		t.Errorf("duplicate FOMC entry not deduped: %q", got)
	}
}
