package api_test

// methods_test.go covers the Client verb methods: every verb refuses on a
// closed Client, the admin hooks refuse anything but a Client, and each verb
// delegates to its implementation with errors.Is intact across the facade.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// newTestClient creates a Client over a fresh store, HOME set to a temp dir.
func newTestClient(t *testing.T) (*api.Client, string) {
	t.Helper()
	return newTestClientWithRows(t, nil)
}

// newTestClientWithRows creates a Client over a store seedFn (when non-nil)
// seeds by its path before the Client opens it; HOME is set to a temp dir so
// MakeTemplate and other HOME users land there.
func newTestClientWithRows(t *testing.T, seedFn func(dbPath string)) (*api.Client, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "state.db")
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if seedFn != nil {
		seedFn(dbPath)
	}
	c, err := api.New(api.Options{StorePath: dbPath, ConfigPath: cfgPath, CreateIfMissing: true})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, home
}

// insertRow inserts a relay_mode off row id at state into the store at dbPath.
func insertRow(t *testing.T, dbPath, id, sessionName, state string) {
	t.Helper()
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("OpenOrInit(%q): %v", dbPath, err)
	}
	defer s.Close() //nolint:errcheck
	if err := s.InsertPending(store.Spawn{ClaudeInstanceID: id, CWD: "/tmp", TmuxSessionName: sessionName, RelayMode: "off"}); err != nil {
		t.Fatalf("InsertPending(%s): %v", id, err)
	}
	if err := seedAgentState(s, dbPath, id, state); err != nil {
		t.Fatalf("seed %s→%s: %v", id, state, err)
	}
}

// TestAllVerbsReturnErrClientClosedAfterClose: every verb on a closed Client
// returns ErrClientClosed before any param is read; the Get, List,
// FindMissing, Expire and Delete results still encode their lists as [] and
// maps as {}, never null (b.hbt, b.4nt).
func TestAllVerbsReturnErrClientClosedAfterClose(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	if api.ErrClientClosed == nil {
		t.Fatal("api.ErrClientClosed is nil; every check below would pass vacuously")
	}
	c, _ := newTestClient(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	ctx := context.Background()
	emptyJSON := map[string]string{
		"Get":         emptyGetJSON,
		"List":        `{"spawns":[]}`,
		"FindMissing": `{"count":0,"ids":[],"unverified":0,"unverified_ids":[]}`,
		"Expire":      `{"count":0,"ids":[],"kept":0,"kept_ids":[]}`,
		"Delete":      `{"results":{}}`,
	}
	for name, call := range map[string]func() (any, error){
		"Version":      func() (any, error) { return c.Version() },
		"Spawn":        func() (any, error) { return c.Spawn(api.SpawnParams{}) },
		"Status":       func() (any, error) { return c.Status("") },
		"Get":          func() (any, error) { return c.Get("") },
		"List":         func() (any, error) { return c.List(api.ListParams{}) },
		"SendKeys":     func() (any, error) { return c.SendKeys(api.SendKeysParams{}) },
		"ReadPane":     func() (any, error) { return c.ReadPane(api.ReadPaneParams{}) },
		"Kill":         func() (any, error) { return c.Kill(api.KillParams{}) },
		"Pause":        func() (any, error) { return c.Pause(ctx, api.PauseParams{}) },
		"Decide":       func() (any, error) { return c.Decide(api.DecideParams{}) },
		"Resume":       func() (any, error) { return c.Resume(api.ResumeParams{}) },
		"FindMissing":  func() (any, error) { return c.FindMissing(ctx) },
		"Expire":       func() (any, error) { return c.Expire(nil) },
		"Delete":       func() (any, error) { return adminapi.Delete(c, nil) },
		"KillFinished": func() (any, error) { return adminapi.KillFinished(c, "") },
		"MakeTemplate": func() (any, error) { return c.MakeTemplate(api.MakeTemplateParams{}) },
	} {
		res, err := call()
		if !errors.Is(err, api.ErrClientClosed) {
			t.Errorf("%s on a closed client: %v; want ErrClientClosed", name, err)
		}
		if want, ok := emptyJSON[name]; ok {
			if got := jsonOf(t, res); got != want {
				t.Errorf("%s on a closed client = %s; want %s", name, got, want)
			}
		}
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v; want nil", err)
	}
}

// TestAdminHooksRefuseNonClient: pkg/api sets internal/adminapi's hooks, and
// each refuses any value but a non-nil *api.Client without running (b.vqr);
// Delete's refusal still encodes results as {}, never null (b.4nt).
func TestAdminHooksRefuseNonClient(t *testing.T) {
	t.Parallel()
	if adminapi.KillFinished == nil || adminapi.Delete == nil {
		t.Fatal("importing pkg/api left an internal/adminapi hook unset")
	}
	for name, c := range map[string]any{"nil": nil, "nil *Client": (*api.Client)(nil), "string": "client"} {
		t.Run(name, func(t *testing.T) {
			if _, err := adminapi.KillFinished(c, "id"); err == nil {
				t.Error("KillFinished: nil error; want a refusal")
			}
			res, err := adminapi.Delete(c, []string{"id"})
			if got := jsonOf(t, res); err == nil || got != `{"results":{}}` {
				t.Errorf("Delete = %s, %v; want a refusal and {\"results\":{}}", got, err)
			}
		})
	}
}

// TestStatusHappy verifies Status returns the row's state for a known id.
func TestStatusHappy(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-st-1", "cd-st-1", store.StatePending)
	})
	if res, err := c.Status("id-st-1"); err != nil || res.State != store.StatePending {
		t.Errorf("Status = %+v, %v; want %s", res, err, store.StatePending)
	}
}

// TestClientVerbsDelegate: each Client verb reaches its implementation, shown
// by one outcome that needs no tmux call, and errors.Is still matches the
// sentinel the implementation returns.
func TestClientVerbsDelegate(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	empty, _ := newTestClient(t)
	rows, _ := newTestClientWithRows(t, func(dbPath string) {
		insertRow(t, dbPath, "id-waiting", "cd-waiting", store.StateWaiting)
		insertRow(t, dbPath, "id-ended", "cd-ended", store.StateEnded)
		insertRow(t, dbPath, "id-perm", "cd-perm", store.StateCheckPermission)
	})
	ctx := context.Background()
	// unless returns err, or a description of the result when ok is false.
	unless := func(ok bool, err error, format string, args ...any) error {
		if err == nil && !ok {
			return fmt.Errorf(format, args...)
		}
		return err
	}
	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"Version", func() error { _, err := empty.Version(); return err }, nil},
		{"Spawn with no cwd", func() error { _, err := empty.Spawn(api.SpawnParams{}); return err }, spawn.ErrCwdMissing},
		{"Get", func() error {
			r, err := rows.Get("id-waiting")
			return unless(r.ClaudeInstanceID == "id-waiting", err, "Get = %+v", r)
		}, nil},
		{"List on an empty store", func() error {
			r, err := empty.List(api.ListParams{})
			return unless(r.Spawns != nil && len(r.Spawns) == 0, err, "Spawns = %#v; want a non-nil []", r.Spawns)
		}, nil},
		{"List with an invalid label", func() error { _, err := empty.List(api.ListParams{Labels: []string{"noequalssign"}}); return err },
			api.ErrListInvalidLabel},
		{"SendKeys to an unknown id", func() error { _, err := empty.SendKeys(api.SendKeysParams{ClaudeInstanceID: "absent"}); return err },
			store.ErrSpawnNotFound},
		{"ReadPane of an unknown id", func() error { _, err := empty.ReadPane(api.ReadPaneParams{ClaudeInstanceID: "absent"}); return err },
			store.ErrSpawnNotFound},
		{"Pause of an ended row", func() error { _, err := rows.Pause(ctx, api.PauseParams{ClaudeInstanceID: "id-ended"}); return err }, nil},
		{"Decide on a relay-off row", func() error {
			_, err := rows.Decide(api.DecideParams{ClaudeInstanceID: "id-perm", RequestToken: "00000000-0000-0000-0000-000000000001", Decision: "allow"})
			return err
		}, api.ErrRelayModeOff},
		{"Resume of a live row", func() error { _, err := rows.Resume(api.ResumeParams{ClaudeInstanceID: "id-waiting"}); return err },
			api.ErrSpawnNotResumable},
		{"FindMissing on an empty store", func() error {
			r, err := empty.FindMissing(ctx)
			return unless(r.Count == 0, err, "Count = %d", r.Count)
		}, nil},
		{"Expire with the default window on an empty store", func() error {
			r, err := empty.Expire(nil)
			ok := r.Count == 0 && r.Kept == 0 && r.IDs != nil && len(r.IDs) == 0 && r.KeptIDs != nil && len(r.KeptIDs) == 0
			return unless(ok, err, "Expire = %#v; want nothing removed or kept, both lists non-nil []", r)
		}, nil},
		{"Delete of no ids", func() error {
			r, err := adminapi.Delete(empty, []string{})
			return unless(r.Results != nil && len(r.Results) == 0, err, "Results = %#v; want a non-nil empty map", r.Results)
		}, nil},
		{"MakeTemplate", func() error {
			r, err := empty.MakeTemplate(api.MakeTemplateParams{Name: "meth-test-tmpl", CWD: "/tmp"})
			if err == nil {
				_, err = os.Stat(r.Path)
			}
			return err
		}, nil},
	}
	for _, tc := range cases {
		if err := tc.call(); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v; want %v", tc.name, err, tc.want)
		}
	}
}

// TestKillEndedRowHappy: Kill on a finished row (ended or missing) is a no-op
// success with kill_sent false and no tmux call, even with its session still there (SR-6.1).
func TestKillEndedRowHappy(t *testing.T) {
	t.Parallel()
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedRow(t, killRowSpec{State: state})
			c, _ := e.client(t)
			if res, err := c.Kill(api.KillParams{ClaudeInstanceID: r.ID}); err != nil || res.KillSent {
				t.Fatalf("Kill(%s) = %+v, %v; want kill_sent false", state, res, err)
			}
			e.assertKillCalls(t)
		})
	}
}
