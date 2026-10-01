package api_test

// pause_trail_test.go covers pause's trail (SR-3.16, SR-7.2, SR-14, SR-15):
// no call event on any return path; ad.provenance.disagree (verb pause,
// source ad_send_keys) at most once per reason per call on api.Pause and
// Client.Pause, written before the wait whatever ends it; and, with the
// trail unwritable, unchanged results and rows (fail-open, run as a child).

import (
	"context"
	"errors"
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
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// ptrEntry is one pause entry point: api.Pause or Client.Pause.
type ptrEntry struct {
	name   string
	client bool
}

// ptrEntries are both entry points.
var ptrEntries = []ptrEntry{{"Pause", false}, {"Client.Pause", true}}

// pause runs pause on id with ctx through the entry point (api.Pause waits
// pauseTimeoutSeconds, Client.Pause the config default).
func (en ptrEntry) pause(ctx context.Context, t *testing.T, e *killEnv, id string) error {
	t.Helper()
	p := api.PauseParams{ClaudeInstanceID: id}
	if en.client {
		c, _ := e.client(t)
		_, err := c.Pause(ctx, p)
		return err
	}
	_, err := e.pauseWithin(ctx, pauseTimeoutSeconds, p)
	return err
}

// ptrCancelAfterEnter is a context cancelled when an Enter call returns,
// so a delivered /exit's wait ends at once.
func ptrCancelAfterEnter(t *testing.T, e *killEnv) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e.rec.AfterCall(tmux.CallSendEnter, func(tmuxfix.SocketCall, error) { cancel() })
	return ctx
}

// ptrEnds ends r's row as its own agent after the Enter call.
func ptrEnds(t *testing.T, e *killEnv, r *killRow) { e.endAfterEnter(t, *r) }

// ptrUnusableSocket seeds a row with no recorded socket whose resolved
// socket directory is unusable.
func ptrUnusableSocket(t *testing.T, e *killEnv) killRow {
	r := e.seedRow(t, killRowSpec{NoSession: true, Opts: []apitest.SpawnOption{apitest.WithNoLaunchToken()}})
	if err := os.Chmod(filepath.Dir(e.defaultSocket), 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	return r
}

// ptrAssertOnlyDisagrees fails when a trail record for id written after mark
// is anything but pause's ad.provenance.disagree (none at all when none) or
// the state transition of the agent's own SessionEnd.
func ptrAssertOnlyDisagrees(t *testing.T, mark int, id string, none bool) {
	t.Helper()
	for _, l := range readAPITrailLines(t)[mark:] {
		if l["claude_instance_id"] != id || l["event"] == "ad.spawn.state_transition" {
			continue
		}
		if none || l["event"] != "ad.provenance.disagree" || l["verb"] != "pause" {
			t.Errorf("trail record %v (verb %v) for %s; want only pause's disagree records", l["event"], l["verb"], id)
		}
	}
}

// TestPauseTrailNoCallEvent: no return path of pause writes a call event
// (no ad.pause.*, no ad.send_keys.called); a path with no lookup writes nothing.
func TestPauseTrailNoCallEvent(t *testing.T) {
	cases := []struct {
		name     string
		seed     sktSeed // nil: an unknown id
		outcome  string
		noLookup bool
		apiOnly  bool // Client.Pause would wait the config's 30 s
	}{
		{name: "unknown id", outcome: "ErrSpawnNotFound", noLookup: true},
		{name: "ended row", seed: sktRow(killRowSpec{State: store.StateEnded, NoSession: true}), outcome: "ok", noLookup: true},
		{name: "missing row", seed: sktRow(killRowSpec{State: store.StateMissing, NoSession: true}), outcome: "ok", noLookup: true},
		{name: "pending row", seed: sktRow(killRowSpec{State: store.StatePending}), outcome: "ErrSpawnNotPausable", noLookup: true},
		{name: "working row", seed: sktRow(killRowSpec{State: store.StateWorking}), outcome: "ErrSpawnNotPausable", noLookup: true},
		{name: "unusable socket directory", seed: ptrUnusableSocket, outcome: "ErrTmuxNotAvailable", noLookup: true},
		{name: "ours, row ends", seed: sktRow(killRowSpec{}, ptrEnds), outcome: "ok"},
		{name: "ours, wait times out", seed: sktRow(killRowSpec{}), outcome: "ErrPauseTimeout", apiOnly: true},
		{name: "leftover", seed: sktRow(killRowSpec{NoSession: true}, sktLeftover), outcome: "ErrTmuxSessionConflict"},
		{name: "pane not found", seed: sktRow(killRowSpec{NoSession: true}, rpnSeedPane), outcome: "ErrTmuxSessionConflict"},
		{name: "gone", seed: sktRow(killRowSpec{NoSession: true}), outcome: "ErrTmuxSendKeys"},
		{name: "different server", seed: sktRow(killRowSpec{}, ktrRebind), outcome: "ErrTmuxNotAvailable"},
		{name: "conflicting labels", seed: sktRow(killRowSpec{}, sktDuplicate), outcome: "ErrTmuxSessionConflict"},
		{name: "unreadable lookup", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallLookup)),
			outcome: "ErrTmuxUnresponsive"},
		{name: "tmux unavailable", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnavailable, tmux.CallLookup)),
			outcome: "ErrTmuxNotAvailable"},
		{name: "/exit call timed out", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallSendText)),
			outcome: "ErrTmuxUnresponsive"},
		{name: "Enter call failed, follow-up ours", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnrecognized, tmux.CallSendEnter)),
			outcome: "ErrTmuxUnresponsive"},
		{name: "follow-up gone", seed: sktRow(killRowSpec{}, sktSessionGoesAfterListing), outcome: "ErrTmuxSendKeys"},
	}
	for _, entry := range ptrEntries {
		for _, tc := range cases {
			if tc.apiOnly && entry.client {
				continue
			}
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				id := "pause-unknown-" + uuid.NewString()[:8]
				if tc.seed != nil {
					id = tc.seed(t, e).ID
				}
				mark := trailMark(t)

				err := entry.pause(context.Background(), t, e, id)

				if name, _ := errnames.Classify(err); (err == nil) != (tc.outcome == "ok") || (err != nil && name != tc.outcome) {
					t.Errorf("err = %v (class %q); want outcome %s", err, name, tc.outcome)
				}
				ptrAssertOnlyDisagrees(t, mark, id, tc.noLookup)
			})
		}
	}
}

// TestPauseTrailClosedClient: a closed Client returns ErrClientClosed, makes
// no tmux call and writes no trail record.
func TestPauseTrailClosedClient(t *testing.T) {
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	c, _ := e.client(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mark := trailMark(t)

	if _, err := c.Pause(context.Background(), pauseParams(r)); !errors.Is(err, api.ErrClientClosed) {
		t.Fatalf("Pause on a closed Client: err = %v; want ErrClientClosed", err)
	}
	assertNoTrailSince(t, mark, r.ID)
	e.assertNoTmuxCall(t)
}

// TestPauseTrailProvenanceDisagree: on both entry points each reason is written
// once per call (verb pause, source ad_send_keys) with what was typed as its
// action, never with a label value or another row's id; no other record.
func TestPauseTrailProvenanceDisagree(t *testing.T) {
	for _, entry := range ptrEntries {
		for _, tc := range keysDisagreeCases() {
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				e := newKillEnv(t)
				r, other := e.seedKeysDisagreeCase(t, tc)
				mark := trailMark(t)

				_ = entry.pause(ptrCancelAfterEnter(t, e), t, e, r.ID)

				assertKeysDisagrees(t, e, pauseDisagrees(t, r.ID), r, "pause", other, tc.want)
				ptrAssertOnlyDisagrees(t, mark, r.ID, false)
			})
		}
	}
}

// ptrDisagreesAtFirstSleep makes the wait's first sleep read id's pause
// disagree records; the result is nil until it slept. Knobs restored at cleanup.
func ptrDisagreesAtFirstSleep(t *testing.T, id string) func() []map[string]any {
	t.Helper()
	interval, sleep := api.PauseTestKnobs()
	t.Cleanup(func() { api.SetPauseTestKnobs(interval, sleep) })
	var seen []map[string]any
	api.SetPauseTestKnobs(interval, func(d time.Duration) {
		if seen == nil {
			seen = append([]map[string]any{}, pauseDisagrees(t, id)...)
		}
		sleep(d)
	})
	return func() []map[string]any { return seen }
}

// TestPauseTrailWrittenBeforeTheWait: the records are written before the wait
// and are the same whether the row ends, the wait times out or the caller cancels.
func TestPauseTrailWrittenBeforeTheWait(t *testing.T) {
	var rows []keysDisagreeCase
	for _, tc := range keysDisagreeCases() {
		if tc.name == "server_restarted" || tc.name == "adopted and name_changed" {
			rows = append(rows, tc)
		}
	}
	outcomes := []struct {
		name    string
		prepare func(*testing.T, *killEnv, killRow) context.Context
		want    error // nil: success
		sleeps  bool  // the wait sleeps before it ends
	}{
		{"row ends", func(t *testing.T, e *killEnv, r killRow) context.Context {
			e.endAfterEnter(t, r)
			return context.Background()
		}, nil, false},
		{"wait times out", func(*testing.T, *killEnv, killRow) context.Context { return context.Background() },
			api.ErrPauseTimeout, true},
		{"caller cancels", func(t *testing.T, e *killEnv, _ killRow) context.Context { return ptrCancelAfterEnter(t, e) },
			context.Canceled, false},
	}
	for _, row := range rows {
		for _, oc := range outcomes {
			t.Run(row.name+"/"+oc.name, func(t *testing.T) {
				e := newKillEnv(t)
				r, other := e.seedKeysDisagreeCase(t, row)
				atSleep := ptrDisagreesAtFirstSleep(t, r.ID)
				mark := trailMark(t)

				_, err := e.pauseWithin(oc.prepare(t, e, r), pauseTimeoutSeconds, pauseParams(r))

				if (oc.want == nil) != (err == nil) || !errors.Is(err, oc.want) {
					t.Fatalf("err = %v; want %v", err, oc.want)
				}
				assertKeysDisagrees(t, e, pauseDisagrees(t, r.ID), r, "pause", other, row.want)
				if oc.sleeps {
					assertKeysDisagrees(t, e, atSleep(), r, "pause", other, row.want)
				}
				ptrAssertOnlyDisagrees(t, mark, r.ID, false)
			})
		}
	}
}

// ptrChildEnv gates TestPauseTrailFailOpenChild and carries the id prefix.
const ptrChildEnv = "AD_PAUSE_TRAIL_FAIL_CHILD"

// ptrLinePrefix marks the child's result lines in its output.
const ptrLinePrefix = "PTR|"

// ptrFailOpenRuns pauses one row per trail shape, ids prefix-<name>, and
// returns one line per call: its error, calls and row columns.
func ptrFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	type setups = []func(*testing.T, *killEnv, *killRow)
	cases := []struct {
		name    string
		spec    killRowSpec
		setups  setups
		apiOnly bool // the wait times out: Client.Pause would wait 30 s
	}{
		{name: "ours", setups: setups{ptrEnds}},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true, NoPane: true}, setups: setups{ptrEnds}},
		{name: "restarted-timeout", setups: setups{ktrRestart}, apiOnly: true},
		{name: "restarted-exit-timeout", setups: setups{ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendText)}},
		{name: "rebound", setups: setups{ktrRebind}},
		{name: "gone", spec: killRowSpec{NoSession: true}},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		spec := tc.spec
		spec.ID = prefix + "-" + tc.name
		r := sktRow(spec, tc.setups...)(t, e)
		entry := ptrEntries[1]
		if tc.apiOnly {
			entry = ptrEntries[0]
		}
		err := entry.pause(context.Background(), t, e, r.ID)
		var calls []tmux.Call
		for _, c := range e.rec.SocketCalls() {
			calls = append(calls, c.Call)
		}
		c := e.columns(t, r.ID)
		lines = append(lines, fmt.Sprintf("%s err=%v calls=%v state=%v row_version=%v server=%v/%v/%v pane=%v/%v/%v",
			tc.name, err, calls, c.State, c.RowVersion, c.TmuxServerPID, c.TmuxServerStarted,
			c.TmuxServerStarttime, c.PaneID, c.PanePID, c.PaneStarttime))
	}
	return lines
}

// TestPauseTrailFailOpen: with the trail unwritable, pause's errors, tmux
// calls and rows equal those of a run with a working trail.
func TestPauseTrailFailOpen(t *testing.T) {
	prefix := "pause-failopen-" + uuid.NewString()[:8]
	want := ptrFailOpenRuns(t, prefix)
	for name, n := range map[string]int{"ours": 0, "adopted": 1, "restarted-timeout": 1, "restarted-exit-timeout": 1,
		"rebound": 1, "gone": 0} {
		if got := len(pauseDisagrees(t, prefix+"-"+name)); got != n {
			t.Fatalf("working trail: ad.provenance.disagree records for %s = %d; want %d", name, got, n)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestPauseTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), ptrChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestPauseTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, ptrLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestPauseTrailFailOpenChild is TestPauseTrailFailOpen's child: it runs the
// calls with an unwritable trail and prints their lines.
func TestPauseTrailFailOpenChild(t *testing.T) {
	prefix := os.Getenv(ptrChildEnv)
	if prefix == "" {
		t.Skip("run only as TestPauseTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.pause_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range ptrFailOpenRuns(t, prefix) {
		fmt.Println(ptrLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
