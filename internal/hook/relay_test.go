package hook_test

// relay_test.go — the relay hook of b.146 step 2 against a real store and,
// for failures a store cannot be made to give on cue, the flakyRelayStore
// double: what it records (rules 2, 4, 14), its first write as one
// transaction cut at its deadline (rule 1, problem 1), no answer without its
// ack (rule 3), its ack's and timeout deny's reserve (rule 4), the parent
// check (problem 7) and the b.146 property for the relay hook (no
// [store] busy_timeout_ms makes it miss Claude Code's kill).

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// relayHookSelf is the identity relayHC's Self reports for the hook process.
var relayHookSelf = store.ProcessIdentity{PID: 54321, Starttime: storefix.SeedPaneStarttime, PIDNamespace: "pid:[4026531836]"}

// relayPayload is a relayed PermissionRequest carrying a tool_use_id.
const relayPayload = `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"},"tool_use_id":"toolu_01ABC"}`

// relayHC is hookConfig for a relayed hook of id from parent p, on
// hookConfig's virtual clock, told its relay timeout (--timeout), reading its
// own identity as relayHookSelf.
func relayHC(id string, p hookParent, timeout time.Duration) hook.HandleConfig {
	hc := hookConfig(envWith(id), p)
	hc.RelayTimeout = timeout
	hc.Self = func() store.ProcessIdentity { return relayHookSelf }
	return hc
}

// onlyRequest returns id's one permission request.
func onlyRequest(t *testing.T, st *store.Store, id string) store.PermissionRow {
	t.Helper()
	rows, err := st.PermissionRequestsForSpawn(id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("permission requests of %s = %d, %v; want 1", id, len(rows), err)
	}
	return rows[0]
}

// decideOnly records verdict on id's one request, as decide does.
func decideOnly(t *testing.T, st *store.Store, id, verdict string) {
	t.Helper()
	reason := ""
	if verdict == "deny" {
		reason = store.DecisionReasonOperator
	}
	if ok, err := st.DecideRelayRequest(id, onlyRequest(t, st, id).RequestToken, verdict, reason, store.WriterProcessDecide,
		time.Time{}, store.DefaultLockWait); err != nil || !ok {
		t.Errorf("decide %s = %v, %v", verdict, ok, err)
	}
}

// fireRelay runs Handle for payload and returns its stdout and how long it took.
func fireRelay(t *testing.T, st hook.HookStore, hc hook.HandleConfig, payload string) (string, time.Duration) {
	t.Helper()
	var out bytes.Buffer
	start := time.Now()
	if err := hook.Handle(context.Background(), strings.NewReader(payload), &out, st, hc, newSilentLogger()); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	return out.String(), time.Since(start)
}

// TestRelayRecordsItsRequest (rules 2, 4, 14): the first write records the
// hook's identity as it read itself, the payload's tool_use_id (and agent_id
// for a subagent's request, in hook_subagent_test.go), and the settle instant
// readers fall back to: the hook's start plus its --timeout, or the relay
// window when it has none, plus the 2 s reserve. A hook that cannot read its
// identity records none.
func TestRelayRecordsItsRequest(t *testing.T) {
	cases := []struct {
		name     string
		timeout  time.Duration // --timeout; 0 = none given
		window   int           // relay.timeout_seconds
		self     bool          // Self reads an identity
		wantKill time.Duration // after the start
	}{
		{"--timeout given", 10 * time.Second, 0, true, 10 * time.Second},
		{"no --timeout: the relay window", 0, 20, true, 20 * time.Second},
		{"identity unreadable", 10 * time.Second, 0, false, 10 * time.Second},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "relay-records-" + string(rune('a'+i))
			st, _ := seedAgentRow(t, id, store.StateWorking)
			hc := relayHC(id, agentParent(t, st, id), tc.timeout)
			hc.Cfg = config.Relay{TimeoutSeconds: tc.window}
			if !tc.self {
				hc.Self = func() store.ProcessIdentity { return store.ProcessIdentity{} }
			}
			start := hc.Now()
			var mid store.Spawn
			var req store.PermissionRow
			atSleep(t, hc, 1, func() {
				mid, req = mustGetSpawn(t, st, id), onlyRequest(t, st, id)
				decideOnly(t, st, id, "allow")
			})

			out, _ := fireRelay(t, st, hc, relayPayload)

			if want := hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n"; out != want {
				t.Errorf("stdout = %q; want %q", out, want)
			}
			if mid.State != store.StateCheckPermission {
				t.Errorf("state while polling = %q; want check_permission", mid.State)
			}
			wantHook := relayHookSelf
			if !tc.self {
				wantHook = store.ProcessIdentity{}
			}
			if wantSettled := start.Add(tc.wantKill + 2*time.Second).UnixMilli(); req.Hook != wantHook || req.ToolUseID != "toolu_01ABC" ||
				req.SettledAt.UnixMilli() != wantSettled || req.AgentID != "" {
				t.Errorf("request = hook %+v, tool_use_id %q, settled_at %d, agent_id %q; want %+v, toolu_01ABC, %d, none",
					req.Hook, req.ToolUseID, req.SettledAt.UnixMilli(), req.AgentID, wantHook, wantSettled)
			}
		})
	}
}

// TestRelayFirstWriteIsOneTransaction (rule 1): a hook that dies at its
// request insert, after its move to check_permission in the same transaction
// (an insert that fails stands in for the death), leaves neither the request
// nor check_permission, and answers today's fail-closed deny.
func TestRelayFirstWriteIsOneTransaction(t *testing.T) {
	const id = "relay-one-tx"
	st, dbPath := seedAgentRow(t, id, store.StateWorking)
	hc := relayHC(id, agentParent(t, st, id), 10*time.Second)
	before := mustGetSpawn(t, st, id)
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`CREATE TRIGGER ad_test_hook_dies_at_insert BEFORE INSERT ON permission_requests
		BEGIN SELECT RAISE(ABORT, 'test: the hook dies at its insert'); END`); err != nil {
		t.Fatalf("install trigger: %v", err)
	}

	out, _ := fireRelay(t, st, hc, relayPayload)

	assertDenyEnvelope(t, bytes.NewBufferString(out))
	assertRowUnchanged(t, st, id, before)
	assertNoRequests(t, st, id)
}

// TestRelayFirstWriteCutAtItsDeadline (problem 1; the b.146 property): under
// a write lock held past the hook's kill, its first write waits only until its
// deadline less the 2 s reserve, then the hook answers the fail-closed deny
// before Claude Code would kill it, whatever [store] busy_timeout_ms is, and
// records nothing. With no time left it does not wait at all.
func TestRelayFirstWriteCutAtItsDeadline(t *testing.T) {
	const timeout = 3500 * time.Millisecond
	cases := []struct {
		name    string
		busyMs  int
		started time.Duration // how long before Handle the hook started
		maxTook time.Duration
	}{
		{"default busy timeout", store.DefaultBusyTimeoutMs, 0, timeout - 2*time.Second + 500*time.Millisecond},
		{"largest busy timeout", config.MaxStoreBusyTimeoutMs, 0, timeout - 2*time.Second + 500*time.Millisecond},
		{"no time left before the reserve", config.MaxStoreBusyTimeoutMs, 2 * time.Second, 500 * time.Millisecond},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := "relay-cut-" + string(rune('a'+i))
			seeded, dbPath := seedAgentRow(t, id, store.StateWorking)
			st, err := store.OpenWithBusyTimeout(dbPath, tc.busyMs)
			if err != nil {
				t.Fatalf("OpenWithBusyTimeout: %v", err)
			}
			defer st.Close()
			hc := relayHC(id, agentParent(t, seeded, id), timeout)
			hc.Now, hc.Clock, hc.Start = time.Now, hook.DefaultPollClock(), time.Now().Add(-tc.started)
			before := mustGetSpawn(t, st, id)
			release := apitest.HoldWriteLock(t, dbPath, time.Minute)

			out, took := fireRelay(t, st, hc, relayPayload)
			release()

			assertDenyEnvelope(t, bytes.NewBufferString(out))
			if took > tc.maxTook {
				t.Errorf("the hook answered after %v; want within %v (its deadline less the reserve, before its %v kill)", took, tc.maxTook, timeout)
			}
			assertRowUnchanged(t, st, id, before)
			assertNoRequests(t, st, id)
		})
	}
}

// TestRelayPrintsNothingWithoutAck (rule 3): once its request is recorded the
// hook writes only an answer whose ack committed. A failed or empty ack, a
// read that keeps failing, a deleted request and a failed or cut timeout deny
// each end with nothing on stdout (Claude Code's dialog then appears) and no
// ad.resume.observed; the timeout deny, its own ack, and a verdict that landed
// before it are written once acked.
func TestRelayPrintsNothingWithoutAck(t *testing.T) {
	allow := []store.PermissionRow{{Decision: "allow"}}
	undecided := []store.PermissionRow{{}}
	busy := store.ErrStoreBusy
	cases := []struct {
		name       string
		st         *flakyRelayStore
		want       string // stdout
		acks, deny int
	}{
		{"ack fails", &flakyRelayStore{getRows: allow, getErrs: []error{nil}, ackErr: errors.New("disk I/O error")}, "", 1, 0},
		{"ack cut by a held lock", &flakyRelayStore{getRows: allow, getErrs: []error{nil}, ackErr: busy}, "", 1, 0},
		{"ack matches nothing", &flakyRelayStore{getRows: allow, getErrs: []error{nil}, ackNone: true}, "", 1, 0},
		{"reads keep failing", &flakyRelayStore{getRows: make([]store.PermissionRow, 7), getErrs: repeatErr(7, errors.New("db error"))}, "", 0, 0},
		{"request deleted", &flakyRelayStore{}, "", 0, 0},
		{"timeout deny fails", &flakyRelayStore{getRows: undecided, getErrs: []error{nil}, denyErr: errors.New("disk I/O error")}, "", 0, 1},
		{"timeout deny cut by a held lock", &flakyRelayStore{getRows: undecided, getErrs: []error{nil}, denyErr: busy}, "", 0, 1},
		{"timeout deny loses to a verdict, acked", &flakyRelayStore{getRows: undecided, getErrs: []error{nil}, denyNone: true, ackDecision: "allow"},
			hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n", 1, 1},
		{"timeout deny", &flakyRelayStore{getRows: undecided, getErrs: []error{nil}},
			hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n", 0, 1},
		{"verdict acked", &flakyRelayStore{getRows: allow, getErrs: []error{nil}},
			hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n", 1, 0},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "relay-noack-" + string(rune('a'+i))
			before := len(readTrailLines(t, trailFile()))

			out, _ := fireRelay(t, tc.st, relayDoubleConfig(id), relayPayload)

			if out != tc.want {
				t.Errorf("stdout = %q; want %q", out, tc.want)
			}
			if len(tc.st.acks) != tc.acks || len(tc.st.denies) != tc.deny {
				t.Errorf("acks/timeout denies = %d/%d; want %d/%d", len(tc.st.acks), len(tc.st.denies), tc.acks, tc.deny)
			}
			wantResume := 0
			if tc.want != "" {
				wantResume = 1
			}
			if n := len(linesAfter(t, before, "ad.resume.observed", id)); n != wantResume {
				t.Errorf("ad.resume.observed lines = %d; want %d (one only for a written answer)", n, wantResume)
			}
		})
	}
}

// TestRelayNoAckInsideTheReserve (rule 4, on an injected clock): the hook acks,
// or makes its timeout deny, only while a commit still leaves 2 s before its
// kill, capping the write's lock wait at what is left to that point; past it
// it writes nothing and exits silently.
func TestRelayNoAckInsideTheReserve(t *testing.T) {
	const timeout = 30 * time.Second
	cases := []struct {
		name     string
		rows     []store.PermissionRow
		readAt   time.Duration // the first read ends this long after the start (a slow read)
		want     string
		acks     []time.Duration // each ack's lock wait
		denies   []time.Duration
		readOnce bool
	}{
		{"verdict read with 2.5 s left", []store.PermissionRow{{Decision: "allow"}}, timeout - 2500*time.Millisecond,
			hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n", []time.Duration{500 * time.Millisecond}, nil, true},
		{"verdict read with 1.9 s left", []store.PermissionRow{{Decision: "allow"}}, timeout - 1900*time.Millisecond, "", nil, nil, true},
		{"poll's end: the deny may wait 1 s", []store.PermissionRow{{}}, 0,
			hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n", nil, []time.Duration{time.Second}, false},
		{"poll's end found with 1.5 s left", []store.PermissionRow{{}}, timeout - 1500*time.Millisecond, "", nil, nil, false},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hc := relayDoubleConfig("relay-reserve-" + string(rune('a'+i)))
			hc.RelayTimeout = timeout
			clock, start := hookClock(t, hc), hc.Now()
			var once sync.Once
			st := &flakyRelayStore{getRows: tc.rows, getErrs: []error{nil}, onGet: func() {
				once.Do(func() { clock.Advance(start.Add(tc.readAt).Sub(clock.Now())) })
			}}

			out, _ := fireRelay(t, st, hc, relayPayload)

			if out != tc.want {
				t.Errorf("stdout = %q; want %q", out, tc.want)
			}
			if got := waits(st.acks); !equalDurations(got, tc.acks) {
				t.Errorf("acks' lock waits = %v; want %v", got, tc.acks)
			}
			if got := waits(st.denies); !equalDurations(got, tc.denies) {
				t.Errorf("timeout denies' lock waits = %v; want %v", got, tc.denies)
			}
		})
	}
}

// waits returns each write's lock wait.
func waits(ws []relayWrite) []time.Duration {
	var out []time.Duration
	for _, w := range ws {
		out = append(out, w.MaxWait)
	}
	return out
}

// equalDurations compares two lists, nil and empty alike.
func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// realSleepAt is a real-time PollClock that runs fn once at its first sleep.
type realSleepAt struct {
	once sync.Once
	fn   func()
}

func (c *realSleepAt) Sleep(ctx context.Context, d time.Duration) {
	c.once.Do(c.fn)
	hook.DefaultPollClock().Sleep(ctx, d)
}

// TestRelayAckKeepsItsReserve (rule 4; the b.146 property): with the write
// lock taken while the hook polls and held past its kill, its ack of a verdict
// and its timeout deny each wait only until 2 s before the kill, whatever
// [store] busy_timeout_ms is; the hook then exits with nothing on stdout and
// nothing acked, before Claude Code would kill it.
func TestRelayAckKeepsItsReserve(t *testing.T) {
	const timeout = 4 * time.Second
	for i, tc := range []struct {
		name    string
		verdict string // recorded before the lock is taken; "" = the timeout deny's case
		busyMs  int
	}{
		{"verdict, default busy timeout", "allow", store.DefaultBusyTimeoutMs},
		{"verdict, largest busy timeout", "allow", config.MaxStoreBusyTimeoutMs},
		{"timeout deny, default busy timeout", "", store.DefaultBusyTimeoutMs},
		{"timeout deny, largest busy timeout", "", config.MaxStoreBusyTimeoutMs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := "relay-reserve-real-" + string(rune('a'+i))
			seeded, dbPath := seedAgentRow(t, id, store.StateWorking)
			st, err := store.OpenWithBusyTimeout(dbPath, tc.busyMs)
			if err != nil {
				t.Fatalf("OpenWithBusyTimeout: %v", err)
			}
			defer st.Close()
			hc := relayHC(id, agentParent(t, seeded, id), timeout)
			var release func()
			hc.Now, hc.Start = time.Now, time.Now()
			hc.Clock = &realSleepAt{fn: func() {
				if tc.verdict != "" {
					decideOnly(t, st, id, tc.verdict)
				}
				release = apitest.HoldWriteLock(t, dbPath, time.Minute)
			}}

			out, took := fireRelay(t, st, hc, relayPayload)
			if release != nil {
				release()
			}

			if out != "" {
				t.Errorf("stdout = %q; want nothing (the ack could not commit with its reserve)", out)
			}
			if limit := timeout - 2*time.Second + 500*time.Millisecond; took > limit {
				t.Errorf("the hook exited after %v; want within %v, its reserve before its %v kill", took, limit, timeout)
			}
			if req := onlyRequest(t, st, id); req.Decision != tc.verdict || !req.DeliveredAt.IsZero() {
				t.Errorf("request = decision %q, delivered_at %v; want %q, none", req.Decision, req.DeliveredAt, tc.verdict)
			}
		})
	}
}

// TestRelayParentCheck (problem 7): before its ack, and before its timeout
// deny, the hook checks its parent is still the Claude Code that started it;
// reparented (another parent pid), its parent's pid reused (another start
// time) or its parent gone, it exits without acking or answering.
func TestRelayParentCheck(t *testing.T) {
	cases := []struct {
		name   string
		change func(p *fakeParentProc, ppid *atomic.Int64, agent hookParent)
	}{
		{"reparented", func(_ *fakeParentProc, ppid *atomic.Int64, _ hookParent) { ppid.Store(1) }},
		{"parent's pid reused", func(p *fakeParentProc, _ *atomic.Int64, agent hookParent) {
			p.Set(agent.PID, procfix.Alive(agent.Start+"1"))
		}},
		{"parent gone", func(p *fakeParentProc, _ *atomic.Int64, agent hookParent) { p.Set(agent.PID, procfix.Gone()) }},
	}
	for i, tc := range cases {
		for path, verdict := range map[string]string{"ack of a verdict": "allow", "timeout deny": ""} {
			t.Run(tc.name+"/"+path, func(t *testing.T) {
				id := "relay-parent-" + string(rune('a'+i)) + verdict
				st, _ := seedAgentRow(t, id, store.StateWorking)
				agent := agentParent(t, st, id)
				hc := relayHC(id, agent, 10*time.Second)
				var ppid atomic.Int64
				ppid.Store(int64(agent.PID))
				hc.ParentPID = func() int { return int(ppid.Load()) }
				parentProc := hc.ParentProc.(*fakeParentProc)
				atSleep(t, hc, 1, func() {
					if verdict != "" {
						decideOnly(t, st, id, verdict)
					}
					tc.change(parentProc, &ppid, agent)
				})

				out, _ := fireRelay(t, st, hc, relayPayload)

				if out != "" {
					t.Errorf("stdout = %q; want nothing", out)
				}
				if req := onlyRequest(t, st, id); req.Decision != verdict || !req.DeliveredAt.IsZero() {
					t.Errorf("request = decision %q, delivered_at %v; want %q, not acked", req.Decision, req.DeliveredAt, verdict)
				}
			})
		}
	}
}

// ackEntered is a relay store whose ack closes entered once, as it begins.
type ackEntered struct {
	*store.Store
	entered chan struct{}
	once    sync.Once
}

func (a *ackEntered) AckRelayDecision(instanceID, requestToken string, at time.Time, maxWait time.Duration, check func() error) (string, string, bool, error) {
	a.once.Do(func() { close(a.entered) })
	return a.Store.AckRelayDecision(instanceID, requestToken, at, maxWait, check)
}

// TestRelayParentCheckAfterTheLockWait (problem 7): the parent check runs once
// the ack has the write lock, so a Claude Code that exits while its relay
// hook's ack waits for a lock another process holds gets no ack once the lock
// is free: the hook exits with nothing on stdout, the verdict unacked.
func TestRelayParentCheckAfterTheLockWait(t *testing.T) {
	const id = "relay-parent-lock-wait"
	seeded, dbPath := seedAgentRow(t, id, store.StateWorking)
	agent := agentParent(t, seeded, id)
	hc := relayHC(id, agent, 10*time.Second)
	hc.Now, hc.Start = time.Now, time.Now()
	var ppid atomic.Int64
	ppid.Store(int64(agent.PID))
	hc.ParentPID = func() int { return int(ppid.Load()) }
	var release func()
	hc.Clock = &realSleepAt{fn: func() {
		decideOnly(t, seeded, id, "allow")
		release = apitest.HoldWriteLock(t, dbPath, time.Minute)
	}}
	st := &ackEntered{Store: seeded, entered: make(chan struct{})}
	go func() {
		<-st.entered
		time.Sleep(200 * time.Millisecond) // the ack waits for the held lock
		ppid.Store(1)                      // Claude Code exits: the hook is reparented
		release()
	}()

	out, took := fireRelay(t, st, hc, relayPayload)

	if out != "" {
		t.Errorf("stdout = %q; want nothing (the parent changed during the ack's wait)", out)
	}
	if took > 10*time.Second-2*time.Second {
		t.Errorf("the hook exited after %v; want before its reserve", took)
	}
	if req := onlyRequest(t, seeded, id); req.Decision != "allow" || !req.DeliveredAt.IsZero() {
		t.Errorf("request = decision %q, delivered_at %v; want allow, not acked", req.Decision, req.DeliveredAt)
	}
}

// TestDecideReportsTheLiveHooksAck (decision 1 A, rules 3, 14, 16), end to
// end on the real clock: with a relay hook polling (its identity this test
// process's, judged through the real /proc), decide records its verdict, the
// hook acks it at its next poll, and decide returns delivered within its 1 s
// wait; the hook then prints that verdict.
func TestDecideReportsTheLiveHooksAck(t *testing.T) {
	const id = "relay-e2e"
	st, dbPath := storefix.OpenTempStore(t)
	if _, err := apitest.SeedSpawn(dbPath, id, store.StateWorking, "", "on", "", false); err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	pc := probe.NewProcChecker()
	start, alive, known := pc.StartTime(os.Getpid())
	ns, nsKnown := probe.SelfPIDNamespace()
	if !alive || !known || !nsKnown {
		t.Fatalf("own identity unreadable: start %q alive %v known %v, pid namespace known %v", start, alive, known, nsKnown)
	}
	hc := relayHC(id, agentParent(t, st, id), 30*time.Second)
	hc.Now, hc.Clock = time.Now, hook.DefaultPollClock()
	hc.Self = func() store.ProcessIdentity {
		return store.ProcessIdentity{PID: os.Getpid(), Starttime: start, PIDNamespace: ns}
	}
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = hook.Handle(context.Background(), strings.NewReader(relayPayload), &out, st, hc, newSilentLogger())
	}()
	var token string
	for deadline := time.Now().Add(5 * time.Second); token == "" && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if rows, _ := st.PermissionRequestsForSpawn(id); len(rows) == 1 {
			token = rows[0].RequestToken
		}
	}
	if token == "" {
		t.Fatal("no request recorded within 5s")
	}

	began := time.Now()
	res, err := api.Decide(st, api.RelayView{Procs: pc, PIDNamespace: probe.SelfPIDNamespace, Now: time.Now, Window: 24 * time.Hour},
		api.DecideParams{ClaudeInstanceID: id, RequestToken: token, Decision: "allow"})
	took := time.Since(began)

	if err != nil || res.Delivery != api.DeliveryDelivered || res.HookAlive == nil || !*res.HookAlive {
		t.Errorf("decide = %+v, %v; want delivered with the hook alive", res, err)
	}
	if took > 1500*time.Millisecond {
		t.Errorf("decide took %v; want within its 1 s wait for the ack", took)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the relay hook did not return within 5s of decide")
	}
	if want := hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "") + "\n"; out.String() != want {
		t.Errorf("hook stdout = %q; want %q", out.String(), want)
	}
}
