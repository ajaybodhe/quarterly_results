package llmagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"quarterly_results/internal/domain"
)

// llmAgentBinary is the command we shell out to. Override with the
// LLM_AGENT_BIN env var if installed somewhere non-standard.
func llmAgentBinary() string {
	if v := os.Getenv("LLM_AGENT_BIN"); v != "" {
		return v
	}
	return "llm-earnings-agent"
}

// FetchRating runs the sibling Python agent against one symbol and parses
// its JSON response. Errors are surfaced to the caller; the enricher logs and
// swallows them so a single failed call never breaks the wider pipeline.
func FetchRating(ctx context.Context, symbol string) (*domain.LLMRating, error) {
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
