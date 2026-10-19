// Package pead calls the sibling llm-earnings-agent's `pead live` command,
// which scores released reports with the post-release signals (Signal 1 at
// the release, Signal 2 at the reaction-day open) trained on the
// --build-dataset event history.
package pead

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/source/llmagent"
)

// Score sends results to `pead live --output json` and returns the signals
// keyed by upper-case symbol. Results without a consensus surprise are
// ignored by the agent.
func Score(ctx context.Context, eventsPath string, results []domain.EarningsResult) (map[string]domain.PostRelease, error) {
	payload, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	bin := llmagent.Binary()
	cmd := exec.CommandContext(ctx, bin, "pead", "live", "--events", eventsPath, "--payload", "-", "--output", "json")
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if len(msg) > 500 {
				msg = msg[len(msg)-500:]
			}
			return nil, fmt.Errorf("%s pead live exit %d: %s", bin, exitErr.ExitCode(), msg)
		}
		return nil, fmt.Errorf("%s pead live: %w", bin, err)
	}
	var signals []domain.PostRelease
	if err := json.Unmarshal(out, &signals); err != nil {
		return nil, fmt.Errorf("decode pead live output: %w", err)
	}
	m := make(map[string]domain.PostRelease, len(signals))
	for _, s := range signals {
		m[strings.ToUpper(s.Symbol)] = s
	}
	return m, nil
}
