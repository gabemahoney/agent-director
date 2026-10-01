package api_test

// expire_trail_test.go covers expire's trail (SR-12.5, SR-14, SR-15): one
// ad.expire.kept per kept row per run with exactly its four fields, none for a
// deleted row or a row another caller removed; one ad.provenance.disagree per
// distinct reason per row per run, its action deleted, the kept reason or
// left_changed; no record holding a label value, a session-environment value
// or another row's id; and fail-open through a child whose trail cannot be
// written. Rows, tmux and process state come from the kill fixture
// (expire_fixture_test.go).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/internal/trail"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// xtrEnvSentinel is a session-environment value every seeded row holds; no
// record may carry it.
const xtrEnvSentinel = "xtr-env-sentinel-7f3a"

// xtrKeptKeys are the exact keys of an ad.expire.kept record: its four fields
// and the trail's event and ts.
var xtrKeptKeys = []string{"claude_instance_id", "event", "reason", "source", "tmux_session_name", "ts"}

// A row's outcome besides a kept reason: deleted, or removed by another
// caller first (in neither list).
const (
	xtrDeleted = ""
	xtrNeither = "-"
)

// xtrRow is a seeded row and what one expire run must do with it: its outcome
// (a kept reason, xtrDeleted or xtrNeither) and its disagree records, in order.
type xtrRow struct {
	r        *killRow
	outcome  string
	disagree []disagreeWant
}

// xtrWorld is one case's killEnv, its expireStore wrapper and its rows, whose
// ids are prefix-<name>.
type xtrWorld struct {
	e      *killEnv
	w      *expireStore
	prefix string
	rows   []*xtrRow
}

// newXtrWorld returns an empty world over a new killEnv.
func newXtrWorld(t *testing.T, prefix string) *xtrWorld {
	t.Helper()
	e := newKillEnv(t)
	return &xtrWorld{e: e, w: e.expireStore(), prefix: prefix}
}

// row seeds row name, ended two hours ago with its agent in state a, and
// records the outcome and disagree records one run must give it.
func (x *xtrWorld) row(t *testing.T, name string, a agentState, outcome string, disagree ...disagreeWant) *killRow {
	t.Helper()
	return x.rowWith(t, name, a, nil, outcome, disagree...)
}

// rowWith is row with mod applied to the spec before seeding.
func (x *xtrWorld) rowWith(t *testing.T, name string, a agentState, mod func(*killRowSpec), outcome string,
	disagree ...disagreeWant) *killRow {
	t.Helper()
	spec := x.e.finishedSpec(2*time.Hour, a, apitest.WithExtraEnv(map[string]string{"XTR_ENV": xtrEnvSentinel}))
	spec.ID = x.prefix + "-" + name
	if mod != nil {
		mod(&spec)
	}
	r := x.e.seedRow(t, spec)
	x.rows = append(x.rows, &xtrRow{r: &r, outcome: outcome, disagree: disagree})
	return &r
}

// restart restarts r's server with no session of r on the new one.
func (x *xtrWorld) restart(r *killRow) {
	x.e.ensureServer(r)
	x.e.rec.RestartServer(r.Socket, tmuxfix.Server{})
	x.e.syncServers()
}

// foreign seeds another row's labelled session beside the rows' (on a server
// already bound) and returns that row's id and token, which no record may hold.
func (x *xtrWorld) foreign() (id, token string) {
	r := x.rows[0].r
	id, token = "other-"+uuid.NewString()[:8], strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	x.e.ensureServer(r)
	x.e.rec.SeedSessions(r.Socket, tmuxfix.SeedSession{Name: "foreign-" + uuid.NewString()[:8],
		Label: tmuxfix.Valid(token, id, r.StoreID)})
	x.e.syncServers()
	return id, token
}

// run runs expire once through call and checks every row's result and
// records, and that the run wrote nothing else.
func (x *xtrWorld) run(t *testing.T, call func() (api.ExpireResult, error)) {
	t.Helper()
	otherID, otherToken := x.foreign()
	mark := trailMark(t)
	res, err := call()
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	want := map[string]string{}
	for _, row := range x.rows {
		if row.outcome != xtrNeither {
			want[row.r.ID] = row.outcome
		}
	}
	assertExpired(t, res, mark, want)

	lines := readAPITrailLines(t)[mark:]
	for _, l := range lines {
		if !slices.ContainsFunc(x.rows, func(row *xtrRow) bool { return l["claude_instance_id"] == row.r.ID }) ||
			(l["event"] != expireKeptEvent && (l["event"] != "ad.provenance.disagree" || l["verb"] != "expire")) {
			t.Errorf("record %s; want only expire's records of the run's rows", ktrJSON(t, l))
		}
	}
	for _, row := range x.rows {
		x.assertRowRecords(t, lines, row, otherID, otherToken)
	}
}

// expire is run through the exported api.Expire on x.w with a one-hour window.
func (x *xtrWorld) expire(t *testing.T) {
	t.Helper()
	x.run(t, func() (api.ExpireResult, error) {
		res, _, err := x.e.expireWith(x.w, olderThan(time.Hour))
		return res, err
	})
}

// assertRowRecords checks row's records among lines: its ad.expire.kept has
// exactly the four fields, its disagree records are row.disagree, and none
// holds a token, the store id, the environment sentinel or another row's id.
func (x *xtrWorld) assertRowRecords(t *testing.T, lines []map[string]any, row *xtrRow, otherID, otherToken string) {
	t.Helper()
	r := row.r
	forbidden := []string{xtrEnvSentinel, x.e.storeID, otherID, otherToken}
	for _, o := range x.rows {
		forbidden = append(forbidden, o.r.Token)
		if o != row {
			forbidden = append(forbidden, o.r.ID)
		}
	}
	var disagrees []map[string]any
	for _, l := range lines {
		if l["claude_instance_id"] != r.ID {
			continue
		}
		ktrAssertNoForeignContent(t, l, forbidden...)
		if l["event"] != expireKeptEvent {
			disagrees = append(disagrees, l)
			continue
		}
		keys := make([]string, 0, len(l))
		for k := range l {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if !slices.Equal(keys, xtrKeptKeys) {
			t.Errorf("%s: ad.expire.kept keys = %v; want %v", r.ID, keys, xtrKeptKeys)
		}
		if l["tmux_session_name"] != r.Name || l["source"] != "ad_expire" || l["reason"] != row.outcome {
			t.Errorf("%s: ad.expire.kept %s; want reason %s, tmux_session_name %q, source ad_expire",
				r.ID, ktrJSON(t, l), row.outcome, r.Name)
		}
	}
	if len(disagrees) != len(row.disagree) {
		t.Errorf("%s: ad.provenance.disagree records = %d; want %d: %v", r.ID, len(disagrees), len(row.disagree), disagrees)
		return
	}
	for i, w := range row.disagree {
		assertDisagreeRecord(t, disagrees[i], *r, "expire", "ad_expire", w)
	}
}

// xtrCase is one world: seed seeds its rows, their expectations and the tmux,
// process and store answers that give them.
type xtrCase struct {
	name string
	seed func(*testing.T, *xtrWorld)
}

// xtrKeptCases covers the kept reasons no disagree reason comes with, and the
// delete outcomes; xtrDisagreeCases covers the other kept reasons.
func xtrKeptCases() []xtrCase {
	stopsSocket := func(f tmux.Failure, first string) func(*testing.T, *xtrWorld) {
		return func(t *testing.T, x *xtrWorld) {
			a := x.row(t, "a", agentGone, first)
			x.row(t, "b", agentGone, "tmux_skipped")
			x.e.rec.Script(a.Socket, tmuxfix.Script{Failure: f}, tmux.CallLookup)
		}
	}
	return []xtrCase{
		{name: "process_alive, with no lookup and so no disagree record", seed: func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentAlive, "process_alive")
			x.e.seedSession(t, r)
			ktrRebind(t, x.e, r)
		}},
		{name: "ours", seed: func(t *testing.T, x *xtrWorld) {
			x.e.seedSession(t, x.row(t, "r", agentGone, "ours"))
		}},
		{name: "leftover_running", seed: func(t *testing.T, x *xtrWorld) {
			x.e.seedLeftover(t, *x.row(t, "r", agentUnreadable, "leftover_running"), tmuxfix.OtherToken)
		}},
		{name: "cant_tell, then tmux_skipped", seed: stopsSocket(tmux.FailUnrecognized, "cant_tell")},
		{name: "tmux_unavailable, then tmux_skipped", seed: stopsSocket(tmux.FailUnavailable, "tmux_unavailable")},
		{name: "deleted, changed_since_examined, store_error and removed by another caller", seed: func(t *testing.T, x *xtrWorld) {
			x.row(t, "a", agentGone, xtrDeleted)
			b := x.row(t, "b", agentZombie, "changed_since_examined")
			c := x.row(t, "c", agentNotRecorded, "store_error")
			d := x.row(t, "d", agentGone, xtrNeither)
			x.w.refuseDelete(b.ID, api.CondChanged)
			x.w.failDelete(c.ID, nil)
			x.w.refuseDelete(d.ID, api.CondAbsent)
		}},
	}
}

// xtrDisagreeCases covers each of the six reasons expire can write, the
// action of every outcome, and the rows that write none.
func xtrDisagreeCases() []xtrCase {
	restarted := func(verdict, action string, ours bool) disagreeWant {
		return disagreeWant{reason: "server_restarted", server: "restarted", verdict: verdict, action: action, ours: ours}
	}
	restartedGone := func(outcome, action string, store func(*expireStore, string)) func(*testing.T, *xtrWorld) {
		return func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentGone, outcome, restarted("gone", action, false))
			x.restart(r)
			if store != nil {
				store(x.w, r.ID)
			}
		}
	}
	conflict := func(reason string) disagreeWant {
		return disagreeWant{reason: reason, server: "match", verdict: "provenance_conflict", action: "provenance_conflict"}
	}
	renamed := func(server string) disagreeWant {
		return disagreeWant{reason: "name_changed", server: server, verdict: "ours", action: "ours", current: "renamed-kill", ours: true}
	}
	return []xtrCase{
		{name: "server_restarted, deleted", seed: restartedGone(xtrDeleted, "deleted", nil)},
		{name: "server_restarted, changed_since_examined", seed: restartedGone("changed_since_examined", "changed_since_examined",
			func(w *expireStore, id string) { w.refuseDelete(id, api.CondChanged) })},
		{name: "server_restarted, store_error", seed: restartedGone("store_error", "store_error",
			func(w *expireStore, id string) { w.failDelete(id, nil) })},
		{name: "server_restarted, removed by another caller: left_changed", seed: restartedGone(xtrNeither, "left_changed",
			func(w *expireStore, id string) { w.refuseDelete(id, api.CondAbsent) })},
		{name: "server_restarted, ours", seed: func(t *testing.T, x *xtrWorld) {
			ktrRestart(t, x.e, x.row(t, "r", agentGone, "ours", restarted("ours", "ours", true)))
		}},
		{name: "server_restarted, leftover_running", seed: func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentGone, "leftover_running", restarted("leftover", "leftover_running", false))
			x.restart(r)
			x.e.seedLeftover(t, *r, tmuxfix.OtherToken)
		}},
		{name: "server_mismatch, tmux_server_changed", seed: func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentGone, "tmux_server_changed", disagreeWant{reason: "server_mismatch", server: "differs",
				verdict: "different_server", action: "tmux_server_changed"})
			x.e.seedSession(t, r)
			ktrRebind(t, x.e, r)
		}},
		{name: "duplicate_label, provenance_conflict", seed: func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentGone, "provenance_conflict", conflict("duplicate_label"))
			x.e.seedSession(t, r)
			sktDuplicate(t, x.e, r)
		}},
		{name: "scope_value, provenance_conflict", seed: func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentGone, "provenance_conflict", conflict("scope_value"))
			x.e.seedSession(t, r)
			x.e.rec.SetScope(r.Socket, tmuxfix.ScopeGlobal, tmuxfix.ScopeValue{SessionID: r.Session.ID, Label: r.current()})
		}},
		{name: "name_changed, ours", seed: func(t *testing.T, x *xtrWorld) {
			ktrRenamed(t, x.e, x.row(t, "r", agentGone, "ours", renamed("match")))
		}},
		{name: "server_restarted and name_changed, one record each", seed: func(t *testing.T, x *xtrWorld) {
			r := x.row(t, "r", agentGone, "ours", restarted("ours", "ours", true), renamed("restarted"))
			x.restart(r)
			ktrRenamed(t, x.e, r)
		}},
		{name: "a lost create reply's ours writes none, never adopted", seed: func(t *testing.T, x *xtrWorld) {
			noReply := func(s *killRowSpec) { s.NoServerIdentity, s.NoPane = true, true }
			x.e.seedSession(t, x.rowWith(t, "r", agentGone, noReply, "ours"))
		}},
		{name: "rows sharing the socket's lookup each get their own", seed: func(t *testing.T, x *xtrWorld) {
			for _, n := range []string{"a", "b"} {
				name := "xtr-renamed-" + n
				r := x.row(t, n, agentGone, "ours", disagreeWant{reason: "name_changed", server: "match", verdict: "ours",
					action: "ours", current: name, ours: true})
				x.e.seedSession(t, r, tmuxfix.WithRowSessionName(name))
			}
			x.row(t, "c", agentGone, xtrDeleted)
			x.row(t, "d", agentAlive, "process_alive")
		}},
	}
}

// xtrRunCases runs each case's world once through api.Expire.
func xtrRunCases(t *testing.T, cases []xtrCase) {
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := newXtrWorld(t, "xtr-"+uuid.NewString()[:8])
			tc.seed(t, x)
			x.expire(t)
		})
	}
}

// TestExpireTrailKept: each kept row writes exactly one ad.expire.kept with
// its reason and four fields; a deleted row and one another caller removed
// write none.
func TestExpireTrailKept(t *testing.T) {
	xtrRunCases(t, xtrKeptCases())
}

// TestExpireTrailProvenanceDisagree: each reason writes one record per row,
// action deleted, the kept reason or left_changed; a row without one writes none.
func TestExpireTrailProvenanceDisagree(t *testing.T) {
	xtrRunCases(t, xtrDisagreeCases())
}

// TestExpireTrailEveryRun: a row still kept gets its records again on each
// run, through api.Expire and through Client.Expire alike.
func TestExpireTrailEveryRun(t *testing.T) {
	x := newXtrWorld(t, "xtr-"+uuid.NewString()[:8])
	ktrRenamed(t, x.e, x.row(t, "a", agentGone, "ours", disagreeWant{reason: "name_changed", server: "match",
		verdict: "ours", action: "ours", current: "renamed-kill", ours: true}))
	x.row(t, "b", agentAlive, "process_alive")
	x.expire(t)
	x.expire(t)
	x.run(t, func() (api.ExpireResult, error) {
		res, _, err := x.e.expireClient(t, olderThan(time.Hour))
		return res, err
	})
}

// xtrChildEnv gates TestExpireTrailFailOpenChild and carries the id prefix.
const xtrChildEnv = "AD_EXPIRE_TRAIL_FAIL_CHILD"

// xtrLinePrefix marks the child's result lines in its output.
const xtrLinePrefix = "XTR|"

// xtrFailOpenRuns runs every case's world once, ids prefix-<n>-<name>, with no
// trail check, and returns one line per case (result lists, log lines, tmux
// calls and each row's columns) and the records a working trail must get.
func xtrFailOpenRuns(t *testing.T, prefix string) (lines []string, kept, disagree int) {
	t.Helper()
	for i, tc := range append(xtrKeptCases(), xtrDisagreeCases()...) {
		x := newXtrWorld(t, fmt.Sprintf("%s-%02d", prefix, i))
		tc.seed(t, x)
		x.foreign()
		res, lg, err := x.e.expireWith(x.w, olderThan(time.Hour))
		var calls []tmux.Call
		for _, c := range x.e.rec.SocketCalls() {
			calls = append(calls, c.Call)
		}
		var rows []string
		for _, row := range x.rows {
			if row.outcome != xtrDeleted && row.outcome != xtrNeither {
				kept++
			}
			disagree += len(row.disagree)
			c, cerr := apitest.ReadSpawnColumns(x.e.dbPath, row.r.ID)
			rows = append(rows, fmt.Sprintf("%s:%t/%v/%v", row.r.ID, cerr == nil, c.State, c.RowVersion))
		}
		lines = append(lines, fmt.Sprintf("%02d err=%v ids=%v kept=%v count=%d/%d logs=%q calls=%v rows=%v",
			i, err, res.IDs, res.KeptIDs, res.Count, res.Kept, lg.lines, calls, rows))
	}
	return lines, kept, disagree
}

// TestExpireTrailFailOpen: with the trail unwritable, every case gives the same
// result, log lines, tmux calls and rows as with a working trail.
func TestExpireTrailFailOpen(t *testing.T) {
	prefix := "xtr-failopen"
	mark := trailMark(t)
	want, kept, disagree := xtrFailOpenRuns(t, prefix)
	if n := len(trailSince(t, mark, expireKeptEvent)); n != kept {
		t.Fatalf("working trail: ad.expire.kept records = %d; want %d", n, kept)
	}
	if n := len(trailSince(t, mark, "ad.provenance.disagree")); n != disagree {
		t.Fatalf("working trail: ad.provenance.disagree records = %d; want %d", n, disagree)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestExpireTrailFailOpenChild$", "-test.count=1", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), xtrChildEnv+"="+prefix)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: TestExpireTrailFailOpenChild") {
		t.Fatalf("child: %v\n%s", err, out)
	}
	var got []string
	for _, l := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(l, xtrLinePrefix); ok {
			got = append(got, rest)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("unwritable trail gave\n%s\nwant (working trail)\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestExpireTrailFailOpenChild is TestExpireTrailFailOpen's child: it runs the
// cases with an unwritable trail and prints their lines.
func TestExpireTrailFailOpenChild(t *testing.T) {
	prefix := os.Getenv(xtrChildEnv)
	if prefix == "" {
		t.Skip("run only as TestExpireTrailFailOpen's child")
	}
	adDir := filepath.Join(apiTrailDir, ".agent-director")
	if err := os.MkdirAll(adDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(adDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(adDir, 0o700) })
	if err := trail.Emit(context.Background(), "ad.test.expire_probe", map[string]any{}); err == nil {
		t.Fatal("trail write succeeded; want it to fail")
	}

	lines, _, _ := xtrFailOpenRuns(t, prefix)
	for _, l := range lines {
		fmt.Println(xtrLinePrefix + l)
	}

	if _, err := os.Stat(apiTrailFilePath()); !os.IsNotExist(err) {
		t.Errorf("trail file stat err = %v; want it never created", err)
	}
}
