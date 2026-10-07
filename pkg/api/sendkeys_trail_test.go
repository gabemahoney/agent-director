package api_test

// sendkeys_trail_test.go covers send-keys' trail at the API level (SR-7.4,
// SR-14, SR-15): one ad.send_keys.called per Client.SendKeys call with its
// outcome by name and row_state, none from a closed Client, and
// ad.provenance.disagree (source ad_send_keys) at most once per reason per
// call on both entry points; with the trail unwritable, results and rows
// are unchanged (fail-open, run as a child of the test binary).

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
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// sktCalled returns id's ad.send_keys.called records, in write order.
func sktCalled(t *testing.T, id string) []map[string]any {
	t.Helper()
	return pendTrail(t, "ad.send_keys.called", id)
}

// sktDisagrees returns id's ad.provenance.disagree records written by send-keys.
func sktDisagrees(t *testing.T, id string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range pendTrail(t, "ad.provenance.disagree", id) {
		if l["verb"] == "send-keys" {
			out = append(out, l)
		}
	}
	return out
}

// sktSeed seeds a case's row and whatever it needs before the call.
type sktSeed func(*testing.T, *killEnv) killRow

// sktRow seeds a row from spec, then runs setups on it.
func sktRow(spec killRowSpec, setups ...func(*testing.T, *killEnv, *killRow)) sktSeed {
	return func(t *testing.T, e *killEnv) killRow {
		r := e.seedRow(t, spec)
		for _, s := range setups {
			s(t, e, &r)
		}
		return r
	}
}

// sktPending seeds a fresh spawn's pending row with shape v and opts.
func sktPending(v pendingShape, opts ...apitest.SpawnOption) sktSeed {
	return func(t *testing.T, e *killEnv) killRow { return e.seedPending(t, pendingFresh, v, opts...) }
}

// sktNoLaunchStart seeds a pending row with no launch start recorded.
func sktNoLaunchStart(t *testing.T, e *killEnv) killRow {
	return e.seedRow(t, e.pendingSpec(pendingFresh, pendingOurs, apitest.WithNoLaunchStartedAt()))
}

// sktUnusable seeds spec's row recording the named unusable-name fixture (seedUnusableRow).
func sktUnusable(spec killRowSpec, fixture string) sktSeed {
	return func(t *testing.T, e *killEnv) killRow { return e.seedUnusableRow(t, spec, unusableFixture(t, fixture)) }
}

// sktUnusablePending seeds a fresh spawn's pending row (launch start, token,
// then opts) recording the pre-b.gqe default name.
func sktUnusablePending(opts ...apitest.SpawnOption) sktSeed {
	return func(t *testing.T, e *killEnv) killRow {
		return sktUnusable(e.pendingSpec(pendingFresh, pendingOurs, opts...), "pre-b.gqe default name")(t, e)
	}
}

// sktReleased gives r one permission request past its relay window at the
// fixture clock, which Client.SendKeys judges the guard by.
func sktReleased(t *testing.T, e *killEnv, r *killRow) {
	storefix.SeedOpenPermissionRequests(t, e.st, r.ID, []string{storefix.TestRequestTokenA})
	storefix.SeedUndeliverablePermissionRequest(t, e.st, e.dbPath, r.ID, storefix.TestRequestTokenA,
		time.Since(e.clock.Now())+2*sendKeysWindow())
}

// sktLeftover replaces r's session with one carrying an earlier launch's label.
func sktLeftover(t *testing.T, e *killEnv, r *killRow) {
	e.seedSession(t, r, tmuxfix.WithRowSessionLabel(r.old(), true))
}

// sktDuplicate seeds a second session carrying r's current label.
func sktDuplicate(_ *testing.T, e *killEnv, r *killRow) {
	e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "dup", Label: r.current()})
}

// sktSessionGoesAfterListing removes r's session once the pane listing
// returns; a bystander keeps the server holding a session.
func sktSessionGoesAfterListing(t *testing.T, e *killEnv, r *killRow) {
	e.seedBystander(t, r.Socket)
	e.rec.RemoveSessionAfter(tmux.CallListPanes, r.Socket, r.Session.ID)
}

// sktRebindAfterText binds a new server on r's socket once the text call returns.
func sktRebindAfterText(t *testing.T, e *killEnv, r *killRow) {
	e.rec.AfterCall(tmux.CallSendText, func(tmuxfix.SocketCall, error) { ktrRebind(t, e, r) })
}

// TestSendKeysTrailCalledPerReturnPath: every return path of Client.SendKeys
// writes exactly one ad.send_keys.called, outcome by name, row_state the state read.
func TestSendKeysTrailCalledPerReturnPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		seed              sktSeed // nil: an unknown id
		allow             bool
		outcome, rowState string
		guard             string // "" is not-applicable
		unusable          bool   // an unusable recorded name: no ad.provenance.disagree either
	}{
		{name: "unknown id", outcome: "ErrSpawnNotFound"},
		{name: "finished row", seed: sktRow(killRowSpec{State: store.StateEnded, NoSession: true}), allow: true,
			outcome: "ErrSpawnNotInteractive", rowState: "ended"},
		{name: "pending without allow_pending", seed: sktPending(pendingOurs),
			outcome: "ErrSpawnNotInteractive", rowState: "pending"},
		{name: "pending with no launch start", seed: sktNoLaunchStart, allow: true,
			outcome: "ErrSpawnNotInteractive", rowState: "pending"},
		{name: "relay held", seed: sktRow(killRowSpec{State: store.StateCheckPermission, RelayOn: true}),
			outcome: "ErrSendKeysWhileRelayed", rowState: "check_permission", guard: "held"},
		{name: "relay released", seed: sktRow(killRowSpec{State: store.StateCheckPermission, RelayOn: true}, sktReleased),
			outcome: "ok", rowState: "check_permission", guard: "released"},
		{name: "ours, live row", seed: sktRow(killRowSpec{}), outcome: "ok", rowState: "waiting"},
		{name: "ours, pending row", seed: sktPending(pendingOurs), allow: true, outcome: "ok", rowState: "pending"},
		{name: "pending leftover", seed: sktPending(pendingLeftover), allow: true,
			outcome: "ErrSpawnNotInteractive", rowState: "pending"},
		{name: "live leftover", seed: sktRow(killRowSpec{NoSession: true}, sktLeftover),
			outcome: "ErrTmuxSessionConflict", rowState: "waiting"},
		{name: "pane not found", seed: sktRow(killRowSpec{NoSession: true}, rpnSeedPane),
			outcome: "ErrTmuxSessionConflict", rowState: "waiting"},
		{name: "gone", seed: sktRow(killRowSpec{NoSession: true}), outcome: "ErrTmuxSendKeys", rowState: "waiting"},
		{name: "different server", seed: sktRow(killRowSpec{}, ktrRebind),
			outcome: "ErrTmuxNotAvailable", rowState: "waiting"},
		{name: "conflicting labels", seed: sktRow(killRowSpec{}, sktDuplicate),
			outcome: "ErrTmuxSessionConflict", rowState: "waiting"},
		{name: "unreadable lookup", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallLookup)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "tmux unavailable", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnavailable, tmux.CallLookup)),
			outcome: "ErrTmuxNotAvailable", rowState: "waiting"},
		{name: "text call timed out", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallSendText)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "Enter call timed out", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailTimeout, tmux.CallSendEnter)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "follow-up ours", seed: sktRow(killRowSpec{}, ktrScript(tmux.FailUnrecognized, tmux.CallSendText)),
			outcome: "ErrTmuxUnresponsive", rowState: "waiting"},
		{name: "follow-up gone", seed: sktRow(killRowSpec{}, sktSessionGoesAfterListing),
			outcome: "ErrTmuxSendKeys", rowState: "waiting"},
		{name: "follow-up different server",
			seed:    sktRow(killRowSpec{}, ktrScript(tmux.FailUnrecognized, tmux.CallSendText), sktRebindAfterText),
			outcome: "ErrTmuxNotAvailable", rowState: "waiting"},
		{name: "unusable name, waiting", seed: sktUnusable(killRowSpec{}, "empty"), unusable: true,
			outcome: "ErrInternal", rowState: "waiting"},
		{name: "unusable name, pending", seed: sktUnusablePending(), allow: true, unusable: true,
			outcome: "ErrInternal", rowState: "pending"},
		{name: "unusable name, pending with no launch start", seed: sktUnusablePending(apitest.WithNoLaunchStartedAt()),
			allow: true, unusable: true, outcome: "ErrSpawnNotInteractive", rowState: "pending"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			id := "sk-unknown-" + uuid.NewString()[:8]
			if tc.seed != nil {
				id = tc.seed(t, e).ID
			}
			p := api.SendKeysParams{ClaudeInstanceID: id, Text: "hi", AllowPending: tc.allow}

			_, _, err := e.sendKeysClient(t, p)

			recs := sktCalled(t, id)
			if len(recs) != 1 {
				t.Fatalf("ad.send_keys.called records = %d; want 1: %v", len(recs), recs)
			}
			guard := tc.guard
			if guard == "" {
				guard = "not-applicable"
			}
			sktAssertCalled(t, recs[0], p, tc.outcome, tc.rowState, guard)
			if name, _ := errnames.Classify(err); (err == nil) != (tc.outcome == "ok") || (err != nil && name != tc.outcome) {
				t.Errorf("err = %v (class %q); want outcome %s", err, name, tc.outcome)
			}
			if d := sktDisagrees(t, id); tc.unusable && len(d) != 0 {
				t.Errorf("ad.provenance.disagree records = %v; want none", d)
			}
		})
	}
}

// sktAssertCalled checks one ad.send_keys.called record of the call with p.
func sktAssertCalled(t *testing.T, rec map[string]any, p api.SendKeysParams, outcome, rowState, guard string) {
	t.Helper()
	fields := map[string]any{
		"claude_instance_id": p.ClaudeInstanceID, "allow_pending": p.AllowPending, "row_state": rowState,
		"guard_evaluation": guard, "outcome": outcome, "source": "ad_send_keys",
	}
	for k, v := range fields {
		if got, ok := rec[k]; !ok || got != v {
			t.Errorf("%s = %v (present %t); want %v", k, got, ok, v)
		}
	}
	ktrAssertCaller(t, rec)
}

// TestSendKeysTrailRowStateIsTheStateRead: a SessionStart applied between the
// row read and the send leaves row_state the state read (pending).
func TestSendKeysTrailRowStateIsTheStateRead(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedPending(t, pendingFresh, pendingOurs)
	e.sessionStartAfter(t, tmux.CallLookup, r, "sess-"+uuid.NewString()[:8])
	p := api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hi", AllowPending: true}

	if _, _, err := e.sendKeysClient(t, p); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	if st := e.columns(t, r.ID).State; st != store.StateWaiting {
		t.Fatalf("state after the call = %v; want %s (the SessionStart applied)", st, store.StateWaiting)
	}
	recs := sktCalled(t, r.ID)
	if len(recs) != 1 {
		t.Fatalf("ad.send_keys.called records = %d; want 1", len(recs))
	}
	sktAssertCalled(t, recs[0], p, "ok", "pending", "not-applicable")
}

// TestSendKeysTrailClosedClient: a closed Client returns ErrClientClosed and
// writes no trail record.
func TestSendKeysTrailClosedClient(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := e.seedRow(t, killRowSpec{})
	c, _ := e.client(t)
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mark := trailMark(t)

	if _, err := c.SendKeys(api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hi"}); !errors.Is(err, api.ErrClientClosed) {
		t.Fatalf("SendKeys on a closed Client: err = %v; want ErrClientClosed", err)
	}
	assertNoTrailSince(t, mark, r.ID)
	e.assertNoTmuxCall(t)
}

// sktEntry is one send-keys entry point: api.SendKeys or Client.SendKeys.
type sktEntry struct {
	name   string
	client bool
}

// send runs send-keys on id through the entry point.
func (en sktEntry) send(t *testing.T, e *killEnv, id string) error {
	t.Helper()
	p := api.SendKeysParams{ClaudeInstanceID: id, Text: "hi"}
	if en.client {
		_, _, err := e.sendKeysClient(t, p)
		return err
	}
	_, err := e.sendKeys(p)
	return err
}

// TestSendKeysTrailProvenanceDisagree: on both entry points each reason is
// written once per call with its fields and action, never with a label value
// or another row's id; the normal Ours case writes none.
func TestSendKeysTrailProvenanceDisagree(t *testing.T) {
	t.Parallel()
	for _, entry := range []sktEntry{{"SendKeys", false}, {"Client.SendKeys", true}} {
		for _, tc := range keysDisagreeCases() {
			t.Run(entry.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				e := newKillEnv(t)
				r, other := e.seedKeysDisagreeCase(t, tc)

				_ = entry.send(t, e, r.ID)

				assertKeysDisagrees(t, e, sktDisagrees(t, r.ID), r, "send-keys", other, tc.want)
				called := sktCalled(t, r.ID)
				if !entry.client {
					if len(called) != 0 {
						t.Errorf("api.SendKeys wrote ad.send_keys.called %d times; want none", len(called))
					}
					return
				}
				if len(called) != 1 {
					t.Fatalf("ad.send_keys.called records = %d; want 1", len(called))
				}
				ktrAssertNoForeignContent(t, called[0], r.Token, e.storeID, other)
			})
		}
	}
}

// sktChildEnv gates TestSendKeysTrailFailOpenChild and carries the id prefix.
const sktChildEnv = "AD_SEND_KEYS_TRAIL_FAIL_CHILD"

// sktLinePrefix marks the child's result lines in its output.
const sktLinePrefix = "SKT|"

// sktFailOpenRuns runs Client.SendKeys on one row per trail shape, ids
// prefix-<name>, and returns one line per call: its error, calls and row columns.
func sktFailOpenRuns(t *testing.T, prefix string) []string {
	t.Helper()
	cases := []struct {
		name    string
		pending bool // a fresh spawn's pending row whose only session is a leftover
		spec    killRowSpec
		setups  []func(*testing.T, *killEnv, *killRow)
	}{
		{name: "ours"},
		{name: "adopted", spec: killRowSpec{NoServerIdentity: true, NoPane: true}},
		{name: "restarted-text-timeout",
			setups: []func(*testing.T, *killEnv, *killRow){ktrRestart, ktrScript(tmux.FailTimeout, tmux.CallSendText)}},
		{name: "rebound", setups: []func(*testing.T, *killEnv, *killRow){ktrRebind}},
		{name: "pending-leftover", pending: true, setups: []func(*testing.T, *killEnv, *killRow){sktLeftover}},
		{name: "gone", spec: killRowSpec{NoSession: true}},
	}
	var lines []string
	for _, tc := range cases {
		e := newKillEnv(t)
		spec := tc.spec
		if tc.pending {
			spec = e.pendingSpec(pendingFresh, pendingLeftover)
		}
		spec.ID = prefix + "-" + tc.name
		r := sktRow(spec, tc.setups...)(t, e)
		_, _, err := e.sendKeysClient(t, api.SendKeysParams{ClaudeInstanceID: r.ID, Text: "hi", AllowPending: tc.pending})
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

// TestSendKeysTrailFailOpen: with the trail unwritable, send-keys' errors,
// tmux calls and rows equal those of a run with a working trail.
func TestSendKeysTrailFailOpen(t *testing.T) {
	t.Parallel()
	prefix := "sk-failopen-" + uuid.NewString()[:8]
	want := sktFailOpenRuns(t, prefix)
	for _, l := range want {
		id := prefix + "-" + strings.Fields(l)[0]
		if n := len(sktCalled(t, id)); n != 1 {
			t.Fatalf("working trail: ad.send_keys.called records for %s = %d; want 1", id, n)
		}
	}
	for _, name := range []string{"adopted", "restarted-text-timeout", "rebound"} {
		if n := len(sktDisagrees(t, prefix+"-"+name)); n != 1 {
			t.Fatalf("working trail: ad.provenance.disagree records for %s = %d; want 1", name, n)
		}
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSendKeysTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), sktChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestSendKeysTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, sktLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestSendKeysTrailFailOpenChild is TestSendKeysTrailFailOpen's child: it
// runs the calls with an unwritable trail and prints their lines.
func TestSendKeysTrailFailOpenChild(t *testing.T) {
	t.Parallel()
	prefix := os.Getenv(sktChildEnv)
	if prefix == "" {
		t.Skip("run only as TestSendKeysTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.send_keys_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	for _, l := range sktFailOpenRuns(t, prefix) {
		fmt.Println(sktLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
