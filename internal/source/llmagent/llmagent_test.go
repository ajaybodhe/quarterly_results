package llmagent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAgents(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", DefaultAgents},
		{"all", AllAgents},
		{" ALL ", AllAgents},
		{"news,transcript", []string{AgentTranscript, AgentNews}}, // canonical order
		{"dynamic, fundamentals ,", []string{AgentFundamentals, AgentDynamic}},
	}
	for _, c := range cases {
		got, err := ParseAgents(c.in)
		if err != nil {
			t.Fatalf("ParseAgents(%q): %v", c.in, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseAgents(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	if _, err := ParseAgents("transcript,sentiment"); err == nil || !strings.Contains(err.Error(), "sentiment") {
		t.Errorf("expected unknown-agent error, got %v", err)
	}
}

func TestNeedsPayload(t *testing.T) {
	if NeedsPayload(nil) {
		t.Error("default agents (transcript,news) should not need the payload")
	}
	if NeedsPayload([]string{AgentTranscript, AgentNews}) {
		t.Error("transcript,news should not need the payload")
	}
	for _, a := range []string{AgentFundamentals, AgentMacro, AgentDynamic} {
		if !NeedsPayload([]string{AgentNews, a}) {
			t.Errorf("%s should need the payload", a)
		}
	}
}

// fakeAgent installs a shell script as LLM_AGENT_BIN that records its argv and
// stdin into dir and prints a minimal valid response.
func fakeAgent(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	body := `#!/bin/sh
printf '%s\n' "$@" > "` + dir + `/args"
cat > "` + dir + `/stdin"
echo '{"symbol":"AAPL","asof":"2026-10-16","rating":{"label":"Positive","score":20,"confidence":0.5,"top_reasons":[]},"sub_ratings":{},"metadata":{"model":"m","runtime":"r","prompt_version":"v1","timestamp":"t","cost_estimate_usd":0,"agents":["transcript","news"]}}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LLM_AGENT_BIN", script)
	return dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFetchRating_TextAgentsSendNoPayload(t *testing.T) {
	dir := fakeAgent(t)
	r, err := FetchRating(context.Background(), "AAPL", Options{})
	if err != nil {
		t.Fatalf("FetchRating: %v", err)
	}
	if r.Rating.Label != "Positive" || !reflect.DeepEqual(r.Metadata.Agents, []string{"transcript", "news"}) {
		t.Errorf("unexpected decode: %+v", r)
	}
	args := readFile(t, filepath.Join(dir, "args"))
	if !strings.Contains(args, "--agents\ntranscript,news\n") {
		t.Errorf("expected default agents in args, got:\n%s", args)
	}
	if strings.Contains(args, "--payload") {
		t.Errorf("text-only agents must not request a payload, got:\n%s", args)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != "" {
		t.Errorf("stdin should be empty, got %q", got)
	}
}

func TestFetchRating_PayloadAgentsGetStdin(t *testing.T) {
	dir := fakeAgent(t)
	payload := []byte(`{"symbol":"AAPL","rsi14":"55.0"}`)
	if _, err := FetchRating(context.Background(), "AAPL", Options{Agents: AllAgents, Payload: payload}); err != nil {
		t.Fatalf("FetchRating: %v", err)
	}
	args := readFile(t, filepath.Join(dir, "args"))
	if !strings.Contains(args, "--payload\n-\n") || !strings.Contains(args, "--agents\nfundamentals,transcript,news,macro,dynamic\n") {
		t.Errorf("unexpected args:\n%s", args)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != string(payload) {
		t.Errorf("stdin = %q, want %q", got, payload)
	}
}

func TestFetchRating_PayloadAgentsRequirePayload(t *testing.T) {
	fakeAgent(t)
	if _, err := FetchRating(context.Background(), "AAPL", Options{Agents: []string{AgentDynamic}}); err == nil {
		t.Error("expected error when a payload agent is selected without a payload")
	}
}
