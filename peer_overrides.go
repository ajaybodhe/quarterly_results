package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// peer_overrides.go layers a curated-then-discovered peer list on top of the
// SIC-based default in peers.go. SIC groups are too coarse (e.g. NBIS lands
// with MU/AMD/INTC instead of CRWV/NET/AKAM), so we keep an explicit map of
// ticker → product/market competitors.
//
// Resolution order for a given target symbol:
//   1. Manual entry in peer_overrides.json   (curated, never refreshed)
//   2. Cached Yahoo recommendationsbysymbol  (auto-populated, never re-fetched)
//   3. Cached LLM list via `claude -p`       (auto-populated, never re-fetched)
//   4. Fall through → existing SIC sector logic in peers.go
//
// Results from steps 2 and 3 are written back into peer_overrides.json so the
// network/LLM call only happens once per ticker.

const peerOverridesDefaultFile = "peer_overrides.json"

type peerOverride struct {
	Peers   []string `json:"peers"`
	Source  string   `json:"source"`  // "manual" | "yahoo" | "llm"
	Updated string   `json:"updated"` // YYYY-MM-DD
}

type peerOverrideStore struct {
	path    string
	mu      sync.Mutex
	entries map[string]peerOverride
}

func peerOverridesPath() string {
	if p := os.Getenv("PEER_OVERRIDES_FILE"); p != "" {
		return p
	}
	return peerOverridesDefaultFile
}

func loadPeerOverrideStore(path string) *peerOverrideStore {
	s := &peerOverrideStore{path: path, entries: map[string]peerOverride{}}
	f, err := os.Open(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logf("Warning: peer overrides %s unreadable: %v", path, err)
		}
		return s
	}
	defer f.Close()
	var m map[string]peerOverride
	if err := json.NewDecoder(f).Decode(&m); err != nil {
		logf("Warning: peer overrides %s parse error: %v", path, err)
		return s
	}
	for k, v := range m {
		s.entries[strings.ToUpper(k)] = v
	}
	return s
}

func (s *peerOverrideStore) get(symbol string) ([]string, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[strings.ToUpper(symbol)]; ok && len(e.Peers) > 0 {
		return append([]string(nil), e.Peers...), e.Source, true
	}
	return nil, "", false
}

func (s *peerOverrideStore) put(symbol string, peers []string, source string) {
	if len(peers) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[strings.ToUpper(symbol)] = peerOverride{
		Peers:   peers,
		Source:  source,
		Updated: time.Now().Format("2006-01-02"),
	}
	s.saveLocked()
}

// saveLocked writes the store atomically. Caller must hold s.mu.
func (s *peerOverrideStore) saveLocked() {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		logf("Warning: peer overrides save failed: %v", err)
		return
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.entries); err != nil {
		f.Close()
		os.Remove(tmp)
		logf("Warning: peer overrides encode failed: %v", err)
		return
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		logf("Warning: peer overrides close failed: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		logf("Warning: peer overrides rename failed: %v", err)
	}
}

// resolvePeerOverrides returns (peers, source) for the symbol, going through
// cache → Yahoo → LLM. Returns (nil, "") to signal "fall through to SIC".
func resolvePeerOverrides(
	ctx context.Context,
	store *peerOverrideStore,
	symbol string,
	yahooClient *http.Client,
) ([]string, string) {
	if peers, src, ok := store.get(symbol); ok {
		return peers, src
	}
	if peers := fetchYahooSimilarPeers(ctx, symbol, yahooClient); len(peers) > 0 {
		store.put(symbol, peers, "yahoo")
		return peers, "yahoo"
	}
	if peers := fetchLLMPeers(ctx, symbol); len(peers) > 0 {
		store.put(symbol, peers, "llm")
		return peers, "llm"
	}
	return nil, ""
}

// ── Yahoo recommendationsbysymbol ────────────────────────────────────────────
//
// Returns Yahoo's "people who view this also view…" list, which is empirically
// close to product competitors for liquid US tickers. No crumb required; we
// pass the yahooClient anyway so cookies are shared with the rest of the app.

func fetchYahooSimilarPeers(ctx context.Context, symbol string, client *http.Client) []string {
	url := fmt.Sprintf("https://query2.finance.yahoo.com/v6/finance/recommendationsbysymbol/%s", symbol)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	var body struct {
		Finance struct {
			Result []struct {
				RecommendedSymbols []struct {
					Symbol string `json:"symbol"`
				} `json:"recommendedSymbols"`
			} `json:"result"`
		} `json:"finance"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	if len(body.Finance.Result) == 0 {
		return nil
	}
	target := strings.ToUpper(symbol)
	seen := map[string]bool{}
	var peers []string
	for _, r := range body.Finance.Result[0].RecommendedSymbols {
		s := strings.ToUpper(strings.TrimSpace(r.Symbol))
		if s == "" || s == target || seen[s] {
			continue
		}
		seen[s] = true
		peers = append(peers, s)
	}
	return peers
}

// ── LLM peers via `claude -p` ────────────────────────────────────────────────
//
// Shells out to the Claude Code CLI (already on PATH for users of the
// --llm-rating flag). Output is a free-form text response; we extract the
// first JSON array of strings.

var llmPeerArrayRegex = regexp.MustCompile(`\[\s*"[^"]+"(?:\s*,\s*"[^"]+")*\s*\]`)

func fetchLLMPeers(ctx context.Context, symbol string) []string {
	prompt := fmt.Sprintf(
		"List 5 to 8 US-listed public stock tickers that are the closest product or market competitors of %s. "+
			"Focus on direct business competition, not broad sector membership. "+
			"Output ONLY a JSON array of ticker symbols (no markdown fence, no prose). "+
			"Example: [\"AAA\",\"BBB\",\"CCC\"]",
		symbol,
	)
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", prompt, "--output-format", "text")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	m := llmPeerArrayRegex.Find(out)
	if m == nil {
		return nil
	}
	var raw []string
	if err := json.Unmarshal(m, &raw); err != nil {
		return nil
	}
	target := strings.ToUpper(symbol)
	seen := map[string]bool{}
	var peers []string
	for _, p := range raw {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || p == target || seen[p] {
			continue
		}
		seen[p] = true
		peers = append(peers, p)
	}
	return peers
}
