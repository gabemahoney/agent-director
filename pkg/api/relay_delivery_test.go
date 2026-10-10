package api_test

// relay_delivery_test.go — a permission request's delivery facts (b.146 rules
// 5, 14 and 15) as get-permission, get and list report them: rule 14's
// liveness table, the check-before-read order, readers that never wait for
// the write lock (problem 4) and hook_gone_at, the facts on the wire, and
// requests recorded before schema v7, judged by time. Seeds: a relay-on row in
// check_permission whose request a relay hook recorded
// (storefix.SeedRelayRequest), a process fake and a clock to judge it with.

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// relayNS is the pid namespace a relayEnv's hook and reader share by default.
const relayNS = "pid:[4026531836]"

// relayEnvHook is the relay hook process a relayEnv's request records.
var relayEnvHook = store.ProcessIdentity{PID: 54321, Starttime: storefix.SeedPaneStarttime, PIDNamespace: relayNS}

// relayEnv is apitest's decide fixture (id-d-1, relay on, check_permission)
// with request A recorded by a relay hook with identity hook and a settle
// instant an hour out, judged with pc, the reader's pid namespace ns ("" =
// unreadable) and the clock now.
type relayEnv struct {
	s       *store.Store
	dbPath  string
	id      string
	pc      *procfix.Checker
	ns      string
	now     time.Time
	settled time.Time
}

// newRelayEnv seeds a relayEnv whose hook recorded identity hook and runs
// alive with that identity.
func newRelayEnv(t *testing.T, hook store.ProcessIdentity) *relayEnv {
	t.Helper()
	s, dbPath := apitest.SeedDecideFixture(t, "on")
	now := time.Now().UTC().Truncate(time.Millisecond)
	e := &relayEnv{s: s, dbPath: dbPath, id: "id-d-1", pc: procfix.New(), ns: relayNS, now: now, settled: now.Add(time.Hour)}
	storefix.SeedRelayRequest(t, s, e.id, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
		ToolInput: `{"cmd":"ls"}`, ToolUseID: "toolu_01", Hook: hook, SettledAt: e.settled})
	if hook.PID > 0 {
		e.pc.Set(hook.PID, procfix.Alive(hook.Starttime))
	}
	return e
}

// view is e's RelayView: its fake, namespace and clock, a one-hour window.
func (e *relayEnv) view() api.RelayView {
	return api.RelayView{Procs: e.pc, PIDNamespace: func() (string, bool) { return e.ns, e.ns != "" },
		Now: func() time.Time { return e.now }, Window: time.Hour}
}

// request reads request A from the store.
func (e *relayEnv) request(t *testing.T) store.PermissionRow {
	t.Helper()
	pr, err := e.s.GetPermissionRequest(e.id, storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	return pr
}

// getPermission runs get-permission on request A through e's view.
func (e *relayEnv) getPermission(t *testing.T, s api.GetPermissionStore) api.GetPermissionResult {
	t.Helper()
	res, err := api.GetPermission(s, e.view(), api.GetPermissionParams{RequestToken: storefix.TestRequestTokenA})
	if err != nil {
		t.Fatalf("GetPermission: %v", err)
	}
	return res
}

// listed returns request A's element of get's and of list's permission_requests
// (nil when the row lists none).
func (e *relayEnv) listed(t *testing.T) (fromGet, fromList *api.PermissionRequestInfo) {
	t.Helper()
	row, err := api.Get(e.s, e.view(), e.id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	res, err := api.List(e.s, e.view(), api.ListParams{})
	if err != nil || len(res.Spawns) != 1 {
		t.Fatalf("List = %+v, %v; want the one row", res.Spawns, err)
	}
	if len(row.PermissionRequests) == 1 {
		fromGet = &row.PermissionRequests[0]
	}
	if reqs := res.Spawns[0].PermissionRequests; len(reqs) == 1 {
		fromList = &reqs[0]
	}
	return fromGet, fromList
}

// aliveIs renders a hook_alive value.
func aliveIs(b *bool) string {
	if b == nil {
		return "null"
	}
	if *b {
		return "true"
	}
	return "false"
}

// TestDeliveryRule14Table (b.146 rules 5, 14, 15): a reader's verdict on the
// recorded relay hook, and the delivery it derives, row by row. Another or an
// unreadable pid namespace, an unreadable /proc or no identity on record is
// "can't tell": not_confirmed until settled_at (confirm_by), fallen_back
// after; no such process or another start time is gone: fallen_back at once
// (the start-time reader answers a zombie as no such process, internal/probe's
// TestLinuxStartTimeReader); the same process alive is not_confirmed; an ack is
// delivered.
// get-permission, get and list report the same facts; a reader that finds a
// request fallen back records hook_gone_at.
func TestDeliveryRule14Table(t *testing.T) {
	t.Parallel()
	other := relayEnvHook.Starttime + "1"
	yes, no := "true", "false"
	cases := []struct {
		name  string
		hook  store.ProcessIdentity // recorded
		proc  *procfix.Process      // what the hook's pid answers; nil: alive with its start time
		ns    string                // the reader's pid namespace
		past  bool                  // the clock is at settled_at
		acked bool
		want  string
		alive string
	}{
		{"alive", relayEnvHook, nil, relayNS, false, false, api.DeliveryNotConfirmed, yes},
		{"no such process", relayEnvHook, ptr(procfix.Gone()), relayNS, false, false, api.DeliveryFallenBack, no},
		{"another start time", relayEnvHook, ptr(procfix.Alive(other)), relayNS, false, false, api.DeliveryFallenBack, no},
		{"another pid namespace, before settled_at", relayEnvHook, nil, "pid:[4026532000]", false, false, api.DeliveryNotConfirmed, "null"},
		{"another pid namespace, at settled_at", relayEnvHook, nil, "pid:[4026532000]", true, false, api.DeliveryFallenBack, "null"},
		{"reader's namespace unreadable, before settled_at", relayEnvHook, nil, "", false, false, api.DeliveryNotConfirmed, "null"},
		{"reader's namespace unreadable, at settled_at", relayEnvHook, nil, "", true, false, api.DeliveryFallenBack, "null"},
		{"/proc unreadable, before settled_at", relayEnvHook, ptr(procfix.Unreadable()), relayNS, false, false, api.DeliveryNotConfirmed, "null"},
		{"/proc unreadable, at settled_at", relayEnvHook, ptr(procfix.Unreadable()), relayNS, true, false, api.DeliveryFallenBack, "null"},
		{"no identity recorded, before settled_at", store.ProcessIdentity{}, nil, relayNS, false, false, api.DeliveryNotConfirmed, "null"},
		{"no identity recorded, at settled_at", store.ProcessIdentity{}, nil, relayNS, true, false, api.DeliveryFallenBack, "null"},
		{"acked, hook gone since", relayEnvHook, ptr(procfix.Gone()), relayNS, false, true, api.DeliveryDelivered, no},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newRelayEnv(t, tc.hook)
			if tc.acked { // decide's verdict, acked by the live hook, which then exits
				if _, err := api.Decide(e.s, e.view(), api.DecideParams{ClaudeInstanceID: e.id, RequestToken: storefix.TestRequestTokenA,
					Decision: "allow", MaxWaitMs: ptr(int64(0))}); err != nil {
					t.Fatalf("decide: %v", err)
				}
				if _, _, ok, err := e.s.AckRelayDecision(e.id, storefix.TestRequestTokenA, e.now, store.DefaultLockWait, nil); err != nil || !ok {
					t.Fatalf("ack = %v, %v", ok, err)
				}
			}
			e.ns = tc.ns
			if tc.proc != nil {
				e.pc.Set(relayEnvHook.PID, *tc.proc)
			}
			if tc.past {
				e.now = e.settled
			}

			fromGet, fromList := e.listed(t)
			res := e.getPermission(t, e.s)

			if res.Delivery != tc.want || aliveIs(res.HookAlive) != tc.alive || !res.ConfirmBy.Equal(e.settled) ||
				res.ToolUseID == nil || *res.ToolUseID != "toolu_01" {
				t.Errorf("get-permission = delivery %q, hook_alive %s, confirm_by %v, tool_use_id %v; want %q, %s, %v, toolu_01",
					res.Delivery, aliveIs(res.HookAlive), res.ConfirmBy, res.ToolUseID, tc.want, tc.alive, e.settled)
			}
			if gone := res.HookGoneAt != nil; gone != (tc.want == api.DeliveryFallenBack) || gone && !res.HookGoneAt.Equal(e.now) {
				t.Errorf("hook_gone_at = %v; want %v only when fallen back", res.HookGoneAt, e.now)
			}
			for reader, got := range map[string]*api.PermissionRequestInfo{"get": fromGet, "list": fromList} {
				switch {
				case tc.acked && got != nil:
					t.Errorf("%s lists the acked request %+v; want none (no request awaits an answer)", reader, got)
				case !tc.acked && (got == nil || got.Delivery != tc.want || aliveIs(got.HookAlive) != tc.alive || !got.ConfirmBy.Equal(e.settled)):
					t.Errorf("%s's request = %+v; want delivery %q, hook_alive %s, confirm_by %v", reader, got, tc.want, tc.alive, e.settled)
				}
			}
		})
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// getPermissionReadsTwice is a GetPermissionStore that runs between once,
// right after its read number at of the request (0: the first).
type getPermissionReadsTwice struct {
	*store.Store
	between func()
	reads   *int
	at      int
}

func (g getPermissionReadsTwice) GetPermissionRequestByToken(token string) (store.PermissionRow, error) {
	pr, err := g.Store.GetPermissionRequestByToken(token)
	if *g.reads++; *g.reads == max(g.at, 1) {
		g.between()
	}
	return pr, err
}

// TestReaderJudgesTheHookBeforeItsRecord (b.146 rule 5's note): a hook that
// acks its verdict and exits right after a reader's first read is never
// reported fallen back: the reader judges the hook, then reads the record it
// reports (check before read), so it sees the ack.
func TestReaderJudgesTheHookBeforeItsRecord(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	if _, err := api.DecideWithSleep(e.s, e.view(), func(time.Duration) {}, api.DecideParams{ClaudeInstanceID: e.id,
		RequestToken: storefix.TestRequestTokenA, Decision: "allow", MaxWaitMs: ptr(int64(0))}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	reads := 0
	res := e.getPermission(t, getPermissionReadsTwice{Store: e.s, reads: &reads, between: func() {
		if _, _, ok, err := e.s.AckRelayDecision(e.id, storefix.TestRequestTokenA, e.now, store.DefaultLockWait, nil); err != nil || !ok {
			t.Errorf("the hook's ack = %v, %v", ok, err)
		}
		e.pc.Set(relayEnvHook.PID, procfix.Gone())
	}})

	if res.Delivery != api.DeliveryDelivered || res.HookGoneAt != nil {
		t.Errorf("get-permission = delivery %q, hook_gone_at %v; want delivered, none", res.Delivery, res.HookGoneAt)
	}
}

// TestReadersNeverWaitForTheWriteLock (b.146 problem 4 and the b.146
// property): with another process holding the write lock and the store's
// busy timeout at its largest, get-permission, get and list return at once
// with the request fallen back and no hook_gone_at, and write none; once the
// lock is free a reader records hook_gone_at, and later readers report that
// first instant.
func TestReadersNeverWaitForTheWriteLock(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	slow, err := store.OpenWithBusyTimeout(e.dbPath, config.MaxStoreBusyTimeoutMs)
	if err != nil {
		t.Fatalf("OpenWithBusyTimeout: %v", err)
	}
	defer slow.Close()
	release := apitest.HoldWriteLock(t, e.dbPath, time.Minute)

	readers := map[string]func() (*time.Time, string){
		"get-permission": func() (*time.Time, string) { r := e.getPermission(t, slow); return r.HookGoneAt, r.Delivery },
		"get": func() (*time.Time, string) {
			row, err := api.Get(slow, e.view(), e.id)
			if err != nil || len(row.PermissionRequests) != 1 {
				t.Fatalf("Get = %+v, %v", row.PermissionRequests, err)
			}
			return row.PermissionRequests[0].HookGoneAt, row.PermissionRequests[0].Delivery
		},
		"list": func() (*time.Time, string) {
			res, err := api.List(slow, e.view(), api.ListParams{})
			if err != nil || len(res.Spawns) != 1 || len(res.Spawns[0].PermissionRequests) != 1 {
				t.Fatalf("List = %+v, %v", res.Spawns, err)
			}
			r := res.Spawns[0].PermissionRequests[0]
			return r.HookGoneAt, r.Delivery
		},
	}
	for name, read := range readers {
		start := time.Now()
		goneAt, delivery := read()
		if took := time.Since(start); took > time.Second {
			t.Errorf("%s took %v under a held write lock; want it at once", name, took)
		}
		if delivery != api.DeliveryFallenBack || goneAt != nil {
			t.Errorf("%s = delivery %q, hook_gone_at %v; want fallen_back with none recorded", name, delivery, goneAt)
		}
	}
	release()
	if got := e.request(t).HookGoneAt; !got.IsZero() {
		t.Fatalf("hook_gone_at = %v after readers under a held lock; want none written", got)
	}

	first := e.now
	if got := e.getPermission(t, slow).HookGoneAt; got == nil || !got.Equal(first) {
		t.Errorf("get-permission with the lock free: hook_gone_at = %v; want %v recorded", got, first)
	}
	e.now = e.now.Add(time.Minute)
	if fromGet, fromList := e.listed(t); fromGet == nil || fromList == nil || fromGet.HookGoneAt == nil ||
		!fromGet.HookGoneAt.Equal(first) || fromList.HookGoneAt == nil || !fromList.HookGoneAt.Equal(first) {
		t.Errorf("get / list later = %+v / %+v; want hook_gone_at %v, the first reader's", fromGet, fromList, first)
	}
}

// TestReaderSkipsHookGoneWhileTheConnectionIsHeld (b.146 problem 4): when
// another call of this process holds the store's one connection once
// get-permission has read the request, the reader does not wait for it: it
// returns at once with the request fallen back and no hook_gone_at, having
// written none; once the connection is free a reader records it.
func TestReaderSkipsHookGoneWhileTheConnectionIsHeld(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, relayEnvHook)
	e.pc.Set(relayEnvHook.PID, procfix.Gone())
	slow, err := store.OpenWithBusyTimeout(e.dbPath, config.MaxStoreBusyTimeoutMs)
	if err != nil {
		t.Fatalf("OpenWithBusyTimeout: %v", err)
	}
	defer slow.Close()
	var (
		release func()
		heldAt  time.Time
		reads   int
	)
	res := e.getPermission(t, getPermissionReadsTwice{Store: slow, reads: &reads, at: 2, between: func() {
		release = holdStoreConnection(t, slow, apitest.HoldWriteLock(t, e.dbPath, time.Minute))
		heldAt = time.Now()
	}})
	took := time.Since(heldAt)
	release()

	if took > time.Second || res.Delivery != api.DeliveryFallenBack || res.HookGoneAt != nil {
		t.Errorf("get-permission = delivery %q, hook_gone_at %v, %v after the connection was taken; want fallen_back, none, at once",
			res.Delivery, res.HookGoneAt, took)
	}
	if got := e.request(t).HookGoneAt; !got.IsZero() {
		t.Fatalf("hook_gone_at = %v; want none written while the connection was held", got)
	}
	if got := e.getPermission(t, slow).HookGoneAt; got == nil || !got.Equal(e.now) {
		t.Errorf("get-permission with the connection free: hook_gone_at = %v; want %v recorded", got, e.now)
	}
}

// TestPreV7RequestsFallBackByTime (b.146 rule 5's compatibility clause): a
// request recorded before schema v7 (no hook identity, no settle instant, as
// the v6→v7 migration leaves one: internal/store's
// TestV7MigrationKeepsV6Rows) is judged by its created_at and the relay window:
// not_confirmed until created_at + window + 2 s (its confirm_by), then
// fallen_back while undecided and delivered once decided; a close's deny
// (find-missing's mark, the Spawn's SessionEnd) counts as no verdict, so it
// reads fallen_back too; its hook is never checked (hook_alive null).
func TestPreV7RequestsFallBackByTime(t *testing.T) {
	t.Parallel()
	decide := func(t *testing.T, s *store.Store, _ string) {
		if ok, err := s.DecidePermissionRequest("id-d-1", storefix.TestRequestTokenA, "allow", "", store.WriterProcessDecide); err != nil || !ok {
			t.Fatalf("decide = %v, %v", ok, err)
		}
	}
	end := func(t *testing.T, s *store.Store, dbPath string) {
		if err := seedAgentState(s, dbPath, "id-d-1", store.StateEnded); err != nil {
			t.Fatalf("SessionEnd: %v", err)
		}
	}
	mark := func(t *testing.T, s *store.Store, _ string) {
		sp, err := s.GetSpawn("id-d-1")
		if err != nil {
			t.Fatalf("GetSpawn: %v", err)
		}
		if _, res, err := s.MarkMissingIfSameLife("id-d-1", sp.Snapshot); err != nil || res != store.CondApplied {
			t.Fatalf("mark = %v, %v", res, err)
		}
	}
	for _, tc := range []struct {
		name   string
		after  func(t *testing.T, s *store.Store, dbPath string) // after the request is recorded; nil none
		at     time.Duration                                     // after confirm_by
		want   string
		reason string // request A's decision_reason then
	}{
		{"undecided, before confirm_by", nil, -time.Millisecond, api.DeliveryNotConfirmed, ""},
		{"undecided, at confirm_by", nil, 0, api.DeliveryFallenBack, ""},
		{"decided, before confirm_by", decide, -time.Millisecond, api.DeliveryNotConfirmed, ""},
		{"decided, at confirm_by", decide, 0, api.DeliveryDelivered, ""},
		{"the SessionEnd's deny, at confirm_by", end, 0, api.DeliveryFallenBack, store.DecisionReasonEnded},
		{"find-missing's deny, at confirm_by", mark, 0, api.DeliveryFallenBack, store.DecisionReasonFindMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, dbPath := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			pr, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
			if err != nil || !pr.PreV7() {
				t.Fatalf("seeded request = %+v, %v; want one recorded before v7", pr, err)
			}
			if tc.after != nil {
				tc.after(t, s, dbPath)
			}
			if got, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA); err != nil || got.DecisionReason != tc.reason {
				t.Fatalf("request A's decision_reason = %q, %v; want %q", got.DecisionReason, err, tc.reason)
			}
			const window = time.Hour
			confirmBy := pr.CreatedAt.Add(window + api.RelayKillSafetyMargin + api.CreatedAtResolution)
			now := confirmBy.Add(tc.at)
			view := api.RelayView{Procs: procfix.New(), PIDNamespace: func() (string, bool) { return relayNS, true },
				Now: func() time.Time { return now }, Window: window}

			res, err := api.GetPermission(s, view, api.GetPermissionParams{RequestToken: storefix.TestRequestTokenA})

			if err != nil || res.Delivery != tc.want || res.HookAlive != nil || !res.ConfirmBy.Equal(confirmBy) {
				t.Errorf("get-permission = delivery %q, hook_alive %s, confirm_by %v, %v; want %q, null, %v",
					res.Delivery, aliveIs(res.HookAlive), res.ConfirmBy, err, tc.want, confirmBy)
			}
		})
	}
}

// TestDeliveryFactsOnTheWire (b.146 rule 15): get-permission's result and each
// element of get's and list's permission_requests carry every delivery fact
// as a JSON key, null when unset, never omitted; pane_answer is none and
// pane_as null before any pane answer (step 2b).
func TestDeliveryFactsOnTheWire(t *testing.T) {
	t.Parallel()
	e := newRelayEnv(t, store.ProcessIdentity{})
	fromGet, fromList := e.listed(t)
	facts := []string{"delivery", "confirm_by", "hook_alive", "hook_gone_at", "attempted_decision", "attempted_at", "tool_use_id",
		"pane_answer", "pane_as"}
	for name, v := range map[string]any{"get-permission": e.getPermission(t, e.s), "get's request": fromGet, "list's request": fromList} {
		var m map[string]any
		if err := json.Unmarshal([]byte(jsonOf(t, v)), &m); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		var missing []string
		for _, k := range facts {
			if _, ok := m[k]; !ok {
				missing = append(missing, k)
			}
		}
		sort.Strings(missing)
		if len(missing) != 0 || m["delivery"] != api.DeliveryNotConfirmed || m["hook_alive"] != nil || m["attempted_decision"] != nil ||
			m["pane_answer"] != "none" || m["pane_as"] != nil {
			t.Errorf("%s JSON = %v; missing %v; want every delivery fact, hook_alive, attempted_decision and pane_as null, pane_answer none",
				name, m, missing)
		}
	}
}

// TestClientJudgesHooksInItsOwnNamespace: the Client judges a recorded relay
// hook with its own start-time reader in its own pid namespace; another
// namespace, or one it cannot read, is "can't tell".
func TestClientJudgesHooksInItsOwnNamespace(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		ns    func() (string, bool)
		want  string
		alive string
	}{
		{"same namespace, hook gone", func() (string, bool) { return relayNS, true }, api.DeliveryFallenBack, "false"},
		{"another namespace", func() (string, bool) { return "pid:[4026532000]", true }, api.DeliveryNotConfirmed, "null"},
		{"namespace unreadable", func() (string, bool) { return "", false }, api.DeliveryNotConfirmed, "null"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newKillEnv(t)
			r := seedRelayRow(t, e)
			storefix.SeedRelayRequest(t, e.st, r.ID, store.RelayRequest{RequestToken: storefix.TestRequestTokenA, ToolName: "Bash",
				ToolInput: `{}`, Hook: relayEnvHook, SettledAt: e.clock.Now().Add(time.Hour)})
			c, _ := e.client(t)
			api.SetSelfPIDNSForTest(c, tc.ns) // e.pc lists no process: the hook's pid is gone

			res, err := c.GetPermission(api.GetPermissionParams{RequestToken: storefix.TestRequestTokenA})

			if err != nil || res.Delivery != tc.want || aliveIs(res.HookAlive) != tc.alive {
				t.Errorf("Client.GetPermission = delivery %q, hook_alive %s, %v; want %q, %s", res.Delivery, aliveIs(res.HookAlive), err, tc.want, tc.alive)
			}
		})
	}
}
