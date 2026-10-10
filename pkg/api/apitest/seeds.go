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
	"time"

	"github.com/google/uuid"

	_ "modernc.org/sqlite"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// SeedSpawn inserts a single spawn row and transitions it to state.
//
//   - dbPath: path to the SQLite store file.
//
//   - id: claude_instance_id; auto-generated UUID if empty.
//
//   - state: target state (e.g. "waiting", "working", "ended"). Defaults to
//     "waiting" if empty.
//
//   - cwd: working directory stored on the row; defaults to "/tmp" if empty.
//
//   - relayMode: relay_mode value ("on"|"off"|""). Defaults to "off" if empty.
//
//   - sessionID: if non-empty, recorded after InsertPending through a gated
//     soft-refresh hook (SR-22.9) so the row has a claude_session_id (required
//     by the resume verb's pre-flight) and keeps its state: a pending row
//     seeded with a session id stays pending. Only the session id is seeded
//     here; the identity columns (pid/proc_starttime/jsonl_path) are seeded
//     via opts below.
//
//   - createStore: if true the store is created when missing (OpenOrInit);
//     if false the store must already exist (Open).
//
//   - opts: optional trailing SpawnOption values seeding columns the store
//     writes above cannot (the v3 identity/liveness columns, the v5 columns,
//     timestamps and raw structured text) and session history
//     (WithSessionHistory). Options and the SR-20.3 defaults are applied by
//     apitest-internal SQL in one transaction after the InsertPending and
//     gated hook sequence:
//     one UPDATE writes the options and defaults, for a pending row with no
//     launch-start option a second UPDATE sets the default launch start from
//     the final started_at, and then each WithSessionHistory entry is
//     inserted into session_history in seeding order (a duplicate session id
//     is an error; with no WithSessionHistory no history is written). None of
//     these statements advances row_version.
//
// The hook writes are the row's own agent's (SR-22.9): every hook write is
// gated on the hook's parent being the row's recorded pane process, so the
// seed pane (TestPanePID with storefix.SeedPaneStarttime) is recorded first
// (storefix.WithSeedPane) and the gated writes carry the matching gate
// (storefix.SeedAgentWrites); the pane and process identity are then written
// back, and the final UPDATE below writes the defaults and options.
//
// SR-20.3 defaults, for every column no option names: a well-formed launch
// token (16 lowercase hex, distinct per row), tmux_socket TestSocket,
// no_pre_trust 0, life_number 0, no process identity (pid and proc_starttime
// NULL) and no server identity (NULL); a live row
// (store.IsLiveState) gets the pane TestPaneID with pid TestPanePID and no
// pane start time, and a row in a terminal state no pane (NULL), so the
// Recorder session tmuxfix.Recorder.SeedRowSession seeds for the row holds
// the row's pane; a pending row's launch start is its final started_at
// (after WithStartedAt) in whole seconds as milliseconds (none when
// started_at does not parse as a time), and a row in any other state has
// none. WithLaunchIdentity and WithNoLaunchToken override the pane default
// with the other launch-identity columns.
//
// Seeding and row_version: InsertPending starts the row at 0, and the gated
// soft refresh that records the session id (when sessionID is non-empty) and
// the gated transition (when state is not "pending") each add one. The seed
// pane writes, the option/default UPDATEs above and the apitest and storefix
// backdating fixtures add nothing. So a pending row seeded with no session id is at 0,
// like a fresh insert, and every other seed starts above 0. Tests assert
// version deltas (after minus before, both read through ReadSpawnColumns),
// never absolute values, except for a row the test inserted itself.
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

	if sessionID != "" || state != store.StatePending {
		if err := storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
			return storefix.SeedAgentWrites(s, gate, id, sessionID, state)
		}); err != nil {
			return "", fmt.Errorf("SeedSpawn: %w", err)
		}
	}

	if err := applySpawnColumns(dbPath, id, state, opts); err != nil {
		return "", err
	}

	return id, nil
}

// applySpawnColumns writes the option overrides and the SR-20.3 defaults onto
// the already-seeded row through a raw parameterised UPDATE, then inserts the
// WithSessionHistory entries, all in one transaction. apitest is the single blessed location for these column
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
		"pid":                   nil,
		"proc_starttime":        nil,
	}
	if store.IsLiveState(state) {
		defaults["pane_id"], defaults["pane_pid"] = TestPaneID, int64(TestPanePID)
	}
	for col, v := range defaults {
		if !o.has(col) {
			o.set(col, v)
		}
	}
	seen := make(map[string]bool, len(o.history))
	for _, h := range o.history {
		if seen[h.SessionID] {
			return fmt.Errorf("SeedSpawn: WithSessionHistory: duplicate session id %q for instance %s (one entry per instance and session id)", h.SessionID, id)
		}
		seen[h.SessionID] = true
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
	if err := insertSessionHistory(tx, id, o.history); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("SeedSpawn: commit options tx: %w", err)
	}
	return nil
}

// insertSessionHistory inserts the WithSessionHistory entries for id on tx,
// one INSERT each in seeding order, so history_id rises with seeding order.
// An entry with no RecordedAt takes the column's default (the current time).
func insertSessionHistory(tx *sql.Tx, id string, entries []SessionHistorySeed) error {
	for _, h := range entries {
		var pathArg any
		if h.JSONLPath != "" {
			pathArg = h.JSONLPath
		}
		var err error
		if h.RecordedAt.IsZero() {
			_, err = tx.Exec(`INSERT INTO session_history
			    (claude_instance_id, claude_session_id, jsonl_path, life_number)
			  VALUES (?, ?, ?, ?)`, id, h.SessionID, pathArg, h.Life)
		} else {
			_, err = tx.Exec(`INSERT INTO session_history
			    (claude_instance_id, claude_session_id, jsonl_path, life_number, recorded_at)
			  VALUES (?, ?, ?, ?, ?)`, id, h.SessionID, pathArg, h.Life,
				h.RecordedAt.UTC().Format(storeTimestampLayout))
		}
		if err != nil {
			return fmt.Errorf("SeedSpawn: WithSessionHistory %q: %w", h.SessionID, err)
		}
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
	raw, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout(%d)", dbPath, store.DefaultBusyTimeoutMs))
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
// using toolName. The spawn row must already exist. The INSERT is the gated
// hook write (SR-22.9) played by the row's own agent (storefix.WithSeedPane;
// the row's pane and process identity are left as they were). Returns both
// the request_id (AUTOINCREMENT) and the request_token (UUIDv4) so callers can
// reference the row by either key.
func SeedPermissionRequest(dbPath, spawnID, toolName string) (PermissionRequestSeed, error) {
	s, err := store.Open(dbPath)
	if err != nil {
		return PermissionRequestSeed{}, fmt.Errorf("SeedPermissionRequest: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck

	requestToken := uuid.NewString()
	if err := storefix.WithSeedPane(dbPath, spawnID, func(gate store.HookGate) error {
		applied, err := s.UpsertOpenPermissionRequest(spawnID, gate, requestToken, toolName, "{}", 0, "")
		if err == nil && !applied.Applied {
			err = fmt.Errorf("hook not applied (reason %q)", applied.Reason)
		}
		return err
	}); err != nil {
		return PermissionRequestSeed{}, fmt.Errorf("SeedPermissionRequest: upsert: %w", err)
	}

	row, err := s.GetPermissionRequest(spawnID, requestToken)
	if err != nil {
		return PermissionRequestSeed{}, fmt.Errorf("SeedPermissionRequest: get: %w", err)
	}
	return PermissionRequestSeed{RequestID: row.RequestID, RequestToken: requestToken}, nil
}

// AgePermissionRequest backdates the created_at of request requestID by
// createdAgo and records its hook_gone_at hookGoneAgo before now, each only
// when positive, through a raw connection (the store writes neither on
// request): a request recorded before schema v7 (SeedPermissionRequest)
// backdated past the relay window has fallen back by time, and one whose
// hook was found gone more than 2 s ago can be recorded answered outside
// agent-director (record-pane-answer; b.146 rule 13).
func AgePermissionRequest(dbPath string, requestID int64, createdAgo, hookGoneAgo time.Duration) error {
	if createdAgo <= 0 && hookGoneAgo <= 0 {
		return nil
	}
	raw, err := openRawStore(dbPath)
	if err != nil {
		return fmt.Errorf("AgePermissionRequest: %w", err)
	}
	defer raw.Close() //nolint:errcheck
	now := time.Now().UTC()
	if createdAgo > 0 {
		if _, err := raw.Exec(`UPDATE permission_requests SET created_at = ? WHERE request_id = ?`,
			now.Add(-createdAgo).Format("2006-01-02 15:04:05"), requestID); err != nil {
			return fmt.Errorf("AgePermissionRequest: created_at: %w", err)
		}
	}
	if hookGoneAgo > 0 {
		if _, err := raw.Exec(`UPDATE permission_requests SET hook_gone_at = ? WHERE request_id = ?`,
			now.Add(-hookGoneAgo).UnixMilli(), requestID); err != nil {
			return fmt.Errorf("AgePermissionRequest: hook_gone_at: %w", err)
		}
	}
	return nil
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
