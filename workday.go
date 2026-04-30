package main

import "time"

// ── US Holiday Calendar ───────────────────────────────────────────────────────

// USHolidayCalendar implements HolidayCalendar for NYSE/Nasdaq.
type USHolidayCalendar struct{}

func (c USHolidayCalendar) IsWorkingDay(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	return !usMarketHolidays(t.Year())[t.Format("2006-01-02")]
}

func (c USHolidayCalendar) PrevWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func (c USHolidayCalendar) NextWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, 1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// isWorkingDay is a package-level convenience for US (backwards compat).
func isWorkingDay(t time.Time) bool  { return USHolidayCalendar{}.IsWorkingDay(t) }
func prevWorkingDay(t time.Time) time.Time { return USHolidayCalendar{}.PrevWorkingDay(t) }
func nextWorkingDay(t time.Time) time.Time { return USHolidayCalendar{}.NextWorkingDay(t) }

// usMarketHolidays returns the set of NYSE/Nasdaq holiday dates for the given year.
func usMarketHolidays(year int) map[string]bool {
	h := make(map[string]bool)
	add := func(t time.Time) { h[t.Format("2006-01-02")] = true }
	obs := func(t time.Time) time.Time {
		switch t.Weekday() {
		case time.Saturday:
			return t.AddDate(0, 0, -1)
		case time.Sunday:
			return t.AddDate(0, 0, 1)
		}
		return t
	}
	nth := func(month time.Month, wd time.Weekday, n int) time.Time {
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		diff := int(wd) - int(first.Weekday())
		if diff < 0 {
			diff += 7
		}
		return first.AddDate(0, 0, diff+(n-1)*7)
	}
	last := func(month time.Month, wd time.Weekday) time.Time {
		l := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
		diff := int(l.Weekday()) - int(wd)
		if diff < 0 {
			diff += 7
		}
		return l.AddDate(0, 0, -diff)
	}
	add(obs(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)))
	add(nth(time.January, time.Monday, 3))
	add(nth(time.February, time.Monday, 3))
	add(goodFriday(year))
	add(last(time.May, time.Monday))
	if year >= 2022 {
		add(obs(time.Date(year, time.June, 19, 0, 0, 0, 0, time.UTC)))
	}
	add(obs(time.Date(year, time.July, 4, 0, 0, 0, 0, time.UTC)))
	add(nth(time.September, time.Monday, 1))
	add(nth(time.November, time.Thursday, 4))
	add(obs(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC)))
	return h
}

// goodFriday returns Good Friday for the given year (Anonymous Gregorian algorithm).
func goodFriday(year int) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	hh := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - hh - k) % 7
	m := (a + 11*hh + 22*l) / 451
	month := (hh + l - 7*m + 114) / 31
	day := ((hh + l - 7*m + 114) % 31) + 1
	easter := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return easter.AddDate(0, 0, -2)
}

// easterMonday returns Easter Monday.
func easterMonday(year int) time.Time {
	return goodFriday(year).AddDate(0, 0, 3)
}

// ── LSE Holiday Calendar ──────────────────────────────────────────────────────

// LSEHolidayCalendar implements HolidayCalendar for the London Stock Exchange.
type LSEHolidayCalendar struct{}

func (c LSEHolidayCalendar) IsWorkingDay(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	return !lseHolidays(t.Year())[t.Format("2006-01-02")]
}

func (c LSEHolidayCalendar) PrevWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func (c LSEHolidayCalendar) NextWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, 1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// lseHolidays returns LSE bank holiday dates for the given year.
// Source: London Stock Exchange trading calendar.
func lseHolidays(year int) map[string]bool {
	h := make(map[string]bool)
	add := func(t time.Time) { h[t.Format("2006-01-02")] = true }
	obs := func(t time.Time) time.Time {
		switch t.Weekday() {
		case time.Saturday:
			return t.AddDate(0, 0, 2)
		case time.Sunday:
			return t.AddDate(0, 0, 1)
		}
		return t
	}
	nth := func(month time.Month, wd time.Weekday, n int) time.Time {
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		diff := int(wd) - int(first.Weekday())
		if diff < 0 {
			diff += 7
		}
		return first.AddDate(0, 0, diff+(n-1)*7)
	}
	last := func(month time.Month, wd time.Weekday) time.Time {
		l := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
		diff := int(l.Weekday()) - int(wd)
		if diff < 0 {
			diff += 7
		}
		return l.AddDate(0, 0, -diff)
	}

	add(obs(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)))   // New Year's Day
	add(goodFriday(year))                                                 // Good Friday
	add(easterMonday(year))                                               // Easter Monday
	add(nth(time.May, time.Monday, 1))                                    // Early May Bank Holiday
	add(last(time.May, time.Monday))                                      // Spring Bank Holiday
	add(last(time.August, time.Monday))                                   // Summer Bank Holiday
	add(obs(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC))) // Christmas Day
	add(obs(time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC))) // Boxing Day
	return h
}

// ── FSE / XETRA Holiday Calendar ─────────────────────────────────────────────

// FSEHolidayCalendar implements HolidayCalendar for Deutsche Börse / XETRA.
type FSEHolidayCalendar struct{}

func (c FSEHolidayCalendar) IsWorkingDay(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	return !fseHolidays(t.Year())[t.Format("2006-01-02")]
}

func (c FSEHolidayCalendar) PrevWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func (c FSEHolidayCalendar) NextWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, 1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

func fseHolidays(year int) map[string]bool {
	h := make(map[string]bool)
	add := func(t time.Time) { h[t.Format("2006-01-02")] = true }
	add(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC))    // New Year's Day
	add(goodFriday(year))                                              // Good Friday
	add(easterMonday(year))                                            // Easter Monday
	add(time.Date(year, time.May, 1, 0, 0, 0, 0, time.UTC))         // Labour Day
	add(time.Date(year, time.December, 24, 0, 0, 0, 0, time.UTC))   // Christmas Eve
	add(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC))   // Christmas Day
	add(time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC))   // Boxing Day
	add(time.Date(year, time.December, 31, 0, 0, 0, 0, time.UTC))   // New Year's Eve
	return h
}

// ── Euronext Holiday Calendar ─────────────────────────────────────────────────

// EuronextHolidayCalendar implements HolidayCalendar for Euronext (Paris/Amsterdam/Brussels/Lisbon).
type EuronextHolidayCalendar struct{}

func (c EuronextHolidayCalendar) IsWorkingDay(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	return !euronextHolidays(t.Year())[t.Format("2006-01-02")]
}

func (c EuronextHolidayCalendar) PrevWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

func (c EuronextHolidayCalendar) NextWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, 1)
	for !c.IsWorkingDay(d) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

func euronextHolidays(year int) map[string]bool {
	h := make(map[string]bool)
	add := func(t time.Time) { h[t.Format("2006-01-02")] = true }
	add(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC))   // New Year's Day
	add(goodFriday(year))                                             // Good Friday
	add(easterMonday(year))                                           // Easter Monday
	add(time.Date(year, time.May, 1, 0, 0, 0, 0, time.UTC))        // Labour Day
	add(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC))  // Christmas Day
	add(time.Date(year, time.December, 26, 0, 0, 0, 0, time.UTC))  // Boxing Day
	return h
}

// ── Factory ───────────────────────────────────────────────────────────────────

// NewHolidayCalendar returns the appropriate HolidayCalendar for the given exchange.
func NewHolidayCalendar(cfg ExchangeConfig) HolidayCalendar {
	switch cfg.Exchange {
	case ExchangeLSE:
		return LSEHolidayCalendar{}
	case ExchangeFSE:
		return FSEHolidayCalendar{}
	case ExchangeEuronext:
		return EuronextHolidayCalendar{}
	default:
		return USHolidayCalendar{}
	}
}
