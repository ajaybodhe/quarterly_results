package main

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
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

// fetchBLSEvents scrapes the BLS news release schedule for a given year.
// Returns NFP, CPI, PPI, and Jobless Claims release dates.
// Source: https://www.bls.gov/schedule/YYYY/home.htm
func fetchBLSEvents(year int) ([]MacroEvent, error) {
	url := fmt.Sprintf("https://www.bls.gov/schedule/%d/home.htm", year)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("BLS schedule HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseBLSSchedule(string(body), year), nil
}

// parseBLSSchedule extracts MacroEvents from BLS schedule HTML.
// BLS uses table rows like: <td>01/10/2025</td> ... Employment Situation ...
func parseBLSSchedule(html string, year int) []MacroEvent {
	// Match rows with a date and a known release name.
	dateRe := regexp.MustCompile(`(\d{2}/\d{2}/\d{4})`)
	var events []MacroEvent

	// Identify which report each date belongs to by scanning the surrounding text.
	// BLS schedule tables have sections per report. We find each report section header
	// and collect dates until the next header.
	type reportDef struct {
		pattern string
		name    string
		detail  string
		impact  string
	}
	reports := []reportDef{
		{"Employment Situation", "NFP", "Non-Farm Payrolls", "high"},
		{"Consumer Price Index", "CPI", "Consumer Price Index", "high"},
		{"Producer Price Index", "PPI", "Producer Price Index", "medium"},
		{"Unemployment Insurance Weekly Claims", "Jobless Claims", "Initial Jobless Claims", "medium"},
		{"Real Earnings", "Real Earnings", "Real Earnings", "medium"},
		{"Employment Cost Index", "ECI", "Employment Cost Index", "medium"},
	}

	for _, rpt := range reports {
		// Find the section for this report type.
		idx := strings.Index(html, rpt.pattern)
		if idx < 0 {
			continue
		}
		// Extract the next ~2000 chars after the header (enough for one report's table).
		end := idx + 2000
		if end > len(html) {
			end = len(html)
		}
		section := html[idx:end]

		// Find the next report header to limit scope.
		for _, other := range reports {
			if other.pattern == rpt.pattern {
				continue
			}
			if i := strings.Index(section[50:], other.pattern); i > 0 {
				if i+50 < end-idx {
					section = section[:i+50]
				}
			}
		}

		matches := dateRe.FindAllString(section, -1)
		for _, m := range matches {
			t, err := time.Parse("01/02/2006", m)
			if err != nil || t.Year() != year {
				continue
			}
			events = append(events, MacroEvent{
				Date:   t.Format("2006-01-02"),
				Name:   rpt.name,
				Detail: rpt.detail,
				Impact: rpt.impact,
			})
		}
	}
	return events
}

// MacroCalendar holds all known macro events, indexed by date for fast lookup.
type MacroCalendar struct {
	events []MacroEvent
}

// LoadMacroCalendar fetches BLS events for all years in the date range and combines
// with hardcoded FOMC dates.
func LoadMacroCalendar(from, to time.Time) *MacroCalendar {
	cal := &MacroCalendar{}

	// Add hardcoded FOMC dates.
	cal.events = append(cal.events, fomcDates...)
	cal.events = append(cal.events, fomcMinutesDates...)

	// Fetch BLS schedule for each year covered by the range.
	years := map[int]bool{}
	for d := from; !d.After(to.AddDate(0, 0, 7)); d = d.AddDate(0, 0, 1) {
		years[d.Year()] = true
	}
	for yr := range years {
		evts, err := fetchBLSEvents(yr)
		if err != nil {
			logf("Warning: could not fetch BLS schedule for %d: %v", yr, err)
			continue
		}
		cal.events = append(cal.events, evts...)
	}

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
