package api_test

// resume_held_trail_test.go covers resume's ad.launch.name_held record after
// "duplicate session" (SR-14, SR-8.5, SR-15; AC-SPN-10, AC-RES-12): exactly
// one per held-name resume, every field per re-lookup outcome and restore
// result, written after ad.resume.restored; none on any other path; fail-open.
// It runs on arrangeHeld (resume_held_fixture_test.go).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rhtWant is one record's expected per-outcome fields; holder says one
// session was identified (sc.Holder), carries and current are true, false or
// nil (null).
type rhtWant struct {
	lookup           string
	sentinel         error
	holder           bool
	carries, current any
}

// rhtRun is one held-name resume: its scene, error and the id's
// ad.launch.name_held records written by the call.
type rhtRun struct {
	sc   *heldScene
	err  error
	recs []map[string]any
}

// rhtResume arranges spec on r, resumes r through w (a plain
// hookedResumeStore when nil) and returns the run; atCreate, when set, runs
// as the create returns, after arrangeHeld's placement.
func (e *killEnv) rhtResume(t *testing.T, r resumeRow, spec heldSpec, w *hookedResumeStore, atCreate func()) rhtRun {
	t.Helper()
	if w == nil {
		w = &hookedResumeStore{st: e.st}
	}
	sc := e.arrangeHeld(t, r, spec)
	if atCreate != nil {
		e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) { atCreate() })
	}
	_, err := e.resumeWith(w, r.ID)
	return rhtRun{sc: sc, err: err, recs: ptRecords(t, sc.before.mark, "ad.launch.name_held", r.ID)}
}

// rhtAssertRecord checks run wrote exactly one record: every SR-14 field
// against w, rowResult and storeError (nil: null), the exact key set and no
// label content, token, other id or session environment.
func (e *killEnv) rhtAssertRecord(t *testing.T, run rhtRun, w rhtWant, rowResult string, storeError any) {
	t.Helper()
	if len(run.recs) != 1 {
		t.Fatalf("ad.launch.name_held records = %d; want 1: %v", len(run.recs), run.recs)
	}
	rec, r := run.recs[0], run.sc.r
	want := map[string]any{
		"source": "ad_resume", "claude_instance_id": r.ID, "launch": "resume", "tmux_session_name": r.Name,
		"tmux_socket": r.Socket, "store_id": e.storeID, "lookup_outcome": w.lookup, "outcome": ptErrName(w.sentinel),
		"row_result": rowResult, "store_error": storeError, "carries_this_id": w.carries, "current_launch": w.current,
		"tmux_session_id": nil, "session_created": nil, "attach_command": nil, "end_command": nil,
	}
	if w.holder {
		h := run.sc.Holder()
		q := func(s string) string { return "'" + s + "'" }
		want["tmux_session_id"], want["session_created"] = h.ID, float64(h.Created)
		want["attach_command"] = "tmux -u -S " + q(r.Socket) + " attach-session -r -t " + q(h.ID)
		want["end_command"] = "tmux -u -S " + q(r.Socket) + " kill-session -t " + q(h.ID)
	}
	for k, v := range ptCaller() {
		want[k] = v
	}
	for k, v := range want {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("name_held[%q] = %v (present %t); want %v", k, got, ok, v)
		}
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	wantKeys := slices.Clone(ptKeys)
	slices.Sort(wantKeys)
	if !slices.Equal(keys, wantKeys) {
		t.Errorf("record keys = %q; want %q", keys, wantKeys)
	}
	ktrAssertNoForeignContent(t, rec, rhtForbid(e, run.sc)...)
}

// rhtForbid is what no record of sc's resume may carry: launch tokens,
// another store's id, every placed label's value and other instance id, and
// the row's session-environment value (its config directory).
func rhtForbid(e *killEnv, sc *heldScene) []string {
	out := []string{sc.r.Token, tmuxfix.OtherToken, apitest.OtherStoreID(e.storeID), sc.r.Trust.dir}
	if tok, ok := sc.Moved.LaunchToken.(string); ok {
		out = append(out, tok)
	}
	for _, s := range append(slices.Clone(sc.Holders), sc.Ours) {
		if l := s.Label; l.Kind == tmux.LabelValid {
			out = append(out, l.Token, tmuxfix.LabelValue(l.Token, s.ID, l.InstanceID, l.StoreID))
			if l.InstanceID != sc.r.ID {
				out = append(out, l.InstanceID)
			}
		}
	}
	return out
}

// rhtAssertOrder fails unless id's ad.resume.* and ad.launch.name_held lines
// since mark are exactly moved_to_pending, restored, name_held.
func rhtAssertOrder(t *testing.T, mark int, id string) {
	t.Helper()
	var got []string
	for _, l := range readAPITrailLines(t)[mark:] {
		ev, _ := l["event"].(string)
		if l["claude_instance_id"] == id && (strings.HasPrefix(ev, "ad.resume.") || ev == "ad.launch.name_held") {
			got = append(got, ev)
		}
	}
	want := []string{"ad.resume.moved_to_pending", "ad.resume.restored", "ad.launch.name_held"}
	if !slices.Equal(got, want) {
		t.Errorf("trail order = %q; want %q", got, want)
	}
}

// TestResumeHeldTrailPerOutcome: each held-name resume writes exactly one
// ad.launch.name_held with every SR-14 field for its re-lookup outcome, after
// one ad.resume.restored naming the same error, and restores the row.
func TestResumeHeldTrailPerOutcome(t *testing.T) {
	t.Parallel()
	conflict, unresponsive, unavailable := api.ErrTmuxSessionConflict, api.ErrTmuxUnresponsive, api.ErrTmuxNotAvailable
	held := func(lookup string, sentinel error, carries, current any) rhtWant {
		return rhtWant{lookup: lookup, sentinel: sentinel, holder: true, carries: carries, current: current}
	}
	none := func(lookup string, sentinel error) rhtWant { return rhtWant{lookup: lookup, sentinel: sentinel} }
	relookup := func(f tmux.Failure) tmuxfix.Script { return tmuxfix.Script{Failure: f} }
	cases := []struct {
		name string
		age  func(e *killEnv) time.Duration // the row's ended_at age at the re-lookup
		spec func(e *killEnv) heldSpec
		want rhtWant
	}{
		{name: "old label", spec: holderOnly(holderOld), want: held("leftover", conflict, true, false)},
		{name: "current label, still stopping",
			age:  func(e *killEnv) time.Duration { return e.cfg.EffectiveStoppingWindow() / 2 },
			spec: func(e *killEnv) heldSpec { return heldSpec{Holder: holderCurrent, Created: rceSettled(e)} },
			want: held("ours", unresponsive, true, true)},
		{name: "current label, still starting",
			spec: func(e *killEnv) heldSpec {
				return heldSpec{Holder: holderCurrent, Created: e.cfg.EffectiveStartingSession() / 2}
			},
			want: held("ours", unresponsive, true, true)},
		{name: "current label, this row's own id",
			spec: func(e *killEnv) heldSpec { return heldSpec{Holder: holderCurrent, Created: rceSettled(e)} },
			want: held("ours", conflict, true, true)},
		{name: "foreign label", spec: holderOnly(holderForeign), want: held("gone", conflict, false, nil)},
		{name: "another store's label", spec: holderOnly(holderOtherStore), want: held("gone", conflict, false, nil)},
		{name: "another store's label naming this id and token", spec: holderOnly(holderOtherStoreOwn),
			want: held("gone", conflict, false, nil)},
		{name: "no label", spec: holderOnly(holderNone), want: held("gone", conflict, false, nil)},
		{name: "malformed label", spec: holderOnly(holderMalformed), want: held("gone", conflict, false, nil)},
		{name: "more than one entry matches", spec: holderOnly(holderAmbiguous), want: none("gone", unresponsive)},
		{name: "conflicting labels", spec: holderOnly(holderConflicting),
			want: held("provenance_conflict", conflict, nil, nil)},
		{name: "vanished", spec: holderOnly(holderVanished), want: none("gone", api.ErrTmuxSessionCreate)},
		{name: "unreadable", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderNone, Relookup: relookup(tmux.FailTimeout)}
		}, want: none("cant_tell", unresponsive)},
		{name: "tmux unavailable", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderNone, Relookup: relookup(tmux.FailUnavailable)}
		}, want: none("tmux_unavailable", unavailable)},
		{name: "socket permission", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderNone, Relookup: relookup(tmux.FailSocketDenied)}
		}, want: none("tmux_unavailable", unavailable)},
		{name: "different server, no holder", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderVanished, Server: heldServerRebound}
		}, want: none("different_server", unavailable)},
		{name: "different server, holder listed", spec: func(*killEnv) heldSpec {
			return heldSpec{Holder: holderNone, Server: heldServerRebound}
		}, want: held("different_server", unavailable, nil, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			age := rceSettled(e)
			if tc.age != nil {
				age = tc.age(e)
			}
			r := e.seedHeldResumable(t, age, agentGone)

			run := e.rhtResume(t, r, tc.spec(e), nil, nil)

			assertOneSentinel(t, run.err, tc.want.sentinel)
			e.rhtAssertRecord(t, run, tc.want, "restored", nil)
			rstAssertRestoredTrail(t, r.ID, true, ptErrName(tc.want.sentinel), nil)
			rhtAssertOrder(t, run.sc.before.mark, r.ID)
			e.assertHeldRestored(t, run.sc)
		})
	}
}

// holderOnly is the heldSpec placing only k's holder.
func holderOnly(k holderKind) func(*killEnv) heldSpec {
	return func(*killEnv) heldSpec { return heldSpec{Holder: k} }
}

// TestResumeHeldTrailRowResult: row_result, store_error and the description's
// restore sentence follow the restore (applied, a versioned write or removal
// first, a store error); hooks before it are ignored; the returned error's
// class is the same throughout.
func TestResumeHeldTrailRowResult(t *testing.T) {
	t.Parallel()
	type arrange func(t *testing.T, e *killEnv, r resumeRow, w *hookedResumeStore)
	cases := []struct {
		name       string
		arrange    arrange                                     // before arrangeHeld
		atCreate   func(t *testing.T, e *killEnv, r resumeRow) // after arrangeHeld's placement
		rowResult  string
		restore    apitest.RestoreOutcome // applied restores to ended, the seeded prior state
		storeError any
	}{
		{name: "applied", rowResult: "restored", restore: apitest.RestoreApplied},
		{name: "ordinary hook and SessionStart before the restore are ignored",
			arrange: func(t *testing.T, e *killEnv, r resumeRow, w *hookedResumeStore) {
				w.afterMove(func() {
					for _, ev := range []string{"Stop", "SessionStart"} {
						got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, r.Spawn.ClaudeSessionID,
							apitest.HookTranscript(r.JSONLPath, true))
						if got != (store.HookApplied{Reason: store.HookReasonNoPaneRecorded}) {
							t.Errorf("%s before the restore = %+v; want ignored, %s", ev, got, store.HookReasonNoPaneRecorded)
						}
					}
				})
			}, rowResult: "restored", restore: apitest.RestoreApplied},
		{name: "versioned write after the move",
			arrange: func(t *testing.T, e *killEnv, r resumeRow, w *hookedResumeStore) {
				parent := e.seedRow(t, killRowSpec{State: store.StateEnded, Agent: agentGone, NoSession: true}).ID
				w.afterMove(func() {
					if err := e.st.SetParentID(r.ID, parent); err != nil {
						t.Errorf("SetParentID: %v", err)
					}
				})
			}, rowResult: "left_changed", restore: apitest.RestoreRowChanged},
		{name: "row removed on the failing create",
			atCreate: func(t *testing.T, e *killEnv, r resumeRow) {
				if err := e.st.DeleteSpawn(r.ID); err != nil {
					t.Errorf("DeleteSpawn: %v", err)
				}
			}, rowResult: "left_changed", restore: apitest.RestoreRowRemoved},
		{name: "restore store error",
			arrange:   func(_ *testing.T, _ *killEnv, _ resumeRow, w *hookedResumeStore) { w.failRestore(nil) },
			rowResult: "still_pending", restore: apitest.RestoreStoreError, storeError: errInjectedStore.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedHeldResumable(t, rceSettled(e), agentGone)
			w := &hookedResumeStore{st: e.st}
			if tc.arrange != nil {
				tc.arrange(t, e, r, w)
			}

			var atCreate func()
			if tc.atCreate != nil {
				atCreate = func() { tc.atCreate(t, e, r) }
			}

			run := e.rhtResume(t, r, heldSpec{Holder: holderNone}, w, atCreate)

			assertOneSentinel(t, run.err, api.ErrTmuxSessionConflict)
			if run.err == nil {
				t.Fatal("resume err = nil; want the held-name refusal")
			}
			applied := tc.restore == apitest.RestoreApplied
			restore := apitest.ResumeRestore{Outcome: tc.restore}
			if applied {
				restore.PriorState = store.StateEnded
			}
			apitest.AssertDescription(t, run.err.Error(), apitest.DescHeldNoValidID(apitest.HeldName{
				Name: r.Name, SessionID: run.sc.Holder().ID, Restore: restore}), rhtForbid(e, run.sc)...)
			want := rhtWant{lookup: "gone", sentinel: api.ErrTmuxSessionConflict, holder: true, carries: false}
			e.rhtAssertRecord(t, run, want, tc.rowResult, tc.storeError)
			rstAssertRestoredTrail(t, r.ID, applied, "ErrTmuxSessionConflict", tc.storeError)
			rhtAssertOrder(t, run.sc.before.mark, r.ID)
			if applied {
				e.assertHeldRestored(t, run.sc)
			}
		})
	}
}

// TestResumeHeldTrailNoOtherPath: a resume that succeeds, is refused before
// the launch, or whose create fails other than "duplicate session" writes no
// ad.launch.name_held.
func TestResumeHeldTrailNoOtherPath(t *testing.T) {
	t.Parallel()
	create := func(s tmuxfix.Script) func(*testing.T, *killEnv, resumeRow) {
		return func(_ *testing.T, e *killEnv, r resumeRow) { e.rec.Script(r.Socket, s, tmux.CallCreate) }
	}
	cases := []struct {
		name    string
		setup   func(*testing.T, *killEnv, resumeRow)
		wantErr bool
	}{
		{name: "success"},
		{name: "name held at the pre-launch lookup", wantErr: true,
			setup: func(t *testing.T, e *killEnv, r resumeRow) { e.seedHolder(t, r.killRow, holderNone) }},
		{name: "create timeout", setup: create(tmuxfix.Script{Failure: tmux.FailTimeout, Times: 1}), wantErr: true},
		{name: "unlabelled session", wantErr: true, setup: func(_ *testing.T, e *killEnv, r resumeRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate).
				Script(r.Socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1}, tmux.CallSetLabel, tmux.CallKillSession)
		}},
		{name: "tmux unavailable at the create", setup: create(tmuxfix.Script{Failure: tmux.FailUnavailable, Times: 1}),
			wantErr: true},
		{name: "other create failure",
			setup: create(tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := e.seedResumable(t, rceSettled(e), agentGone)
			if tc.setup != nil {
				tc.setup(t, e, r)
			}
			mark := trailMark(t)

			_, err := e.resume(r.ID)

			if (err != nil) != tc.wantErr {
				t.Fatalf("Resume err = %v; want error %t", err, tc.wantErr)
			}
			if recs := ptRecords(t, mark, "ad.launch.name_held", r.ID); len(recs) != 0 {
				t.Errorf("ad.launch.name_held records = %v; want none", recs)
			}
		})
	}
}

// rhtChildEnv gates TestResumeHeldTrailFailOpenChild and carries the id prefix.
const rhtChildEnv = "AD_RESUME_HELD_TRAIL_FAIL_CHILD"

// rhtLinePrefix marks the child's result lines in its output.
const rhtLinePrefix = "RHT|"

// rhtFailOpenRuns runs one held-name resume per holder and restore result,
// ids prefix-<name>, and returns one line each: the error and the row, with
// the per-test socket and cwd replaced.
func rhtFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	cases := []struct {
		name   string
		holder holderKind
		w      func(*hookedResumeStore)
	}{
		{name: "old", holder: holderOld},
		{name: "no-label", holder: holderNone},
		{name: "vanished", holder: holderVanished},
		{name: "ambiguous", holder: holderAmbiguous},
		{name: "still-pending", holder: holderNone, w: func(w *hookedResumeStore) { w.failRestore(nil) }},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		spec := e.heldResumableSpec(rceSettled(e), agentGone)
		spec.ID = prefix + "-" + tc.name
		r := e.seedResumableRow(t, spec)
		w := &hookedResumeStore{st: e.st}
		if tc.w != nil {
			tc.w(w)
		}
		run := e.rhtResume(t, r, heldSpec{Holder: tc.holder}, w, nil)
		c := e.columns(t, r.ID)
		l := fmt.Sprintf("%s err=%v state=%v ended_at=%v row_version=%v launch_started_at=%v parent=%v socket=%v",
			tc.name, run.err, c.State, c.EndedAt, c.RowVersion, c.LaunchStartedAt, c.ParentID, c.TmuxSocket)
		lines = append(lines, strings.NewReplacer(r.Socket, "<socket>", r.CWD, "<cwd>").Replace(l))
	}
	return lines
}

// TestResumeHeldTrailFailOpen: with the trail unwritable, held-name resumes
// return the same errors and leave the same rows as with a working trail.
func TestResumeHeldTrailFailOpen(t *testing.T) {
	t.Parallel()
	prefix := "resume-held-failopen-" + uuid.NewString()[:8]
	mark := trailMark(t)
	want := rhtFailOpenRuns(t, prefix)
	for _, l := range want {
		id := prefix + "-" + strings.Fields(l)[0]
		if n := len(ptRecords(t, mark, "ad.launch.name_held", id)); n != 1 {
			t.Fatalf("working trail: ad.launch.name_held records for %s = %d; want 1", id, n)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestResumeHeldTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), rhtChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestResumeHeldTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, rhtLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestResumeHeldTrailFailOpenChild is TestResumeHeldTrailFailOpen's child: it
// runs the resumes with a 0500 .agent-director and prints their lines.
func TestResumeHeldTrailFailOpenChild(t *testing.T) {
	t.Parallel()
	prefix := os.Getenv(rhtChildEnv)
	if prefix == "" {
		t.Skip("run only as TestResumeHeldTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.resume_held_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range rhtFailOpenRuns(t, prefix) {
		fmt.Println(rhtLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
