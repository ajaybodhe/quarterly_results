package enrich

import (
	"testing"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/source/sec"
)

func TestReportedQuarter(t *testing.T) {
	cases := map[string]string{
		"2026-07-23": "Q2 2026", // SMMT
		"2026-02-23": "Q4 2025", // late Q4 release
		"2026-01-12": "Q4 2025",
		"2025-10-28": "Q3 2025",
		"2026-04-15": "Q1 2026", // 15 days after Q1 closed
	}
	for in, want := range cases {
		d, _ := time.Parse("2006-01-02", in)
		if got := reportedQuarter(d); got != want {
			t.Errorf("reportedQuarter(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestFreshPressRelease_OnlyForTheReportedQuarter(t *testing.T) {
	read := &domain.LLMSubAnalysis{Score: 0.6, Confidence: 0.5}
	s := &domain.FinancialSummary{
		DriftFeatures: &domain.DriftFeatures{AnnouncementDate: "2026-07-23"},
		LLMRating: &domain.LLMRating{
			SubRatings: domain.LLMSubRatings{Transcript: read},
			Metadata:   domain.LLMMetadata{TranscriptQuarter: "Q2 2026"},
		},
	}
	if freshPressRelease(s) != read {
		t.Error("matching quarter should use the press-release read")
	}
	s.LLMRating.Metadata.TranscriptQuarter = "Q1 2026" // agent read last quarter's release
	if freshPressRelease(s) != nil {
		t.Error("a previous quarter's release must not feed the drift prediction")
	}
	if freshPressRelease(&domain.FinancialSummary{}) != nil {
		t.Error("no LLM rating → nil")
	}
}

func TestTimingOn(t *testing.T) {
	acc := func(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
	rels := []sec.EarningsRelease{
		{FilingDate: "2026-07-15", Accepted: acc("2026-07-15T14:33:00Z")},
		{FilingDate: "2026-10-14", Accepted: acc("2026-10-14T14:39:00Z")}, // BLK: 10:39 ET → pre-open release
	}
	if got := timingOn(rels, "2026-10-14"); got != "bmo" {
		t.Errorf("timingOn = %q, want bmo", got)
	}
	if got := timingOn(rels, "2026-10-20"); got != "" {
		t.Errorf("no release that day → %q, want empty", got)
	}
}
