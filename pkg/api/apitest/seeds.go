package apitest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
//   - opts: optional trailing SpawnOption values seeding columns the store
//     writes above cannot (the v3 identity/liveness columns, the v5 columns,
//     timestamps and raw structured text). Options and the SR-20.3 defaults
//     are applied by apitest-internal SQL UPDATEs in one transaction after
//     the InsertPending/RecordSessionStartIdentity/ApplyHookTransition
//     sequence: one UPDATE writes the options and defaults, and for a
//     pending row with no launch-start option a second UPDATE sets the
//     default launch start from the final started_at. Neither UPDATE
//     advances row_version.
//
// SR-20.3 defaults, for every column no option names: a well-formed launch
// token (16 lowercase hex, distinct per row), tmux_socket TestSocket,
// no_pre_trust 0, life_number 0, and no server or pane identity (NULL); a
// pending row's launch start is its final started_at (after WithStartedAt) in
// whole seconds as milliseconds (none when started_at does not parse as a
// time), and a row in any other state has none. The live-row pane default
// matching the Recorder's seeded session waits for the Recorder's session
// table (Epic t1.h98.a2 Task 3).
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
		if err := s.RecordSessionStartIdentity(id, sessionID, "", false, 0, ""); err != nil {
			return "", fmt.Errorf("SeedSpawn: RecordSessionStartIdentity %q: %w", sessionID, err)
		}
	}

	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false, "test_seed"); err != nil {
			return "", fmt.Errorf("SeedSpawn: ApplyHookTransition %q: %w", state, err)
		}
	}

	if err := applySpawnColumns(dbPath, id, state, opts); err != nil {
		return "", err
	}

	return id, nil
}

// applySpawnColumns writes the option overrides and the SR-20.3 defaults onto
// the already-seeded row through a raw parameterised UPDATE, in one
// transaction. apitest is the single blessed location for these column
// literals (SR-20.2 binds the no-inline-SQL rule to tests, not this helper).
// The column names come from the options' literals, never from input. The
// pending default launch start is a second statement because it reads the
// final started_at, which the first may have written.
func applySpawnColumns(dbPath, id, state string, opts []SpawnOption) error {
	o := &spawnOpts{}
	for _, opt := range opts {
		opt(o)
	}
	token, err := newLaunchToken()
	if err != nil {
		return fmt.Errorf("SeedSpawn: launch token: %w", err)
	}
	defaults := map[string]any{
		"launch_token":          token,
		"tmux_socket":           TestSocket,
		"no_pre_trust":          int64(0),
		"life_number":           int64(0),
		"tmux_server_pid":       nil,
		"tmux_server_started":   nil,
		"tmux_server_starttime": nil,
		"pane_id":               nil,
		"pane_pid":              nil,
		"pane_starttime":        nil,
	}
	for col, v := range defaults {
		if !o.has(col) {
			o.set(col, v)
		}
	}
	pendingDefaultStart := !o.has("launch_started_at") && state == store.StatePending
	if !o.has("launch_started_at") && !pendingDefaultStart {
		o.set("launch_started_at", nil)
	}

	cols := make([]string, 0, len(o.cols))
	for col := range o.cols {
		cols = append(cols, col)
	}
	sort.Strings(cols)
	setClauses := make([]string, 0, len(cols))
	args := make([]any, 0, len(cols)+1)
	for _, col := range cols {
		setClauses = append(setClauses, col+" = ?")
		args = append(args, o.cols[col])
	}
	args = append(args, id)

	raw, err := openRawStore(dbPath)
	if err != nil {
		return fmt.Errorf("SeedSpawn: %w", err)
	}
	defer raw.Close() //nolint:errcheck

	tx, err := raw.Begin()
	if err != nil {
		return fmt.Errorf("SeedSpawn: begin options tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	q := "UPDATE spawns SET " + strings.Join(setClauses, ", ") + " WHERE claude_instance_id = ?"
	if _, err := tx.Exec(q, args...); err != nil {
		return fmt.Errorf("SeedSpawn: apply options UPDATE: %w", err)
	}
	if pendingDefaultStart {
		if _, err := tx.Exec(`UPDATE spawns
		    SET launch_started_at = CAST(strftime('%s', started_at) AS INTEGER) * 1000
		  WHERE claude_instance_id = ?`, id); err != nil {
			return fmt.Errorf("SeedSpawn: default launch start UPDATE: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("SeedSpawn: commit options tx: %w", err)
	}
	return nil
}

// newLaunchToken returns a fresh well-formed launch token: 64 random bits as
// 16 lowercase hexadecimal characters (SR-3.5).
func newLaunchToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// openRawStore opens a raw connection to an existing store file, never
// creating one, with the store's busy timeout.
func openRawStore(dbPath string) (*sql.DB, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, fmt.Errorf("store file: %w", err)
	}
	raw, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, fmt.Errorf("raw open: %w", err)
	}
	return raw, nil
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
