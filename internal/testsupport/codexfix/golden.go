package codexfix

import (
	"encoding/json"
	"os"
	"sort"
	"testing"
)

// HookTrustGolden is internal/spawn/testdata/hook-trust-golden.json: the key
// and trust hash Codex itself lists for every hook entry AD composes (see
// internal/spawn/testdata/PROVENANCE.md). The loader only reads the file; it
// computes nothing, so a test that compares AD's composed keys and hashes
// against it compares against Codex's own listing.
type HookTrustGolden struct {
	// Versions are the Codex versions the capture agrees on.
	Versions []string `json:"versions"`
	// KeySource is the literal source segment of every key.
	KeySource string `json:"keySource"`
	// BinaryPaths and InstalledPaths are the paths the scenarios use, by path
	// name ("plain", "special"): the running binary's path (hook entries) and
	// the installed path (help entry).
	BinaryPaths    map[string]string `json:"binaryPaths"`
	InstalledPaths map[string]string `json:"installedPaths"`
	// Scenarios are keyed by scenario name (plain-same-group-limit4000, ...).
	Scenarios map[string]GoldenScenario `json:"scenarios"`
}

// GoldenScenario is one capture: a path, a help-entry layout, a relay window.
type GoldenScenario struct {
	// Path is "plain" or "special" (BinaryPaths and InstalledPaths key).
	Path string `json:"path"`
	// Layout is absent, same-group, own-group, same-group-help-first or
	// own-group-help-first ("absent": no help entry).
	Layout string `json:"layout"`
	// HelpLimit is the help entry's additionalContextLimit; nil for none.
	HelpLimit *int `json:"helpLimit"`
	// RelayWindow is the PreToolUse and PermissionRequest timeout.
	RelayWindow int `json:"relayWindow"`
	// ToolMatcher is the matcher on the three tool events; nil for none (the
	// form AD uses).
	ToolMatcher *string `json:"toolMatcher"`
	Note        string  `json:"note"`
	// Entries are the listed handlers, one per hook handler.
	Entries []GoldenEntry `json:"entries"`
}

// GoldenEntry is one handler as Codex lists it.
type GoldenEntry struct {
	// Kind is "ad-hook" or "help".
	Kind  string `json:"kind"`
	Event string `json:"event"`
	// Group and Handler are the 0-based positions the key carries.
	Group   int    `json:"group"`
	Handler int    `json:"handler"`
	Command string `json:"command"`
	// Matcher is the group's matcher; nil for none.
	Matcher *string `json:"matcher"`
	// TimeoutPassed is the timeout the launch passed (nil: none passed);
	// TimeoutSec is the timeout Codex lists (and hashes), normalised.
	TimeoutPassed *int `json:"timeoutPassed"`
	TimeoutSec    int  `json:"timeoutSec"`
	// AdditionalContextLimitPassed is the limit passed (nil: none);
	// AdditionalContextLimit is what Codex lists.
	AdditionalContextLimitPassed *int `json:"additionalContextLimitPassed"`
	AdditionalContextLimit       *int `json:"additionalContextLimit"`
	// Key and Hash are Codex's own key and currentHash.
	Key  string `json:"key"`
	Hash string `json:"hash"`
	// Identity is the JSON text that is hashed (as recompute.py computes it).
	Identity string `json:"identity"`
}

// LoadHookTrustGolden reads hook-trust-golden.json from path (a test in
// internal/spawn passes "testdata/hook-trust-golden.json").
func LoadHookTrustGolden(t testing.TB, path string) HookTrustGolden {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("codexfix.LoadHookTrustGolden: %v", err)
	}
	var g HookTrustGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatalf("codexfix.LoadHookTrustGolden: decode %s: %v", path, err)
	}
	return g
}

// ScenarioNames returns the scenario names, sorted.
func (g HookTrustGolden) ScenarioNames() []string {
	out := make([]string, 0, len(g.Scenarios))
	for n := range g.Scenarios {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KeyHashes returns the scenario's key -> hash map, the shape of a launch's
// hooks.state (Launch.State).
func (s GoldenScenario) KeyHashes() map[string]string {
	m := make(map[string]string, len(s.Entries))
	for _, e := range s.Entries {
		m[e.Key] = e.Hash
	}
	return m
}
