package tracking

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"quarterly_results/internal/backtest"
	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
	"quarterly_results/internal/indicators"
)

// PriceFetcher returns a symbol's daily price history, oldest → newest.
type PriceFetcher func(symbol string) ([]domain.PricePoint, error)

const (
	// Records whose earnings dates fall within this window describe the same
	// report (companies often confirm or move the date between runs).
	sameReportDays = 45

	// A prediction only counts if it was logged before the release. Releases
	// come before the open (bmo), after the close (amc), or at an unknown time,
	// in which case it must be logged before the earnings date itself.
	bmoCutoffHour = 6
	amcCutoffHour = 16

	// Sub-agent calls use the aggregator's label thresholds (score × confidence
	// on the 100-point scale, and a minimum confidence).
	subLabelScore      = 15.0
	subLabelConfidence = 0.4
)

// Outcome pairs the last pre-release prediction for a report with the
// reaction that followed, measured like EarningsReaction.RetPct.
type Outcome struct {
	Symbol       string         `json:"symbol"`
	EarningsDate string         `json:"earnings_date"`
	EarningsTime string         `json:"earnings_time,omitempty"`
	PredictedAt  time.Time      `json:"predicted_at"`
	ReactionDay  string         `json:"reaction_day"`
	RetPct       float64        `json:"ret_pct"`
	Actual       string         `json:"actual"`
	Rating       *Prediction    `json:"rating,omitempty"`
	LLM          *LLMPrediction `json:"llm,omitempty"`
}

// PredictorStats summarises one predictor over the outcomes it covered.
// Hit rate is hits / directional calls, as in the walk-forward backtest.
type PredictorStats struct {
	Name               string  `json:"name"`
	N                  int     `json:"n"`
	Directional        int     `json:"directional"`
	Hits               int     `json:"hits"`
	HitRate            float64 `json:"hit_rate"`
	AvgRetWhenPositive float64 `json:"avg_ret_when_positive"`
	AvgRetWhenNegative float64 `json:"avg_ret_when_negative"`
}

// Report is the result of scoring a prediction log.
type Report struct {
	Scored       int              `json:"scored"`
	Pending      int              `json:"pending"`       // reaction not observable yet
	NoPrediction int              `json:"no_prediction"` // only logged after the release
	NoPrices     int              `json:"no_prices"`     // price history missing or too short
	Baseline     float64          `json:"baseline"`      // P(actual = most common actual label)
	Predictors   []PredictorStats `json:"predictors"`
	Outcomes     []Outcome        `json:"outcomes"`
}

// Score resolves every logged report against what the stock actually did.
func Score(records []Record, fetch PriceFetcher, now time.Time) Report {
	var rep Report
	priceCache := map[string][]domain.PricePoint{}
	priceErr := map[string]bool{}

	for _, group := range groupReports(records) {
		latest := group[0]
		for _, r := range group[1:] {
			if r.LoggedAt.After(latest.LoggedAt) {
				latest = r
			}
		}
		cutoff := releaseCutoff(latest)
		if now.Before(cutoff) {
			rep.Pending++
			continue
		}
		var pred *Record
		for i := range group {
			r := &group[i]
			if r.LoggedAt.Before(cutoff) && (pred == nil || r.LoggedAt.After(pred.LoggedAt)) {
				pred = r
			}
		}
		if pred == nil {
			rep.NoPrediction++
			continue
		}

		prices, ok := priceCache[latest.Symbol]
		if !ok && !priceErr[latest.Symbol] {
			p, err := fetch(latest.Symbol)
			if err != nil || len(p) == 0 {
				priceErr[latest.Symbol] = true
			} else {
				priceCache[latest.Symbol] = p
				prices = p
			}
		}
		if len(prices) == 0 {
			rep.NoPrices++
			continue
		}

		announce, err := time.Parse("2006-01-02", latest.EarningsDate)
		if err != nil {
			rep.NoPrices++
			continue
		}
		lastBar := prices[len(prices)-1].Date
		if lastBar.Before(announce) {
			rep.Pending++
			continue
		}
		rxDay, priorClose, ok := indicators.ReactionDay(announce, latest.EarningsTime, prices)
		if !ok || priorClose == 0 {
			rep.NoPrices++
			continue
		}
		if lastBar.Before(rxDay) {
			rep.Pending++
			continue
		}
		rxClose, ok := indicators.ClosestPrice(prices, rxDay)
		if !ok {
			rep.NoPrices++
			continue
		}
		ret := indicators.PctChange(priorClose, rxClose)
		rep.Outcomes = append(rep.Outcomes, Outcome{
			Symbol:       latest.Symbol,
			EarningsDate: latest.EarningsDate,
			EarningsTime: latest.EarningsTime,
			PredictedAt:  pred.LoggedAt,
			ReactionDay:  rxDay.Format("2006-01-02"),
			RetPct:       ret,
			Actual:       backtest.LabelFromReturn(ret),
			Rating:       pred.Rating,
			LLM:          pred.LLM,
		})
	}

	sort.Slice(rep.Outcomes, func(i, j int) bool {
		a, b := rep.Outcomes[i], rep.Outcomes[j]
		if a.EarningsDate != b.EarningsDate {
			return a.EarningsDate < b.EarningsDate
		}
		return a.Symbol < b.Symbol
	})
	rep.Scored = len(rep.Outcomes)
	rep.Baseline = baseline(rep.Outcomes)
	rep.Predictors = predictorStats(rep.Outcomes)
	return rep
}

// groupReports splits records into one group per (symbol, report): records
// for the same symbol whose earnings dates lie within sameReportDays of the
// group's first date belong to the same report.
func groupReports(records []Record) [][]Record {
	bySymbol := map[string][]Record{}
	for _, r := range records {
		sym := strings.ToUpper(r.Symbol)
		bySymbol[sym] = append(bySymbol[sym], r)
	}
	symbols := make([]string, 0, len(bySymbol))
	for s := range bySymbol {
		symbols = append(symbols, s)
	}
	sort.Strings(symbols)

	var out [][]Record
	for _, sym := range symbols {
		rs := bySymbol[sym]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].EarningsDate < rs[j].EarningsDate })
		var cur []Record
		var start time.Time
		for _, r := range rs {
			d, err := time.Parse("2006-01-02", r.EarningsDate)
			if err != nil {
				continue
			}
			if cur != nil && d.Sub(start) > sameReportDays*24*time.Hour {
				out = append(out, cur)
				cur = nil
			}
			if cur == nil {
				start = d
			}
			cur = append(cur, r)
		}
		if cur != nil {
			out = append(out, cur)
		}
	}
	return out
}

// releaseCutoff is the latest moment a prediction can be logged and still
// count as made before the release, in the exchange's local time.
func releaseCutoff(r Record) time.Time {
	cfg, err := exchange.ConfigByName(r.Exchange)
	if err != nil {
		cfg = exchange.DefaultUSConfig
	}
	loc := location(cfg)
	d, err := time.ParseInLocation("2006-01-02", r.EarningsDate, loc)
	if err != nil {
		return time.Time{}
	}
	switch r.EarningsTime {
	case "bmo":
		return d.Add(bmoCutoffHour * time.Hour)
	case "amc":
		return d.Add(amcCutoffHour * time.Hour)
	default:
		return d
	}
}

// subLabel turns a single agent's score/confidence into a call using the
// same thresholds the Python aggregator applies to the blended score.
func subLabel(s SubPrediction) string {
	if s.Confidence < subLabelConfidence {
		return "Neutral"
	}
	v := s.Score * s.Confidence * 100
	switch {
	case v >= subLabelScore:
		return "Positive"
	case v <= -subLabelScore:
		return "Negative"
	default:
		return "Neutral"
	}
}

func baseline(outcomes []Outcome) float64 {
	if len(outcomes) == 0 {
		return 0
	}
	counts := map[string]int{}
	best := 0
	for _, o := range outcomes {
		counts[o.Actual]++
		if counts[o.Actual] > best {
			best = counts[o.Actual]
		}
	}
	return float64(best) / float64(len(outcomes))
}

// predictorStats compares the deterministic Rating, the LLM rating, the
// Rating restricted to the stocks the LLM also covered (a like-for-like
// comparison), and each LLM sub-agent.
func predictorStats(outcomes []Outcome) []PredictorStats {
	type call struct {
		label string
		ret   float64
	}
	calls := map[string][]call{}
	var subNames []string
	seenSub := map[string]bool{}
	for _, o := range outcomes {
		if o.Rating != nil {
			calls["rating"] = append(calls["rating"], call{o.Rating.Label, o.RetPct})
		}
		if o.LLM == nil {
			continue
		}
		calls["llm"] = append(calls["llm"], call{o.LLM.Label, o.RetPct})
		if o.Rating != nil {
			calls["rating (LLM-covered stocks)"] = append(calls["rating (LLM-covered stocks)"], call{o.Rating.Label, o.RetPct})
		}
		for name, sub := range o.LLM.Sub {
			key := "llm." + name
			if !seenSub[key] {
				seenSub[key] = true
				subNames = append(subNames, key)
			}
			calls[key] = append(calls[key], call{subLabel(sub), o.RetPct})
		}
	}
	sort.Strings(subNames)

	var out []PredictorStats
	for _, name := range append([]string{"rating", "rating (LLM-covered stocks)", "llm"}, subNames...) {
		cs, ok := calls[name]
		if !ok {
			continue
		}
		st := PredictorStats{Name: name, N: len(cs)}
		var sumPos, sumNeg float64
		var nPos, nNeg int
		for _, c := range cs {
			switch c.label {
			case "Positive":
				nPos++
				sumPos += c.ret
			case "Negative":
				nNeg++
				sumNeg += c.ret
			default:
				continue
			}
			st.Directional++
			if backtest.LabelFromReturn(c.ret) == c.label {
				st.Hits++
			}
		}
		if st.Directional > 0 {
			st.HitRate = float64(st.Hits) / float64(st.Directional)
		}
		if nPos > 0 {
			st.AvgRetWhenPositive = sumPos / float64(nPos)
		}
		if nNeg > 0 {
			st.AvgRetWhenNegative = sumNeg / float64(nNeg)
		}
		out = append(out, st)
	}
	return out
}

// FormatReport renders a report as a plain-text table.
func FormatReport(rep Report, path string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Prediction track record — %s\n", path)
	fmt.Fprintf(&b, "Scored %d · pending %d · logged only after release %d · price data missing %d\n",
		rep.Scored, rep.Pending, rep.NoPrediction, rep.NoPrices)
	if rep.Scored == 0 {
		b.WriteString("Nothing to score yet — log predictions with --log-predictions before reports come out.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Baseline (always guess the most common actual label): %.0f%%\n\n", rep.Baseline*100)
	fmt.Fprintf(&b, "%-28s %5s %6s %5s %9s %11s %11s\n", "Predictor", "N", "Calls", "Hits", "Hit rate", "Avg ret ▲", "Avg ret ▼")
	for _, s := range rep.Predictors {
		hit := "—"
		if s.Directional > 0 {
			hit = fmt.Sprintf("%.0f%%", s.HitRate*100)
		}
		fmt.Fprintf(&b, "%-28s %5d %6d %5d %9s %11s %11s\n",
			s.Name, s.N, s.Directional, s.Hits, hit, fmtRet(s.AvgRetWhenPositive), fmtRet(s.AvgRetWhenNegative))
	}
	b.WriteString("\nCalls = Positive/Negative predictions; hit rate = calls whose sign matched a >1% reaction.\n")
	return b.String()
}

func fmtRet(v float64) string {
	if v == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.2f%%", v)
}
