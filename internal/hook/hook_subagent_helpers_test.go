package hook_test

// hook_subagent_helpers_test.go — the row half of the shared helpers of the
// subagent-capture handler tests (b.xjn, SRD SR-15.1, SR-15.3):
// hook_subagent_records_test.go, hook_subagent_claims_test.go,
// hook_subagent_inputs_test.go and hook_subagent_safety_test.go. The payload
// variants and fixture constants are in hook_subagent_payloads_test.go. They
// sit in package hook_test because internal/testsupport/storefix imports
// internal/hook.
//
//   - subRow and its fire methods: one seeded row, hook.Handle with an injected
//     receipt time and a captured hook log.
//   - wantEntry, assertEntries and assertLaunches: what a hook stored; step and
//     script run a sequence of hooks and check the store after each.

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// ---------------------------------------------------------------------------
// One seeded row and the hooks fired at it

// subRow is a seeded live row and its store, whose own agent and a foreign
// process fire hooks.
type subRow struct {
	t      *testing.T
	st     *store.Store
	dbPath string
	id     string
}

// newSubRow seeds id in the working state with its pane recorded (and any
// opts) in a temp store.
func newSubRow(t *testing.T, id string, opts ...apitest.SpawnOption) *subRow {
	t.Helper()
	return newSubRowIn(t, id, store.StateWorking, opts...)
}

// newSubRowIn is newSubRow in state.
func newSubRowIn(t *testing.T, id, state string, opts ...apitest.SpawnOption) *subRow {
	t.Helper()
	st, dbPath := seedAgentRow(t, id, state, opts...)
	return &subRow{t: t, st: st, dbPath: dbPath, id: id}
}

// fired is one hook run's observable outcome.
type fired struct {
	// Stdout is what the hook wrote to stdout; every run asserts it is empty.
	Stdout string
	// Log is the hook log's text.
	Log string
	// LogLines is the number of lines in Log.
	LogLines int
	// Fired is the run's one ad.hook.fired line.
	Fired map[string]any
	// Trail is every trail line the run wrote.
	Trail []map[string]any
	// Ignored is the ad.hook.ignored lines of the row.
	Ignored []map[string]any
}

// upsert is ad.hook.fired's upsert_outcome.
func (f fired) upsert() any { return f.Fired["upsert_outcome"] }

// transitions is the run's ad.spawn.state_transition lines.
func (f fired) transitions() []map[string]any {
	var out []map[string]any
	for _, line := range f.Trail {
		if line["event"] == "ad.spawn.state_transition" {
			out = append(out, line)
		}
	}
	return out
}

// fire runs payload as a hook of the row's own agent, receipt time atMs.
func (r *subRow) fire(payload []byte, atMs int64) fired {
	r.t.Helper()
	return r.fireFrom(agentParent(r.t, r.st, r.id), payload, atMs)
}

// fireForeign runs payload as a hook of another process carrying the row's id.
func (r *subRow) fireForeign(payload []byte, atMs int64) fired {
	r.t.Helper()
	return r.fireFrom(foreignParent(r.t, r.st, r.id), payload, atMs)
}

// fireFrom runs hook.Handle for the row from p with HandleConfig.Start at atMs
// (relay off), asserting it returns nil with empty stdout and wrote one
// ad.hook.fired.
func (r *subRow) fireFrom(p hookParent, payload []byte, atMs int64) fired {
	r.t.Helper()
	hc := hookConfig(envHook(r.id, ""), p)
	hc.Start = time.UnixMilli(atMs)
	var stdout, logBuf bytes.Buffer
	before := len(readTrailLines(r.t, trailFile()))
	if err := hook.Handle(context.Background(), bytes.NewReader(payload), &stdout, r.st, hc, log.New(&logBuf, "", 0)); err != nil {
		r.t.Fatalf("Handle: %v; want nil (fail-open)", err)
	}
	f := fired{Stdout: stdout.String(), Log: logBuf.String(), Trail: readTrailLines(r.t, trailFile())[before:]}
	if f.Stdout != "" {
		r.t.Errorf("stdout = %q; want empty", f.Stdout)
	}
	if f.Log != "" {
		f.LogLines = strings.Count(strings.TrimRight(f.Log, "\n"), "\n") + 1
	}
	var all []map[string]any
	for _, line := range f.Trail {
		switch {
		case line["event"] == "ad.hook.fired":
			all = append(all, line)
		case line["event"] == "ad.hook.ignored" && line["instance_id"] == r.id:
			f.Ignored = append(f.Ignored, line)
		}
	}
	if len(all) != 1 {
		r.t.Fatalf("ad.hook.fired lines = %d; want 1", len(all))
	}
	f.Fired = all[0]
	return f
}

// run fires each payload in order as the own agent, step n at at(n+1), and
// returns the last run.
func (r *subRow) run(payloads ...[]byte) fired {
	r.t.Helper()
	var last fired
	for i, p := range payloads {
		last = r.fire(p, at(int64(i)+1))
	}
	return last
}

// spawn reads the row's raw columns.
func (r *subRow) spawn() apitest.SpawnColumns {
	r.t.Helper()
	c, err := apitest.ReadSpawnColumns(r.dbPath, r.id)
	if err != nil {
		r.t.Fatalf("ReadSpawnColumns(%q): %v", r.id, err)
	}
	return c
}

// rows reads the row's entries and claimable launches.
func (r *subRow) rows() apitest.SubagentRows {
	r.t.Helper()
	rows, err := apitest.ReadSubagentRows(r.dbPath, r.id)
	if err != nil {
		r.t.Fatalf("ReadSubagentRows(%q): %v", r.id, err)
	}
	return rows
}

// ---------------------------------------------------------------------------
// What was stored

// wantEntry is one expected entry; a field left nil is expected NULL, except
// session (nil is subSessionID). Times are Unix ms (int64); opened 0 is not
// checked.
type wantEntry struct {
	id        string
	state     string
	confirmed bool
	session   any
	typ       any
	desc      any
	model     any
	parent    any // parent_type
	parentID  any
	toolUseID any
	runMode   any
	first     any // first_started_at
	started   any
	stopped   any
	lost      any
	closed    any // close_reason
	opened    int64
}

// running is the expected record of a start's entry that no launch result has
// filled: confirmed (claimed) or not, running, its start's type only, started
// and opened at ms.
func running(id, typ string, confirmed bool, ms int64) wantEntry {
	w := wantEntry{id: id, state: "running", confirmed: confirmed, first: ms, started: ms, opened: ms}
	if typ != "" {
		w.typ = typ
	}
	return w
}

// stoppedAt is w stopped by a stop event (a SubagentStop's) at ms.
func (w wantEntry) stoppedAt(ms int64) wantEntry {
	w.state, w.stopped, w.closed = "stopped", ms, "stop_event"
	return w
}

// filled is a running record whose own background launch result has applied:
// every field set, run_mode background, parent the agent.
func filled(r *subRow, id, typ, desc, model, toolUseID string, ms int64) wantEntry {
	w := running(id, typ, true, ms)
	w.desc, w.model, w.toolUseID, w.runMode = desc, model, toolUseID, "background"
	w.parent, w.parentID = "agent", r.id
	return w
}

func (w wantEntry) sessionValue() any {
	if w.session == nil {
		return subSessionID
	}
	return w.session
}

// assertEntries fails unless the row's entries are exactly wants, matched by
// subagent id, each in the row's current launch.
func assertEntries(t *testing.T, r *subRow, wants ...wantEntry) {
	t.Helper()
	rows := r.rows()
	token := r.spawn().LaunchToken
	byID := map[string]apitest.SubagentEntryColumns{}
	for _, e := range rows.Entries {
		byID[e.SubagentID.(string)] = e
	}
	if len(rows.Entries) != len(wants) {
		var ids []any
		for _, e := range rows.Entries {
			ids = append(ids, e.SubagentID)
		}
		t.Errorf("entries = %v; want %d of them", ids, len(wants))
	}
	for _, w := range wants {
		got, ok := byID[w.id]
		if !ok {
			t.Errorf("no entry %q", w.id)
			continue
		}
		confirmed := int64(0)
		if w.confirmed {
			confirmed = 1
		}
		for _, c := range []struct {
			name      string
			got, want any
		}{
			{"state", got.State, w.state},
			{"confirmed", got.Confirmed, confirmed},
			{"launch_token", got.LaunchToken, token},
			{"session_id", got.SessionID, w.sessionValue()},
			{"subagent_type", got.SubagentType, w.typ},
			{"description", got.Description, w.desc},
			{"model", got.Model, w.model},
			{"parent_type", got.ParentType, w.parent},
			{"parent_id", got.ParentID, w.parentID},
			{"tool_use_id", got.ToolUseID, w.toolUseID},
			{"run_mode", got.RunMode, w.runMode},
			{"first_started_at", got.FirstStartedAt, w.first},
			{"started_at", got.StartedAt, w.started},
			{"stopped_at", got.StoppedAt, w.stopped},
			{"lost_at", got.LostAt, w.lost},
			{"close_reason", got.CloseReason, w.closed},
		} {
			if !reflect.DeepEqual(c.got, c.want) {
				t.Errorf("entry %s: %s = %#v; want %#v", w.id, c.name, c.got, c.want)
			}
		}
		if w.opened != 0 && !reflect.DeepEqual(got.OpenedAt, w.opened) {
			t.Errorf("entry %s: opened_at = %#v; want %d", w.id, got.OpenedAt, w.opened)
		}
	}
}

// records are the row's confirmed entries (the ones a verb would return), by id.
func records(r *subRow) []string {
	r.t.Helper()
	var ids []string
	for _, e := range r.rows().Entries {
		if e.Confirmed == int64(1) {
			ids = append(ids, e.SubagentID.(string))
		}
	}
	return ids
}

// wantLaunch is one expected claimable launch.
type wantLaunch struct {
	toolUseID, typ string
	opened         int64
}

// assertLaunches fails unless the row's claimable launches are exactly wants,
// oldest first, each in the row's current launch.
func assertLaunches(t *testing.T, r *subRow, wants ...wantLaunch) {
	t.Helper()
	got := r.rows().Launches
	token := r.spawn().LaunchToken
	if len(got) != len(wants) {
		t.Errorf("claimable launches = %d (%+v); want %d", len(got), got, len(wants))
		return
	}
	for i, w := range wants {
		g := got[i]
		if g.ToolUseID != w.toolUseID || g.SubagentType != w.typ || g.LaunchToken != token ||
			(w.opened != 0 && g.OpenedAt != w.opened) {
			t.Errorf("claimable launch %d = %+v; want tool_use_id %q type %q opened %d in launch %v",
				i, g, w.toolUseID, w.typ, w.opened, token)
		}
	}
}

// ---------------------------------------------------------------------------
// Scans

// storeContains reports whether the store file at dbPath, or its write-ahead
// log, holds needle anywhere.
func storeContains(t *testing.T, dbPath, needle string) bool {
	t.Helper()
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		b, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if bytes.Contains(b, []byte(needle)) {
			return true
		}
	}
	return false
}

// trailContains reports whether any of lines holds needle once encoded.
func trailContains(t *testing.T, lines []map[string]any, needle string) bool {
	t.Helper()
	for _, line := range lines {
		b, err := json.Marshal(line)
		if err != nil {
			t.Fatalf("marshal trail line: %v", err)
		}
		if strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Scripts

// step is one hook of a script: payload fired at receipt time at, then the
// store checked: its entries and claimable launches must be exactly entries and
// launches. upsert, when set, is the hook's ad.hook.fired upsert_outcome.
type step struct {
	name     string
	payload  []byte
	at       int64
	upsert   string
	entries  []wantEntry
	launches []wantLaunch
}

// script fires each step as the row's own agent, in order, one subtest each.
func (r *subRow) script(t *testing.T, steps []step) {
	t.Helper()
	for _, s := range steps {
		t.Run(s.name, func(t *testing.T) {
			outer := r.t
			r.t = t
			defer func() { r.t = outer }()
			f := r.fire(s.payload, s.at)
			if s.upsert != "" && f.upsert() != s.upsert {
				t.Errorf("upsert_outcome = %v; want %q", f.upsert(), s.upsert)
			}
			assertEntries(t, r, s.entries...)
			assertLaunches(t, r, s.launches...)
		})
	}
}
