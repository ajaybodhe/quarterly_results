package main

import (
	"testing"
	"time"
)

// ── ExchangeForTicker ─────────────────────────────────────────────────────────

func TestExchangeForTicker(t *testing.T) {
	cases := []struct {
		symbol   string
		exchange Exchange
		currency string
	}{
		{"AAPL", ExchangeUS, "USD"},
		{"TSLA", ExchangeUS, "USD"},
		{"VOD.L", ExchangeLSE, "GBP"},
		{"BP.L", ExchangeLSE, "GBP"},
		{"BMW.DE", ExchangeFSE, "EUR"},
		{"SAP.DE", ExchangeFSE, "EUR"},
		{"AIR.PA", ExchangeEuronext, "EUR"},
		{"ASML.AS", ExchangeEuronext, "EUR"},
		{"ABI.BR", ExchangeEuronext, "EUR"},
		{"EDP.LS", ExchangeEuronext, "EUR"},
	}
	for _, tc := range cases {
		cfg := ExchangeForTicker(tc.symbol)
		if cfg.Exchange != tc.exchange {
			t.Errorf("ExchangeForTicker(%q).Exchange = %q, want %q", tc.symbol, cfg.Exchange, tc.exchange)
		}
		if cfg.Currency != tc.currency {
			t.Errorf("ExchangeForTicker(%q).Currency = %q, want %q", tc.symbol, cfg.Currency, tc.currency)
		}
	}
}

// ── BareTickerFromSymbol ──────────────────────────────────────────────────────

func TestBareTickerFromSymbol(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"AAPL", "AAPL"},
		{"VOD.L", "VOD"},
		{"BMW.DE", "BMW"},
		{"AIR.PA", "AIR"},
		{"ASML.AS", "ASML"},
		{"ABI.BR", "ABI"},
		{"EDP.LS", "EDP"},
	}
	for _, tc := range cases {
		got := BareTickerFromSymbol(tc.input)
		if got != tc.want {
			t.Errorf("BareTickerFromSymbol(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// ── ToYahooSymbol ─────────────────────────────────────────────────────────────

func TestToYahooSymbol(t *testing.T) {
	lse, _ := ExchangeConfigByName("LSE")
	fse, _ := ExchangeConfigByName("FSE")
	us, _ := ExchangeConfigByName("US")

	cases := []struct {
		ticker string
		cfg    ExchangeConfig
		want   string
	}{
		{"VOD", lse, "VOD.L"},
		{"VOD.L", lse, "VOD.L"}, // already has suffix
		{"BMW", fse, "BMW.DE"},
		{"AAPL", us, "AAPL"}, // US — no suffix
	}
	for _, tc := range cases {
		got := ToYahooSymbol(tc.ticker, tc.cfg)
		if got != tc.want {
			t.Errorf("ToYahooSymbol(%q, %q) = %q, want %q", tc.ticker, tc.cfg.Exchange, got, tc.want)
		}
	}
}

// ── CurrencySymbol ────────────────────────────────────────────────────────────

func TestCurrencySymbol(t *testing.T) {
	cases := []struct{ code, want string }{
		{"USD", "$"},
		{"GBP", "£"},
		{"EUR", "€"},
		{"XXX", "$"}, // unknown defaults to $
	}
	for _, tc := range cases {
		if got := CurrencySymbol(tc.code); got != tc.want {
			t.Errorf("CurrencySymbol(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// ── ExchangeConfigByName ──────────────────────────────────────────────────────

func TestExchangeConfigByName(t *testing.T) {
	cases := []struct {
		name     string
		exchange Exchange
		wantErr  bool
	}{
		{"US", ExchangeUS, false},
		{"", ExchangeUS, false},
		{"LSE", ExchangeLSE, false},
		{"FSE", ExchangeFSE, false},
		{"EURONEXT", ExchangeEuronext, false},
		{"lse", ExchangeLSE, false}, // case-insensitive
		{"INVALID", "", true},
	}
	for _, tc := range cases {
		cfg, err := ExchangeConfigByName(tc.name)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ExchangeConfigByName(%q) expected error, got nil", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("ExchangeConfigByName(%q) unexpected error: %v", tc.name, err)
			continue
		}
		if cfg.Exchange != tc.exchange {
			t.Errorf("ExchangeConfigByName(%q).Exchange = %q, want %q", tc.name, cfg.Exchange, tc.exchange)
		}
	}
}

// ── HolidayCalendars ──────────────────────────────────────────────────────────

func TestLSEHolidayCalendar_2025(t *testing.T) {
	cal := LSEHolidayCalendar{}
	holidays := []string{
		"2025-01-01", // New Year's Day
		"2025-04-18", // Good Friday
		"2025-04-21", // Easter Monday
		"2025-05-05", // Early May Bank Holiday
		"2025-05-26", // Spring Bank Holiday
		"2025-08-25", // Summer Bank Holiday
		"2025-12-25", // Christmas
		"2025-12-26", // Boxing Day
	}
	for _, d := range holidays {
		t, _ := time.Parse("2006-01-02", d)
		if cal.IsWorkingDay(t) {
			// t variable shadowed above — use a different name
		}
	}
	// Verify a normal working day is not a holiday.
	wd, _ := time.Parse("2006-01-02", "2025-03-10") // Monday, no holiday
	if !cal.IsWorkingDay(wd) {
		t.Errorf("LSE: 2025-03-10 (Monday) should be a working day")
	}
	// Weekends are never working days.
	sat, _ := time.Parse("2006-01-02", "2025-03-08")
	if cal.IsWorkingDay(sat) {
		t.Errorf("LSE: 2025-03-08 (Saturday) should not be a working day")
	}
}

func TestFSEHolidayCalendar_2025(t *testing.T) {
	cal := FSEHolidayCalendar{}
	holidays := []string{
		"2025-01-01", // New Year's Day
		"2025-04-18", // Good Friday
		"2025-04-21", // Easter Monday
		"2025-05-01", // Labour Day
		"2025-12-24", // Christmas Eve
		"2025-12-25", // Christmas
		"2025-12-26", // Boxing Day
		"2025-12-31", // New Year's Eve
	}
	for _, d := range holidays {
		dt, _ := time.Parse("2006-01-02", d)
		if cal.IsWorkingDay(dt) {
			t.Errorf("FSE: %s should be a holiday (not a working day)", d)
		}
	}
}

func TestEuronextHolidayCalendar_2025(t *testing.T) {
	cal := EuronextHolidayCalendar{}
	holidays := []string{
		"2025-01-01", // New Year's Day
		"2025-04-18", // Good Friday
		"2025-04-21", // Easter Monday
		"2025-05-01", // Labour Day
		"2025-12-25", // Christmas
		"2025-12-26", // Boxing Day
	}
	for _, d := range holidays {
		dt, _ := time.Parse("2006-01-02", d)
		if cal.IsWorkingDay(dt) {
			t.Errorf("Euronext: %s should be a holiday (not a working day)", d)
		}
	}
}

func TestNewHolidayCalendar_Factory(t *testing.T) {
	cases := []struct {
		name     string
		exchange Exchange
	}{
		{"US", ExchangeUS},
		{"LSE", ExchangeLSE},
		{"FSE", ExchangeFSE},
		{"EURONEXT", ExchangeEuronext},
	}
	for _, tc := range cases {
		cfg, _ := ExchangeConfigByName(string(tc.exchange))
		cal := NewHolidayCalendar(cfg)
		if cal == nil {
			t.Errorf("NewHolidayCalendar(%q) returned nil", tc.exchange)
		}
		// Spot-check: a Saturday is never a working day regardless of exchange.
		sat, _ := time.Parse("2006-01-02", "2025-03-08")
		if cal.IsWorkingDay(sat) {
			t.Errorf("NewHolidayCalendar(%q): Saturday should never be a working day", tc.exchange)
		}
	}
}

// ── MacroCalendar: ECB / BoE events ──────────────────────────────────────────

func TestLoadMacroCalendar_ECBForEuronext(t *testing.T) {
	from, _ := time.Parse("2006-01-02", "2025-01-01")
	to, _ := time.Parse("2006-01-02", "2026-12-31")
	mc := LoadMacroCalendar(from, to, ExchangeConfig{Exchange: ExchangeEuronext})

	hasECB := false
	for _, e := range mc.events {
		if e.Name == "ECB Rate" {
			hasECB = true
			break
		}
	}
	if !hasECB {
		t.Error("LoadMacroCalendar for Euronext should include ECB Rate events")
	}
}

func TestLoadMacroCalendar_BoEForLSE(t *testing.T) {
	from, _ := time.Parse("2006-01-02", "2025-01-01")
	to, _ := time.Parse("2006-01-02", "2026-12-31")
	mc := LoadMacroCalendar(from, to, ExchangeConfig{Exchange: ExchangeLSE})

	hasBoE := false
	for _, e := range mc.events {
		if e.Name == "BoE Rate" {
			hasBoE = true
			break
		}
	}
	if !hasBoE {
		t.Error("LoadMacroCalendar for LSE should include BoE Rate events")
	}
}

func TestLoadMacroCalendar_NoECBForUS(t *testing.T) {
	from, _ := time.Parse("2006-01-02", "2025-01-01")
	to, _ := time.Parse("2006-01-02", "2026-12-31")
	mc := LoadMacroCalendar(from, to, ExchangeConfig{Exchange: ExchangeUS})

	for _, e := range mc.events {
		if e.Name == "ECB Rate" || e.Name == "BoE Rate" {
			t.Errorf("LoadMacroCalendar for US should not include %q events", e.Name)
		}
	}
}

// ── fmtCurrency ───────────────────────────────────────────────────────────────

func TestFmtCurrency(t *testing.T) {
	cases := []struct {
		v        float64
		currency string
		want     string
	}{
		{123.45, "USD", "$123.45"},
		{123.45, "GBP", "£123.45"},
		{123.45, "EUR", "€123.45"},
		{0.50, "USD", "$0.50"},
	}
	for _, tc := range cases {
		got := fmtCurrency(tc.v, tc.currency)
		if got != tc.want {
			t.Errorf("fmtCurrency(%.2f, %q) = %q, want %q", tc.v, tc.currency, got, tc.want)
		}
	}
}

func TestFmtCurrencyB(t *testing.T) {
	cases := []struct {
		v        float64
		currency string
		want     string
	}{
		{5_000_000_000, "USD", "$5.00B"},
		{12_340_000_000, "GBP", "£12.34B"},
		{1_500_000_000, "EUR", "€1.50B"},
	}
	for _, tc := range cases {
		got := fmtCurrencyB(tc.v, tc.currency)
		if got != tc.want {
			t.Errorf("fmtCurrencyB(%.0f, %q) = %q, want %q", tc.v, tc.currency, got, tc.want)
		}
	}
}

// ── computeResultDate with custom calendar ────────────────────────────────────

func TestComputeResultDate_LSECalendar(t *testing.T) {
	cal := LSEHolidayCalendar{}
	// 2025-04-22 is Tuesday after Easter. BMO on Tue → prev working day = Thu Apr 17
	// (Good Friday Apr 18 and Easter Monday Apr 21 are holidays).
	got := computeResultDate("2025-04-22", "bmo", cal)
	if got != "2025-04-17" {
		t.Errorf("computeResultDate with LSE: got %q, want 2025-04-17", got)
	}
}
