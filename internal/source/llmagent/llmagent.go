package llmagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"quarterly_results/internal/domain"
)

// Agent names understood by `llm-earnings-agent analyze --agents`.
const (
	AgentFundamentals = "fundamentals"
	AgentTranscript   = "transcript"
	AgentNews         = "news"
	AgentMacro        = "macro"
	AgentDynamic      = "dynamic"
)

// AllAgents lists every specialist agent in the Python project's canonical order.
var AllAgents = []string{AgentFundamentals, AgentTranscript, AgentNews, AgentMacro, AgentDynamic}

// DefaultAgents are the agents that read inputs this repo doesn't have (the
// latest earnings press release and recent news headlines). The other three
// re-score fields the deterministic signals already cover, so they are opt-in.
var DefaultAgents = []string{AgentTranscript, AgentNews}

// payloadAgents read this repo's JSON result for the stock. When any of them
// runs, the result is sent on stdin so the Python side doesn't re-run
// quarterly_results itself.
var payloadAgents = map[string]bool{AgentFundamentals: true, AgentMacro: true, AgentDynamic: true}

// siblingAgentBinary is where `uv pip install -e .` puts the CLI in the
// sibling llm_earnings_agent checkout, relative to this repo's root.
const siblingAgentBinary = "../llm_earnings_agent/.venv/bin/llm-earnings-agent"

// Options controls one FetchRating call.
type Options struct {
	// Agents to run; empty means DefaultAgents.
	Agents []string
	// Payload is this stock's quarterly_results JSON result. Required when
	// any payload-reading agent is selected (see NeedsPayload).
	Payload []byte
}

// ParseAgents parses a --llm-agents value: a comma-separated subset of
// AllAgents, or "all". An empty value selects DefaultAgents.
func ParseAgents(spec string) ([]string, error) {
	spec = strings.TrimSpace(strings.ToLower(spec))
	switch spec {
	case "":
		return DefaultAgents, nil
	case "all":
		return AllAgents, nil
	}
	want := map[string]bool{}
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part != "" {
			want[part] = true
		}
	}
	var out []string
	for _, a := range AllAgents {
		if want[a] {
			out = append(out, a)
			delete(want, a)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for a := range want {
			unknown = append(unknown, a)
		}
		return nil, fmt.Errorf("unknown agent(s) %s — choose from %s or all",
			strings.Join(unknown, ","), strings.Join(AllAgents, ","))
	}
	if len(out) == 0 {
		return DefaultAgents, nil
	}
	return out, nil
}

// NeedsPayload reports whether any selected agent reads this repo's JSON
// result, i.e. whether FetchRating must wait until the stock is enriched.
func NeedsPayload(agents []string) bool {
	for _, a := range effectiveAgents(agents) {
		if payloadAgents[a] {
			return true
		}
	}
	return false
}

func effectiveAgents(agents []string) []string {
	if len(agents) == 0 {
		return DefaultAgents
	}
	return agents
}

// llmAgentBinary is the command we shell out to: LLM_AGENT_BIN if set, else
// llm-earnings-agent on PATH, else the sibling checkout's virtualenv.
func llmAgentBinary() string {
	if v := os.Getenv("LLM_AGENT_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("llm-earnings-agent"); err == nil {
		return p
	}
	if _, err := os.Stat(siblingAgentBinary); err == nil {
		return siblingAgentBinary
	}
	return "llm-earnings-agent"
}

// FetchRating runs the sibling Python agent against one symbol and parses
// its JSON response. Errors are surfaced to the caller; the enricher logs and
// swallows them so a single failed call never breaks the wider pipeline.
func FetchRating(ctx context.Context, symbol string, opts Options) (*domain.LLMRating, error) {
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	agents := effectiveAgents(opts.Agents)
	needsPayload := NeedsPayload(agents)
	if needsPayload && len(opts.Payload) == 0 {
		return nil, fmt.Errorf("agents %s need the stock's JSON payload", strings.Join(agents, ","))
	}
	bin := llmAgentBinary()

	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	args := []string{"analyze", "--symbol", symbol, "--output", "json", "--agents", strings.Join(agents, ",")}
	if needsPayload {
		args = append(args, "--payload", "-")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if needsPayload {
		cmd.Stdin = bytes.NewReader(opts.Payload)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%s exit %d: %s", bin, exitErr.ExitCode(), trimStderr(exitErr.Stderr))
		}
		return nil, fmt.Errorf("%s: %w", bin, err)
	}

	var r domain.LLMRating
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
