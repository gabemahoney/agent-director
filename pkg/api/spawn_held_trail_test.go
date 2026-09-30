package api_test

// spawn_held_trail_test.go covers plain spawn's ad.launch.name_held record
// after "duplicate session" (SR-14, SR-9.4, SR-15; AC-SPN-10): exactly one
// per held-name launch, every field checked per re-lookup outcome and per end
// write result, none on any other path, and fail-open. It uses the heldEnv
// fixture (spawn_held_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// ptName is the requested name; ptCreated the holders' creation epoch.
const (
	ptName    = "held-trail"
	ptCreated = int64(1790000100)
)

// ptKeys is an ad.launch.name_held record's full key set for plain spawn's
// held-name path: SR-14's fields plus the envelope's event and ts.
var ptKeys = []string{"event", "ts", "source", "claude_instance_id", "launch", "tmux_session_name",
	"tmux_socket", "tmux_session_id", "session_created", "store_id", "carries_this_id", "current_launch",
	"lookup_outcome", "outcome", "row_result", "store_error", "attach_command", "end_command",
	"caller_process", "caller_pid", "caller_hostname", "caller_user"}

// ptHolder is a session holding ptName as sessionID, created at ptCreated.
func ptHolder(sessionID string, label tmux.Label, labelSet bool) tmuxfix.SeedSession {
	s := heldSession(ptName, sessionID, label, labelSet)
	s.Created = ptCreated
	return s
}

// ptWant is one record's expected per-case fields. sessionID "" means no
// holder identified; carries and current are true, false or nil (null).
type ptWant struct {
	lookup, outcome, rowResult string
	sessionID                  string
	carries, current           any
	storeError                 bool // store_error holds the store's error text
}

// ptRecords returns id's event records added after the first mark lines.
func ptRecords(t *testing.T, mark int, event, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range trailSince(t, mark, event) {
		if r["claude_instance_id"] == id {
			out = append(out, r)
		}
	}
	return out
}

// ptCaller is the caller fields every record carries: this test process.
func ptCaller() map[string]any {
	host, _ := os.Hostname()
	var username string
	if u, err := user.Current(); err == nil {
		username = u.Username
	}
	return map[string]any{"caller_process": filepath.Base(os.Args[0]), "caller_pid": float64(os.Getpid()),
		"caller_hostname": host, "caller_user": username}
}

// ptErrName is err's catalogued err_name, as the record's outcome carries it.
func ptErrName(err error) string {
	name, _ := errnames.Classify(err)
	return name
}

// ptAssertRecord checks rec is id's plain-spawn record matching w: every
// field, the exact key set, and none of the forbidden values in its text.
func (e heldEnv) ptAssertRecord(t *testing.T, rec map[string]any, id string, w ptWant, forbid ...string) {
	t.Helper()
	q := func(s string) string { return "'" + s + "'" }
	want := map[string]any{
		"source": "ad_spawn", "claude_instance_id": id, "launch": "spawn", "tmux_session_name": ptName,
		"tmux_socket": e.socket, "store_id": e.storeID, "lookup_outcome": w.lookup, "outcome": w.outcome,
		"row_result": w.rowResult, "carries_this_id": w.carries, "current_launch": w.current,
		"tmux_session_id": nil, "session_created": nil, "attach_command": nil, "end_command": nil, "store_error": nil,
	}
	if w.sessionID != "" {
		want["tmux_session_id"], want["session_created"] = w.sessionID, float64(ptCreated)
		want["attach_command"] = "tmux -u -S " + q(e.socket) + " attach-session -r -t " + q(w.sessionID)
		want["end_command"] = "tmux -u -S " + q(e.socket) + " kill-session -t " + q(w.sessionID)
	}
	for k, v := range ptCaller() {
		want[k] = v
	}
	if w.storeError {
		msg, _ := rec["store_error"].(string)
		if msg == "" || !strings.Contains(e.logs.String(), msg) {
			t.Errorf("store_error = %v; want the store's error text, as in the WARN line %q", rec["store_error"], e.logs.String())
		}
		delete(want, "store_error")
	}
	for k, v := range want {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("name_held[%q] = %v (present %t); want %v", k, got, ok, v)
		}
	}
	var keys []string
	for k := range rec {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wantKeys := slices.Clone(ptKeys)
	slices.Sort(wantKeys)
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("record keys = %q; want %q", keys, wantKeys)
	}
	text, _ := json.Marshal(rec)
	for _, v := range forbid {
		if v != "" && strings.Contains(string(text), v) {
			t.Errorf("record %s contains %q", text, v)
		}
	}
}

// ptForbid lists what no record may carry: launch tokens, another store's id,
// the seeded label values and other ids.
func (e heldEnv) ptForbid(id, rowToken string, seeded []tmuxfix.SeedSession, extra ...string) []string {
	out := append([]string{rowToken, tmuxfix.Token, tmuxfix.OtherToken, apitest.OtherStoreID(e.storeID)}, extra...)
	for _, s := range seeded {
		if s.Label.Kind == tmux.LabelValid {
			out = append(out, tmuxfix.LabelValue(s.Label.Token, s.ID, id, s.Label.StoreID))
		}
	}
	return out
}

// TestSpawnHeldTrailPerOutcome: each held-name spawn writes exactly one
// ad.launch.name_held with every SR-14 field for its re-lookup outcome.
func TestSpawnHeldTrailPerOutcome(t *testing.T) {
	other := "other-" + uuid.NewString()[:8]
	noLabel := func(heldEnv, string) []tmuxfix.SeedSession {
		return []tmuxfix.SeedSession{ptHolder("$4", tmux.Label{}, false)}
	}
	script := func(s tmuxfix.Script) func(e heldEnv) {
		s.Times = 1
		return func(e heldEnv) { e.rec.Script(e.socket, s, tmux.CallLookup) }
	}
	conflict := ptErrName(api.ErrTmuxSessionConflict)
	cases := []struct {
		name     string
		holders  func(e heldEnv, id string) []tmuxfix.SeedSession // seeded before the spawn
		late     func(e heldEnv, id string) []tmuxfix.SeedSession // seeded as the scan's lookup returns
		relookup func(e heldEnv)                                  // run as the scan's lookup returns
		vanish   bool
		want     ptWant
		disagree bool // one scope_value ad.provenance.disagree record
	}{
		{name: "old label", late: func(e heldEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{e.leftover(ptName, "$4", id, ptCreated)}
		}, want: ptWant{lookup: "leftover", outcome: conflict, sessionID: "$4", carries: true, current: false}},
		{name: "foreign label", holders: func(e heldEnv, _ string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{ptHolder("$4", tmuxfix.Valid(tmuxfix.OtherToken, other, e.storeID), true)}
		}, want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "another store's label naming this id", holders: func(e heldEnv, id string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{ptHolder("$4", tmuxfix.Valid(tmuxfix.OtherToken, id, apitest.OtherStoreID(e.storeID)), true)}
		}, want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "no label", holders: noLabel,
			want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "invalid label", holders: func(heldEnv, string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{ptHolder("$4", tmux.Label{}, true)}
		}, want: ptWant{lookup: "gone", outcome: conflict, sessionID: "$4", carries: false}},
		{name: "vanished", holders: noLabel, vanish: true,
			want: ptWant{lookup: "gone", outcome: ptErrName(api.ErrTmuxSessionCreate)}},
		{name: "more than one listing entry matches", holders: func(heldEnv, string) []tmuxfix.SeedSession {
			return []tmuxfix.SeedSession{ptHolder("$4", tmux.Label{}, false), ptHolder("$5", tmux.Label{}, false)}
		}, want: ptWant{lookup: "gone", outcome: ptErrName(api.ErrTmuxUnresponsive)}},
		{name: "conflicting labels", holders: noLabel, relookup: func(e heldEnv) {
			e.rec.SetScope(e.socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{})
		}, want: ptWant{lookup: "provenance_conflict", outcome: conflict, sessionID: "$4"}, disagree: true},
		{name: "unreadable", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailTimeout}),
			want: ptWant{lookup: "cant_tell", outcome: ptErrName(api.ErrTmuxUnresponsive)}},
		{name: "tmux unavailable", holders: noLabel, relookup: script(tmuxfix.Script{Failure: tmux.FailSocketDenied}),
			want: ptWant{lookup: "tmux_unavailable", outcome: ptErrName(api.ErrTmuxNotAvailable)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			var seeded []tmuxfix.SeedSession
			if tc.holders != nil {
				seeded = tc.holders(e, id)
				e.rec.SeedSessions(e.socket, seeded...)
			}
			if tc.vanish {
				e.rec.RemoveSessionAfter(tmux.CallCreate, e.socket, seeded[0].ID)
			}
			mark := trailLen(t)

			run := e.spawnHeld(t, id, ptName, func() {
				if tc.late != nil {
					late := tc.late(e, id)
					seeded = append(seeded, late...)
					e.rec.SeedSessions(e.socket, late...)
				}
				if tc.relookup != nil {
					tc.relookup(e)
				}
			})

			if name := ptErrName(run.err); name != tc.want.outcome {
				t.Errorf("err = %v (%s); want %s", run.err, name, tc.want.outcome)
			}
			token := e.assertEndedRow(t, id, run.createdAt)
			recs := ptRecords(t, mark, "ad.launch.name_held", id)
			if len(recs) != 1 {
				t.Fatalf("ad.launch.name_held records = %d; want 1: %v", len(recs), recs)
			}
			tc.want.rowResult = "ended"
			forbid := e.ptForbid(id, token, seeded, other)
			e.ptAssertRecord(t, recs[0], id, tc.want, forbid...)

			dis := ptRecords(t, mark, "ad.provenance.disagree", id)
			if !tc.disagree {
				if len(dis) != 0 {
					t.Errorf("ad.provenance.disagree records = %v; want none", dis)
				}
				return
			}
			if len(dis) != 1 {
				t.Fatalf("ad.provenance.disagree records = %d; want 1: %v", len(dis), dis)
			}
			want := map[string]any{"verb": "spawn", "source": "ad_spawn", "reason": tmux.ReasonScopeValue,
				"tmux_socket": e.socket, "tmux_session_name": ptName, "tmux_session_id": "$4",
				"current_session_name": nil, "server": tmux.ServerUnknown, "verdict": "provenance_conflict", "action": "ended"}
			for k, v := range ptCaller() {
				want[k] = v
			}
			for k, v := range want {
				if got, ok := dis[0][k]; !ok || got != v {
					t.Errorf("disagree[%q] = %v (present %t); want %v", k, got, ok, v)
				}
			}
		})
	}
}

// TestSpawnHeldTrailRowResult: row_result and store_error follow the end
// write: applied, a competing versioned write first, or a store error.
func TestSpawnHeldTrailRowResult(t *testing.T) {
	cases := []struct {
		name     string
		atCreate func(t *testing.T, e heldEnv, id string) // run as the create returns, before the end write
		want     ptWant
	}{
		{name: "applied", want: ptWant{rowResult: "ended"}},
		{name: "competing write", atCreate: func(t *testing.T, e heldEnv, id string) {
			apitest.SeedSessionID(t, e.dbPath, id, "sess-competing")
		}, want: ptWant{rowResult: "left_changed"}},
		{name: "store error", atCreate: func(t *testing.T, e heldEnv, id string) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		}, want: ptWant{rowResult: "still_pending", storeError: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			seeded := []tmuxfix.SeedSession{ptHolder("$4", tmux.Label{}, false)}
			e.rec.SeedSessions(e.socket, seeded...)
			if tc.atCreate != nil {
				e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { tc.atCreate(t, e, id) })
			}
			mark := trailLen(t)

			run := e.spawnHeld(t, id, ptName, nil)

			assertOneSentinel(t, run.err, api.ErrTmuxSessionConflict)
			cols, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil {
				t.Fatalf("ReadSpawnColumns: %v", err)
			}
			token, _ := cols.LaunchToken.(string)
			recs := ptRecords(t, mark, "ad.launch.name_held", id)
			if len(recs) != 1 {
				t.Fatalf("ad.launch.name_held records = %d; want 1: %v", len(recs), recs)
			}
			w := tc.want
			w.lookup, w.outcome, w.sessionID, w.carries = "gone", ptErrName(api.ErrTmuxSessionConflict), "$4", false
			e.ptAssertRecord(t, recs[0], id, w, e.ptForbid(id, token, seeded)...)
		})
	}
}

// TestSpawnHeldTrailNoOtherPath: a spawn whose create succeeds, times out or
// fails otherwise writes no ad.launch.name_held.
func TestSpawnHeldTrailNoOtherPath(t *testing.T) {
	cases := []struct {
		name    string
		create  *tmuxfix.Script
		wantErr bool
	}{
		{name: "success"},
		{name: "create timeout", create: &tmuxfix.Script{Failure: tmux.FailTimeout}, wantErr: true},
		{name: "create unrecognised reply", create: &tmuxfix.Script{Failure: tmux.FailUnrecognized,
			FirstLine: "create: unexpected reply", ExitStatus: 1, HadStdout: true}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newHeldEnv(t)
			id := heldID()
			if tc.create != nil {
				e.rec.Script(e.socket, *tc.create, tmux.CallCreate)
			}
			mark := trailLen(t)

			_, err := e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id,
				TmuxSessionName: ptName, TmuxSessionNameSupplied: true})

			if (err != nil) != tc.wantErr {
				t.Fatalf("Spawn err = %v; want error %t", err, tc.wantErr)
			}
			if recs := ptRecords(t, mark, "ad.launch.name_held", id); len(recs) != 0 {
				t.Errorf("ad.launch.name_held records = %v; want none", recs)
			}
		})
	}
}

// TestSpawnScanNameHeldKeys: the label scan's record through the shared
// emitter keeps its exact key set, leftover_count included.
func TestSpawnScanNameHeldKeys(t *testing.T) {
	e := newScanEnv(t)
	id := scanID()
	e.rec.SeedSessions(e.socket, e.leftover("old-life", "$5", id, ptCreated))
	mark := trailLen(t)

	_, _ = e.c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: id})

	recs := ptRecords(t, mark, "ad.launch.name_held", id)
	if len(recs) != 1 {
		t.Fatalf("ad.launch.name_held records = %d; want 1", len(recs))
	}
	var keys []string
	for k := range recs[0] {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := append(slices.Clone(ptKeys), "leftover_count")
	slices.Sort(want)
	if !slices.Equal(keys, want) {
		t.Errorf("scan record keys = %q; want %q", keys, want)
	}
	if recs[0]["row_result"] != "not_inserted" || recs[0]["leftover_count"] != float64(1) {
		t.Errorf("row_result, leftover_count = %v, %v; want not_inserted, 1", recs[0]["row_result"], recs[0]["leftover_count"])
	}
}

// ptChildEnv gates TestSpawnHeldTrailFailOpenChild and carries the id prefix.
const ptChildEnv = "AD_SPAWN_HELD_TRAIL_FAIL_CHILD"

// ptLinePrefix marks the child's result lines in its output.
const ptLinePrefix = "PT|"

// ptFailOpenRuns runs one held-name spawn per holder and end-write result,
// ids prefix-<name>, and returns one line each: error text, row and WARNs.
func ptFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	cases := []struct {
		name     string
		old      bool
		vanish   bool
		atCreate func(t *testing.T, e heldEnv, id string)
	}{
		{name: "old", old: true},
		{name: "no-label"},
		{name: "vanished", vanish: true},
		{name: "left-changed", atCreate: func(t *testing.T, e heldEnv, id string) {
			apitest.SeedSessionID(t, e.dbPath, id, "sess-competing")
		}},
		{name: "still-pending", atCreate: func(t *testing.T, e heldEnv, id string) {
			storefix.InjectWriteFailure(t, e.dbPath, storefix.WriteFailReuseRestore, id)
		}},
	}
	var lines []string
	for _, tc := range cases {
		e := newHeldEnv(t)
		id := prefix + "-" + tc.name
		if !tc.old {
			e.rec.SeedSessions(e.socket, ptHolder("$4", tmux.Label{}, false))
		}
		if tc.vanish {
			e.rec.RemoveSessionAfter(tmux.CallCreate, e.socket, "$4")
		}
		if tc.atCreate != nil {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { tc.atCreate(t, e, id) })
		}
		run := e.spawnHeld(t, id, ptName, func() {
			if tc.old {
				e.rec.SeedSessions(e.socket, e.leftover(ptName, "$4", id, ptCreated))
			}
		})
		c, err := apitest.ReadSpawnColumns(e.dbPath, id)
		if err != nil {
			t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
		}
		lines = append(lines, fmt.Sprintf("%s err=%v state=%v row_version=%v ended_at=%v launch_started_at=%v warns=%d",
			tc.name, run.err, c.State, c.RowVersion, c.EndedAt, c.LaunchStartedAt, strings.Count(e.logs.String(), "WARN")))
	}
	return lines
}

// TestSpawnHeldTrailFailOpen: with the trail file unwritable, held-name
// spawns return the same errors and leave the same rows as with a working trail.
func TestSpawnHeldTrailFailOpen(t *testing.T) {
	prefix := "held-failopen-" + uuid.NewString()[:8]
	mark := trailLen(t)
	want := ptFailOpenRuns(t, prefix)
	for _, l := range want {
		id := prefix + "-" + strings.Fields(l)[0]
		if n := len(ptRecords(t, mark, "ad.launch.name_held", id)); n != 1 {
			t.Fatalf("working trail: ad.launch.name_held records for %s = %d; want 1", id, n)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSpawnHeldTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), ptChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestSpawnHeldTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, ptLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestSpawnHeldTrailFailOpenChild is TestSpawnHeldTrailFailOpen's child: it
// makes the trail file read-only, runs the spawns and prints their lines.
func TestSpawnHeldTrailFailOpenChild(t *testing.T) {
	prefix := os.Getenv(ptChildEnv)
	if prefix == "" {
		t.Skip("run only as TestSpawnHeldTrailFailOpen's child")
	}
	if err := os.MkdirAll(filepath.Dir(apiTrailFilePath()), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(apiTrailFilePath(), nil, 0o400); err != nil {
		t.Fatalf("create read-only trail file: %v", err)
	}
	if err := trail.Emit(context.Background(), "ad.test.held_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range ptFailOpenRuns(t, prefix) {
		fmt.Println(ptLinePrefix + l)
	}

	if fi, err := os.Stat(apiTrailFilePath()); err != nil || fi.Size() != 0 {
		t.Errorf("trail file stat = %v, %v; want it empty", fi, err)
	}
}
