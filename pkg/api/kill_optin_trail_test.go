package api_test

// kill_optin_trail_test.go covers kill's trail on the finished-row opt-in's
// path (SR-6.4, SR-6.6, SR-14): one ad.kill.called per return path with
// include_finished true and the lookup's token, kill_sent with and without the
// opt-in, ad.provenance.disagree on its lookup (no adopted, since nothing is
// written) and a failing trail changing nothing.

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
)

// kotAssertCalled checks rec, one opt-in ad.kill.called of r, against want
// with ktrAssertCalled's checks, but with include_finished true.
func kotAssertCalled(t *testing.T, rec map[string]any, r killRow, want ktrCalled) {
	t.Helper()
	if rec["include_finished"] != true {
		t.Errorf("include_finished = %v; want true", rec["include_finished"])
	}
	optIn := maps.Clone(rec)
	optIn["include_finished"] = false // ktrAssertCalled pins the plain kill's value
	ktrAssertCalled(t, optIn, r, want)
}

// kotNeverReportedIn reports whether rec has the never-reported-in refusal's
// fields: include_finished true, lookup ours, ErrTmuxSessionConflict, no kill.
func kotNeverReportedIn(rec map[string]any) bool {
	return rec["include_finished"] == true && rec["lookup_outcome"] == "ours" &&
		rec["outcome"] == "ErrTmuxSessionConflict" && rec["kill_sent"] == false
}

// TestKillIncludeFinishedTrailPerReturnPath: every return path of the opt-in on
// an ended or missing row writes one ad.kill.called with include_finished true
// and the lookup's token; only "never reported in" has its field combination.
func TestKillIncludeFinishedTrailPerReturnPath(t *testing.T) {
	past := func(a agentState) startingRow { return kosReportedIn("", a) }
	gone := func(a agentState) startingRow { s := past(a); s.noSession = true; return s }
	viewer := func(t *testing.T, e *killEnv, r *killRow) {
		e.seedViewer(t, *r)
		e.syncServers()
		ktrDies(t, e, r)
	}
	notRun := tmux.TokenNotRun
	cases := []struct {
		name  string
		row   startingRow
		setup func(*testing.T, *killEnv, *killRow)
		want  ktrCalled
	}{
		{name: "gone, agent gone", row: gone(agentGone),
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: notRun, check: "gone", agentPID: true}},
		{name: "gone, agent pane in a viewer", row: gone(agentAlive), setup: viewer,
			want: ktrCalled{outcome: "ok", lookup: "gone", followup: notRun, check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "gone, agent runs, no pane", row: gone(agentAlive),
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "gone", followup: notRun, check: "alive", agentPID: true}},
		{name: "leftover", row: gone(agentAlive), setup: func(t *testing.T, e *killEnv, r *killRow) {
			e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true), e.createdBefore(defWindow+defBound))
		}, want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "leftover", followup: notRun, check: notRun}},
		{name: "different server", row: past(agentAlive), setup: ktrRebind,
			want: ktrCalled{outcome: "ErrTmuxNotAvailable", lookup: "different_server", followup: notRun, check: notRun}},
		{name: "conflicting labels", row: past(agentAlive), setup: func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
		}, want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "provenance_conflict", followup: notRun, check: notRun}},
		{name: "unreadable lookup", row: past(agentAlive), setup: ktrScript(tmux.FailTimeout, tmux.CallLookup),
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "cant_tell", followup: notRun, check: notRun}},
		{name: "tmux unavailable", row: past(agentAlive), setup: ktrScript(tmux.FailUnavailable, tmux.CallLookup),
			want: ktrCalled{outcome: "ErrTmuxNotAvailable", lookup: "tmux_unavailable", followup: notRun, check: notRun}},
		{name: "still stopping", row: startingRow{endedAgo: defWindow - time.Second, age: defWindow + defBound},
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: notRun, check: notRun}},
		{name: "still starting", row: startingRow{endedAgo: defWindow, age: defBound - time.Second},
			want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: notRun, check: notRun}},
		{name: "never reported in", row: startingRow{endedAgo: defWindow, noPID: true, age: defWindow + defBound},
			want: ktrCalled{outcome: "ErrTmuxSessionConflict", lookup: "ours", followup: notRun, check: notRun}},
		{name: "reported in, killed", row: past(agentAlive), setup: ktrDies,
			want: ktrCalled{outcome: "ok", lookup: "ours", followup: notRun, check: "gone", killSent: true, paneKilled: true, agentPID: true}},
		{name: "reported in, agent outlives the wait", row: past(agentAlive),
			want: ktrCalled{outcome: "ErrTmuxKillFailed", lookup: "ours", followup: notRun, check: "alive", killSent: true, paneKilled: true, agentPID: true}},
		{name: "reported in, follow-up unreadable", row: past(agentUnreadable), setup: func(_ *testing.T, e *killEnv, r *killRow) {
			e.rec.Script(r.Socket, tmuxfix.Script{Times: 1}, tmux.CallLookup).
				Script(r.Socket, tmuxfix.Script{Failure: tmux.FailTimeout}, tmux.CallLookup)
		}, want: ktrCalled{outcome: "ErrTmuxUnresponsive", lookup: "ours", followup: "cant_tell", check: "unreadable", killSent: true, paneKilled: true, agentPID: true}},
	}
	for _, state := range kosFinished {
		for _, tc := range cases {
			t.Run(state+", "+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				tc.row.state = state
				r := e.seedStarting(t, tc.row).killRow
				if tc.setup != nil {
					tc.setup(t, e, &r)
				}

				res, err := e.killOptIn(r.ID)

				recs := killCalled(t, r.ID)
				if len(recs) != 1 {
					t.Fatalf("ad.kill.called records = %d; want 1: %v", len(recs), recs)
				}
				kotAssertCalled(t, recs[0], r, tc.want)
				ktrAssertOutcome(t, err, tc.want.outcome)
				if err == nil && res.KillSent != tc.want.killSent {
					t.Errorf("KillResult.KillSent = %t; want %t", res.KillSent, tc.want.killSent)
				}
				if got, want := kotNeverReportedIn(recs[0]), tc.name == "never reported in"; got != want {
					t.Errorf("never-reported-in fields (include_finished, ours, ErrTmuxSessionConflict, no kill) = %t; want %t", got, want)
				}
			})
		}
	}
}

// TestKillIncludeFinishedTrailKillSent: on one reported-in ended row, kill
// without the opt-in sends nothing and makes no tmux call; with it, it kills.
func TestKillIncludeFinishedTrailKillSent(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedStarting(t, kosReportedIn(store.StateEnded, agentAlive)).killRow
	ktrDies(t, e, &r)

	res, err := e.kill(r.ID)
	if err != nil || res.KillSent {
		t.Fatalf("kill without the opt-in = %+v, %v; want kill_sent false, nil", res, err)
	}
	e.assertKillCalls(t)
	res, err = e.killOptIn(r.ID)
	if err != nil || !res.KillSent {
		t.Fatalf("kill with the opt-in = %+v, %v; want kill_sent true, nil", res, err)
	}
	e.assertKillCalls(t, seqOurs...)

	recs := killCalled(t, r.ID)
	if len(recs) != 2 {
		t.Fatalf("ad.kill.called records = %d; want 2", len(recs))
	}
	without := ktrCalled{outcome: "ok", lookup: tmux.TokenNotRun, followup: tmux.TokenNotRun, check: tmux.TokenNotRun}
	ktrAssertCalled(t, recs[0], r, without)
	kotAssertCalled(t, recs[1], r, ktrCalled{outcome: "ok", lookup: "ours", followup: tmux.TokenNotRun, check: "gone",
		killSent: true, paneKilled: true, agentPID: true})
}

// kotOldSession is the age of a reported-in row's own session: past the
// bound and older than an ended_at the window ago.
var kotOldSession = defWindow + defBound

// kotFinished seeds s's ended row with no session, then its own session aged
// kotOldSession with opts, and makes the agent exit at the session kill.
func kotFinished(t *testing.T, e *killEnv, s startingRow, opts ...tmuxfix.RowSessionOption) killRow {
	t.Helper()
	s.state, s.noSession = store.StateEnded, true
	r := e.seedStarting(t, s).killRow
	e.seedSession(t, &r, append([]tmuxfix.RowSessionOption{e.createdBefore(kotOldSession)}, opts...)...)
	e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
	return r
}

// TestKillIncludeFinishedTrailProvenanceDisagree: the opt-in's lookup writes
// each reason once with source ad_kill, name_changed on a refusal too, and no
// adopted for an adoption-due kill; the normal case writes none.
func TestKillIncludeFinishedTrailProvenanceDisagree(t *testing.T) {
	reported := kosReportedIn("", agentAlive)
	never := reported
	never.noPID = true
	renamed := func(action string) disagreeWant {
		return disagreeWant{reason: "name_changed", server: "match", verdict: "ours", action: action, current: "renamed-kill", ours: true}
	}
	cases := []struct {
		name string
		seed func(*testing.T, *killEnv) killRow
		want []disagreeWant
	}{
		{name: "normal ours writes none", seed: func(t *testing.T, e *killEnv) killRow { return kotFinished(t, e, reported) }},
		{name: "server_restarted", seed: func(t *testing.T, e *killEnv) killRow {
			r := e.seedStarting(t, startingRow{state: store.StateEnded, endedAgo: defWindow, noSession: true}).killRow
			e.rec.RestartServer(r.Socket, tmuxfix.Server{})
			e.syncServers()
			e.seedSession(t, &r, e.createdBefore(kotOldSession))
			e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
			return r
		}, want: []disagreeWant{{reason: "server_restarted", server: "restarted", verdict: "ours", action: "kill_sent", ours: true}}},
		{name: "adoption due, no adopted written", seed: func(t *testing.T, e *killEnv) killRow {
			spec := e.resumableSpec(defWindow, agentAlive)
			spec.NoServerIdentity = true
			r := e.seedResumableRow(t, spec).killRow
			e.seedSession(t, &r, e.createdBefore(kotOldSession))
			e.setAfterCall(tmux.CallKillSession, procfix.Gone(), r.AgentPID)
			return r
		}},
		{name: "name_changed, killed", seed: func(t *testing.T, e *killEnv) killRow {
			return kotFinished(t, e, reported, tmuxfix.WithRowSessionName("renamed-kill"))
		}, want: []disagreeWant{renamed("kill_sent")}},
		{name: "name_changed, never reported in", seed: func(t *testing.T, e *killEnv) killRow {
			return kotFinished(t, e, never, tmuxfix.WithRowSessionName("renamed-kill"))
		}, want: []disagreeWant{renamed("nothing_sent")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			r := tc.seed(t, e)
			before := e.columns(t, r.ID)

			_, _ = e.killOptIn(r.ID)

			recs := killDisagrees(t, r.ID)
			if len(recs) != len(tc.want) {
				t.Fatalf("ad.provenance.disagree records = %d; want %d: %v", len(recs), len(tc.want), recs)
			}
			for i, want := range tc.want {
				assertDisagreeRecord(t, recs[i], r, "kill", "ad_kill", want)
				ktrAssertNoForeignContent(t, recs[i], r.Token, e.storeID)
			}
			e.assertRowUnchanged(t, r.ID, before)
			if e.store.adoptTries != 0 {
				t.Errorf("adoption writes attempted = %d; want none on a finished row", e.store.adoptTries)
			}
		})
	}
}

// kotChildEnv gates TestKillIncludeFinishedTrailFailOpenChild and carries "1".
const kotChildEnv = "AD_KILL_OPTIN_TRAIL_FAIL_CHILD"

// kotFailOpenRuns runs one reported-in and one never-reported-in opt-in kill
// and returns their ids and one line each: result, error (id elided) and the row.
func kotFailOpenRuns(t *testing.T) (ids, lines []string) {
	t.Helper()
	never := kosReportedIn(store.StateEnded, agentAlive)
	never.noPID = true
	for _, s := range []startingRow{kosReportedIn(store.StateMissing, agentAlive), never} {
		e := newKillEnv(t)
		r := e.seedStarting(t, s).killRow
		ktrDies(t, e, &r)
		res, err := e.killOptIn(r.ID)
		c := e.columns(t, r.ID)
		ids = append(ids, r.ID)
		lines = append(lines, strings.ReplaceAll(fmt.Sprintf("kill_sent=%t err=%v state=%v row_version=%v pid=%v ended_at=%v server=%v/%v pane=%v/%v",
			res.KillSent, err, c.State, c.RowVersion, c.PID, c.EndedAt, c.TmuxServerPID, c.TmuxServerStarted, c.PaneID, c.PanePID), r.ID, "<id>"))
	}
	return ids, lines
}

// TestKillIncludeFinishedTrailFailOpen: with the trail unwritable, opt-in
// kills give the results, errors and rows of a run with a working trail.
func TestKillIncludeFinishedTrailFailOpen(t *testing.T) {
	ids, want := kotFailOpenRuns(t)
	for _, id := range ids {
		if n := len(killCalled(t, id)); n != 1 {
			t.Fatalf("working trail: ad.kill.called records for %s = %d; want 1", id, n)
		}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestKillIncludeFinishedTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), kotChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestKillIncludeFinishedTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, ktrLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestKillIncludeFinishedTrailFailOpenChild is TestKillIncludeFinishedTrailFailOpen's
// child: it runs the kills with an unwritable trail and prints their lines.
func TestKillIncludeFinishedTrailFailOpenChild(t *testing.T) {
	if os.Getenv(kotChildEnv) == "" {
		t.Skip("run only as TestKillIncludeFinishedTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.kill_optin_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}
	_, lines := kotFailOpenRuns(t)
	for _, l := range lines {
		fmt.Println(ktrLinePrefix + l)
	}
	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
