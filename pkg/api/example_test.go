// Package api_test exercises the pkg/api public surface from the outside.
// This file contains shared construction helpers and the five runnable
// ExampleClient_* functions that mirror the README's Verb examples section.
package api_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// ── Shared helpers ────────────────────────────────────────────────────────────

// buildClient is the construction core shared by exampleClient and mustClient.
// It calls store.OpenOrInit at storePath (creating schema on first call),
// then opens an api.Client against the same path, wiring a tmuxfix.Recorder
// as the TmuxClient injection. Both the store and client hold independent
// connections to the same SQLite file; callers must close both.
func buildClient(storePath string) (*api.Client, *store.Store, *tmuxfix.Recorder, error) {
	s, err := store.OpenOrInit(storePath)
	if err != nil {
		return nil, nil, nil, err
	}
	rec := tmuxfix.NewRecorder()
	c, err := api.New(api.Options{
		StorePath:  storePath,
		TmuxClient: rec,
	})
	if err != nil {
		_ = s.Close()
		return nil, nil, nil, err
	}
	return c, s, rec, nil
}

// exampleClient opens a fresh isolated store, wires a tmuxfix.Recorder, and
// constructs an api.Client — all without a *testing.T. Used exclusively by
// ExampleClient_* functions whose signature is func ExampleX() and cannot
// receive testing.TB.
//
// Uses os.MkdirTemp rather than t.TempDir. Panics on any construction failure;
// a panic during go test surfaces as a clear example-failed result with a stack.
//
// Returns (client, store, recorder, cleanup). Callers must defer cleanup.
// The store is the seeding handle for example bodies: call s.InsertPending and
// s.ApplyHookTransition directly, panicking on error (matching this helper's
// panic-on-failure contract).
func exampleClient() (*api.Client, *store.Store, *tmuxfix.Recorder, func()) {
	tmpDir, err := os.MkdirTemp("", "pkg-api-example-*")
	if err != nil {
		panic("exampleClient: MkdirTemp: " + err.Error())
	}
	path := filepath.Join(tmpDir, "state.db")
	c, s, rec, err := buildClient(path)
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		panic("exampleClient: buildClient: " + err.Error())
	}
	return c, s, rec, func() {
		_ = c.Close()
		_ = s.Close()
		_ = os.RemoveAll(tmpDir)
	}
}

// mustClient opens a fresh isolated store and constructs an api.Client for
// TestX-style functions that DO receive a *testing.T. Cleanup is registered
// via t.Cleanup; callers do not need to close either resource explicitly.
func mustClient(t *testing.T) (*api.Client, *tmuxfix.Recorder) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	c, s, rec, err := buildClient(path)
	if err != nil {
		t.Fatalf("mustClient: buildClient: %v", err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		_ = s.Close()
	})
	return c, rec
}

// seedRow inserts a Spawn row at the given state into s without *testing.T.
// Panics on any store error (example-function contract). labels may be nil.
func seedRow(s *store.Store, id, state string, labels map[string]string) {
	sp := store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  "sess-" + id,
		RelayMode:        "off",
		Labels:           labels,
	}
	if err := s.InsertPending(sp); err != nil {
		panic("seedRow: InsertPending " + id + ": " + err.Error())
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			panic("seedRow: ApplyHookTransition " + id + "→" + state + ": " + err.Error())
		}
	}
}

// ── Runnable examples ─────────────────────────────────────────────────────────
//
// Each ExampleClient_* function wraps its consumer-facing snippet in
// // README:start <name> / // README:end markers. TestREADMEExamplesStayInSync
// (readme_sync_test.go) extracts those regions and diffs them against the
// corresponding Go code block in pkg/api/README.md. Any divergence is a
// test failure — update both the example body and the README together.

// ExampleClient_Spawn demonstrates launching a tracked Claude Code instance.
// SpawnParams.ClaudeInstanceID is seeded to a fixed value so the output is
// deterministic. In production usage omit it and the library mints a UUID4.
func ExampleClient_Spawn() {
	c, _, _, cleanup := exampleClient()
	defer cleanup()
	// README:start ExampleClient_Spawn
	result, err := c.Spawn(api.SpawnParams{
		CWD:              "/tmp",
		RelayMode:        "on",
		ClaudeInstanceID: "claude_example",
		AgentDirectorLabels: map[string]string{
			"project": "widget",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.ClaudeInstanceID)
	// README:end

	// Output:
	// claude_example
}

// ExampleClient_Status demonstrates reading the current lifecycle state of a
// tracked Spawn. The row is seeded in pending state using the instance ID from
// the README example so the labeled region can reference the literal ID string.
func ExampleClient_Status() {
	c, s, _, cleanup := exampleClient()
	defer cleanup()
	// Seed the README's example instance ID in pending state.
	seedRow(s, "claude_2026-05-22T18-23-15", store.StatePending, nil)
	// README:start ExampleClient_Status
	res, err := c.Status("claude_2026-05-22T18-23-15")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.State) // e.g. "waiting"
	// README:end

	// Output:
	// pending
}

// ExampleClient_List demonstrates filtering Spawn rows by state and label.
// Two rows with distinct states are seeded so the filter returns both;
// Unordered output is used because List makes no ordering guarantee.
func ExampleClient_List() {
	c, s, _, cleanup := exampleClient()
	defer cleanup()
	// Seed two rows with the project=widget label in different live states.
	seedRow(s, "list-waiting-example", store.StateWaiting,
		map[string]string{"project": "widget"})
	seedRow(s, "list-working-example", store.StateWorking,
		map[string]string{"project": "widget"})
	// README:start ExampleClient_List
	res, err := c.List(api.ListParams{
		State:  []string{"waiting", "working"},
		Labels: []string{"project=widget"},
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, row := range res.Spawns {
		fmt.Println(row.ClaudeInstanceID, row.State)
	}
	// README:end

	// Unordered output:
	// list-waiting-example waiting
	// list-working-example working
}

// ExampleClient_SendKeys demonstrates delivering text to a Spawn's tmux pane.
// The CR-strip behavior (SRD §4.3) is always applied before delivery;
// Enter is always appended to submit the composed buffer (press_enter=true).
func ExampleClient_SendKeys() {
	c, s, rec, cleanup := exampleClient()
	defer cleanup()
	// Seed a live Spawn in waiting state (interactive — required by SendKeys).
	seedRow(s, "claude_2026-05-22T18-23-15", store.StateWaiting, nil)
	// README:start ExampleClient_SendKeys
	_, err := c.SendKeys(api.SendKeysParams{
		ClaudeInstanceID: "claude_2026-05-22T18-23-15",
		Text:             "what is 2+2?",
	})
	if err != nil {
		log.Fatal(err)
	}
	// README:end
	// The recorder captures what tmux actually received. pressEnter is always
	// true — SendKeys always appends an Enter to submit the composed buffer.
	calls := rec.CallsOfKind(tmuxfix.CallSendKeys)
	fmt.Println(calls[0].PressEnter)
	// Output:
	// true
}

// ExampleClient_Kill demonstrates terminating a Spawn's tmux session.
// Kill is idempotent on terminal states (ended/missing).
func ExampleClient_Kill() {
	c, s, _, cleanup := exampleClient()
	defer cleanup()
	// Seed a live Spawn in working state.
	seedRow(s, "claude_2026-05-22T18-23-15", store.StateWorking, nil)
	// README:start ExampleClient_Kill
	_, err := c.Kill(api.KillParams{
		ClaudeInstanceID: "claude_2026-05-22T18-23-15",
	})
	if err != nil {
		log.Fatal(err)
	}
	// README:end

	// Output:
}
