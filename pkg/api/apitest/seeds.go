package apitest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/store"
)

// SeedSpawn inserts a single spawn row and transitions it to state.
//
//   - dbPath: path to the SQLite store file.
//   - id: claude_instance_id; auto-generated UUID if empty.
//   - state: target state (e.g. "waiting", "working", "ended"). Defaults to
//     "waiting" if empty.
//   - cwd: working directory stored on the row; defaults to "/tmp" if empty.
//   - relayMode: relay_mode value ("on"|"off"|""). Defaults to "off" if empty.
//   - sessionID: if non-empty, calls s.RecordSessionStartIdentity after
//     InsertPending so the row has a claude_session_id (required by the resume
//     verb's pre-flight). Only the session id is seeded here; the identity
//     columns (pid/proc_starttime/jsonl_path) are seeded via opts below.
//   - createStore: if true the store is created when missing (OpenOrInit);
//     if false the store must already exist (Open).
//
//   - opts: optional trailing SpawnOption values seeding the schema-v3 columns
//     (pid, proc_starttime, jsonl_path, extra_env, liveness_unverified_since,
//     liveness_note). Options are applied by an apitest-internal SQL UPDATE on
//     the seeded row after the InsertPending/RecordSessionStartIdentity/
//     ApplyHookTransition sequence; only explicitly provided columns are written. Supplying no
//     options preserves the store's defaults (NULL columns; extra_env = '{}').
//
// Returns the claude_instance_id that was written.
func SeedSpawn(dbPath, id, state, cwd, relayMode, sessionID string, createStore bool, opts ...SpawnOption) (string, error) {
	var (
		s   *store.Store
		err error
	)
	if createStore {
		s, err = store.OpenOrInit(dbPath)
	} else {
		s, err = store.Open(dbPath)
	}
	if err != nil {
		return "", fmt.Errorf("SeedSpawn: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	if id == "" {
		id = uuid.NewString()
	}
	if cwd == "" {
		cwd = "/tmp"
	}
	if state == "" {
		state = store.StateWaiting
	}
	if relayMode == "" {
		relayMode = "off"
	}

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              cwd,
		TmuxSessionName:  "ts-" + id,
		RelayMode:        relayMode,
	}); err != nil {
		return "", fmt.Errorf("SeedSpawn: InsertPending: %w", err)
	}

	if sessionID != "" {
		if err := s.RecordSessionStartIdentity(id, sessionID, "", 0, ""); err != nil {
			return "", fmt.Errorf("SeedSpawn: RecordSessionStartIdentity %q: %w", sessionID, err)
		}
	}

	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false, "test_seed"); err != nil {
			return "", fmt.Errorf("SeedSpawn: ApplyHookTransition %q: %w", state, err)
		}
	}

	if len(opts) > 0 {
		if err := applySpawnOpts(dbPath, id, opts); err != nil {
			return "", err
		}
	}

	return id, nil
}

// applySpawnOpts writes the requested schema-v3 column overrides onto the
// already-seeded row via a raw parameterized SQL UPDATE. apitest is the single
// blessed location for these column literals (SR-12.2 binds the no-hardcoded-SQL
// rule to tests, not this mandated helper). Only fields with a non-nil pointer
// are written, so unset options leave the store's defaults untouched; extra_env
// is marshaled to a JSON object (nil map already normalized to {} by WithExtraEnv).
func applySpawnOpts(dbPath, id string, opts []SpawnOption) error {
	o := &spawnOpts{}
	for _, opt := range opts {
		opt(o)
	}

	setClauses := make([]string, 0, 6)
	args := make([]any, 0, 7)
	if o.pid != nil {
		setClauses = append(setClauses, "pid = ?")
		args = append(args, *o.pid)
	}
	if o.procStarttime != nil {
		setClauses = append(setClauses, "proc_starttime = ?")
		args = append(args, *o.procStarttime)
	}
	if o.jsonlPath != nil {
		setClauses = append(setClauses, "jsonl_path = ?")
		args = append(args, *o.jsonlPath)
	}
	if o.extraEnv != nil {
		encoded, err := json.Marshal(o.extraEnv)
		if err != nil {
			return fmt.Errorf("SeedSpawn: marshal extra_env: %w", err)
		}
		setClauses = append(setClauses, "extra_env = ?")
		args = append(args, string(encoded))
	}
	if o.livenessUnverifiedSince != nil {
		setClauses = append(setClauses, "liveness_unverified_since = ?")
		args = append(args, *o.livenessUnverifiedSince)
	}
	if o.livenessNote != nil {
		setClauses = append(setClauses, "liveness_note = ?")
		args = append(args, *o.livenessNote)
	}
	if len(setClauses) == 0 {
		return nil
	}

	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return fmt.Errorf("SeedSpawn: raw open: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	q := "UPDATE spawns SET " + strings.Join(setClauses, ", ") + " WHERE claude_instance_id = ?"
	args = append(args, id)
	if _, err := raw.Exec(q, args...); err != nil {
		return fmt.Errorf("SeedSpawn: apply options UPDATE: %w", err)
	}
	return nil
}

// SeedParentChild sets the parent_id on childID to parentID.
// Both rows must already exist in the store at dbPath.
func SeedParentChild(dbPath, parentID, childID string) error {
	s, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("SeedParentChild: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	if err := s.SetParentID(childID, parentID); err != nil {
		return fmt.Errorf("SeedParentChild: SetParentID: %w", err)
	}
	return nil
}

// PermissionRequestSeed is the result of SeedPermissionRequest.
type PermissionRequestSeed struct {
	// RequestID is the AUTOINCREMENT row id assigned by the store on insert.
	RequestID int64
	// RequestToken is the UUIDv4 token used to address the request from clients.
	RequestToken string
}

// SeedPermissionRequest inserts an open permission request for spawnID
// using toolName. The spawn row must already exist. Returns both the request_id
// (AUTOINCREMENT) and the request_token (UUIDv4) so callers can reference the
// row by either key.
func SeedPermissionRequest(dbPath, spawnID, toolName string) (PermissionRequestSeed, error) {
	s, err := store.Open(dbPath)
	if err != nil {
		return PermissionRequestSeed{}, fmt.Errorf("SeedPermissionRequest: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	requestToken := uuid.NewString()
	if err := s.UpsertOpenPermissionRequest(spawnID, requestToken, toolName, "{}", 0, ""); err != nil {
		return PermissionRequestSeed{}, fmt.Errorf("SeedPermissionRequest: upsert: %w", err)
	}

	row, err := s.GetPermissionRequest(spawnID, requestToken)
	if err != nil {
		return PermissionRequestSeed{}, fmt.Errorf("SeedPermissionRequest: get: %w", err)
	}
	return PermissionRequestSeed{RequestID: row.RequestID, RequestToken: requestToken}, nil
}

// SeedTemplate writes body to templatesDir/<name>.toml (creating the
// directory if missing). Returns the absolute path of the written file.
func SeedTemplate(templatesDir, name, body string) (string, error) {
	if err := os.MkdirAll(templatesDir, 0o700); err != nil {
		return "", fmt.Errorf("SeedTemplate: mkdir %q: %w", templatesDir, err)
	}
	outPath := filepath.Join(templatesDir, name+".toml")
	if err := os.WriteFile(outPath, []byte(body), 0o600); err != nil {
		return "", fmt.Errorf("SeedTemplate: write %q: %w", outPath, err)
	}
	return outPath, nil
}

// InitStore creates a fresh initialized SQLite store at dbPath
// (creating parent directories as needed), then closes it immediately.
// Returns the dbPath so callers can chain: path, err := InitStore(p).
func InitStore(dbPath string) (string, error) {
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		return "", fmt.Errorf("InitStore: %w", err)
	}
	if err := s.Close(); err != nil {
		return "", fmt.Errorf("InitStore: close: %w", err)
	}
	return dbPath, nil
}
