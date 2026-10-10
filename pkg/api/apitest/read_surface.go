package apitest

// read_surface.go — the fixed store and the case table that pin status, get
// and list output byte for byte (b.3ie: with [state] settle_ms unset or 0 the
// read surfaces must stay exactly as they were). The CLI test captures the
// goldens; the MCP and TS-client tests (through ts-helper's
// seed-read-surface-store) compare against the same files.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// readSurfaceBase is the instant every fixed time of SeedReadSurfaceStore is
// offset from.
var readSurfaceBase = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// readSurfaceRow is one row SeedReadSurfaceStore seeds: state is both the
// seeded state and the final raw state, as every hooks sequence (events the
// row's own agent fires after the seed) returns to it.
type readSurfaceRow struct {
	id, state, relayMode, sessionID string
	finished                        bool // ended or missing: ended_at is pinned
	opts                            []SpawnOption
	hooks                           []string
}

// readSurfaceRows are the fixed rows, in seeding order: one per state, plus
// rows whose raw state went back and forth through hooks before settling.
func readSurfaceRows() []readSurfaceRow {
	ended := "/srv/rs/rs-ended/"
	return []readSurfaceRow{
		{id: "rs-pending", state: store.StatePending, opts: []SpawnOption{
			WithRawLabels(`{"role":"worker","team":"alpha"}`), WithRawClaudeArgs(`["--model","opus"]`),
			WithLivenessUnverifiedSince("2026-01-02 03:04:35"), WithLivenessNote("unreported")}},
		{id: "rs-waiting", state: store.StateWaiting, sessionID: "sess-rs-waiting"},
		{id: "rs-working", state: store.StateWorking, relayMode: "on"},
		{id: "rs-ask-user", state: store.StateAskUser},
		{id: "rs-check-permission", state: store.StateCheckPermission, relayMode: "on"},
		{id: "rs-ended", state: store.StateEnded, sessionID: "sess-rs-ended-2", finished: true, opts: []SpawnOption{
			WithJsonlPath(ended + "sess-rs-ended-2.jsonl"),
			WithSessionHistory(SessionHistorySeed{SessionID: "sess-rs-ended-1", JSONLPath: ended + "sess-rs-ended-1.jsonl",
				RecordedAt: readSurfaceBase.Add(5*time.Minute + 20*time.Second)})}},
		{id: "rs-missing", state: store.StateMissing, finished: true, opts: []SpawnOption{WithNoLaunchToken()}},
		{id: "rs-flap-waiting", state: store.StateWaiting, hooks: []string{"PreToolUse", "Stop", "PreToolUse", "Stop"}},
		{id: "rs-flap-working", state: store.StateWorking, hooks: []string{"Stop", "PreToolUse"}},
		{id: "rs-flap-check-permission", state: store.StateCheckPermission, hooks: []string{"PostToolUse", "PermissionRequest"}},
	}
}

// readSurfaceRequest is one permission request SeedReadSurfaceStore records:
// by the relay hook (b.146 rule 1, settled far ahead, so it reads
// not_confirmed) or, when relay is false, as one recorded before schema v7
// (its hook found gone at hookGone, so it reads fallen_back and no read
// writes it).
type readSurfaceRequest struct {
	spawnID, token, tool, input, toolUseID string
	relay                                  bool
	created, hookGone                      time.Time
}

// readSurfaceRequests are the fixed requests, in insertion (request_id) order.
var readSurfaceRequests = []readSurfaceRequest{
	{spawnID: "rs-working", token: storefix.TestRequestTokenC, tool: "Write", input: `{"file_path":"/srv/rs/rs-working/notes.md"}`,
		created: readSurfaceBase.Add(2*time.Minute + 10*time.Second), hookGone: readSurfaceBase.Add(3 * time.Minute)},
	{spawnID: "rs-check-permission", token: storefix.TestRequestTokenA, tool: "Bash", input: `{"command":"ls"}`,
		toolUseID: "toolu_rs_a", relay: true, created: readSurfaceBase.Add(4*time.Minute + 10*time.Second)},
	{spawnID: "rs-check-permission", token: storefix.TestRequestTokenB, tool: "Bash", input: `{"command":"rm -rf build"}`,
		created: readSurfaceBase.Add(4*time.Minute + 20*time.Second), hookGone: readSurfaceBase.Add(5 * time.Minute)},
}

// readSurfaceSettledAt is the relay request's settle instant: far enough
// ahead that it reads not_confirmed on any run.
var readSurfaceSettledAt = time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)

// SeedReadSurfaceStore creates the store at dbPath and seeds the fixed rows
// the settle-off goldens pin status, get and list on: a row in every state
// (pending with labels, args and a liveness note; waiting with a session id
// and a parent; working with an open request; ask_user; check_permission with
// a relay request and one from before schema v7; ended with a transcript and
// session history; missing from before the release), plus three rows whose
// raw state flapped through hooks and came back. Every time the read surfaces
// show is pinned last (started_at, last_seen_at, ended_at, requested_at,
// hook_gone_at), and no read of the store writes, so each read gives the same
// bytes on every run. Last, every row's settle columns are scrambled
// (ReadSurfaceSettled, ReadSurfaceStampMs), which status, get and list with
// no settle time must ignore. It returns the row ids in seeding order and
// fails when a row does not end in its intended raw state.
func SeedReadSurfaceStore(dbPath string) ([]string, error) {
	rows := readSurfaceRows()
	ids := make([]string, 0, len(rows))
	for i, r := range rows {
		started := readSurfaceBase.Add(time.Duration(i) * time.Minute)
		opts := append([]SpawnOption{WithStartedAt(started)}, r.opts...)
		if r.finished {
			opts = append(opts, WithEndedAt(started.Add(50*time.Second)))
		}
		if _, err := SeedSpawn(dbPath, r.id, r.state, "/srv/rs/"+r.id, r.relayMode, r.sessionID, true, opts...); err != nil {
			return nil, fmt.Errorf("SeedReadSurfaceStore: %w", err)
		}
		ids = append(ids, r.id)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("SeedReadSurfaceStore: open store: %w", err)
	}
	defer s.Close() //nolint:errcheck
	for _, r := range rows {
		if err := fireReadSurfaceHooks(s, dbPath, r.id, r.hooks); err != nil {
			return nil, err
		}
	}
	if err := SeedParentChild(dbPath, "rs-working", "rs-waiting"); err != nil {
		return nil, fmt.Errorf("SeedReadSurfaceStore: %w", err)
	}
	for _, req := range readSurfaceRequests {
		if err := recordReadSurfaceRequest(s, dbPath, req); err != nil {
			return nil, err
		}
	}
	if err := pinReadSurfaceTimes(dbPath, rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		cols, err := ReadSpawnColumns(dbPath, r.id)
		if err != nil {
			return nil, fmt.Errorf("SeedReadSurfaceStore: %w", err)
		}
		if cols.State != r.state {
			return nil, fmt.Errorf("SeedReadSurfaceStore: %s ended in state %v; want %s", r.id, cols.State, r.state)
		}
		// The scramble (b.3ie): a settled state other than the raw one, stamped
		// far ahead (a negative age never settles), so every read with a
		// settle time reports ReadSurfaceSettled(state) and one without must
		// report the raw state, byte for byte as the goldens pin it.
		if err := SetSettle(dbPath, r.id, ReadSurfaceSettled(r.state), ReadSurfaceStampMs); err != nil {
			return nil, fmt.Errorf("SeedReadSurfaceStore: scramble %s: %w", r.id, err)
		}
	}
	return ids, nil
}

// ReadSurfaceStampMs is the state_changed_ms SeedReadSurfaceStore gives every
// row: 2099-01-01, ahead of any clock a test runs on.
var ReadSurfaceStampMs = readSurfaceSettledAt.UnixMilli()

// readSurfaceStates are the seven states, in the order ReadSurfaceSettled
// rotates them.
var readSurfaceStates = []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateAskUser,
	store.StateCheckPermission, store.StateEnded, store.StateMissing}

// ReadSurfaceSettled is the settled state SeedReadSurfaceStore gives a row
// whose raw state is state: the next state of the seven, never state itself.
func ReadSurfaceSettled(state string) string {
	for i, s := range readSurfaceStates {
		if s == state {
			return readSurfaceStates[(i+1)%len(readSurfaceStates)]
		}
	}
	return store.StateWaiting
}

// fireReadSurfaceHooks fires each event at id's row as its own agent, in order.
func fireReadSurfaceHooks(s *store.Store, dbPath, id string, events []string) error {
	if len(events) == 0 {
		return nil
	}
	return storefix.WithSeedPane(dbPath, id, func(gate store.HookGate) error {
		for _, ev := range events {
			applied, err := storefix.FireHook(s, id, ev, "", gate.ParentPID, gate.ParentStart)
			if err == nil && !applied.Applied {
				err = fmt.Errorf("not applied (reason %q)", applied.Reason)
			}
			if err != nil {
				return fmt.Errorf("SeedReadSurfaceStore: %s on %s: %w", ev, id, err)
			}
		}
		return nil
	})
}

// recordReadSurfaceRequest records req through the gated write of its row's
// own agent: the relay hook's insert, or the insert of a request recorded
// before schema v7.
func recordReadSurfaceRequest(s *store.Store, dbPath string, req readSurfaceRequest) error {
	err := storefix.WithSeedPane(dbPath, req.spawnID, func(gate store.HookGate) error {
		var (
			applied store.HookApplied
			err     error
		)
		if req.relay {
			_, applied, err = s.InsertRelayRequest(req.spawnID, gate, store.RelayRequest{RequestToken: req.token,
				ToolName: req.tool, ToolInput: req.input, ToolUseID: req.toolUseID, SettledAt: readSurfaceSettledAt},
				0, store.DefaultLockWait)
		} else {
			applied, err = s.UpsertOpenPermissionRequest(req.spawnID, gate, req.token, req.tool, req.input, 0,
				store.WriterProcessHook)
		}
		if err == nil && !applied.Applied {
			err = fmt.Errorf("not applied (reason %q)", applied.Reason)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("SeedReadSurfaceStore: request %s on %s: %w", req.token, req.spawnID, err)
	}
	return nil
}

// pinReadSurfaceTimes writes, in one transaction, every time the hook and
// request writes stamped with the clock: each row's last_seen_at (30 s after
// its started_at) and each request's created_at and hook_gone_at.
func pinReadSurfaceTimes(dbPath string, rows []readSurfaceRow) error {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return fmt.Errorf("SeedReadSurfaceStore: %w", err)
	}
	defer raw.Close() //nolint:errcheck
	tx, err := raw.Begin()
	if err != nil {
		return fmt.Errorf("SeedReadSurfaceStore: begin pin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	for i, r := range rows {
		seen := readSurfaceBase.Add(time.Duration(i)*time.Minute + 30*time.Second)
		if _, err := tx.Exec(`UPDATE spawns SET last_seen_at = ? WHERE claude_instance_id = ?`,
			seen.Format(storeTimestampLayout), r.id); err != nil {
			return fmt.Errorf("SeedReadSurfaceStore: pin last_seen_at of %s: %w", r.id, err)
		}
	}
	for _, req := range readSurfaceRequests {
		var gone any
		if !req.hookGone.IsZero() {
			gone = req.hookGone.UnixMilli()
		}
		if _, err := tx.Exec(`UPDATE permission_requests SET created_at = ?, hook_gone_at = ?
		                       WHERE claude_instance_id = ? AND request_token = ?`,
			req.created.Format(storeTimestampLayout), gone, req.spawnID, req.token); err != nil {
			return fmt.Errorf("SeedReadSurfaceStore: pin request %s: %w", req.token, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("SeedReadSurfaceStore: commit pin tx: %w", err)
	}
	return nil
}

// ReadSurfaceCases is the case table in <dir>/cases.json that the settle-off
// tests of every surface (CLI, MCP, TS client) share: each variant (a
// config.toml, or none) is run over every case against SeedReadSurfaceStore's
// store, and each case's output must equal <dir>/golden/<name>.json byte for
// byte (the CLI's stdout; an MCP text result or the TS client's
// JSON.stringify plus a newline).
type ReadSurfaceCases struct {
	Variants []SettleVariant   `json:"variants"`
	Cases    []ReadSurfaceCase `json:"cases"`
	dir      string
}

// SettleVariant is one config a case runs under: Config is config.toml's
// whole text, nil for no config file.
type SettleVariant struct {
	Name   string  `json:"name"`
	Config *string `json:"config"`
}

// ReadSurfaceCase is one status, get or list call: Params are the verb's
// MCP arguments (CLIArgs gives the CLI's flags).
type ReadSurfaceCase struct {
	Name   string         `json:"name"`
	Verb   string         `json:"verb"`
	Params map[string]any `json:"params"`
}

// LoadReadSurfaceCases reads <dir>/cases.json, failing the test when it
// cannot be read or parsed.
func LoadReadSurfaceCases(t testing.TB, dir string) ReadSurfaceCases {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		t.Fatalf("LoadReadSurfaceCases: %v", err)
	}
	var c ReadSurfaceCases
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatalf("LoadReadSurfaceCases: parse %s: %v", dir, err)
	}
	c.dir = dir
	return c
}

// GoldenPath is the golden file of the case named name.
func (c ReadSurfaceCases) GoldenPath(name string) string {
	return filepath.Join(c.dir, "golden", name+".json")
}

// Golden returns the golden bytes of the case named name, failing the test
// when the file is missing.
func (c ReadSurfaceCases) Golden(t testing.TB, name string) string {
	t.Helper()
	raw, err := os.ReadFile(c.GoldenPath(name))
	if err != nil {
		t.Fatalf("golden of %s: %v", name, err)
	}
	return string(raw)
}

// CLIArgs is the case as a CLI argv: the verb, then each param as its flag
// (underscores as dashes) in name order, a list joined by commas.
func (c ReadSurfaceCase) CLIArgs() []string {
	keys := make([]string, 0, len(c.Params))
	for k := range c.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	args := []string{c.Verb}
	for _, k := range keys {
		var v string
		switch x := c.Params[k].(type) {
		case []any:
			parts := make([]string, len(x))
			for i, p := range x {
				parts[i] = fmt.Sprint(p)
			}
			v = strings.Join(parts, ",")
		case float64:
			v = strconv.FormatFloat(x, 'f', -1, 64)
		default:
			v = fmt.Sprint(x)
		}
		args = append(args, "--"+strings.ReplaceAll(k, "_", "-"), v)
	}
	return args
}

// WriteConfig writes the variant's config.toml to path, creating its
// directory; with no config it writes nothing.
func (v SettleVariant) WriteConfig(t testing.TB, path string) {
	t.Helper()
	if v.Config == nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	if err := os.WriteFile(path, []byte(*v.Config), 0o600); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
}
