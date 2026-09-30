package hook_test

// sessionstart_wait_test.go — AC-HOOK-05 (SR-22.9 "SessionStart before the
// identity write", SR-13.4): a SessionStart on a pending row with no pane waits,
// re-reading every 250 ms on hookConfig's virtual clock, until launch start plus
// the pending grace. Driven through hook.Handle against a real store.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// swInterval is SessionStart's re-read interval (SR-22.9).
const swInterval = 250 * time.Millisecond

// swDeadline is the real time one Handle may take before the test fails
// rather than hangs.
const swDeadline = 30 * time.Second

var errSwRead = errors.New("sw: injected read failure")

// swStore is the real store recording the clock's sleep count at each GetSpawn;
// read failAt (1-based, 0 = never) fails.
type swStore struct {
	*store.Store
	clock  *advancingClock
	failAt int
	reads  []int
}

func (s *swStore) GetSpawn(id string) (store.Spawn, error) {
	s.reads = append(s.reads, len(s.clock.Sleeps()))
	if len(s.reads) == s.failAt {
		return store.Spawn{}, errSwRead
	}
	return s.Store.GetSpawn(id)
}

// swKind is a pending row's launch shape: its seed options for the launch
// start; resumed rows keep the session id session-start-resume.json reports.
type swKind struct {
	name    string
	opts    func(launch time.Time) []apitest.SpawnOption
	resumed bool
}

// swKinds: a fresh spawn's row, a reuse's (a later life) and a resumed row's
// (started_at hours old, its session id and transcript kept, recent launch start).
var swKinds = []swKind{
	{"fresh spawn", func(launch time.Time) []apitest.SpawnOption {
		return []apitest.SpawnOption{apitest.WithStartedAt(launch)}
	}, false},
	{"reuse", func(launch time.Time) []apitest.SpawnOption {
		return []apitest.SpawnOption{apitest.WithStartedAt(launch), apitest.WithLifeNumber(2),
			apitest.WithSessionHistory(apitest.SessionHistorySeed{SessionID: "sw-earlier-life", Life: 1})}
	}, false},
	{"resume", func(launch time.Time) []apitest.SpawnOption {
		return []apitest.SpawnOption{apitest.WithStartedAt(launch.Add(-9 * time.Hour)), apitest.WithLifeNumber(1),
			apitest.WithJsonlPath("/x/sw-resumed.jsonl")}
	}, true},
}

// swResumedSession is the session id a resumed row keeps: the one its
// SessionStart (session-start-resume.json) reports.
func swResumedSession(t *testing.T) string {
	t.Helper()
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(readPayloadFixture(t, "session-start-resume.json"), &p); err != nil || p.SessionID == "" {
		t.Fatalf("session-start-resume.json session_id = %q (err %v)", p.SessionID, err)
	}
	return p.SessionID
}

// swNoPane is a launch's token and socket before its identity write: no pane.
var swNoPane = store.LaunchIdentity{Token: "5555555555555555", Socket: apitest.TestSocket}

// atSleep runs fn once, right after hc's clock's nth sleep.
func atSleep(t *testing.T, hc hook.HandleConfig, n int, fn func()) {
	t.Helper()
	c := hookClock(t, hc)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runAt, c.run = n, fn
}

// swDuring arranges a write for the wait (on hc's clock) before Handle runs.
type swDuring func(t *testing.T, hc hook.HandleConfig, st *store.Store, id string)

// swCase is one SessionStart (or other hook) on a row with no pane.
type swCase struct {
	name     string
	fixture  string        // payload fixture; "" = session-start-startup.json
	state    string        // seeded state; "" = pending
	age      time.Duration // launch start = clock start - age
	noLaunch bool          // launch_started_at NULL
	grace    time.Duration // hc.PendingGrace; 0 = hookConfig's default
	noGrace  bool          // hc.PendingGrace = 0
	nilNow   bool          // hc.Now = nil
	foreign  bool          // parent = foreignParent (a leftover), else agentParent
	failAt   int           // swStore.failAt
	cancelAt int           // ctx cancelled right after this sleep; 0 = never
	frozen   bool          // hc.Now stays at the clock start: Sleep does not advance it
	during   swDuring

	sleeps int           // sleeps taken (one re-read after each)
	slack  int           // up to sleeps+slack sleeps are accepted
	waited time.Duration // virtual time slept; 0 = sleeps × 250 ms
	reason string        // ad.hook.ignored reason; "" = applied (or gone)
	after  string        // row state after
	gone   bool          // the row is deleted during the wait
}

// TestSessionStartWait: the wait's outcomes, its bound and its no-wait cases for
// a fresh spawn's, a reuse's and a resumed row (AC-HOOK-05, SR-22.9, SR-13.4).
func TestSessionStartWait(t *testing.T) {
	grace := config.Tmux{}.EffectivePendingGrace()
	longer, shorter := grace+10*time.Second, grace/2
	bound := int(grace / swInterval) // sleeps from launch start to the bound
	identityAt := func(n int) swDuring {
		return func(t *testing.T, hc hook.HandleConfig, st *store.Store, id string) {
			identityAtSleep(t, hc, st, id, n)
		}
	}
	storeWriteAt := func(n int, write func(*store.Store, string) error) swDuring {
		return func(t *testing.T, hc hook.HandleConfig, st *store.Store, id string) {
			atSleep(t, hc, n, func() {
				if err := write(st, id); err != nil {
					t.Errorf("write at sleep %d: %v", n, err)
				}
			})
		}
	}
	markMissing := func(st *store.Store, id string) error {
		prior, err := st.MarkSpawnMissing(id)
		if err == nil && prior != store.StatePending {
			err = errors.New("MarkSpawnMissing: prior " + prior + ", want pending")
		}
		return err
	}
	bumpVersion := func(st *store.Store, id string) error { return st.ClearLivenessUnverified(id) }
	deleteRow := func(st *store.Store, id string) error { return st.DeleteSpawn(id) }
	frozenLeft := grace - 10*time.Second // time left at the start of the frozen-clock case
	nopane, mismatch := store.HookReasonNoPaneRecorded, store.HookReasonPIDMismatch
	pending, waiting := store.StatePending, store.StateWaiting

	cases := []swCase{
		// AC-HOOK-05's outcomes.
		{name: "identity lands during the wait", during: identityAt(3), sleeps: 3, after: waiting},
		{name: "identity lands at the bound: the last gated write applies", during: identityAt(bound), sleeps: bound, after: waiting},
		{name: "bound passes", sleeps: bound, waited: grace, reason: nopane, after: pending},
		{name: "bound passes mid-interval: no sleep past it", age: 100 * time.Millisecond, sleeps: bound,
			waited: grace - 100*time.Millisecond, reason: nopane, after: pending},
		{name: "leftover waits, then the agent's pane lands", foreign: true, during: identityAt(3), sleeps: 3,
			reason: mismatch, after: pending},
		{name: "row leaves pending", during: storeWriteAt(2, markMissing), sleeps: 2, reason: nopane, after: store.StateMissing},
		{name: "row changes version", during: storeWriteAt(2, bumpVersion), sleeps: 2, reason: nopane, after: pending},
		{name: "store read error ends the wait fail-open", failAt: 3, sleeps: 2, reason: nopane, after: pending},
		{name: "row deleted during the wait: the wait ends, nothing ignored", during: storeWriteAt(2, deleteRow),
			sleeps: 2, gone: true},
		{name: "context cancelled ends the wait", cancelAt: 4, sleeps: 4, reason: nopane, after: pending},
		// A clock whose Sleep does not advance Now: the re-read cap (time left at the
		// start / 250 ms) ends the wait and the hook still returns.
		{name: "frozen Now: the re-read cap ends the wait", frozen: true, age: grace - frozenLeft,
			sleeps: int(frozenLeft / swInterval), slack: 1, reason: nopane, after: pending},
		// A grace other than the default moves the bound.
		{name: "longer grace: past the default, still waits", grace: longer, age: grace + 5*time.Second,
			sleeps: int(5 * time.Second / swInterval), reason: nopane, after: pending},
		{name: "shorter grace: earlier bound", grace: shorter, sleeps: int(shorter / swInterval), reason: nopane, after: pending},
		{name: "shorter grace: inside the default, past this grace", grace: shorter, age: shorter + time.Second,
			reason: nopane, after: pending},
		// No wait at all.
		{name: "launch start NULL", noLaunch: true, reason: nopane, after: pending},
		{name: "past the grace", age: grace, reason: nopane, after: pending},
		{name: "ended row with no pane", state: store.StateEnded, reason: nopane, after: store.StateEnded},
		{name: "Stop on a pending row with no pane", fixture: "stop.json", reason: nopane, after: pending},
		{name: "SessionStart with agent_id", fixture: "session-start-subagent.json",
			reason: store.HookReasonSubagentEvent, after: pending},
		{name: "zero grace", noGrace: true, reason: nopane, after: pending},
		{name: "nil Now", nilNow: true, reason: nopane, after: pending},
	}
	for k, kind := range swKinds {
		for i, tc := range cases {
			id := "sw-" + strconv.Itoa(k) + "-" + strconv.Itoa(i)
			t.Run(kind.name+"/"+tc.name, func(t *testing.T) { runSessionStartWait(t, id, kind, tc) })
		}
	}
}

// runSessionStartWait seeds tc's row id in kind's shape, fires one hook on
// virtual time and checks the sleeps, re-reads, row and trail.
func runSessionStartWait(t *testing.T, id string, kind swKind, tc swCase) {
	start := time.UnixMilli(time.Now().UnixMilli()) // whole ms: exact sleep counts
	launch := start.Add(-tc.age)
	state := tc.state
	if state == "" {
		state = store.StatePending
	}
	opts := append(kind.opts(launch), apitest.WithLaunchIdentity(swNoPane), apitest.WithLaunchStartedAt(launch.UnixMilli()))
	if tc.noLaunch {
		opts = append(opts, apitest.WithNoLaunchStartedAt())
	}
	kept := "" // the resumed row's session id, which must survive
	if kind.resumed {
		kept = swResumedSession(t)
	}
	st, dbPath := storefix.OpenTempStore(t) // seedAgentRow with a session id
	if _, err := apitest.SeedSpawn(dbPath, id, state, "", "", kept, false, opts...); err != nil {
		t.Fatalf("SeedSpawn(%q, %q): %v", id, state, err)
	}
	if kept != "" && mustGetSpawn(t, st, id).ClaudeSessionID != kept {
		t.Fatalf("seeded claude_session_id = %q; want %q", mustGetSpawn(t, st, id).ClaudeSessionID, kept)
	}
	parent := agentParent(t, st, id) // read before the pane lands: the agent whose pane lands
	if tc.foreign {
		parent = foreignParent(t, st, id)
	}
	hc := hookConfig(envHook(id, ""), parent)
	clock := hookClock(t, hc)
	clock.mu.Lock()
	*clock.now = start
	clock.mu.Unlock()
	switch {
	case tc.noGrace:
		hc.PendingGrace = 0
	case tc.grace != 0:
		hc.PendingGrace = tc.grace
	}
	if tc.nilNow {
		hc.Now = nil
	}
	if tc.frozen {
		hc.Now = func() time.Time { return start }
	}
	// Hang guard: ctx is cancelled at cancelAt, or else well past any bound, so
	// a wait that ignores its bound and cap ends there (and fails) rather than hangs.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	guard := 4 * (int(hc.PendingGrace/swInterval) + 1)
	clock.mu.Lock()
	clock.cancel, clock.cancelAfter = cancel, guard
	if tc.cancelAt != 0 {
		clock.cancelAfter = tc.cancelAt
	}
	clock.mu.Unlock()
	if tc.during != nil {
		tc.during(t, hc, st, id)
	}
	fixture := tc.fixture
	switch {
	case fixture != "":
	case kind.resumed:
		fixture = "session-start-resume.json"
	default:
		fixture = "session-start-startup.json"
	}
	prior, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns: %v", err)
	}
	before := len(readTrailLines(t, trailFile()))
	ss := &swStore{Store: st, clock: clock, failAt: tc.failAt}
	var out, logBuf bytes.Buffer

	payload := readPayloadFixture(t, fixture)
	done := make(chan error, 1)
	go func() { done <- hook.Handle(ctx, bytes.NewReader(payload), &out, ss, hc, log.New(&logBuf, "", 0)) }()
	select {
	case err := <-done:
		if err != nil || out.Len() != 0 {
			t.Fatalf("Handle = %v, stdout %q; want nil and empty (fail-open)", err, out.String())
		}
	case <-time.After(swDeadline):
		t.Fatalf("Handle did not return within %v (%d sleeps)", swDeadline, len(clock.Sleeps()))
	}

	// Sleeps: 250 ms each, the last cut short at the bound; the virtual time waited.
	sleeps := clock.Sleeps()
	var waited time.Duration
	for i, d := range sleeps {
		waited += d
		if d > swInterval || (d != swInterval && i != len(sleeps)-1) {
			t.Errorf("sleep %d = %v; want %v (only the last may be shorter)", i+1, d, swInterval)
		}
	}
	n := tc.sleeps // sleeps taken, when within tc.sleeps..tc.sleeps+tc.slack
	if len(sleeps) > n && len(sleeps) <= n+tc.slack {
		n = len(sleeps)
	}
	wantWaited := tc.waited
	if wantWaited == 0 {
		wantWaited = time.Duration(n) * swInterval
	}
	if len(sleeps) != n || waited != wantWaited {
		t.Errorf("sleeps = %d (%v); want %d..%d (%v)", len(sleeps), waited, tc.sleeps, tc.sleeps+tc.slack, wantWaited)
	}
	if tc.cancelAt == 0 && len(sleeps) >= guard {
		t.Errorf("the wait ran to the hang guard (%d sleeps): no bound or re-read cap ended it", len(sleeps))
	}
	// One re-read after each sleep: read i (0-based) follows min(i, n) sleeps.
	for i, got := range ss.reads {
		if want := min(i, n); got != want {
			t.Errorf("row reads at sleep counts %v; want 0, 1, …, %d, then %d", ss.reads, n, n)
			break
		}
	}
	if len(ss.reads) <= n {
		t.Errorf("row reads = %v; want one before the wait and one after each of %d sleeps", ss.reads, n)
	}
	if tc.failAt != 0 && !strings.Contains(logBuf.String(), errSwRead.Error()) {
		t.Errorf("log = %q; want the read failure", logBuf.String())
	}

	outcome := store.UpsertNoChange
	if tc.reason == "" && !tc.gone {
		outcome = store.UpsertUpdated
	}
	swAssertTrail(t, before, id, tc.reason, outcome)
	if tc.gone {
		if _, err := st.GetSpawn(id); !errors.Is(err, store.ErrSpawnNotFound) {
			t.Errorf("GetSpawn after the delete = %v; want ErrSpawnNotFound", err)
		}
		return
	}

	row := mustGetSpawn(t, st, id)
	if kept != "" && row.ClaudeSessionID != kept {
		t.Errorf("claude_session_id = %q; want the kept %q", row.ClaudeSessionID, kept)
	}
	if row.State != tc.after {
		t.Errorf("state = %q; want %q", row.State, tc.after)
	}
	if tc.reason == "" && row.PID != parent.PID {
		t.Errorf("pid = %d; want the parent %d", row.PID, parent.PID)
	}
	if tc.reason != "" && tc.during == nil {
		if got, err := apitest.ReadSpawnColumns(dbPath, id); err != nil || !reflect.DeepEqual(got, prior) {
			t.Errorf("row changed by an ignored hook (err %v):\n got %+v\nwant %+v", err, got, prior)
		}
	}
}

// swAssertTrail checks id got one ad.hook.fired with outcome and one
// ad.hook.ignored with reason, or none when reason is "".
func swAssertTrail(t *testing.T, before int, id, reason string, outcome store.UpsertOutcome) {
	t.Helper()
	var fired []any
	for _, row := range readTrailLines(t, trailFile())[before:] {
		if row["event"] == "ad.hook.fired" && row["claude_instance_id"] == id {
			fired = append(fired, row["upsert_outcome"])
		}
	}
	wantOutcome := string(outcome)
	if len(fired) != 1 || fired[0] != wantOutcome {
		t.Errorf("ad.hook.fired upsert_outcome = %v; want one %q", fired, wantOutcome)
	}
	ignored := hookIgnoredAfter(t, before, id)
	switch {
	case reason == "" && len(ignored) != 0:
		t.Errorf("ad.hook.ignored = %v; want none", ignored)
	case reason != "" && (len(ignored) != 1 || ignored[0]["reason"] != reason):
		t.Errorf("ad.hook.ignored = %v; want one with reason %q", ignored, reason)
	}
}
