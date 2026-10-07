package tmux_test

import (
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Tests of agent-process selection and the shared recorded-process judgement
// (SR-3.8; LFR M8, C1). Selection signals nothing: pid_mismatch is retired.

// The default recorded pids and start times of a row's two identities.
const (
	agentSSPID      = 4100
	agentPanePID    = 4200
	agentStart      = procstarttimefix.LinuxProcStarttime
	agentOtherStart = procstarttimefix.DarwinProcStarttime
)

// agentIdentOpt overrides one field of an identity.
type agentIdentOpt func(*tmux.ProcIdentity)

// identPID and identStart override the pid and the start time.
func identPID(pid int) agentIdentOpt    { return func(p *tmux.ProcIdentity) { p.PID = pid } }
func identStart(s string) agentIdentOpt { return func(p *tmux.ProcIdentity) { p.Starttime = s } }

// agentIdent is {pid, agentStart} with opts applied.
func agentIdent(pid int, opts ...agentIdentOpt) tmux.ProcIdentity {
	id := tmux.ProcIdentity{PID: pid, Starttime: agentStart}
	for _, o := range opts {
		o(&id)
	}
	return id
}

// ssIdent and paneIdent are the row's SessionStart and pane identities.
func ssIdent(opts ...agentIdentOpt) tmux.ProcIdentity   { return agentIdent(agentSSPID, opts...) }
func paneIdent(opts ...agentIdentOpt) tmux.ProcIdentity { return agentIdent(agentPanePID, opts...) }

// identNone is an identity with no pid and no start time.
var identNone = tmux.ProcIdentity{}

// selSS and selPane are the selections of the SessionStart and pane identity.
func selSS(id tmux.ProcIdentity) tmux.AgentProcess {
	return tmux.AgentProcess{Source: tmux.AgentSessionStart, Identity: id}
}
func selPane(id tmux.ProcIdentity) tmux.AgentProcess {
	return tmux.AgentProcess{Source: tmux.AgentPane, Identity: id}
}

// The selection is compared whole, so it carries only a source and an
// identity: no disagree signal of any kind when the identities differ.
func TestSelectAgentProcess(t *testing.T) {
	cases := []struct {
		name          string
		session, pane tmux.ProcIdentity
		want          tmux.AgentProcess
	}{
		{"SessionStart only", ssIdent(), identNone, selSS(ssIdent())},
		{"pane only", identNone, paneIdent(), selPane(paneIdent())},
		{"neither recorded", identNone, identNone, tmux.AgentProcess{}},
		{"both with the same pid and start time", ssIdent(), paneIdent(identPID(agentSSPID)), selSS(ssIdent())},
		{"both with different pids: pane wins", ssIdent(), paneIdent(identStart(agentOtherStart)),
			selPane(paneIdent(identStart(agentOtherStart)))},
		{"equal pids with different start times: pane wins", ssIdent(),
			paneIdent(identPID(agentSSPID), identStart(agentOtherStart)),
			selPane(paneIdent(identPID(agentSSPID), identStart(agentOtherStart)))},
		{"equal pids, SessionStart start time missing: SessionStart as recorded", ssIdent(identStart("")),
			paneIdent(identPID(agentSSPID)), selSS(ssIdent(identStart("")))},
		{"equal pids, pane start time missing: SessionStart", ssIdent(),
			paneIdent(identPID(agentSSPID), identStart("")), selSS(ssIdent())},
		{"pid-only pane differing from SessionStart: pane wins", ssIdent(), paneIdent(identStart("")),
			selPane(paneIdent(identStart("")))},
		{"pid-only SessionStart differing from pane: pane wins", ssIdent(identStart("")), paneIdent(),
			selPane(paneIdent())},
		{"pid-only SessionStart alone", ssIdent(identStart("")), identNone, selSS(ssIdent(identStart("")))},
		{"SessionStart start time with no pid is not recorded", ssIdent(identPID(0)), paneIdent(),
			selPane(paneIdent())},
		{"pane start time with a negative pid is not recorded", ssIdent(), paneIdent(identPID(-1)),
			selSS(ssIdent())},
		{"start times with no positive pid on either side", ssIdent(identPID(0)), paneIdent(identPID(-1)),
			tmux.AgentProcess{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tmux.SelectAgentProcess(tc.session, tc.pane); got != tc.want {
				t.Errorf("SelectAgentProcess = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// checkJudged asserts the judgement, the pids the reader was asked (at most
// one call) and that no environment was read.
func checkJudged(t *testing.T, pc *procfix.Checker, got, want tmux.ProcState, asked []int) {
	t.Helper()
	if got != want {
		t.Errorf("JudgeProcess = %d, want %d", got, want)
	}
	if calls := pc.StartTimeCalls(); !slices.Equal(calls, asked) {
		t.Errorf("start time asked of %v, want %v", calls, asked)
	}
	if n := pc.EnvReads(); n != 0 {
		t.Errorf("%d environment reads, want 0", n)
	}
}

// JudgeProcess answers from one start-time read: none, alive, gone or unknown.
// A pid-only identity is never alive.
func TestJudgeProcess(t *testing.T) {
	const pid = agentPanePID
	instanceEnv := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "agent-x"}
	cases := []struct {
		name  string
		id    tmux.ProcIdentity
		proc  *procfix.Process // nil: the pid is not listed (answers gone)
		want  tmux.ProcState
		asked []int
	}{
		{"not recorded", identNone, nil, tmux.ProcNone, nil},
		{"start time with no pid", paneIdent(identPID(0)), listed(procfix.Alive(agentStart)), tmux.ProcNone, nil},
		{"negative pid", paneIdent(identPID(-1)), listed(procfix.Alive(agentStart)), tmux.ProcNone, nil},
		{"alive with the recorded start", paneIdent(), listed(procfix.Alive(agentStart)), tmux.ProcAlive, []int{pid}},
		{"alive with the recorded start and an instance environment", paneIdent(),
			listed(procfix.Alive(agentStart).WithEnv(instanceEnv)), tmux.ProcAlive, []int{pid}},
		{"alive with another start", paneIdent(), listed(procfix.Alive(agentOtherStart)), tmux.ProcGone, []int{pid}},
		{"not listed", paneIdent(), nil, tmux.ProcGone, []int{pid}},
		{"gone", paneIdent(), listed(procfix.Gone()), tmux.ProcGone, []int{pid}},
		{"zombie", paneIdent(), listed(procfix.Zombie()), tmux.ProcGone, []int{pid}},
		{"unreadable", paneIdent(), listed(procfix.Unreadable()), tmux.ProcUnknown, []int{pid}},
		{"pid-only alive", paneIdent(identStart("")), listed(procfix.Alive(agentStart)), tmux.ProcUnknown, []int{pid}},
		{"pid-only unreadable", paneIdent(identStart("")), listed(procfix.Unreadable()), tmux.ProcUnknown, []int{pid}},
		{"pid-only absent", paneIdent(identStart("")), nil, tmux.ProcGone, []int{pid}},
		{"pid-only zombie", paneIdent(identStart("")), listed(procfix.Zombie()), tmux.ProcGone, []int{pid}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			if tc.proc != nil {
				pc.Set(tc.id.PID, *tc.proc)
			}
			checkJudged(t, pc, tmux.JudgeProcess(pc, tc.id), tc.want, tc.asked)
		})
	}
}

// Judging the selection asks only about the selected pid. AC-FM-19 / LFR M8:
// a SessionStart pid of an unrelated process carrying the row's instance id
// loses to the pane, and its environment is never read.
func TestJudgeProcess_SelectedAgent(t *testing.T) {
	unrelated := procfix.Alive(agentStart).WithEnv(map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "agent-x"})
	cases := []struct {
		name          string
		session, pane tmux.ProcIdentity
		paneProc      procfix.Process
		want          tmux.ProcState
		asked         []int
	}{
		{"pids differ, pane alive", ssIdent(), paneIdent(), procfix.Alive(agentStart), tmux.ProcAlive,
			[]int{agentPanePID}},
		{"pids differ, pane gone", ssIdent(), paneIdent(), procfix.Gone(), tmux.ProcGone, []int{agentPanePID}},
		{"pids differ, pane zombie", ssIdent(), paneIdent(), procfix.Zombie(), tmux.ProcGone, []int{agentPanePID}},
		{"pid-only pane differs, pane alive", ssIdent(), paneIdent(identStart("")), procfix.Alive(agentStart),
			tmux.ProcUnknown, []int{agentPanePID}},
		{"neither recorded", identNone, identNone, procfix.Alive(agentStart), tmux.ProcNone, nil},
		// Equal pids, SessionStart start time missing: the pid-only SessionStart
		// identity is selected and never judged alive, though the pane's would be.
		{"equal pids, pid-only SessionStart", ssIdent(identStart("")), paneIdent(identPID(agentSSPID)),
			procfix.Gone(), tmux.ProcUnknown, []int{agentSSPID}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := procfix.New()
			pc.Set(agentSSPID, unrelated)
			pc.Set(agentPanePID, tc.paneProc)
			sel := tmux.SelectAgentProcess(tc.session, tc.pane)
			checkJudged(t, pc, tmux.JudgeProcess(pc, sel.Identity), tc.want, tc.asked)
		})
	}
}

// listed is p as a listed process-table entry.
func listed(p procfix.Process) *procfix.Process { return &p }
