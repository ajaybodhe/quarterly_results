package tracking

import (
	"fmt"
	"math"
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
// Hit rate is hits / directional calls, as in the walk-forward backtest, with
// a 95% Wilson interval: on samples this small the interval matters more than
// the point estimate.
type PredictorStats struct {
	Name               string  `json:"name"`
	N                  int     `json:"n"`
	Directional        int     `json:"directional"`
	Hits               int     `json:"hits"`
	HitRate            float64 `json:"hit_rate"`
	HitRateLow         float64 `json:"hit_rate_low"`
	HitRateHigh        float64 `json:"hit_rate_high"`
	AvgRetWhenPositive float64 `json:"avg_ret_when_positive"`
	AvgRetWhenNegative float64 `json:"avg_ret_when_negative"`
}

// Call is one predictor's label for one outcome and the return that followed.
type Call struct {
	Label  string
	RetPct float64
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

	// Drift horizon: one outcome per (report, horizon) whose window completed.
	DriftPending    int                `json:"drift_pending"` // windows not complete yet
	DriftBaselines  map[string]float64 `json:"drift_baselines,omitempty"`
	DriftPredictors []PredictorStats   `json:"drift_predictors,omitempty"`
	DriftOutcomes   []DriftOutcome     `json:"drift_outcomes,omitempty"`

	// Post-release signals (release = Signal 1, intraday = Signal 2).
	PostReleasePending    int                  `json:"post_release_pending"`
	PostReleaseLate       int                  `json:"post_release_late"`
	PostReleasePredictors []PredictorStats     `json:"post_release_predictors,omitempty"`
	PostReleaseOutcomes   []PostReleaseOutcome `json:"post_release_outcomes,omitempty"`
}

// DriftOutcome pairs the first post-reaction drift prediction for a report
// with the move over the following trading days. The window starts at the
// close of the day the prediction was logged, so it never includes drift the
// prediction could have seen.
type DriftOutcome struct {
	Symbol      string         `json:"symbol"`
	ReactionDay string         `json:"reaction_day"`
	PredictedAt time.Time      `json:"predicted_at"`
	Bars        int            `json:"bars"` // 5 or 20 trading days
	Start       string         `json:"start"`
	End         string         `json:"end"`
	RetPct      float64        `json:"ret_pct"`  // stock return over the window
	Adjusted    bool           `json:"adjusted"` // true when the sector ETF was subtracted
	AdjRetPct   float64        `json:"adj_ret_pct"`
	Actual      string         `json:"actual"` // label of AdjRetPct (or RetPct when not adjusted)
	Rating      *Prediction    `json:"rating,omitempty"`
	LLM         *LLMPrediction `json:"llm,omitempty"`
}

// driftHorizons are the windows, in trading bars, each drift prediction is scored on.
var driftHorizons = []int{5, 20}

// cachedFetcher wraps a PriceFetcher so each symbol is fetched at most once.
func cachedFetcher(fetch PriceFetcher) func(string) []domain.PricePoint {
	cache := map[string][]domain.PricePoint{}
	failed := map[string]bool{}
	return func(sym string) []domain.PricePoint {
		if p, ok := cache[sym]; ok || failed[sym] {
			return p
		}
		p, err := fetch(sym)
		if err != nil || len(p) == 0 {
			failed[sym] = true
			return nil
		}
		cache[sym] = p
		return p
	}
}

// scoreDrift scores drift-horizon records: per (symbol, reaction day) the
// earliest record counts, measured from the close of the day it was logged.
func scoreDrift(records []Record, pricesFor func(string) []domain.PricePoint, now time.Time, rep *Report) {
	first := map[string]Record{}
	for _, r := range records {
		key := strings.ToUpper(r.Symbol) + "|" + r.ReactionDay
		if prev, ok := first[key]; !ok || r.LoggedAt.Before(prev.LoggedAt) {
			first[key] = r
		}
	}
	for _, r := range first {
		prices := pricesFor(r.Symbol)
		start, ok := startBar(prices, r)
		if !ok {
			rep.DriftPending += len(driftHorizons)
			continue
		}
		var etf []domain.PricePoint
		if r.SectorETF != "" {
			etf = pricesFor(r.SectorETF)
		}
		for _, n := range driftHorizons {
			ret, end, ok := indicators.ForwardReturn(prices, start, n)
			if !ok {
				rep.DriftPending++
				continue
			}
			o := DriftOutcome{
				Symbol: r.Symbol, ReactionDay: r.ReactionDay, PredictedAt: r.LoggedAt, Bars: n,
				Start: start.Format("2006-01-02"), End: end.Format("2006-01-02"),
				RetPct: ret, AdjRetPct: ret, Rating: r.Rating, LLM: r.LLM,
			}
			if bench, ok := indicators.ReturnBetween(etf, start, end); ok {
				o.Adjusted, o.AdjRetPct = true, ret-bench
			}
			o.Actual = backtest.LabelFromReturn(o.AdjRetPct)
			rep.DriftOutcomes = append(rep.DriftOutcomes, o)
		}
	}
	sort.Slice(rep.DriftOutcomes, func(i, j int) bool {
		a, b := rep.DriftOutcomes[i], rep.DriftOutcomes[j]
		if a.ReactionDay != b.ReactionDay {
			return a.ReactionDay < b.ReactionDay
		}
		if a.Symbol != b.Symbol {
			return a.Symbol < b.Symbol
		}
		return a.Bars < b.Bars
	})

	rep.DriftBaselines = map[string]float64{}
	for _, n := range driftHorizons {
		var actual []string
		var rules, llm []Call
		for _, o := range rep.DriftOutcomes {
			if o.Bars != n {
				continue
			}
			actual = append(actual, o.Actual)
			if o.Rating != nil {
				rules = append(rules, Call{o.Rating.Label, o.AdjRetPct})
			}
			if o.LLM != nil {
				llm = append(llm, Call{o.LLM.Label, o.AdjRetPct})
			}
		}
		if len(actual) == 0 {
			continue
		}
		rep.DriftBaselines[fmt.Sprintf("drift%d", n)] = Baseline(actual)
		rep.DriftPredictors = append(rep.DriftPredictors, Summarize(fmt.Sprintf("drift%d: rules", n), rules))
		if len(llm) > 0 {
			rep.DriftPredictors = append(rep.DriftPredictors, Summarize(fmt.Sprintf("drift%d: llm", n), llm))
		}
	}
}

// startBar is the first bar on or after the exchange-local date the drift
// prediction was logged: its close is the drift window's starting point.
func startBar(prices []domain.PricePoint, r Record) (time.Time, bool) {
	cfg, err := exchange.ConfigByName(r.Exchange)
	if err != nil {
		cfg = exchange.DefaultUSConfig
	}
	y, m, d := r.LoggedAt.In(location(cfg)).Date()
	loggedDay := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	for _, p := range prices {
		if !p.Date.Before(loggedDay) {
			return p.Date, true
		}
	}
	return time.Time{}, false
}

// Score resolves every logged report against what the stock actually did.
func Score(records []Record, fetch PriceFetcher, now time.Time) Report {
	var rep Report
	pricesFor := cachedFetcher(fetch)

	var reactionRecs, driftRecs, postRecs []Record
	for _, r := range records {
		switch r.horizon() {
		case HorizonDrift:
			driftRecs = append(driftRecs, r)
		case HorizonRelease, HorizonIntraday:
			postRecs = append(postRecs, r)
		default:
			reactionRecs = append(reactionRecs, r)
		}
	}
	scoreDrift(driftRecs, pricesFor, now, &rep)
	scorePostRelease(postRecs, pricesFor, &rep)

	for _, group := range groupReports(reactionRecs) {
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

		prices := pricesFor(latest.Symbol)
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
	labels := make([]string, len(outcomes))
	for i, o := range outcomes {
		labels[i] = o.Actual
	}
	return Baseline(labels)
}

// Baseline is the share of actual labels equal to the most common one — the
// hit rate of always guessing that label.
func Baseline(actual []string) float64 {
	if len(actual) == 0 {
		return 0
	}
	counts := map[string]int{}
	best := 0
	for _, a := range actual {
		counts[a]++
		if counts[a] > best {
			best = counts[a]
		}
	}
	return float64(best) / float64(len(actual))
}

// Summarize scores one predictor's calls: directional calls, hits against a
// ±1% reaction, hit rate with its 95% Wilson interval, and average return
// after Positive and after Negative calls.
func Summarize(name string, calls []Call) PredictorStats { return SummarizeDeadband(name, calls, 1) }

// SummarizeDeadband is Summarize with a custom deadband (% move needed for a
// hit); 0 scores on sign alone, suited to small intraday moves.
func SummarizeDeadband(name string, calls []Call, deadband float64) PredictorStats {
	st := PredictorStats{Name: name, N: len(calls)}
	var sumPos, sumNeg float64
	var nPos, nNeg int
	for _, c := range calls {
		switch c.Label {
		case "Positive":
			nPos++
			sumPos += c.RetPct
		case "Negative":
			nNeg++
			sumNeg += c.RetPct
		default:
			continue
		}
		st.Directional++
		if labelWithDeadband(c.RetPct, deadband) == c.Label {
			st.Hits++
		}
	}
	if st.Directional > 0 {
		st.HitRate = float64(st.Hits) / float64(st.Directional)
		st.HitRateLow, st.HitRateHigh = wilson(st.Hits, st.Directional)
	}
	if nPos > 0 {
		st.AvgRetWhenPositive = sumPos / float64(nPos)
	}
	if nNeg > 0 {
		st.AvgRetWhenNegative = sumNeg / float64(nNeg)
	}
	return st
}

func labelWithDeadband(ret, deadband float64) string {
	switch {
	case ret > deadband:
		return "Positive"
	case ret < -deadband:
		return "Negative"
	default:
		return "Neutral"
	}
}

// wilson returns the 95% Wilson score interval for k successes in n trials.
func wilson(k, n int) (lo, hi float64) {
	const z = 1.96
	p, nf := float64(k)/float64(n), float64(n)
	den := 1 + z*z/nf
	center := (p + z*z/(2*nf)) / den
	half := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf)) / den
	return math.Max(0, center-half), math.Min(1, center+half)
}

// predictorStats compares the deterministic Rating, the LLM rating, the
// Rating restricted to the stocks the LLM also covered (a like-for-like
// comparison), and each LLM sub-agent.
func predictorStats(outcomes []Outcome) []PredictorStats {
	calls := map[string][]Call{}
	var subNames []string
	seenSub := map[string]bool{}
	for _, o := range outcomes {
		if o.Rating != nil {
			calls["rating"] = append(calls["rating"], Call{o.Rating.Label, o.RetPct})
		}
		if o.LLM == nil {
			continue
		}
		calls["llm"] = append(calls["llm"], Call{o.LLM.Label, o.RetPct})
		if o.Rating != nil {
			calls["rating (LLM-covered stocks)"] = append(calls["rating (LLM-covered stocks)"], Call{o.Rating.Label, o.RetPct})
		}
		for name, sub := range o.LLM.Sub {
			key := "llm." + name
			if !seenSub[key] {
				seenSub[key] = true
				subNames = append(subNames, key)
			}
			calls[key] = append(calls[key], Call{subLabel(sub), o.RetPct})
		}
	}
	sort.Strings(subNames)

	var out []PredictorStats
	for _, name := range append([]string{"rating", "rating (LLM-covered stocks)", "llm"}, subNames...) {
		if cs, ok := calls[name]; ok {
			out = append(out, Summarize(name, cs))
		}
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
	} else {
		fmt.Fprintf(&b, "Baseline (always guess the most common actual label): %.0f%%\n\n", rep.Baseline*100)
		b.WriteString(FormatStats(rep.Predictors))
		b.WriteString("\nCalls = Positive/Negative predictions; hit rate = calls whose sign matched a >1% reaction.\n")
	}

	fmt.Fprintf(&b, "\nPost-earnings drift — %d window(s) scored · %d pending\n", len(rep.DriftOutcomes), rep.DriftPending)
	if len(rep.DriftPredictors) == 0 {
		b.WriteString("Nothing to score yet — log runs covering just-released reports (e.g. --from <report date>).\n")
		b.WriteString(formatPostRelease(rep))
		return b.String()
	}
	for _, n := range driftHorizons {
		if bl, ok := rep.DriftBaselines[fmt.Sprintf("drift%d", n)]; ok {
			fmt.Fprintf(&b, "Baseline drift%d (sector-adjusted, most common label): %.0f%%\n", n, bl*100)
		}
	}
	b.WriteString("\n" + FormatStats(rep.DriftPredictors))
	b.WriteString("\nDrift windows start at the close of the day the prediction was logged and are sector-adjusted when the ETF is known.\n")
	b.WriteString(formatPostRelease(rep))
	return b.String()
}

// FormatStats renders predictor stats as a plain-text table.
func FormatStats(stats []PredictorStats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-30s %5s %6s %5s %9s %11s %11s %11s\n", "Predictor", "N", "Calls", "Hits", "Hit rate", "95% CI", "Avg ret ▲", "Avg ret ▼")
	for _, s := range stats {
		hit, ci := "—", "—"
		if s.Directional > 0 {
			hit = fmt.Sprintf("%.0f%%", s.HitRate*100)
			ci = fmt.Sprintf("%.0f–%.0f%%", s.HitRateLow*100, s.HitRateHigh*100)
		}
		fmt.Fprintf(&b, "%-30s %5d %6d %5d %9s %11s %11s %11s\n",
			s.Name, s.N, s.Directional, s.Hits, hit, ci, fmtRet(s.AvgRetWhenPositive), fmtRet(s.AvgRetWhenNegative))
	}
	return b.String()
}

func fmtRet(v float64) string {
	if v == 0 {
		return "—"
	}
	return fmt.Sprintf("%+.2f%%", v)
}
