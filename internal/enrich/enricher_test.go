package enrich

import (
	"testing"
)

func TestNewEnricher(t *testing.T) {
	e := New()
	if e == nil || e.httpClient == nil || e.yahoo == nil || e.secClient == nil {
		t.Error("NewEnricher returned incomplete Enricher")
	}
}

func TestParseFiscalQuarterEnd(t *testing.T) {
	cases := []struct {
		input   string
		wantStr string // expected last-day-of-month as YYYY-MM-DD, or "" if invalid
	}{
		{"Mar/2026", "2026-03-31"},
		{"Dec/2025", "2025-12-31"},
		{"Feb/2024", "2024-02-29"}, // leap year
		{"Feb/2025", "2025-02-28"}, // non-leap year
		{"Jan/2026", "2026-01-31"},
		{"", ""},
		{"bad", ""},
	}
	for _, tc := range cases {
		got, ok := parseFiscalQuarterEnd(tc.input)
		if tc.wantStr == "" {
			if ok {
				t.Errorf("parseFiscalQuarterEnd(%q): expected failure, got %v", tc.input, got)
			}
			continue
		}
		if !ok {
			t.Errorf("parseFiscalQuarterEnd(%q): unexpected failure", tc.input)
			continue
		}
		if got.Format("2006-01-02") != tc.wantStr {
			t.Errorf("parseFiscalQuarterEnd(%q) = %s, want %s", tc.input, got.Format("2006-01-02"), tc.wantStr)
		}
	}
}
