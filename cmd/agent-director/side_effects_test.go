package main_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite" // raw driver access for the store tampering below

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// dbFreeShapes are the argv shapes run() dispatches before setupClient: none
// may load the config or open or create the store (SR-4.1/4.2, b.8dr).
var dbFreeShapes = []struct {
	name string
	argv []string
}{
	{name: "help", argv: []string{"help"}},
	{name: "--help", argv: []string{"--help"}},
	{name: "no-args", argv: nil},
	{name: "version", argv: []string{"version"}},
	{name: "version --json", argv: []string{"version", "--json"}},
}

// TestDBFreeVerbsProduceNoStore: each DB-free shape, bare or after a global
// flag (parsed and stripped), exits 0 with a JSON envelope and empty stderr,
// and creates nothing under HOME or at the flag's target.
func TestDBFreeVerbsProduceNoStore(t *testing.T) {
	type shape struct {
		name string
		argv func(dir string) []string
	}
	var shapes []shape
	for _, s := range dbFreeShapes {
		shapes = append(shapes, shape{s.name, func(string) []string { return s.argv }})
	}
	shapes = append(shapes,
		shape{"--store-path before version", func(dir string) []string {
			return []string{"--store-path", filepath.Join(dir, "s.db"), "version"}
		}},
		shape{"--home before help", func(dir string) []string { return []string{"--home", dir, "help"} }},
		shape{"--tmux-command before version", func(dir string) []string {
			return []string{"--tmux-command", filepath.Join(dir, "tmux"), "version"}
		}},
	)
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			stdout, stderr, code := runCLIWithHome(t, home, s.argv(dir)...)
			var payload map[string]any
			if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &payload) != nil {
				t.Fatalf("exit=%d stderr=%q stdout=%.200q; want 0, empty stderr and a JSON envelope", code, stderr, stdout)
			}
			assertHomeTree(t, home)
			assertHomeTree(t, dir)
		})
	}
}

// rawExec runs query on home's state.db through the raw driver, as only a
// hand edit could.
func rawExec(t *testing.T, home, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", stateDB(home))
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// TestStoreOpenRefusals: a store-opening verb on a store it cannot open exits
// 1 with only the open's envelope, which never carries the store id (SR-5.1,
// SR-15); an older-than-binary store gets the admin-only dead end with no
// self-service breadcrumb (SR-1.4);
// and the hook stays fail-open on each (SRD §3.2). The new store's 0700
// directory and 0600 file are checked before tampering.
func TestStoreOpenRefusals(t *testing.T) {
	cases := []struct {
		name, tamper, wantErr string
		wantDesc, bannedDesc  []string
	}{
		{"older schema", "PRAGMA user_version = 1", "ErrSchemaMigrationRequired",
			[]string{"state.db is schema v1", "this binary requires v", "Migration must be performed by an administrator", "install process"},
			// no self-service breadcrumb: a flag, a path, an env var or a verb
			[]string{"--", "/", "AGENT_DIRECTOR_", "migrate", "spawn", "list ", "resume", "expire", "delete"}},
		{"newer schema", "PRAGMA user_version = 99", "ErrSchemaMismatch", nil, nil},
		{"store id removed", "DELETE FROM store_meta WHERE key = 'store_id'", "ErrSchemaMismatch", nil, nil},
		{"store id not hex", "UPDATE store_meta SET value = 'SECRETVALUEXXXXX' WHERE key = 'store_id'", "ErrSchemaMismatch", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bootstrapDB(t, home)
			for path, want := range map[string]os.FileMode{directorDir(home): 0o700, stateDB(home): 0o600} {
				if mode := statMode(t, path); mode != want {
					t.Errorf("%s mode = %o; want %o", path, mode, want)
				}
			}
			storeID, err := apitest.ReadStoreID(stateDB(home))
			if err != nil {
				t.Fatalf("ReadStoreID: %v", err)
			}
			rawExec(t, home, tc.tamper)

			stdout, stderr, code := runCLIWithHome(t, home, "list")
			env := assertOnlyEnvelope(t, stdout, stderr, code, tc.wantErr)
			for _, want := range tc.wantDesc {
				if !strings.Contains(env.ErrDescription, want) {
					t.Errorf("description %q lacks %q", env.ErrDescription, want)
				}
			}
			for _, banned := range tc.bannedDesc {
				if strings.Contains(env.ErrDescription, banned) {
					t.Errorf("description %q leaks the self-service breadcrumb %q", env.ErrDescription, banned)
				}
			}
			for _, secret := range []string{storeID, "SECRETVALUEXXXXX"} {
				if strings.Contains(stderr, secret) {
					t.Errorf("stderr %q carries the store id value %q", stderr, secret)
				}
			}

			stdout, stderr, code, _ = runBounded(t, home, map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-refused"},
				`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`, false, surfaceDeadline, "hook")
			if code != 0 || stdout != "" {
				t.Errorf("hook exit=%d stdout=%q; want 0 and empty (fail-open; stderr=%q)", code, stdout, stderr)
			}
		})
	}
}

// statMode returns path's permission bits or fails the test.
func statMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}
