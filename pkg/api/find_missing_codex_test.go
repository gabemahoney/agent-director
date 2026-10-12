package api_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// find-missing on Codex rows (SR-6.5, SR-7.1, SR-7.5, MC-2): a live Codex row whose agent process is proven gone
// and which holds an end report (its agent's SessionEnd) moves to ended and is in neither result list; every
// other gone row is marked missing. The cells sweep a fake store (the outcome tables); the records, the lists and
// the follow-ups run on a real store. Each test owns uuid row ids, so all run in parallel.

// cxEndReported is the end report (Unix ms) a cell's row holds, and cxPID the agent process its checker answers.
var cxEndReported = fmNow.Add(-time.Minute).UnixMilli()

const cxPID = 1700

// cxCell is one sweep of a single Codex or control row: what it is judged by and what it must get.
type cxCell struct {
	row        store.LiveSpawnIdentity
	pc         *procfix.Checker
	rec        *tmuxfix.Recorder
	ops        []string // the row's writes, in order
	ids, unver []string // the result lists
	newState   string   // the state of the row's one find-missing tick ("" = it has none that moves the row)
	reason     string   // that tick's reconciliation_reason
	noTmux     bool     // no tmux call is made
}

// runCxCell sweeps c's row alone and asserts its lists, writes, move tick and tmux calls, and that the move
// writes no ad.spawn.state_transition.
func runCxCell(t *testing.T, c cxCell) {
	t.Helper()
	id := c.row.InstanceID
	st := &fakeFindMissingStore{rows: []store.LiveSpawnIdentity{c.row}}
	if c.rec == nil {
		c.rec = tmuxfix.NewRecorder()
	}

	res, _, before := sweepFrom(t, st, c.pc, fmSweep{tmux: c.rec})

	assertLists(t, res, c.ids, c.unver)
	if got := st.ops(id); !equalStrings(got, c.ops) {
		t.Errorf("writes = %v; want %v", got, c.ops)
	}
	var moves []map[string]any
	for _, tk := range ticksSince(t, before, id) {
		if tk["new_state"] != nil {
			moves = append(moves, tk)
		}
	}
	switch {
	case c.newState == "" && len(moves) != 0:
		t.Errorf("ticks moving the row = %v; want none", moves)
	case c.newState != "" && len(moves) != 1:
		t.Fatalf("ticks moving the row = %v; want one to %s", moves, c.newState)
	case c.newState != "":
		assertAPITrailStr(t, moves[0], "new_state", c.newState)
		assertAPITrailStr(t, moves[0], "prior_state", c.row.State)
		assertAPITrailStr(t, moves[0], "reconciliation_reason", c.reason)
		assertAPITrailStr(t, moves[0], "source", "ad_find_missing")
	}
	if got := ptRecords(t, before, "ad.spawn.state_transition", id); len(got) != 0 {
		t.Errorf("ad.spawn.state_transition records = %v; want none", got)
	}
	if c.noTmux && len(c.rec.SocketCalls()) != 0 {
		t.Errorf("tmux calls = %+v; want none", c.rec.SocketCalls())
	}
}

// cxRow is a row of agent in state, past the default grace period when pending, judged by the process cxPID: a
// pending row records it as its pane process, any other as its SessionStart process. report sets its end report.
func cxRow(agent, state string, report bool) store.LiveSpawnIdentity {
	id := "cx-" + uuid.NewString()
	opts := []fmRowOpt{withAgent(agent), withLaunch(state, 0), withSessionStart(cxPID, fmStart)}
	if state == store.StatePending {
		opts = []fmRowOpt{withAgent(agent), withLaunch(state, fmNow.Add(-fmGrace-time.Second).UnixMilli()), withPane(cxPID, fmStart)}
	}
	if report {
		opts = append(opts, withEndReport(cxEndReported))
	}
	return liveRow(id, opts...)
}

// cxProc is a process column: what the checker answers for cxPID, and the tmux evidence behind it.
type cxProc struct {
	name string
	proc procfix.Process
	// want fills the outcome of a Codex row in state (pending = past grace) holding an end report or not.
	want func(c *cxCell, state string, report bool)
}

var cxProcs = []cxProc{
	{name: "alive", proc: procfix.Alive(fmStart), want: func(c *cxCell, state string, _ bool) {
		c.noTmux = true
		if state == store.StatePending {
			c.ops = []string{"unreported"} // alive, no hook since its launch: noted, still pending (b.kdf)
		}
	}},
	{name: "proven gone", proc: procfix.Gone(), want: func(c *cxCell, _ string, report bool) {
		c.noTmux, c.reason = true, "proc_absent"
		if report {
			c.ops, c.newState = []string{"ended"}, store.StateEnded
			return
		}
		c.ops, c.newState, c.ids = []string{"mark"}, store.StateMissing, []string{c.row.InstanceID}
	}},
	{name: "unjudgeable", proc: procfix.Unreadable(), want: func(c *cxCell, _ string, _ bool) {
		// As for a Claude row: the lookup decides, and tmux that cannot tell leaves the row unverified.
		c.rec, c.ops, c.unver = fmCantTell(), []string{"note"}, []string{c.row.InstanceID}
	}},
}

// TestFindMissingCodexOutcomeTable: on the recorded-identity path every live state x process x end report cell of
// a Codex row gets its outcome: gone with an end report ends the row (in neither list), gone without one marks it
// missing, alive leaves it (a pending row past grace is noted unreported) and unjudgeable is as for any row.
func TestFindMissingCodexOutcomeTable(t *testing.T) {
	t.Parallel()
	states := []string{store.StatePending, store.StateWaiting, store.StateWorking, store.StateCheckPermission}
	for _, state := range states {
		for _, p := range cxProcs {
			for _, report := range []bool{true, false} {
				name := fmt.Sprintf("%s/%s/end report %v", state, p.name, report)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					c := cxCell{row: cxRow(store.AgentCodex, state, report), pc: procfix.New()}
					c.pc.Set(cxPID, p.proc)
					p.want(&c, state, report)
					runCxCell(t, c)
				})
			}
		}
	}
}

// cxAdoptedCell is a Codex row recording no pane (a lost create reply) whose adopted pane's process (the token
// pane its own session lists) is gone.
func cxAdoptedCell(agent string, report bool) cxCell {
	opts := []fmRowOpt{withAgent(agent), withLaunch(store.StatePending, fmNow.Add(-fmGrace-time.Second).UnixMilli()), withServer()}
	if report {
		opts = append(opts, withEndReport(cxEndReported))
	}
	r := liveRow("cx-"+uuid.NewString(), opts...)
	pc := mxChecker(mxAdoptPID, procfix.Gone())
	return cxCell{row: r, pc: pc, rec: mxRec(mxSession(r.TmuxSessionName, mxOwn(r), mxTokenPane)), reason: "proc_absent"}
}

// TestFindMissingCodexOtherPaths: the adopted-pane path ends a Codex row holding an end report and marks one
// without it missing; tmux evidence alone marks the row missing whatever its end report; a Claude row with an end
// report column set (a control: only a Codex row ends) is marked missing.
func TestFindMissingCodexOtherPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cell func() cxCell
	}{
		{"adopted pane gone, end report", func() cxCell {
			c := cxAdoptedCell(store.AgentCodex, true)
			c.ops, c.newState = []string{"adopt", "ended"}, store.StateEnded
			return c
		}},
		{"adopted pane gone, no end report", func() cxCell {
			c := cxAdoptedCell(store.AgentCodex, false)
			c.ops, c.newState, c.ids = []string{"adopt", "mark"}, store.StateMissing, []string{c.row.InstanceID}
			return c
		}},
		{"tmux session absent, end report", func() cxCell {
			c := cxCell{row: cxRow(store.AgentCodex, store.StateWorking, true), pc: procfix.New(), reason: "tmux_absent"}
			c.pc.Set(cxPID, procfix.Unreadable())
			c.ops, c.newState, c.ids = []string{"mark"}, store.StateMissing, []string{c.row.InstanceID}
			return c
		}},
		{"name held by an unlabelled session, end report", func() cxCell {
			c := cxCell{row: cxRow(store.AgentCodex, store.StateWorking, true), pc: procfix.New(), reason: "tmux_name_held"}
			c.pc.Set(cxPID, procfix.Unreadable())
			c.rec = mxHeldBy(mxRecorded, mxNoLabel)(c.row, nil)
			c.ops, c.newState, c.ids = []string{"mark"}, store.StateMissing, []string{c.row.InstanceID}
			return c
		}},
		{"claude row gone, end report column set", func() cxCell {
			c := cxCell{row: cxRow(store.AgentClaude, store.StateWorking, true), pc: procfix.New(), reason: "proc_absent", noTmux: true}
			c.pc.Set(cxPID, procfix.Gone())
			c.ops, c.newState, c.ids = []string{"mark"}, store.StateMissing, []string{c.row.InstanceID}
			return c
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCxCell(t, tc.cell())
		})
	}
}

// cxSeed seeds Codex (or, with agent claude, Claude) row id at dbPath in state, running as process cxPID, holding
// an end report when report, and returns it open (creating the store when create).
func cxSeed(t *testing.T, dbPath string, create bool, id, agent, state string, report bool, opts ...apitest.SpawnOption) {
	t.Helper()
	cxSeedIn(t, dbPath, create, id, agent, state, "/tmp", "", report, opts...)
}

// cxSeedIn is cxSeed for a row in cwd with session id sessionID.
func cxSeedIn(t *testing.T, dbPath string, create bool, id, agent, state, cwd, sessionID string, report bool, opts ...apitest.SpawnOption) {
	t.Helper()
	li := store.LaunchIdentity{Token: trailToken, Socket: apitest.TestSocket, PaneID: apitest.TestPaneID, PanePID: cxPID, PaneStarttime: fmStart}
	all := append(agentRowOpts(agent, apitest.WithLaunchIdentity(li), apitest.WithPID(cxPID), apitest.WithProcStarttime(fmStart)), opts...)
	if report {
		all = append(all, apitest.WithEndReportedAt(cxEndReported))
	}
	if _, err := apitest.SeedSpawn(dbPath, id, state, cwd, "on", sessionID, create, all...); err != nil {
		t.Fatalf("SeedSpawn %s: %v", id, err)
	}
}

// moveWant is what assertMoveRecords expects of a row's move off its live state: the state it left and the one it
// reached, the decision_reason of the denies, the proc_absent tick's reason and the tokens of the requests it closed.
type moveWant struct {
	prior, now string
	denyReason string
	reason     string
	tokens     []string // in request-id order
}

// assertMoveRecords fails unless the trail after mark holds, for id, exactly SR-6.5's records of a move: per closed
// request in request-id order one ad.row_mutation.committed (a deny, writer find_missing, decision_reason
// denyReason) then one permission_orphan_closeout tick; then the row's one find-missing tick (prior state, new
// state, reason, source ad_find_missing); and nothing else (no ad.spawn.state_transition, no other event).
func assertMoveRecords(t *testing.T, mark int, id string, w moveWant) {
	t.Helper()
	var got, want []string
	var lines []map[string]any
	for _, l := range readAPITrailLines(t)[mark:] {
		if l["instance_id"] != id {
			continue
		}
		lines = append(lines, l)
		got = append(got, fmt.Sprintf("%v/%v", l["event"], l["request_token"]))
	}
	for _, tok := range w.tokens {
		want = append(want, "ad.row_mutation.committed/"+tok, "ad.find_missing.tick/"+tok)
	}
	want = append(want, "ad.find_missing.tick/<nil>")
	if !slices.Equal(got, want) {
		t.Fatalf("trail records of %s = %v; want %v", id, got, want)
	}
	for _, l := range lines {
		switch {
		case l["event"] == "ad.row_mutation.committed":
			for k, v := range map[string]string{"source": "ad_store", "writer_process": store.WriterProcessFindMissing,
				"mutation_kind": "update", "decision": "deny", "decision_reason": w.denyReason} {
				assertAPITrailStr(t, l, k, v)
			}
		case l["request_token"] != nil:
			assertAPITrailStr(t, l, "reconciliation_reason", "permission_orphan_closeout")
			assertAPITrailStr(t, l, "source", "ad_find_missing")
		default:
			assertAPITrailStr(t, l, "prior_state", w.prior)
			assertAPITrailStr(t, l, "new_state", w.now)
			assertAPITrailStr(t, l, "reconciliation_reason", w.reason)
			assertAPITrailStr(t, l, "source", "ad_find_missing")
		}
	}
}

// TestFindMissingCodexEndedRecords: the move of a gone Codex row holding an end report closes its requests (denied
// ended) and leaves exactly SR-6.5's records, and the row reads ended at once through status, get and list; decide
// on a request the move denied is ErrNoOpenPermissionRequest. Without an end report the row is marked missing and
// its requests are denied find_missing, as before.
func TestFindMissingCodexEndedRecords(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		state    string
		requests int
		report   bool
		want     moveWant
	}{
		{name: "no open request", state: store.StateWorking, requests: 0, report: true,
			want: moveWant{prior: store.StateWorking, now: store.StateEnded, denyReason: store.DecisionReasonEnded}},
		{name: "one open request", state: store.StateCheckPermission, requests: 1, report: true,
			want: moveWant{prior: store.StateCheckPermission, now: store.StateEnded, denyReason: store.DecisionReasonEnded}},
		{name: "three open requests", state: store.StateCheckPermission, requests: 3, report: true,
			want: moveWant{prior: store.StateCheckPermission, now: store.StateEnded, denyReason: store.DecisionReasonEnded}},
		{name: "two open requests, no end report", state: store.StateCheckPermission, requests: 2, report: false,
			want: moveWant{prior: store.StateCheckPermission, now: store.StateMissing, denyReason: store.DecisionReasonFindMissing}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := "cx-" + uuid.NewString()
			s, dbPath := openSeeded(t, func(t *testing.T, p string, create bool, _ string) {
				cxSeed(t, p, create, id, store.AgentCodex, tc.state, tc.report)
			})
			var tokens []string
			for range tc.requests {
				perm, err := apitest.SeedPermissionRequest(dbPath, id, "Bash")
				if err != nil {
					t.Fatalf("SeedPermissionRequest: %v", err)
				}
				tokens = append(tokens, perm.RequestToken)
			}
			pc := procfix.New()
			pc.Set(cxPID, procfix.Gone())
			mark := trailLen(t)

			res := mustFindMissing(t, s, pc)

			if tc.report {
				assertLists(t, res, nil, nil)
			} else {
				assertLists(t, res, []string{id}, nil)
			}
			w := tc.want
			w.reason, w.tokens = "proc_absent", tokens
			assertMoveRecords(t, mark, id, w)
			cols := readOne(t, dbPath, id)
			if cols.State != tc.want.now || cols.EndedAt == nil {
				t.Errorf("state, ended_at = %v, %v; want %s, set", cols.State, cols.EndedAt, tc.want.now)
			}
			for _, tok := range tokens {
				if pr, err := s.GetPermissionRequest(id, tok); err != nil || pr.Decision != "deny" || pr.DecisionReason != tc.want.denyReason {
					t.Errorf("request %s = %+v, %v; want denied, reason %s", tok, pr, err, tc.want.denyReason)
				}
			}
			if !tc.report {
				return
			}
			assertReadsEnded(t, s, id)
			if len(tokens) > 0 {
				_, err := api.Decide(s, api.RelayView{Window: 24 * time.Hour}, api.DecideParams{InstanceID: id, RequestToken: tokens[0], Decision: "allow"})
				if !errors.Is(err, store.ErrNoOpenPermissionRequest) {
					t.Errorf("decide on a request the move denied = %v; want ErrNoOpenPermissionRequest", err)
				}
				if pr, err := s.GetPermissionRequest(id, tokens[0]); err != nil || pr.Decision != "deny" {
					t.Errorf("request after decide = %+v, %v; want still denied", pr, err)
				}
			}
		})
	}
}

// readOne reads id's row raw through apitest.ReadSpawnColumns.
func readOne(t *testing.T, dbPath, id string) apitest.SpawnColumns {
	t.Helper()
	c, err := apitest.ReadSpawnColumns(dbPath, id)
	if err != nil {
		t.Fatalf("ReadSpawnColumns(%s): %v", id, err)
	}
	return c
}

// assertReadsEnded fails unless status, get and list all give id's state as ended.
func assertReadsEnded(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if st, err := api.Status(s, id); err != nil || st.State != store.StateEnded {
		t.Errorf("status = %+v, %v; want ended", st, err)
	}
	if row, err := api.Get(s, api.RelayView{}, id); err != nil || row.State != store.StateEnded {
		t.Errorf("get state = %q, %v; want ended", row.State, err)
	}
	res, err := api.List(s, api.RelayView{}, api.ListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range res.Spawns {
		if r.InstanceID == id {
			if r.State != store.StateEnded {
				t.Errorf("list state = %q; want ended", r.State)
			}
			return
		}
	}
	t.Errorf("list has no row %s", id)
}

// TestFindMissingCodexRowNotHealed: a Codex row with a session id and no transcript path is never healed, though a
// file sits at the Claude-composed path; a Claude row beside it is healed as before.
func TestFindMissingCodexRowNotHealed(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	t.Setenv("HOME", t.TempDir())
	const cwd = "/tmp/proj"
	dbPath := t.TempDir() + "/state.db"
	sessions := map[string]string{store.AgentClaude: "session-heal-claude", store.AgentCodex: "session-heal-codex"}
	paths := map[string]string{}
	for i, agent := range []string{store.AgentClaude, store.AgentCodex} {
		cxSeedIn(t, dbPath, i == 0, "heal-"+agent, agent, store.StateWorking, cwd, sessions[agent], false)
		paths[agent] = apitest.SeedJsonl(t, cwd, sessions[agent])
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	pc := procfix.New()
	pc.Set(cxPID, procfix.Alive(fmStart))

	res := mustFindMissing(t, s, pc)

	assertLists(t, res, nil, nil)
	if got := readOne(t, dbPath, "heal-"+store.AgentClaude).JSONLPath; got != paths[store.AgentClaude] {
		t.Errorf("claude jsonl_path = %v; want it healed to %s", got, paths[store.AgentClaude])
	}
	if got := readOne(t, dbPath, "heal-"+store.AgentCodex).JSONLPath; got != nil {
		t.Errorf("codex jsonl_path = %v; want NULL (never healed)", got)
	}
}
