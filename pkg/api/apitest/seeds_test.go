package apitest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
)

// TestSeedSpawn_HappyPath verifies the explicit-id, state=working, sessionID path.
func TestSeedSpawn_HappyPath(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	const id = "test-spawn-id"
	const sessionID = "sess-abc"

	got, err := SeedSpawn(dbPath, id, "working", "/srv", "on", sessionID, true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	if got != id {
		t.Errorf("returned id = %q; want %q", got, id)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if sp.State != "working" {
		t.Errorf("state = %q; want working", sp.State)
	}
	if sp.ClaudeSessionID != sessionID {
		t.Errorf("session_id = %q; want %q", sp.ClaudeSessionID, sessionID)
	}
}

// TestSeedSpawn_Defaults covers the empty-arg defaulting rules for id, state, cwd, relayMode.
func TestSeedSpawn_Defaults(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		id         string
		state      string
		cwd        string
		relayMode  string
		wantState  string
		wantCWD    string
		wantRelay  string
	}{
		{
			name:      "empty id generates uuid",
			id:        "",
			state:     "waiting",
			cwd:       "/tmp",
			relayMode: "off",
			wantState: "waiting",
			wantCWD:   "/tmp",
			wantRelay: "off",
		},
		{
			name:      "empty state defaults to waiting",
			id:        "def-state",
			state:     "",
			cwd:       "/tmp",
			relayMode: "off",
			wantState: store.StateWaiting,
			wantCWD:   "/tmp",
			wantRelay: "off",
		},
		{
			name:      "empty cwd defaults to /tmp",
			id:        "def-cwd",
			state:     "waiting",
			cwd:       "",
			relayMode: "off",
			wantState: "waiting",
			wantCWD:   "/tmp",
			wantRelay: "off",
		},
		{
			name:      "empty relayMode defaults to off",
			id:        "def-relay",
			state:     "waiting",
			cwd:       "/tmp",
			relayMode: "",
			wantState: "waiting",
			wantCWD:   "/tmp",
			wantRelay: "off",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dbPath := filepath.Join(t.TempDir(), "state.db")

			gotID, err := SeedSpawn(dbPath, tc.id, tc.state, tc.cwd, tc.relayMode, "", true)
			if err != nil {
				t.Fatalf("SeedSpawn: %v", err)
			}

			// For the empty-id case: verify a UUID was generated.
			if tc.id == "" {
				if gotID == "" {
					t.Fatal("expected a generated UUID; got empty string")
				}
				if _, parseErr := uuid.Parse(gotID); parseErr != nil {
					t.Errorf("generated id %q is not a valid UUID: %v", gotID, parseErr)
				}
			} else if gotID != tc.id {
				t.Errorf("returned id = %q; want %q", gotID, tc.id)
			}

			s, err := store.Open(dbPath)
			if err != nil {
				t.Fatalf("store.Open: %v", err)
			}
			defer s.Close() //nolint:errcheck

			sp, err := s.GetSpawn(gotID)
			if err != nil {
				t.Fatalf("GetSpawn(%q): %v", gotID, err)
			}
			if sp.State != tc.wantState {
				t.Errorf("state = %q; want %q", sp.State, tc.wantState)
			}
			if sp.CWD != tc.wantCWD {
				t.Errorf("cwd = %q; want %q", sp.CWD, tc.wantCWD)
			}
			if sp.RelayMode != tc.wantRelay {
				t.Errorf("relay_mode = %q; want %q", sp.RelayMode, tc.wantRelay)
			}
		})
	}
}

// TestSeedSpawn_NewColumnOptions_RoundTrip drives the schema-v3 columns through
// the extended SeedSpawn options (WithPID / WithProcStarttime / WithJsonlPath /
// WithExtraEnv / WithLivenessUnverifiedSince / WithLivenessNote) — NOT hand-rolled
// SQL in the test (SR-12.1/12.2) — and reads them back through store.GetSpawn.
// It exercises the re-exported per-OS proc_starttime constant so the seeder's
// blessed fixture value is used rather than an inline literal.
func TestSeedSpawn_NewColumnOptions_RoundTrip(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	const id = "seed-new-cols"

	wantEnv := map[string]string{"FOO": "bar", "TOKEN": "xyz"}
	const (
		wantPID       = 7777
		wantJsonlPath = "/var/log/claude/seed.jsonl"
		wantSince     = "2026-09-20T12:00:00Z"
		wantNote      = "seeded liveness note"
	)

	got, err := SeedSpawn(dbPath, id, "working", "/srv", "on", "", true,
		WithPID(wantPID),
		WithProcStarttime(LinuxProcStarttime),
		WithJsonlPath(wantJsonlPath),
		WithExtraEnv(wantEnv),
		WithLivenessUnverifiedSince(wantSince),
		WithLivenessNote(wantNote),
	)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	if got != id {
		t.Errorf("returned id = %q; want %q", got, id)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if sp.PID != wantPID {
		t.Errorf("PID = %d; want %d", sp.PID, wantPID)
	}
	if sp.ProcStarttime != LinuxProcStarttime {
		t.Errorf("ProcStarttime = %q; want %q", sp.ProcStarttime, LinuxProcStarttime)
	}
	if sp.JSONLPath != wantJsonlPath {
		t.Errorf("JSONLPath = %q; want %q", sp.JSONLPath, wantJsonlPath)
	}
	if !reflect.DeepEqual(sp.ExtraEnv, wantEnv) {
		t.Errorf("ExtraEnv = %v; want %v (seeded as a map)", sp.ExtraEnv, wantEnv)
	}
	if sp.LivenessUnverifiedSince != wantSince {
		t.Errorf("LivenessUnverifiedSince = %q; want %q", sp.LivenessUnverifiedSince, wantSince)
	}
	if sp.LivenessNote != wantNote {
		t.Errorf("LivenessNote = %q; want %q", sp.LivenessNote, wantNote)
	}
}

// TestSeedSpawn_NoOptions_DefaultsNewColumns proves that omitting the SpawnOption
// arguments leaves the schema-v3 columns at their defaults: an empty NON-NIL
// ExtraEnv map and zero-value identity/liveness fields (NULL columns under
// COALESCE). This is the seeder-layer counterpart to the store's migrated-row
// decode test — the row is never touched by the option UPDATE.
func TestSeedSpawn_NoOptions_DefaultsNewColumns(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	const id = "seed-no-opts"

	if _, err := SeedSpawn(dbPath, id, "waiting", "/tmp", "off", "", true); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if sp.ExtraEnv == nil {
		t.Errorf("ExtraEnv = nil; want empty non-nil map when no option seeded")
	}
	if len(sp.ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v; want empty map", sp.ExtraEnv)
	}
	if sp.PID != 0 {
		t.Errorf("PID = %d; want 0 (unset NULL column)", sp.PID)
	}
	if sp.ProcStarttime != "" {
		t.Errorf("ProcStarttime = %q; want empty (unset NULL column)", sp.ProcStarttime)
	}
	if sp.JSONLPath != "" {
		t.Errorf("JSONLPath = %q; want empty (unset NULL column)", sp.JSONLPath)
	}
	if sp.LivenessUnverifiedSince != "" {
		t.Errorf("LivenessUnverifiedSince = %q; want empty (unset NULL column)", sp.LivenessUnverifiedSince)
	}
	if sp.LivenessNote != "" {
		t.Errorf("LivenessNote = %q; want empty (unset NULL column)", sp.LivenessNote)
	}
}

// TestSeedSpawn_WithExtraEnv_NilMapIsEmptyObject proves WithExtraEnv(nil) seeds
// the canonical empty object rather than a NULL/absent column, reading back as an
// empty non-nil map through store.GetSpawn.
func TestSeedSpawn_WithExtraEnv_NilMapIsEmptyObject(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	const id = "seed-nil-env"

	if _, err := SeedSpawn(dbPath, id, "waiting", "/tmp", "off", "", true,
		WithExtraEnv(nil),
	); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	sp, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if sp.ExtraEnv == nil {
		t.Errorf("ExtraEnv = nil; want empty non-nil map for WithExtraEnv(nil)")
	}
	if len(sp.ExtraEnv) != 0 {
		t.Errorf("ExtraEnv = %v; want empty map for WithExtraEnv(nil)", sp.ExtraEnv)
	}
}

// TestSeedSpawn_CreateStoreFalse_MissingStore verifies error when store doesn't exist and createStore=false.
func TestSeedSpawn_CreateStoreFalse_MissingStore(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "nonexistent", "state.db")

	_, err := SeedSpawn(dbPath, "some-id", "waiting", "/tmp", "off", "", false)
	if err == nil {
		t.Fatal("expected error when store is missing and createStore=false; got nil")
	}
}

// TestSeedParentChild verifies the child row's parent_id is updated.
func TestSeedParentChild(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	parentID, err := SeedSpawn(dbPath, "parent-id", "waiting", "/tmp", "off", "", true)
	if err != nil {
		t.Fatalf("SeedSpawn parent: %v", err)
	}
	childID, err := SeedSpawn(dbPath, "child-id", "waiting", "/tmp", "off", "", false)
	if err != nil {
		t.Fatalf("SeedSpawn child: %v", err)
	}

	if err := SeedParentChild(dbPath, parentID, childID); err != nil {
		t.Fatalf("SeedParentChild: %v", err)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	sp, err := s.GetSpawn(childID)
	if err != nil {
		t.Fatalf("GetSpawn child: %v", err)
	}
	if sp.ParentID != parentID {
		t.Errorf("parent_id = %q; want %q", sp.ParentID, parentID)
	}
}

// TestSeedPermissionRequest verifies the returned seeds and the readable row.
func TestSeedPermissionRequest(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	spawnID, err := SeedSpawn(dbPath, "perm-spawn", "waiting", "/tmp", "off", "", true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}

	seed, err := SeedPermissionRequest(dbPath, spawnID, "Bash")
	if err != nil {
		t.Fatalf("SeedPermissionRequest: %v", err)
	}
	if seed.RequestID == 0 {
		t.Error("RequestID should be non-zero")
	}
	if seed.RequestToken == "" {
		t.Error("RequestToken should be non-empty")
	}
	if _, parseErr := uuid.Parse(seed.RequestToken); parseErr != nil {
		t.Errorf("RequestToken %q is not a valid UUID: %v", seed.RequestToken, parseErr)
	}

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close() //nolint:errcheck

	row, err := s.GetPermissionRequest(spawnID, seed.RequestToken)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if row.RequestID != seed.RequestID {
		t.Errorf("row.RequestID = %d; want %d", row.RequestID, seed.RequestID)
	}
	if row.ToolName != "Bash" {
		t.Errorf("row.ToolName = %q; want Bash", row.ToolName)
	}
}

// TestSeedTemplate covers happy-path creation and idempotent re-creation.
func TestSeedTemplate(t *testing.T) {
	t.Parallel()

	t.Run("happy path creates file with body", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		templatesDir := filepath.Join(dir, "templates")
		const body = "[template]\nfoo = \"bar\"\n"

		path, err := SeedTemplate(templatesDir, "my-template", body)
		if err != nil {
			t.Fatalf("SeedTemplate: %v", err)
		}
		if !filepath.IsAbs(path) {
			t.Errorf("returned path %q is not absolute", path)
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Errorf("file does not exist: %v", statErr)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("ReadFile: %v", readErr)
		}
		if string(got) != body {
			t.Errorf("file body = %q; want %q", string(got), body)
		}
	})

	t.Run("existing dir is ok", func(t *testing.T) {
		t.Parallel()
		templatesDir := t.TempDir() // already exists

		if _, err := SeedTemplate(templatesDir, "first", "a"); err != nil {
			t.Fatalf("first SeedTemplate: %v", err)
		}
		if _, err := SeedTemplate(templatesDir, "second", "b"); err != nil {
			t.Fatalf("second SeedTemplate on existing dir: %v", err)
		}
	})
}

// TestInitStore verifies the store file is created, re-openable, and closes cleanly.
func TestInitStore(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.db")

	got, err := InitStore(dbPath)
	if err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if got != dbPath {
		t.Errorf("returned path = %q; want %q", got, dbPath)
	}
	if _, statErr := os.Stat(dbPath); statErr != nil {
		t.Errorf("db file does not exist: %v", statErr)
	}

	// Re-open via store.Open to verify the schema is valid.
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open after InitStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("store.Close: %v", err)
	}
}
