package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// LLMRating is the JSON contract emitted by the sibling `llm-earnings-agent`
// Python project. It is rendered alongside the deterministic Recommendation
// but does not feed back into ComputeRecommendation — the two ratings stay
// independent until the LLM track record warrants mixing.
type LLMRating struct {
	Symbol     string         `json:"symbol"`
	AsOf       string         `json:"asof"`
	Rating     LLMRatingScore `json:"rating"`
	SubRatings LLMSubRatings  `json:"sub_ratings"`
	Metadata   LLMMetadata    `json:"metadata"`
}

type LLMRatingScore struct {
	Label      string   `json:"label"` // Positive | Negative | Neutral
	Score      float64  `json:"score"` // [-100, +100]
	Confidence float64  `json:"confidence"`
	TopReasons []string `json:"top_reasons"`
}

type LLMSubAnalysis struct {
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
	Reasoning  string  `json:"reasoning"`
}

type LLMSubRatings struct {
	Fundamentals *LLMSubAnalysis `json:"fundamentals,omitempty"`
	Transcript   *LLMSubAnalysis `json:"transcript,omitempty"`
	News         *LLMSubAnalysis `json:"news,omitempty"`
}

type LLMMetadata struct {
	Model            string  `json:"model"`
	Runtime          string  `json:"runtime"`
	PromptVersion    string  `json:"prompt_version"`
	Timestamp        string  `json:"timestamp"`
	CostEstimateUSD  float64 `json:"cost_estimate_usd"`
	TranscriptQuarter string `json:"transcript_quarter,omitempty"`
}

// llmAgentBinary is the command we shell out to. Override with the
// LLM_AGENT_BIN env var if installed somewhere non-standard.
func llmAgentBinary() string {
	if v := os.Getenv("LLM_AGENT_BIN"); v != "" {
		return v
	}
	return "llm-earnings-agent"
}

// FetchLLMRating runs the sibling Python agent against one symbol and parses
// its JSON response. Errors are surfaced to the caller; the enricher logs and
// swallows them so a single failed call never breaks the wider pipeline.
func FetchLLMRating(ctx context.Context, symbol string) (*LLMRating, error) {
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	bin := llmAgentBinary()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, "analyze", "--symbol", symbol, "--output", "json")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s exit %d: %s", bin, exitErr.ExitCode(), trimStderr(exitErr.Stderr))
		}
		return nil, fmt.Errorf("%s: %w", bin, err)
	}

	var r LLMRating
	if err := json.Unmarshal(out, &r); err != nil {
		return nil, fmt.Errorf("decode %s output: %w", bin, err)
	}
	return &r, nil
}

func trimStderr(b []byte) string {
	const max = 500
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// writeLLMRating renders the LLM rating block below the deterministic Rating
// block in a stock card. Kept format-similar so the eye can compare them.
func writeLLMRating(w io.Writer, r *LLMRating) {
	if r == nil {
		return
	}
	mark := "•"
	switch r.Rating.Label {
	case "Positive":
		mark = "▲"
	case "Negative":
		mark = "▼"
	}
	fmt.Fprintf(w, "LLM      %s %-9s  Score %+6.1f  Confidence %.0f%%  (%s)\n",
		mark, r.Rating.Label, r.Rating.Score, r.Rating.Confidence*100, r.Metadata.Model)
	for _, reason := range r.Rating.TopReasons {
		fmt.Fprintf(w, "         %s\n", reason)
	}
}
