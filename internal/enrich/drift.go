package enrich

import (
	"fmt"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/events"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/sector"
	"quarterly_results/internal/source/sec"
)

// driftFeatures returns post-release drift features when the stock's report
// came out recently enough for a live drift prediction. The calendar's release
// date, timing and reported surprise come first — they are available the
// morning after the report, weeks before the 10-Q reaches SEC XBRL — then the
// newest SEC-backed reaction.
func (e *Enricher) driftFeatures(res domain.EarningsResult, row domain.CalendarRow, s *domain.FinancialSummary, prices []domain.PricePoint) *domain.DriftFeatures {
	var etf []domain.PricePoint
	if s.SectorETF != "" {
		etf, _ = sector.ETFPrices(s.SectorETF, e.httpClient)
	}
	if res.EarningsDate != "" {
		timing := res.EarningsTime
		if timing == "" {
			timing = e.secReleaseTiming(res.Symbol, res.EarningsDate)
		}
		if f, ok := events.LiveFeatures(res.EarningsDate, timing, row.EPSSurprisePct, prices, etf, s.SectorETF); ok {
			return f
		}
	}
	var latest *domain.EarningsReaction
	for i := range s.EarningsReactions {
		if latest == nil || s.EarningsReactions[i].AnnouncementDate > latest.AnnouncementDate {
			latest = &s.EarningsReactions[i]
		}
	}
	if latest == nil {
		return nil
	}
	announce, err1 := time.Parse("2006-01-02", latest.AnnouncementDate)
	reactionDay, err2 := time.Parse("2006-01-02", latest.ReactionDay)
	if err1 != nil || err2 != nil || !events.Fresh(reactionDay, prices) {
		return nil
	}
	f, ok := events.Features(announce, reactionDay, latest.PriorClose, latest.EPSBeatPct, prices, etf, s.SectorETF)
	if !ok {
		return nil
	}
	return f
}

// secReleaseTiming returns the release session (bmo/amc) from the earnings
// 8-K's EDGAR acceptance time when the calendar doesn't give one — the same
// rule the historical dataset uses, so live features match training. Without
// it the reaction day falls back to a price-gap guess, which picked the wrong
// day for BlackRock's 2026-10-14 pre-market release. "" when unknown.
func (e *Enricher) secReleaseTiming(symbol, date string) string {
	if e.secClient == nil || !(e.exCfg.Exchange == exchange.ExchangeUS || e.exCfg.Exchange == "") {
		return ""
	}
	releases, err := e.secClient.FetchEarningsReleases(symbol, date)
	if err != nil {
		return ""
	}
	return timingOn(releases, date)
}

// timingOn is the session of the earnings release filed on date, if any.
func timingOn(releases []sec.EarningsRelease, date string) string {
	for _, r := range releases {
		if r.FilingDate == date {
			return events.ReleaseTiming(r)
		}
	}
	return ""
}

// freshPressRelease returns the LLM's read of the earnings press release only
// when it read the release for the quarter just reported. Right after a report
// SEC may not have indexed the new 8-K yet, and the agent then reads the
// previous quarter's release — stale for a drift prediction.
func freshPressRelease(s *domain.FinancialSummary) *domain.LLMSubAnalysis {
	if s.LLMRating == nil || s.LLMRating.SubRatings.Transcript == nil || s.DriftFeatures == nil {
		return nil
	}
	announce, err := time.Parse("2006-01-02", s.DriftFeatures.AnnouncementDate)
	if err != nil || s.LLMRating.Metadata.TranscriptQuarter != reportedQuarter(announce) {
		return nil
	}
	return s.LLMRating.SubRatings.Transcript
}

// reportedQuarter labels the calendar quarter a release reports on — the last
// one that ended at least 10 days before it — in the Python agent's
// transcript_quarter format ("Q2 2026").
func reportedQuarter(announce time.Time) string {
	d := announce.AddDate(0, 0, -10)
	q, y := (int(d.Month())-1)/3+1, d.Year()
	quarterEnd := time.Date(y, time.Month(q*3)+1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if d.Before(quarterEnd) {
		if q--; q == 0 {
			q, y = 4, y-1
		}
	}
	return fmt.Sprintf("Q%d %d", q, y)
}
