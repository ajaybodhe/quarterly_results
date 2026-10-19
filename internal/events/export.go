package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"quarterly_results/internal/backtest"
	"quarterly_results/internal/domain"
	"quarterly_results/internal/recommend"
	"quarterly_results/internal/sector"
	"quarterly_results/internal/source/yahoo"
	"quarterly_results/internal/tracking"
)

// Event is one historical announcement: point-in-time features, the
// rule-based drift prediction made from them, and the outcomes that followed.
type Event struct {
	EventID     string  `json:"event_id,omitempty"` // SYMBOL|announcement date
	Symbol      string  `json:"symbol"`
	Exchange    string  `json:"exchange"`
	CIK         int     `json:"cik,omitempty"`
	SIC         int     `json:"sic,omitempty"`
	Period      string  `json:"period"`                 // fiscal quarter end
	EPS         float64 `json:"eps,omitempty"`          // diluted EPS on today's share basis (split-adjusted)
	EPSReported float64 `json:"eps_reported,omitempty"` // diluted EPS as filed
	// Nasdaq consensus for the release (filled by --backfill-consensus):
	// features.eps_surprise_pct holds the surprise %.
	EPSConsensus *float64             `json:"eps_consensus,omitempty"`
	EPSStreet    *float64             `json:"eps_street,omitempty"` // reported EPS on the street (adjusted) basis
	NasdaqTiming string               `json:"nasdaq_timing,omitempty"`
	SplitWindow  bool                 `json:"split_window,omitempty"` // quarter near a split: EPS may mix share bases
	Accession    string               `json:"accession,omitempty"`    // earnings 8-K, for the press-release text join
	PrimaryDoc   string               `json:"primary_doc,omitempty"`  // the 8-K's primary document
	ExportedAt   time.Time            `json:"exported_at"`
	Features     domain.DriftFeatures `json:"features"`
	Rules        tracking.Prediction  `json:"rules"`
	Labels       Labels               `json:"labels"`
}

// Exporter turns enriched stocks into events, fetching the price and sector
// ETF histories the features and labels need.
type Exporter struct {
	httpClient *http.Client
	exchange   string
}

func NewExporter(httpClient *http.Client, exchange string) *Exporter {
	return &Exporter{httpClient: httpClient, exchange: exchange}
}

// Events builds one event per past earnings reaction in the summary.
func (x *Exporter) Events(s *domain.FinancialSummary, now time.Time) ([]Event, error) {
	if s == nil || len(s.EarningsReactions) == 0 {
		return nil, nil
	}
	prices, err := yahoo.FetchChartHistory(x.httpClient, s.Symbol)
	if err != nil {
		return nil, fmt.Errorf("prices for %s: %w", s.Symbol, err)
	}
	var etf []domain.PricePoint
	if s.SectorETF != "" {
		etf, _ = sector.ETFPrices(s.SectorETF, x.httpClient)
	}
	var out []Event
	for _, rxn := range s.EarningsReactions {
		announce, err1 := time.Parse("2006-01-02", rxn.AnnouncementDate)
		reactionDay, err2 := time.Parse("2006-01-02", rxn.ReactionDay)
		if err1 != nil || err2 != nil {
			continue
		}
		f, ok := Features(announce, reactionDay, rxn.PriorClose, rxn.EPSBeatPct, prices, etf, s.SectorETF)
		if !ok {
			continue
		}
		rules := recommend.ComputeDrift(f, nil) // no point-in-time LLM read for past releases
		out = append(out, Event{
			Symbol:     s.Symbol,
			Exchange:   x.exchange,
			Period:     rxn.Period,
			ExportedAt: now.UTC().Truncate(time.Second),
			Features:   *f,
			Rules:      tracking.Prediction{Label: rules.Label, Score: rules.Score, Confidence: rules.Confidence},
			Labels:     LabelsAt(announce, reactionDay, prices, etf, nil),
		})
	}
	return out, nil
}

// Append writes events to a JSON-lines file.
func Append(path string, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, e := range evs {
		line, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		w.Write(append(line, '\n'))
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load reads events from a JSON-lines file. When the same announcement was
// exported more than once, the most recent export wins (its labels are the
// most complete).
func Load(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	latest := map[string]Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		key := strings.ToUpper(e.Symbol) + "|" + e.Features.AnnouncementDate
		if prev, ok := latest[key]; !ok || !e.ExportedAt.Before(prev.ExportedAt) {
			latest[key] = e
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(latest))
	for _, e := range latest {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Features.AnnouncementDate != out[j].Features.AnnouncementDate {
			return out[i].Features.AnnouncementDate < out[j].Features.AnnouncementDate
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

// HorizonReport compares predictors on one drift label.
type HorizonReport struct {
	Label      string                    `json:"label"`
	N          int                       `json:"n"`
	Baseline   float64                   `json:"baseline"`
	Predictors []tracking.PredictorStats `json:"predictors"`
}

// Evaluate scores the rule-based drift prediction against simple baselines on
// each sector-adjusted drift horizon, plus a pre-announcement placebo.
//
//   - rules:          recommend.ComputeDrift (what live runs use, minus the LLM)
//   - reaction-only:  drift continues in the reaction's direction (±1%)
//   - surprise-only:  drift follows the consensus EPS surprise's sign (±1%)
//   - SUE-only:       drift follows the consensus-free surprise (|SUE| ≥ 1σ)
func Evaluate(evs []Event) []HorizonReport {
	horizons := []struct {
		name string
		get  func(Labels) *float64
	}{
		{"drift5_adj (reaction close → +5 bars, minus sector ETF)", func(l Labels) *float64 { return l.Drift5Adj }},
		{"drift20_adj (reaction close → +20 bars, minus sector ETF)", func(l Labels) *float64 { return l.Drift20Adj }},
		{"drift5_mkt (reaction close → +5 bars, minus SPY)", func(l Labels) *float64 { return l.Drift5Mkt }},
		{"drift20_mkt (reaction close → +20 bars, minus SPY)", func(l Labels) *float64 { return l.Drift20Mkt }},
		{"placebo: pre7_ret (the week before the announcement)", func(l Labels) *float64 { return l.Pre7Ret }},
	}
	var out []HorizonReport
	for _, h := range horizons {
		var rules, reaction, surprise, sue []tracking.Call
		var actual []string
		for _, e := range evs {
			v := h.get(e.Labels)
			if v == nil {
				continue
			}
			actual = append(actual, backtest.LabelFromReturn(*v))
			rules = append(rules, tracking.Call{Label: e.Rules.Label, RetPct: *v})
			reaction = append(reaction, tracking.Call{Label: backtest.LabelFromReturn(e.Features.ReactionRetPct), RetPct: *v})
			if e.Features.EPSSurprisePct != nil {
				surprise = append(surprise, tracking.Call{Label: backtest.LabelFromReturn(*e.Features.EPSSurprisePct), RetPct: *v})
			}
			if e.Features.SUE != nil {
				sue = append(sue, tracking.Call{Label: sueLabel(*e.Features.SUE), RetPct: *v})
			}
		}
		out = append(out, HorizonReport{
			Label:    h.name,
			N:        len(actual),
			Baseline: tracking.Baseline(actual),
			Predictors: []tracking.PredictorStats{
				tracking.Summarize("rules (drift signals)", rules),
				tracking.Summarize("reaction-only", reaction),
				tracking.Summarize("surprise-only", surprise),
				tracking.Summarize("SUE-only", sue),
			},
		})
	}
	return out
}

// sueLabel calls a direction when the surprise is at least one σ.
func sueLabel(sue float64) string {
	switch {
	case sue >= 1:
		return "Positive"
	case sue <= -1:
		return "Negative"
	default:
		return "Neutral"
	}
}

// FormatEvaluation renders Evaluate's output as plain text.
func FormatEvaluation(reports []HorizonReport, nEvents int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Post-earnings drift evaluation — %d events\n", nEvents)
	for _, r := range reports {
		fmt.Fprintf(&b, "\n%s\n  N=%d with a completed window · baseline (most common actual label) %.0f%%\n",
			r.Label, r.N, r.Baseline*100)
		if r.N == 0 {
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(tracking.FormatStats(r.Predictors), "\n"), "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	b.WriteString("\nHit rate = Positive/Negative calls whose sign matched a >1% move. Treat any edge whose 95% CI\n" +
		"overlaps the baseline as unproven. The placebo row should sit near the baseline.\n")
	return b.String()
}
