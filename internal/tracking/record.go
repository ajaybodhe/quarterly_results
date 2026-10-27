// Package tracking keeps a running log of pre-earnings predictions and scores
// them against the reactions that actually happened, so the deterministic
// Rating and the LLM rating build a comparable track record over time.
package tracking

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/exchange"
)

// DefaultPath is the log file used when --predictions-file is not set.
const DefaultPath = "predictions.jsonl"

// Prediction is one predictor's call on an upcoming earnings reaction.
type Prediction struct {
	Label      string  `json:"label"`      // Positive / Negative / Neutral
	Score      float64 `json:"score"`      // [-100, +100]
	Confidence float64 `json:"confidence"` // [0, 1]
}

// SubPrediction is one LLM specialist agent's raw output.
type SubPrediction struct {
	Score      float64 `json:"score"`      // [-1, +1]
	Confidence float64 `json:"confidence"` // [0, 1]
}

// LLMPrediction is the LLM agent's aggregate call plus its per-agent scores.
type LLMPrediction struct {
	Prediction
	Agents        []string                 `json:"agents,omitempty"`
	Model         string                   `json:"model,omitempty"`
	PromptVersion string                   `json:"prompt_version,omitempty"` // pinned for drift monitoring
	Sub           map[string]SubPrediction `json:"sub,omitempty"`
}

// Prediction horizons. Records written before horizons existed have none and
// are reaction predictions.
const (
	HorizonReaction = "reaction" // pre-release call on the reaction-day move
	HorizonDrift    = "drift"    // post-reaction call on the next 5 / 20 trading days
	HorizonRelease  = "release"  // Signal 1: after the release, before the reaction-day close
	HorizonIntraday = "intraday" // Signal 2: at the reaction-day open, open→close minus SPY
)

// Record is one logged prediction for one stock's report. A stock is usually
// logged several times; scoring uses the latest reaction record made before
// the release, and the earliest drift record made after the reaction (see Score).
type Record struct {
	LoggedAt     time.Time      `json:"logged_at"`
	Horizon      string         `json:"horizon,omitempty"` // HorizonReaction (default) or HorizonDrift
	Symbol       string         `json:"symbol"`
	Exchange     string         `json:"exchange"`
	EarningsDate string         `json:"earnings_date"`           // YYYY-MM-DD
	EarningsTime string         `json:"earnings_time,omitempty"` // bmo / amc / ""
	ReactionDay  string         `json:"reaction_day,omitempty"`  // drift records: the reaction already observed
	SectorETF    string         `json:"sector_etf,omitempty"`    // drift records: benchmark for sector-adjusted drift
	Decile       int            `json:"decile,omitempty"`        // intraday records: Signal 2 decile (1..10)
	Source       string         `json:"source,omitempty"`        // release/intraday: where the EPS surprise came from (nasdaq / web (…))
	Rating       *Prediction    `json:"rating,omitempty"`        // Recommendation, or DriftRecommendation for drift records
	LLM          *LLMPrediction `json:"llm,omitempty"`
}

func (r Record) horizon() string {
	if r.Horizon == "" {
		return HorizonReaction
	}
	return r.Horizon
}

// NewRecord builds a log record from an assembled result. ok is false when the
// result isn't a pre-earnings prediction: no upcoming earnings date (the
// assembler falls back to the last past announcement) or no rating at all.
func NewRecord(r domain.EarningsResult, cfg exchange.Config, now time.Time) (Record, bool) {
	if r.EarningsDate == "" || (r.Recommendation == nil && r.LLMRating == nil) {
		return Record{}, false
	}
	if r.EarningsDate < now.In(location(cfg)).Format("2006-01-02") {
		return Record{}, false
	}
	rec := Record{
		LoggedAt:     now.UTC().Truncate(time.Second),
		Horizon:      HorizonReaction,
		Symbol:       r.Symbol,
		Exchange:     string(cfg.Exchange),
		EarningsDate: r.EarningsDate,
		EarningsTime: r.EarningsTime,
	}
	if rr := r.Recommendation; rr != nil {
		rec.Rating = &Prediction{Label: rr.Label, Score: rr.Score, Confidence: rr.Confidence}
	}
	rec.LLM = llmPrediction(r.LLMRating)
	return rec, true
}

// NewDriftRecord builds a drift-horizon record from a result that carries a
// post-reaction DriftRecommendation (a report released in the last few days).
// The LLM rating, read right after the release, is logged alongside it.
func NewDriftRecord(r domain.EarningsResult, cfg exchange.Config, now time.Time) (Record, bool) {
	f, d := r.DriftFeatures, r.DriftRecommendation
	if f == nil || d == nil {
		return Record{}, false
	}
	return Record{
		LoggedAt:     now.UTC().Truncate(time.Second),
		Horizon:      HorizonDrift,
		Symbol:       r.Symbol,
		Exchange:     string(cfg.Exchange),
		EarningsDate: f.AnnouncementDate,
		EarningsTime: r.EarningsTime,
		ReactionDay:  f.ReactionDay,
		SectorETF:    f.SectorETF,
		Rating:       &Prediction{Label: d.Label, Score: d.Score, Confidence: d.Confidence},
		LLM:          llmPrediction(r.LLMRating),
	}, true
}

func llmPrediction(l *domain.LLMRating) *LLMPrediction {
	if l == nil {
		return nil
	}
	p := &LLMPrediction{
		Prediction:    Prediction{Label: l.Rating.Label, Score: l.Rating.Score, Confidence: l.Rating.Confidence},
		Agents:        l.Metadata.Agents,
		Model:         l.Metadata.Model,
		PromptVersion: l.Metadata.PromptVersion,
		Sub:           map[string]SubPrediction{},
	}
	for name, sub := range map[string]*domain.LLMSubAnalysis{
		"fundamentals": l.SubRatings.Fundamentals,
		"transcript":   l.SubRatings.Transcript,
		"news":         l.SubRatings.News,
		"macro":        l.SubRatings.Macro,
		"dynamic":      l.SubRatings.Dynamic,
	} {
		if sub != nil {
			p.Sub[name] = SubPrediction{Score: sub.Score, Confidence: sub.Confidence}
		}
	}
	return p
}

// Log appends records to a JSON-lines file. Safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	path string
}

func NewLog(path string) *Log { return &Log{path: path} }

// Path returns the file the log writes to.
func (l *Log) Path() string { return l.path }

// Append writes one record as a single JSON line.
func (l *Log) Append(rec Record) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Load reads every record from a JSON-lines log. Blank lines are skipped.
func Load(path string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, n, err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// location returns the exchange's timezone, falling back to New York.
func location(cfg exchange.Config) *time.Location {
	if cfg.Timezone != "" {
		if loc, err := time.LoadLocation(cfg.Timezone); err == nil {
			return loc
		}
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.UTC
	}
	return loc
}
