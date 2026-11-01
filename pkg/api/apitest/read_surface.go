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
	"regexp"
	"slices"
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
	// settledRecord: the hooks' last raw change moved the record of the value
	// the scramble makes settled (ReadSurfaceSettled) to settled_evidence, so a
	// settling read confirms it (ReadSurfaceSettledConfirmation).
	settledRecord bool
}

// readSurfaceRows are the fixed rows, in seeding order: one per state, plus
// rows whose raw state went back and forth through hooks before settling.
func readSurfaceRows() []readSurfaceRow {
	ended := "/srv/rs/rs-ended/"
	return []readSurfaceRow{
		{id: "rs-pending", state: store.StatePending, opts: []SpawnOption{
			WithRawLabels(`{"role":"worker","team":"alpha"}`), WithRawAgentArgs(`["--model","opus"]`),
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
		{id: "rs-flap-waiting", state: store.StateWaiting, hooks: []string{"PreToolUse", "Stop", "PreToolUse", "Stop"}, settledRecord: true},
		{id: "rs-flap-working", state: store.StateWorking, hooks: []string{"Stop", "PreToolUse"}},
		{id: "rs-flap-check-permission", state: store.StateCheckPermission, hooks: []string{"PostToolUse", "PermissionRequest"}},
		{id: "rs-family-parent", state: store.StateWaiting},
		{id: "rs-family-child", state: store.StateWaiting},
		{id: "rs-family-grandchild", state: store.StateWorking},
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
// hook_gone_at, the state records' times), and a read writes only its own
// probe's process record, whose times NormalizeReadSurfaceProbes stands in
// for, so each read gives the same bytes on every run. Last, every row's settle columns are scrambled
// (ReadSurfaceSettled, ReadSurfaceStampMs), which status, get and list with
// no settle time must ignore but for get's and list's state_changed_at, the
// stamp. It returns the row ids in seeding order and fails when a row does
// not end in its intended raw state.
func SeedReadSurfaceStore(dbPath string, writerPID ...int) ([]string, error) {
	live, err := ReadSurfaceProcessOptions(writerPID...)
	if err != nil {
		return nil, err
	}
	rows := readSurfaceRows()
	ids := make([]string, 0, len(rows))
	for i, r := range rows {
		started := readSurfaceBase.Add(time.Duration(i) * time.Minute)
		opts := append([]SpawnOption{WithStartedAt(started), WithNoPane(), live[0], live[1]}, r.opts...)
		if r.state == store.StatePending {
			// An alive launch with a recorded pane retains its unreported note.
			opts = append(opts, func(o *spawnOpts) {
				o.set("pane_pid", o.cols["pid"])
				o.set("pane_starttime", o.cols["proc_starttime"])
			})
		}
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
	for _, link := range [][2]string{{"rs-family-parent", "rs-family-child"}, {"rs-family-child", "rs-family-grandchild"}} {
		if err := SeedParentChild(dbPath, link[0], link[1]); err != nil {
			return nil, fmt.Errorf("SeedReadSurfaceStore: %w", err)
		}
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
		family, _, _, err := ReadFamilyHistory(dbPath, r.id)
		if err != nil {
			return nil, fmt.Errorf("SeedReadSurfaceStore: %w", err)
		}
		if err := SetFamilyHistory(dbPath, r.id, family, ReadSurfaceSettledFamily(family), ReadSurfaceStampMs); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// ReadSurfaceStampMs is the state_changed_ms SeedReadSurfaceStore gives every
// row: 2099-01-01, ahead of any clock a test runs on, with a millisecond
// fraction get's and list's state_changed_at must keep (b.k6i).
var ReadSurfaceStampMs = readSurfaceSettledAt.Add(123 * time.Millisecond).UnixMilli()

// ReadSurfaceSettledFamily is the independent binary family scramble.
func ReadSurfaceSettledFamily(raw string) string {
	if raw == store.StateWorking {
		return store.StateWaiting
	}
	return store.StateWorking
}

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

// readSurfaceSeen is the last_seen_at pinReadSurfaceTimes gives row i, and
// the time of its state record; readSurfaceSettledAt is its settled record's.
func readSurfaceSeen(i int) time.Time {
	return readSurfaceBase.Add(time.Duration(i)*time.Minute + 30*time.Second)
}

func readSurfaceSettledRecordAt(i int) time.Time {
	return readSurfaceBase.Add(time.Duration(i)*time.Minute + 20*time.Second)
}

// pinReadSurfaceTimes writes, in one transaction, every time the hook and
// request writes stamped with the clock: each row's last_seen_at (30 s after
// its started_at), the at and checked of its state record (the same instant)
// and of its settled record (10 s earlier), and each request's created_at and
// hook_gone_at.
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
		seen, settled := readSurfaceSeen(i), readSurfaceSettledRecordAt(i).UnixMilli()
		if _, err := tx.Exec(`UPDATE spawns SET last_seen_at = ?,
		    state_evidence = CASE WHEN json_valid(state_evidence) THEN json_set(state_evidence, '$.at', ?, '$.checked', ?) END,
		    settled_evidence = CASE WHEN json_valid(settled_evidence) THEN json_set(settled_evidence, '$.at', ?, '$.checked', ?) END
		  WHERE instance_id = ?`,
			seen.Format(storeTimestampLayout), seen.UnixMilli(), seen.UnixMilli(), settled, settled, r.id); err != nil {
			return fmt.Errorf("SeedReadSurfaceStore: pin last_seen_at of %s: %w", r.id, err)
		}
	}
	for _, req := range readSurfaceRequests {
		if !req.relay {
			if _, err := tx.Exec(`UPDATE permission_requests SET owner_launch_token=NULL, owner_permission_epoch=NULL WHERE instance_id=? AND request_token=?`, req.spawnID, req.token); err != nil {
				return err
			}
		}
		var gone any
		if !req.hookGone.IsZero() {
			gone = req.hookGone.UnixMilli()
		}
		if _, err := tx.Exec(`UPDATE permission_requests SET created_at = ?, hook_gone_at = ?
		                       WHERE instance_id = ? AND request_token = ?`,
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

// ReadSurfaceProbed stands in, in the goldens, for both times of a live row's
// alive process record and the checked_at of a working row's failed pane
// attempt: the first confirmation and latest check use runtime clocks.
const ReadSurfaceProbed = "probed-by-this-read"

// probedRecord is a process record its own read confirmed by probing.
var probedRecord = regexp.MustCompile(`"process_liveness":\{"status":"confirmed","confirmed_at":"([^"]*)","source":"process","checked_at":"([^"]*)","reason":null\}`)

var displayAttemptRecord = regexp.MustCompile(`"displayed_state":\{"status":"not_confirmed","confirmed_at":null,"source":null,"checked_at":"([^"]*)","reason":"unreadable"\}`)

// NormalizeReadSurfaceProbes validates the exact record shape and pins each
// row's first process confirmation in first (one map per seeded store). The
// first confirmation must equal its first checked_at; every checked_at must
// fall within this call's [from, to]. Later reads retain the pinned time.
// Only these validated process times and failed pane-attempt times are
// replaced by ReadSurfaceProbed; every other byte is kept. c identifies status
// results, which do not carry instance_id themselves.
// Must use: a Go read-surface test normalizes probe times through this,
// never a second normalizer.
func NormalizeReadSurfaceProbes(t testing.TB, out string, from, to time.Time, first map[string]string, c ReadSurfaceCase) string {
	t.Helper()
	AssertConfirmationsShape(t, out)
	type row struct {
		ID            string `json:"instance_id"`
		Confirmations struct {
			Process struct {
				Status    string `json:"status"`
				Confirmed string `json:"confirmed_at"`
				Source    string `json:"source"`
				Checked   string `json:"checked_at"`
			} `json:"process_liveness"`
		} `json:"confirmations"`
	}
	var doc struct {
		row
		Spawns []row `json:"spawns"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	rows := doc.Spawns
	if rows == nil {
		rows = []row{doc.row}
		if rows[0].ID == "" {
			rows[0].ID, _ = c.Params["instance_id"].(string)
		}
	}
	for _, r := range rows {
		rec := r.Confirmations.Process
		if rec.Status != "confirmed" || rec.Source != "process" {
			continue // the unchanged golden checks every other record exactly
		}
		checked, err := time.Parse("2006-01-02T15:04:05.000Z", rec.Checked)
		if err != nil || checked.Before(from.Truncate(time.Millisecond)) || checked.After(to) {
			t.Errorf("%s process checked_at %q: want a millisecond UTC time within the read [%s, %s]", r.ID, rec.Checked, from, to)
		}
		if r.ID == "" {
			t.Fatal("probed process record has no fixture row identity")
		}
		want, seen := first[r.ID]
		if !seen {
			want = rec.Checked
			first[r.ID] = want
		}
		if rec.Confirmed != want {
			t.Errorf("%s process confirmed_at %q; want first confirmation %q", r.ID, rec.Confirmed, want)
		}
	}
	out = probedRecord.ReplaceAllStringFunc(out, func(rec string) string {
		m := probedRecord.FindStringSubmatch(rec)
		return strings.NewReplacer(m[1], ReadSurfaceProbed, m[2], ReadSurfaceProbed).Replace(rec)
	})
	// A working fixture has no pane: its own attempted capture is unreadable.
	return displayAttemptRecord.ReplaceAllStringFunc(out, func(rec string) string {
		m := displayAttemptRecord.FindStringSubmatch(rec)
		at, err := time.Parse("2006-01-02T15:04:05.000Z", m[1])
		if err != nil || at.Before(from.Truncate(time.Millisecond)) || at.After(to) {
			t.Errorf("display attempt %s: checked_at must be within the read [%s, %s]", rec, from, to)
			return rec
		}
		return strings.ReplaceAll(rec, m[1], ReadSurfaceProbed)
	})
}

// The SR-4.5 record enums, and the record time form.
var (
	confirmationKeys     = []string{"checked_at", "confirmed_at", "reason", "source", "status"}
	confirmationStatuses = []string{"confirmed", "not_confirmed", "unknown", "pending"}
	confirmationSources  = []string{"agent_event", "caller_command", "process", "tmux", "pane", "transcript", "store"}
	confirmationReasons  = []string{"no_evidence", "unreadable", "busy", "stale_observation", "deadline",
		"operation_pending", "pending_grace", "unsupported", "commit_unconfirmed"}
	confirmationTime = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$`)
)

// AssertConfirmationsShape fails t unless out, one status, get, list or sweep
// result (b.x9o), gives every agent result (the result itself, or each of
// list's spawns or sweep's checks) process_liveness alive, exited or unknown and confirmations
// of exactly the state, process_liveness and displayed_state records, each of
// exactly status, confirmed_at, source, checked_at and reason, every one
// present: statuses, sources and reasons from their enums, times RFC3339 UTC
// with milliseconds or null, source null exactly when confirmed_at is, reason
// null exactly when confirmed. A display source, when present, is pane.
// No nested object (a request, a prior session, the context) carries a
// confirmations wrapper, and family_state has no record.
// Must use: a Go read or sweep test checks confirmations shape through this,
// never a second shape check.
func AssertConfirmationsShape(t testing.TB, out string) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("result is not a JSON object: %v: %q", err, out)
	}
	results := []any{doc}
	if spawns, ok := doc["spawns"].([]any); ok {
		results = spawns
	}
	checks, sweep := doc["checks"]
	if sweep {
		var ok bool
		results, ok = checks.([]any)
		if !ok {
			t.Fatalf("checks must be a non-null array: %v", checks)
		}
	}
	for _, r := range results {
		row, _ := r.(map[string]any)
		if sweep {
			keys := []string{"completion", "confirmations", "family_state", "idle_screen_availability", "instance_id", "process_liveness", "reason", "state"}
			if _, ok := row["displayed_state"]; ok {
				keys = append(keys, "displayed_state")
				slices.Sort(keys)
			}
			if got := sortedKeys(row); !slices.Equal(got, keys) {
				t.Errorf("check keys %v; want %v", got, keys)
			}
			if id, ok := row["instance_id"].(string); !ok || id == "" {
				t.Errorf("check instance_id = %v", row["instance_id"])
			}
			if !slices.Contains([]string{"pending", "working", "waiting", "ask_user", "check_permission", "ended", "missing"}, fmt.Sprint(row["state"])) {
				t.Errorf("check state = %v", row["state"])
			}
			if !slices.Contains([]string{"working", "waiting"}, fmt.Sprint(row["family_state"])) {
				t.Errorf("check family_state = %v", row["family_state"])
			}
			if !slices.Contains([]string{"available", "unavailable"}, fmt.Sprint(row["idle_screen_availability"])) {
				t.Errorf("check idle_screen_availability = %v", row["idle_screen_availability"])
			}
			switch row["completion"] {
			case "complete":
				if row["reason"] != nil {
					t.Errorf("complete check has reason %v", row["reason"])
				}
			case "incomplete":
				if !slices.Contains([]string{"deadline", "busy", "unreadable", "stale_observation", "no_evidence", "commit_unconfirmed", "unsupported", "unattempted"}, fmt.Sprint(row["reason"])) {
					t.Errorf("incomplete check reason = %v", row["reason"])
				}
			default:
				t.Errorf("check completion = %v", row["completion"])
			}
		}
		records, ok := row["confirmations"].(map[string]any)
		if !ok || !slices.Contains([]string{"alive", "exited", "unknown"}, fmt.Sprint(row["process_liveness"])) {
			t.Errorf("agent result %v: want process_liveness alive, exited or unknown and a confirmations object", row)
			continue
		}
		if keys := sortedKeys(records); !slices.Equal(keys, []string{"displayed_state", "process_liveness", "state"}) {
			t.Errorf("confirmations keys %v; want exactly displayed_state, process_liveness and state", keys)
		}
		for field, v := range records {
			rec, _ := v.(map[string]any)
			if keys := sortedKeys(rec); !slices.Equal(keys, confirmationKeys) {
				t.Errorf("%s record keys %v; want exactly %v", field, keys, confirmationKeys)
				continue
			}
			status, _ := rec["status"].(string)
			confirmedAt, checkedAt := rec["confirmed_at"], rec["checked_at"]
			for _, at := range []any{confirmedAt, checkedAt} {
				if s, isString := at.(string); at != nil && (!isString || !confirmationTime.MatchString(s)) {
					t.Errorf("%s record time %v; want null or RFC3339 UTC milliseconds", field, at)
				}
			}
			source, reason := rec["source"], rec["reason"]
			switch {
			case !slices.Contains(confirmationStatuses, status):
				t.Errorf("%s record status %q is not one of %v", field, status, confirmationStatuses)
			case (confirmedAt == nil) != (source == nil) || (source != nil && !slices.Contains(confirmationSources, fmt.Sprint(source))):
				t.Errorf("%s record source %v with confirmed_at %v; want a known source exactly when confirmed_at is set", field, source, confirmedAt)
			case (status == "confirmed") != (reason == nil) || (reason != nil && !slices.Contains(confirmationReasons, fmt.Sprint(reason))):
				t.Errorf("%s record %s with reason %v; want a known reason exactly when not confirmed", field, status, reason)
			case field == "displayed_state" && source != nil && source != "pane":
				t.Errorf("displayed_state source %v; want pane", source)
			case status == "confirmed" && (confirmedAt == nil || checkedAt == nil):
				t.Errorf("confirmed record lacks evidence: %v", rec)
			case confirmedAt != nil && checkedAt != nil && fmt.Sprint(checkedAt) < fmt.Sprint(confirmedAt):
				t.Errorf("check predates confirmation: %v", rec)
			}
		}
		for k, v := range row {
			if k != "confirmations" && nestedConfirmations(v) {
				t.Errorf("agent result field %s nests a confirmations wrapper: %v", k, v)
			}
		}
	}
}

// nestedConfirmations reports whether v holds, at any depth, an object with a
// confirmations key.
func nestedConfirmations(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := x["confirmations"]; ok {
			return true
		}
		for _, e := range x {
			if nestedConfirmations(e) {
				return true
			}
		}
	case []any:
		for _, e := range x {
			if nestedConfirmations(e) {
				return true
			}
		}
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReadSurfaceSettledConfirmation is the state record a read with a settle
// time reports for row id, which shows its scrambled settled value: that
// value's own record where the row's hooks left one (settledRecord), else
// unknown, never the raw state's record.
// Must use: a read-surface test expects a settled state record through this,
// never a locally built one.
func ReadSurfaceSettledConfirmation(id string) map[string]any {
	for i, r := range readSurfaceRows() {
		if r.id == id && r.settledRecord {
			at := readSurfaceSettledRecordAt(i).Format("2006-01-02T15:04:05.000Z")
			return map[string]any{"status": "confirmed", "confirmed_at": at, "source": "agent_event", "checked_at": at, "reason": nil}
		}
	}
	return map[string]any{"status": "unknown", "confirmed_at": nil, "source": nil, "checked_at": nil, "reason": "no_evidence"}
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
