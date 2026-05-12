package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerOverrideStore_LoadAndCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peer_overrides.json")

	seed := map[string]peerOverride{
		"NBIS": {Peers: []string{"CRWV", "NET", "AKAM"}, Source: "manual", Updated: "2026-05-12"},
	}
	b, _ := json.Marshal(seed)
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	store := loadPeerOverrideStore(path)
	peers, src, ok := store.get("nbis") // lookup is case-insensitive
	if !ok {
		t.Fatalf("expected NBIS entry to load")
	}
	if src != "manual" {
		t.Errorf("source = %q, want manual", src)
	}
	if len(peers) != 3 || peers[0] != "CRWV" {
		t.Errorf("peers = %v, want [CRWV NET AKAM]", peers)
	}

	// New entry should be persisted on put().
	store.put("DT", []string{"DDOG", "ESTC"}, "llm")
	reloaded := loadPeerOverrideStore(path)
	got, src, ok := reloaded.get("DT")
	if !ok || src != "llm" || len(got) != 2 || got[0] != "DDOG" {
		t.Errorf("after reload: peers=%v src=%q ok=%v", got, src, ok)
	}

	// Manual seed must still be present after the put().
	if _, _, ok := reloaded.get("NBIS"); !ok {
		t.Errorf("NBIS entry lost after put()")
	}
}

func TestPeerOverrideStore_MissingFile(t *testing.T) {
	store := loadPeerOverrideStore(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if _, _, ok := store.get("X"); ok {
		t.Errorf("expected miss on empty store")
	}
	// put() must create the file.
	store.put("X", []string{"Y", "Z"}, "yahoo")
	if _, _, ok := store.get("X"); !ok {
		t.Errorf("expected hit after put")
	}
}

func TestPeerOverrideStore_EmptyPeersIgnored(t *testing.T) {
	store := loadPeerOverrideStore(filepath.Join(t.TempDir(), "peer_overrides.json"))
	store.put("X", nil, "yahoo")
	if _, _, ok := store.get("X"); ok {
		t.Errorf("empty peer list should not be cached")
	}
}
