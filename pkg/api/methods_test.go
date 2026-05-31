package api_test

// methods_test.go covers the 15 Client verb methods added in Task 2.
//
// Organisation:
//   A. TestAllVerbsReturnErrClientClosedAfterClose — table-driven; every verb on
//      a closed client must return ErrClientClosed.
//   B. Per-verb happy-path / delegation tests (one per verb).
//   C. TestListFacadePreservesErrListInvalidLabel — verifies that errors.Is
//      matching across the pkg/api → internal/api boundary is not broken.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// newTestClient creates a Client backed by a fresh temp store. StorePath and
// ConfigPath are explicit absolute paths so tilde expansion via user.Current()
// is bypassed. HOME is still overridden via t.Setenv so operations that use
// os.UserHomeDir() (e.g. MakeTemplate) land in the temp dir.
func newTestClient(t *testing.T) (*api.Client, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "state.db")
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("newTestClient write config: %v", err)
	}
	c, err := api.New(api.Options{
		StorePath:       dbPath,
		ConfigPath:      cfgPath,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("newTestClient api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, home
}

// newTestClientWithRows creates a Client whose backing store has been
// pre-seeded by seedFn before the Client is opened. seedFn receives the
// absolute DB path and is responsible for opening, inserting, and closing the
// store.
func newTestClientWithRows(t *testing.T, seedFn func(dbPath string)) (*api.Client, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "state.db")
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("newTestClientWithRows write config: %v", err)
	}
	// Seed BEFORE opening the Client so the Client sees the rows on startup.
	seedFn(dbPath)
	c, err := api.New(api.Options{
		StorePath:  dbPath,
		ConfigPath: cfgPath,
	})
	if err != nil {
		t.Fatalf("newTestClientWithRows api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, home
}

// insertRow opens the store at dbPath, inserts one Spawn row with the given id,
// sessionName, and state, then closes the store. relay_mode is always "off".
func insertRow(t *testing.T, dbPath, id, sessionName, state string) {
	t.Helper()
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("insertRow OpenOrInit(%q): %v", dbPath, err)
	}
	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  sessionName,
		RelayMode:        "off",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("insertRow InsertPending(%s): %v", id, err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			_ = s.Close()
			t.Fatalf("insertRow ApplyHookTransition(%s→%s): %v", id, state, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("insertRow Close: %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Part A — ErrClientClosed table test
// ─────────────────────────────────────────────────────────────────────────────

// TestAllVerbsReturnErrClientClosedAfterClose is the primary contract test for
// the closed-flag guard: every verb method, called on a Client that has already
// been closed, must return ErrClientClosed and nothing else. Zero-value params
// are intentional — the guard fires before any param inspection.
func TestAllVerbsReturnErrClientClosedAfterClose(t *testing.T) {
	c, _ := newTestClient(t)
	// Close explicitly now; the t.Cleanup-registered Close becomes a no-op
	// because Close is idempotent.
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"Version", func() error { _, err := c.Version(); return err }},
		{"Spawn", func() error { _, err := c.Spawn(api.SpawnParams{}); return err }},
		{"Status", func() error { _, err := c.Status(""); return err }},
		{"Get", func() error { _, err := c.Get(""); return err }},
		{"List", func() error { _, err := c.List(api.ListParams{}); return err }},
		{"SendKeys", func() error { _, err := c.SendKeys(api.SendKeysParams{}); return err }},
		{"ReadPane", func() error { _, err := c.ReadPane(api.ReadPaneParams{}); return err }},
		{"Kill", func() error { _, err := c.Kill(api.KillParams{}); return err }},
		{"Pause", func() error { _, err := c.Pause(ctx, api.PauseParams{}); return err }},
		{"Decide", func() error { _, err := c.Decide(api.DecideParams{}); return err }},
		{"Resume", func() error { _, err := c.Resume(api.ResumeParams{}); return err }},
		{"FindMissing", func() error { _, err := c.FindMissing(ctx); return err }},
		{"Expire", func() error { _, err := c.Expire(nil); return err }},
		{"Delete", func() error { _, err := c.Delete(nil); return err }},
		{"MakeTemplate", func() error { _, err := c.MakeTemplate(api.MakeTemplateParams{}); return err }},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if !errors.Is(err, api.ErrClientClosed) {
				t.Errorf("%s on closed client: got %v; want ErrClientClosed", tc.name, err)
			}
		})
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Part B — per-verb happy-path / delegation tests
// ─────────────────────────────────────────────────────────────────────────────

// TestVersionHappy verifies Version delegates and returns a populated result.
// Version and Commit may be empty strings in test builds (no ldflags); the test
// asserts only that no error is returned.
func TestVersionHappy(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
}

// TestSpawnDelegation proves Spawn delegates to internal/api. An empty CWD
// is a known-bad input that exercises pkg/api.Client.Spawn →
// internal/api.Spawn → spawn.Validate before any tmux interaction, returning
// spawn.ErrCwdMissing.
func TestSpawnDelegation(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.Spawn(api.SpawnParams{}) // empty CWD → ErrCwdMissing
	if !errors.Is(err, spawn.ErrCwdMissing) {
		t.Fatalf("Spawn(empty CWD): got %v; want spawn.ErrCwdMissing", err)
	}
}

// TestStatusHappy verifies Status returns the row's state for a known id.
func TestStatusHappy(t *testing.T) {
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-st-1", "cd-st-1", store.StatePending)
	})
	res, err := c.Status("id-st-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if res.State != store.StatePending {
		t.Errorf("State = %q; want %q", res.State, store.StatePending)
	}
}

// TestGetHappy verifies Get returns the full spawn row for a known id.
func TestGetHappy(t *testing.T) {
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-get-1", "cd-get-1", store.StatePending)
	})
	row, err := c.Get("id-get-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.ClaudeInstanceID != "id-get-1" {
		t.Errorf("ClaudeInstanceID = %q; want id-get-1", row.ClaudeInstanceID)
	}
}

// TestListEmptyStoreJSONStability verifies the JSON-stability invariant:
// List against an empty store returns a non-nil Spawns slice (encodes as []
// not null). Library callers and jq pipelines depend on this.
func TestListEmptyStoreJSONStability(t *testing.T) {
	c, _ := newTestClient(t)
	res, err := c.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Spawns == nil {
		t.Fatal("Spawns is nil; want non-nil empty slice (JSON-stability invariant)")
	}
	if len(res.Spawns) != 0 {
		t.Errorf("len(Spawns) = %d; want 0", len(res.Spawns))
	}
}

// TestSendKeysDelegation proves SendKeys delegates to internal/api. An unknown
// id causes internal/api.SendKeys to return store.ErrSpawnNotFound from the
// store lookup — no tmux call is needed to exercise the delegation path.
func TestSendKeysDelegation(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.SendKeys(api.SendKeysParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("SendKeys(absent id): got %v; want store.ErrSpawnNotFound", err)
	}
}

// TestReadPaneDelegation proves ReadPane delegates to internal/api. An unknown
// id causes internal/api.ReadPane to return store.ErrSpawnNotFound before any
// tmux capture is attempted.
func TestReadPaneDelegation(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.ReadPane(api.ReadPaneParams{ClaudeInstanceID: "absent"})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("ReadPane(absent id): got %v; want store.ErrSpawnNotFound", err)
	}
}

// TestKillEndedRowHappy verifies Kill on a terminal-state row is a no-op
// success: the post-condition ("session gone") is already met, so Kill returns
// nil without invoking tmux. This validates the idempotency contract.
func TestKillEndedRowHappy(t *testing.T) {
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-k-ended", "cd-k-ended", store.StateEnded)
	})
	_, err := c.Kill(api.KillParams{ClaudeInstanceID: "id-k-ended"})
	if err != nil {
		t.Fatalf("Kill(ended): %v", err)
	}
}

// TestPauseEndedRowHappy verifies Pause on an already-ended row is a no-op
// success. The desired post-condition is met before the /exit send-keys path
// is reached, so no tmux call is made.
func TestPauseEndedRowHappy(t *testing.T) {
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-p-ended", "cd-p-ended", store.StateEnded)
	})
	_, err := c.Pause(context.Background(), api.PauseParams{ClaudeInstanceID: "id-p-ended"})
	if err != nil {
		t.Fatalf("Pause(ended): %v", err)
	}
}

// TestDecideDelegation proves Decide delegates to internal/api. A spawn with
// relay_mode="off" causes internal/api.Decide to return ErrRelayModeOff after
// the store lookup, exercising the delegation chain without needing a live
// permission-request row.
func TestDecideDelegation(t *testing.T) {
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		// insertRow uses relay_mode="off" by default.
		insertRow(t, dbPath, "id-dec-1", "cd-dec-1", store.StateCheckPermission)
	})
	_, err := c.Decide(api.DecideParams{
		ClaudeInstanceID: "id-dec-1",
		RequestToken:     "00000000-0000-0000-0000-000000000001",
		Decision:         "allow",
	})
	if !errors.Is(err, api.ErrRelayModeOff) {
		t.Fatalf("Decide(relay_mode=off): got %v; want api.ErrRelayModeOff", err)
	}
}

// TestResumeDelegation proves Resume delegates to internal/api. A spawn in a
// live state (waiting) causes internal/api.Resume to return ErrSpawnNotResumable
// before any JSONL or tmux check, exercising the delegation chain cleanly.
func TestResumeDelegation(t *testing.T) {
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-res-1", "cd-res-1", store.StateWaiting)
	})
	_, err := c.Resume(api.ResumeParams{ClaudeInstanceID: "id-res-1"})
	if !errors.Is(err, api.ErrSpawnNotResumable) {
		t.Fatalf("Resume(waiting): got %v; want api.ErrSpawnNotResumable", err)
	}
}

// TestFindMissingEmptyStoreHappy verifies FindMissing against an empty store
// returns no error and Count=0 (fast no-op path: no live IDs to reconcile).
func TestFindMissingEmptyStoreHappy(t *testing.T) {
	c, _ := newTestClient(t)
	res, err := c.FindMissing(context.Background())
	if err != nil {
		t.Fatalf("FindMissing: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("Count = %d; want 0 (empty store)", res.Count)
	}
}

// TestExpireNilOlderThanHappy verifies Expire with a nil olderThan (use the
// config-default retention window) against an empty store returns no error and
// removes nothing.
func TestExpireNilOlderThanHappy(t *testing.T) {
	c, _ := newTestClient(t)
	res, err := c.Expire(nil)
	if err != nil {
		t.Fatalf("Expire(nil): %v", err)
	}
	if res.Count != 0 {
		t.Errorf("Count = %d; want 0 (empty store)", res.Count)
	}
}

// TestDeleteEmptySliceHappy verifies Delete with an empty id slice returns no
// error and a non-nil empty map. This is the degenerate-input defence.
func TestDeleteEmptySliceHappy(t *testing.T) {
	c, _ := newTestClient(t)
	res, err := c.Delete([]string{})
	if err != nil {
		t.Fatalf("Delete([]): %v", err)
	}
	if res.Results == nil {
		t.Fatalf("Results is nil; want empty non-nil map")
	}
	if len(res.Results) != 0 {
		t.Errorf("len(Results) = %d; want 0", len(res.Results))
	}
}

// TestMakeTemplateHappy verifies MakeTemplate writes a valid template file and
// returns the absolute path. HOME is set to the test's temp dir so the
// templates directory is isolated.
func TestMakeTemplateHappy(t *testing.T) {
	c, _ := newTestClient(t)
	res, err := c.MakeTemplate(api.MakeTemplateParams{Name: "meth-test-tmpl", CWD: "/tmp"})
	if err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	if res.Path == "" {
		t.Fatal("MakeTemplate returned empty Path")
	}
	if _, statErr := os.Stat(res.Path); statErr != nil {
		t.Errorf("template file %q not found after MakeTemplate: %v", res.Path, statErr)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Part C — errors.Is chain preserved across the facade
// ─────────────────────────────────────────────────────────────────────────────

// TestListFacadePreservesErrListInvalidLabel proves the pkg/api delegation
// layer does not break the errors.Is chain: a label string without an "="
// separator triggers api.ErrListInvalidLabel inside internal/api.List, and
// that sentinel is still matchable via errors.Is on the returned error.
func TestListFacadePreservesErrListInvalidLabel(t *testing.T) {
	c, _ := newTestClient(t)
	_, err := c.List(api.ListParams{Labels: []string{"noequalssign"}})
	if !errors.Is(err, api.ErrListInvalidLabel) {
		t.Fatalf("List(invalid label): got %v; want errors.Is match for api.ErrListInvalidLabel", err)
	}
}
