package main

import "time"

// isWorkingDay returns true if t is a NYSE/NASDAQ trading day
// (not a weekend and not a US market holiday).
func isWorkingDay(t time.Time) bool {
	if t.Weekday() == time.Saturday || t.Weekday() == time.Sunday {
		return false
	}
	holidays := usMarketHolidays(t.Year())
	return !holidays[t.Format("2006-01-02")]
}

// prevWorkingDay returns the most recent working day strictly before t.
func prevWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, -1)
	for !isWorkingDay(d) {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// nextWorkingDay returns the first working day strictly after t.
func nextWorkingDay(t time.Time) time.Time {
	d := t.AddDate(0, 0, 1)
	for !isWorkingDay(d) {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// usMarketHolidays returns the set of NYSE/NASDAQ holiday dates for the given year.
// Holidays that fall on a Saturday are observed on Friday; on Sunday, on Monday.
func usMarketHolidays(year int) map[string]bool {
	h := make(map[string]bool)

	addHoliday := func(t time.Time) {
		h[t.Format("2006-01-02")] = true
	}
	observed := func(t time.Time) time.Time {
		switch t.Weekday() {
		case time.Saturday:
			return t.AddDate(0, 0, -1)
		case time.Sunday:
			return t.AddDate(0, 0, 1)
		}
		return t
	}
	// nth occurrence of weekday in month
	nthWeekday := func(month time.Month, wd time.Weekday, n int) time.Time {
		first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
		diff := int(wd) - int(first.Weekday())
		if diff < 0 {
			diff += 7
		}
		return first.AddDate(0, 0, diff+(n-1)*7)
	}
	// last occurrence of weekday in month
	lastWeekday := func(month time.Month, wd time.Weekday) time.Time {
		last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
		diff := int(last.Weekday()) - int(wd)
		if diff < 0 {
			diff += 7
		}
		return last.AddDate(0, 0, -diff)
	}

	// New Year's Day
	addHoliday(observed(time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC)))
	// Martin Luther King Jr. Day – 3rd Monday of January
	addHoliday(nthWeekday(time.January, time.Monday, 3))
	// Presidents' Day – 3rd Monday of February
	addHoliday(nthWeekday(time.February, time.Monday, 3))
	// Good Friday
	addHoliday(goodFriday(year))
	// Memorial Day – last Monday of May
	addHoliday(lastWeekday(time.May, time.Monday))
	// Juneteenth National Independence Day (observed from 2022)
	if year >= 2022 {
		addHoliday(observed(time.Date(year, time.June, 19, 0, 0, 0, 0, time.UTC)))
	}
	// Independence Day
	addHoliday(observed(time.Date(year, time.July, 4, 0, 0, 0, 0, time.UTC)))
	// Labor Day – 1st Monday of September
	addHoliday(nthWeekday(time.September, time.Monday, 1))
	// Thanksgiving – 4th Thursday of November
	addHoliday(nthWeekday(time.November, time.Thursday, 4))
	// Christmas Day
	addHoliday(observed(time.Date(year, time.December, 25, 0, 0, 0, 0, time.UTC)))

	return h
}

// goodFriday returns Good Friday for the given year using the Anonymous Gregorian algorithm.
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
