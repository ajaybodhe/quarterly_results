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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"quarterly_results/internal/domain"
	"quarterly_results/internal/source/llmagent"
)

// Score sends results to `pead live --output json` and returns the signals
// keyed by upper-case symbol. Results without a consensus surprise are
// ignored by the agent — unless web is set, in which case the agent first
// reads the surprise (and guidance) for released reports from the web.
func Score(ctx context.Context, eventsPath string, results []domain.EarningsResult, web bool) (map[string]domain.PostRelease, error) {
	payload, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	timeout := 10 * time.Minute
	args := []string{"pead", "live", "--events", eventsPath, "--payload", "-", "--output", "json"}
	if web {
		timeout = 30 * time.Minute // LLM + web search, ~2 min per report, 3 at a time
		args = append(args, "--web", "--web-cache", filepath.Join(filepath.Dir(eventsPath), "web_earnings"))
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	bin := llmagent.Binary()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = os.Stderr // progress and web-backup notes
	cmd.Stdin = bytes.NewReader(payload)
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if len(msg) > 500 {
				msg = msg[len(msg)-500:]
			}
			if msg == "" {
				msg = "see its output above"
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
