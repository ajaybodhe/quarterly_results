package tracking

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/indicators"
)

// closeHour is the regular-session close (local time) on the reaction day:
// a post-release prediction must be logged before it to count.
const closeHour = 16

// NewReleaseRecord logs Signal 1 for a released report.
func NewReleaseRecord(r domain.EarningsResult, cfg exchange.Config, now time.Time) (Record, bool) {
	p := r.PostRelease
	if p == nil || p.Signal1 == nil {
		return Record{}, false
	}
	s := p.Signal1
	return Record{
		LoggedAt:     now.UTC().Truncate(time.Second),
		Horizon:      HorizonRelease,
		Symbol:       r.Symbol,
		Exchange:     string(cfg.Exchange),
		EarningsDate: p.AnnouncementDate,
		EarningsTime: r.EarningsTime,
		ReactionDay:  p.ReactionDay,
		Rating:       &Prediction{Label: s.Label, Score: (s.PUp - 0.5) * 200, Confidence: math.Abs(s.PUp-0.5) * 2},
	}, true
}

// NewIntradayRecord logs Signal 2 (only once the reaction-day open is known).
func NewIntradayRecord(r domain.EarningsResult, cfg exchange.Config, now time.Time) (Record, bool) {
	p := r.PostRelease
	if p == nil || p.Signal2 == nil || p.ReactionDay == "" {
		return Record{}, false
	}
	s := p.Signal2
	label := "Neutral"
	switch {
	case strings.HasPrefix(s.Lean, "LONG"):
		label = "Positive"
	case strings.HasPrefix(s.Lean, "SHORT"):
		label = "Negative"
	}
	return Record{
		LoggedAt:     now.UTC().Truncate(time.Second),
		Horizon:      HorizonIntraday,
		Symbol:       r.Symbol,
		Exchange:     string(cfg.Exchange),
		EarningsDate: p.AnnouncementDate,
		EarningsTime: r.EarningsTime,
		ReactionDay:  p.ReactionDay,
		Decile:       s.Decile,
		Rating:       &Prediction{Label: label, Score: s.PredictedPct, Confidence: float64(s.Decile) / 10},
	}, true
}

// PostReleaseOutcome is a scored Signal 1 or Signal 2 call.
type PostReleaseOutcome struct {
	Horizon     string      `json:"horizon"`
	Symbol      string      `json:"symbol"`
	ReactionDay string      `json:"reaction_day"`
	PredictedAt time.Time   `json:"predicted_at"`
	RetPct      float64     `json:"ret_pct"` // release: reaction-day return; intraday: open→close minus SPY
	Decile      int         `json:"decile,omitempty"`
	Rating      *Prediction `json:"rating,omitempty"`
}

// scorePostRelease scores release and intraday records: per report, the
// earliest record logged before the reaction-day close counts.
func scorePostRelease(records []Record, pricesFor func(string) []domain.PricePoint, rep *Report) {
	first := map[string]Record{}
	for _, r := range records {
		key := r.horizon() + "|" + strings.ToUpper(r.Symbol) + "|" + r.EarningsDate
		if prev, ok := first[key]; !ok || r.LoggedAt.Before(prev.LoggedAt) {
			first[key] = r
		}
	}
	var spy []domain.PricePoint
	for _, r := range first {
		prices := pricesFor(r.Symbol)
		announce, err := time.Parse("2006-01-02", r.EarningsDate)
		if err != nil || len(prices) == 0 {
			rep.PostReleasePending++
			continue
		}
		rxDay, priorClose, ok := reactionFor(r, announce, prices)
		if !ok || prices[len(prices)-1].Date.Before(rxDay) {
			rep.PostReleasePending++
			continue
		}
		if !r.LoggedAt.Before(sessionClose(r, rxDay)) {
			rep.PostReleaseLate++
			continue
		}
		bar, ok := barAt(prices, rxDay)
		if !ok {
			rep.PostReleasePending++
			continue
		}
		o := PostReleaseOutcome{Horizon: r.horizon(), Symbol: r.Symbol, ReactionDay: rxDay.Format("2006-01-02"),
			PredictedAt: r.LoggedAt, Decile: r.Decile, Rating: r.Rating}
		if r.horizon() == HorizonRelease {
			o.RetPct = indicators.PctChange(priorClose, bar.Close)
		} else {
			if spy == nil {
				spy = pricesFor("SPY")
			}
			sb, ok := barAt(spy, rxDay)
			if !ok || bar.Open == 0 || sb.Open == 0 {
				rep.PostReleasePending++
				continue
			}
			o.RetPct = indicators.PctChange(bar.Open, bar.Close) - indicators.PctChange(sb.Open, sb.Close)
		}
		rep.PostReleaseOutcomes = append(rep.PostReleaseOutcomes, o)
	}
	sort.Slice(rep.PostReleaseOutcomes, func(i, j int) bool {
		a, b := rep.PostReleaseOutcomes[i], rep.PostReleaseOutcomes[j]
		if a.ReactionDay != b.ReactionDay {
			return a.ReactionDay < b.ReactionDay
		}
		return a.Symbol+a.Horizon < b.Symbol+b.Horizon
	})

	var s1, s2 []Call
	for _, o := range rep.PostReleaseOutcomes {
		if o.Rating == nil {
			continue
		}
		if o.Horizon == HorizonRelease {
			s1 = append(s1, Call{o.Rating.Label, o.RetPct})
		} else {
			s2 = append(s2, Call{o.Rating.Label, o.RetPct})
		}
	}
	if len(s1) > 0 {
		rep.PostReleasePredictors = append(rep.PostReleasePredictors, Summarize("signal 1: reaction day (±1%)", s1))
	}
	if len(s2) > 0 {
		rep.PostReleasePredictors = append(rep.PostReleasePredictors, SummarizeDeadband("signal 2: open→close−SPY (sign)", s2, 0))
	}
}

// reactionFor resolves the reaction day and prior close: the logged reaction
// day when known, else the release timing via indicators.ReactionDay.
func reactionFor(r Record, announce time.Time, prices []domain.PricePoint) (time.Time, float64, bool) {
	if r.ReactionDay != "" {
		day, err := time.Parse("2006-01-02", r.ReactionDay)
		if err != nil {
			return time.Time{}, 0, false
		}
		prior, ok := indicators.ClosestPrice(prices, day.AddDate(0, 0, -1))
		return day, prior, ok && prior != 0
	}
	return indicators.ReactionDay(announce, r.EarningsTime, prices)
}

func sessionClose(r Record, day time.Time) time.Time {
	cfg, err := exchange.ConfigByName(r.Exchange)
	if err != nil {
		cfg = exchange.DefaultUSConfig
	}
	y, m, d := day.Date()
	return time.Date(y, m, d, closeHour, 0, 0, 0, location(cfg))
}

func barAt(prices []domain.PricePoint, day time.Time) (domain.PricePoint, bool) {
	want := day.Format("2006-01-02")
	for _, p := range prices {
		if p.Date.Format("2006-01-02") == want {
			return p, true
		}
	}
	return domain.PricePoint{}, false
}

// formatPostRelease renders the post-release section of the track record.
func formatPostRelease(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\nPost-release signals — %d call(s) scored · %d pending · %d logged after the reaction-day close\n",
		len(rep.PostReleaseOutcomes), rep.PostReleasePending, rep.PostReleaseLate)
	if len(rep.PostReleasePredictors) == 0 {
		b.WriteString("Nothing to score yet — run with --post-release --log-predictions after reports come out.\n")
		return b.String()
	}
	b.WriteString(FormatStats(rep.PostReleasePredictors))
	b.WriteString("Signal 1 hits need a >1% reaction in the called direction; Signal 2 hits only need the right sign.\n")
	return b.String()
}
