package main

import (
	"fmt"
	"strings"
	"time"
)

// MacroEvent is a scheduled high-impact economic event.
type MacroEvent struct {
	Date   string // YYYY-MM-DD
	Name   string // e.g. "FOMC", "CPI", "NFP"
	Detail string // short description
	Impact string // "high" or "medium"
}

// fomcDates are the FOMC decision release dates (day 2 of each meeting, when statement is released).
// Source: https://www.federalreserve.gov/monetarypolicy/fomccalendars.htm
var fomcDates = []MacroEvent{
	// 2025
	{"2025-01-29", "FOMC", "Fed rate decision", "high"},
	{"2025-03-19", "FOMC", "Fed rate decision", "high"},
	{"2025-05-07", "FOMC", "Fed rate decision", "high"},
	{"2025-06-18", "FOMC", "Fed rate decision", "high"},
	{"2025-07-30", "FOMC", "Fed rate decision", "high"},
	{"2025-09-17", "FOMC", "Fed rate decision", "high"},
	{"2025-10-29", "FOMC", "Fed rate decision", "high"},
	{"2025-12-10", "FOMC", "Fed rate decision", "high"},
	// 2026
	{"2026-01-28", "FOMC", "Fed rate decision", "high"},
	{"2026-03-18", "FOMC", "Fed rate decision", "high"},
	{"2026-04-29", "FOMC", "Fed rate decision", "high"},
	{"2026-06-17", "FOMC", "Fed rate decision", "high"},
	{"2026-07-29", "FOMC", "Fed rate decision", "high"},
	{"2026-09-16", "FOMC", "Fed rate decision", "high"},
	{"2026-10-28", "FOMC", "Fed rate decision", "high"},
	{"2026-12-09", "FOMC", "Fed rate decision", "high"},
}

// fomcMinutesDates are the FOMC meeting minutes release dates (3 weeks after meeting).
var fomcMinutesDates = []MacroEvent{
	// 2025
	{"2025-02-19", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-04-09", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-05-28", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-07-09", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-08-20", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-10-08", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-11-19", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2025-12-31", "FOMC Minutes", "Fed meeting minutes", "medium"},
	// 2026
	{"2026-02-18", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2026-04-08", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2026-05-20", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2026-07-08", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2026-08-19", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2026-10-07", "FOMC Minutes", "Fed meeting minutes", "medium"},
	{"2026-11-18", "FOMC Minutes", "Fed meeting minutes", "medium"},
}

// blsNFPDates are the BLS Employment Situation (Non-Farm Payrolls) release dates.
// BLS website is behind Akamai WAF and blocks programmatic access, so dates are hardcoded.
// Source: https://www.bls.gov/schedule/YYYY/home.htm  (update annually)
var blsNFPDates = []MacroEvent{
	// 2025
	{"2025-01-10", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-02-07", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-03-07", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-04-04", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-05-02", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-06-06", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-07-03", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-08-01", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-09-05", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-10-03", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-11-07", "NFP", "Non-Farm Payrolls", "high"},
	{"2025-12-05", "NFP", "Non-Farm Payrolls", "high"},
	// 2026
	{"2026-01-09", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-02-06", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-03-06", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-04-03", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-05-08", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-06-05", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-07-02", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-08-07", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-09-04", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-10-02", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-11-06", "NFP", "Non-Farm Payrolls", "high"},
	{"2026-12-04", "NFP", "Non-Farm Payrolls", "high"},
}

// blsCPIDates are the BLS Consumer Price Index release dates.
var blsCPIDates = []MacroEvent{
	// 2025
	{"2025-01-15", "CPI", "Consumer Price Index", "high"},
	{"2025-02-12", "CPI", "Consumer Price Index", "high"},
	{"2025-03-12", "CPI", "Consumer Price Index", "high"},
	{"2025-04-10", "CPI", "Consumer Price Index", "high"},
	{"2025-05-13", "CPI", "Consumer Price Index", "high"},
	{"2025-06-11", "CPI", "Consumer Price Index", "high"},
	{"2025-07-11", "CPI", "Consumer Price Index", "high"},
	{"2025-08-12", "CPI", "Consumer Price Index", "high"},
	{"2025-09-10", "CPI", "Consumer Price Index", "high"},
	{"2025-10-15", "CPI", "Consumer Price Index", "high"},
	{"2025-11-12", "CPI", "Consumer Price Index", "high"},
	{"2025-12-10", "CPI", "Consumer Price Index", "high"},
	// 2026
	{"2026-01-15", "CPI", "Consumer Price Index", "high"},
	{"2026-02-11", "CPI", "Consumer Price Index", "high"},
	{"2026-03-11", "CPI", "Consumer Price Index", "high"},
	{"2026-04-10", "CPI", "Consumer Price Index", "high"},
	{"2026-05-12", "CPI", "Consumer Price Index", "high"},
	{"2026-06-10", "CPI", "Consumer Price Index", "high"},
	{"2026-07-14", "CPI", "Consumer Price Index", "high"},
	{"2026-08-12", "CPI", "Consumer Price Index", "high"},
	{"2026-09-10", "CPI", "Consumer Price Index", "high"},
	{"2026-10-13", "CPI", "Consumer Price Index", "high"},
	{"2026-11-12", "CPI", "Consumer Price Index", "high"},
	{"2026-12-10", "CPI", "Consumer Price Index", "high"},
}

// blsPPIDates are the BLS Producer Price Index release dates.
var blsPPIDates = []MacroEvent{
	// 2025
	{"2025-01-14", "PPI", "Producer Price Index", "medium"},
	{"2025-02-13", "PPI", "Producer Price Index", "medium"},
	{"2025-03-13", "PPI", "Producer Price Index", "medium"},
	{"2025-04-11", "PPI", "Producer Price Index", "medium"},
	{"2025-05-15", "PPI", "Producer Price Index", "medium"},
	{"2025-06-12", "PPI", "Producer Price Index", "medium"},
	{"2025-07-15", "PPI", "Producer Price Index", "medium"},
	{"2025-08-14", "PPI", "Producer Price Index", "medium"},
	{"2025-09-11", "PPI", "Producer Price Index", "medium"},
	{"2025-10-16", "PPI", "Producer Price Index", "medium"},
	{"2025-11-13", "PPI", "Producer Price Index", "medium"},
	{"2025-12-11", "PPI", "Producer Price Index", "medium"},
	// 2026
	{"2026-01-16", "PPI", "Producer Price Index", "medium"},
	{"2026-02-12", "PPI", "Producer Price Index", "medium"},
	{"2026-03-12", "PPI", "Producer Price Index", "medium"},
	{"2026-04-14", "PPI", "Producer Price Index", "medium"},
	{"2026-05-13", "PPI", "Producer Price Index", "medium"},
	{"2026-06-11", "PPI", "Producer Price Index", "medium"},
	{"2026-07-15", "PPI", "Producer Price Index", "medium"},
	{"2026-08-13", "PPI", "Producer Price Index", "medium"},
	{"2026-09-11", "PPI", "Producer Price Index", "medium"},
	{"2026-10-14", "PPI", "Producer Price Index", "medium"},
	{"2026-11-13", "PPI", "Producer Price Index", "medium"},
	{"2026-12-11", "PPI", "Producer Price Index", "medium"},
}

// MacroCalendar holds all known macro events, indexed by date for fast lookup.
type MacroCalendar struct {
	events []MacroEvent
}

// LoadMacroCalendar builds the macro event calendar from hardcoded FOMC and BLS dates.
// BLS dates (NFP, CPI, PPI) are hardcoded because bls.gov blocks programmatic access
// via Akamai WAF. Update the blsNFP/CPI/PPI slices in macro.go each January.
func LoadMacroCalendar(from, to time.Time) *MacroCalendar {
	cal := &MacroCalendar{}
	cal.events = append(cal.events, fomcDates...)
	cal.events = append(cal.events, fomcMinutesDates...)
	cal.events = append(cal.events, blsNFPDates...)
	cal.events = append(cal.events, blsCPIDates...)
	cal.events = append(cal.events, blsPPIDates...)
	return cal
}

// EventsNear returns macro events within windowDays of the given date string (YYYY-MM-DD).
func (mc *MacroCalendar) EventsNear(dateStr string, windowDays int) []MacroEvent {
	target, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return nil
	}
	var out []MacroEvent
	for _, e := range mc.events {
		t, err := time.Parse("2006-01-02", e.Date)
		if err != nil {
			continue
		}
		diff := int(t.Sub(target).Hours() / 24)
		if diff < 0 {
			diff = -diff
		}
		if diff <= windowDays {
			out = append(out, e)
		}
	}
	return out
}

// FormatMacroContext returns a compact string for display in the earnings table.
// e.g. "⚠ FOMC same-day | CPI -1d"
func FormatMacroContext(events []MacroEvent, earningsDate string) string {
	if len(events) == 0 {
		return ""
	}
	target, _ := time.Parse("2006-01-02", earningsDate)
	var parts []string
	seen := map[string]bool{}
	for _, e := range events {
		key := e.Name + e.Date
		if seen[key] {
			continue
		}
		seen[key] = true

		t, _ := time.Parse("2006-01-02", e.Date)
		diff := int(t.Sub(target).Hours() / 24)
		var when string
		switch diff {
		case 0:
			when = "same-day"
		case 1:
			when = "+1d"
		case -1:
			when = "-1d"
		default:
			when = fmt.Sprintf("%+dd", diff)
		}
		parts = append(parts, fmt.Sprintf("%s %s", e.Name, when))
	}
	if len(parts) == 0 {
		return ""
	}
	return "⚠ " + strings.Join(parts, " | ")
}
