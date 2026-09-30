package api_test

// security_test.go is SR-15's secret and other-row-id test: beside the row a
// verb acts on, a no-id session and another row's session each carry
// SECRET=xyz (in their create's environment and their pane processes'), and
// neither xyz nor the other row's id may appear in the verb's result, error
// description, client log or trail. It is a per-verb table (kill first,
// Epic 10; later Epics add their verbs).

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// securitySecret is the value SR-15 plants in the other sessions' environment.
const securitySecret = "xyz"

// securityHolder says which planted session holds the target row's session name.
type securityHolder int

const (
	securityHolderNone  securityHolder = iota // each planted session has its own name
	securityHolderNoID                        // the no-id session holds the row's name
	securityHolderOther                       // the other row's session holds the row's name
)

// securityScene is one SR-15 arrangement: the target row, another row, and
// the ids of the no-id session and the other row's session on target's socket.
type securityScene struct {
	e                   *killEnv
	target, other       killRow
	noIDSess, otherSess string
	extraSess           tmuxfix.SeedSession
}

// securityCase is one arrangement of a verb: the target row's spec, who holds
// its name, extra setup, and the expected error (nil: success) and description.
type securityCase struct {
	name     string
	target   killRowSpec
	holder   securityHolder
	arrange  func(t *testing.T, s *securityScene)
	wantErr  error
	desc     func(s *securityScene) apitest.DescCase
	disagree bool // the call writes at least one ad.provenance.disagree record
}

// securityVerb is one verb under SR-15: its call through the Client, the
// trail event it writes once per call, and its arrangements.
type securityVerb struct {
	verb  string
	event string
	call  func(c *api.Client, id string) (any, error)
	cases []securityCase
}

// securityVerbs is the per-verb table; later Epics append their verbs.
var securityVerbs = []securityVerb{{
	verb:  "kill",
	event: "ad.kill.called",
	call:  func(c *api.Client, id string) (any, error) { return c.Kill(api.KillParams{ClaudeInstanceID: id}) },
	cases: securityKillCases,
}}

// securityKillCases meet the planted sessions on kill's Gone, Leftover,
// conflicting-labels and Ours paths (SR-6.1).
var securityKillCases = []securityCase{
	{
		name:    "gone, name held by the no-id session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxKillFailed,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescKillNoPane(s.target.ID, s.target.Name, s.target.AgentPID)
		},
	},
	{
		name:    "gone, name held by the other row's session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxKillFailed,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescKillNoPane(s.target.ID, s.target.Name, s.target.AgentPID)
		},
	},
	{
		name:   "leftover",
		target: killRowSpec{NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			s.e.seedSession(t, &s.target, tmuxfix.WithRowSessionLabel(s.target.old(), true))
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescKillLeftover([]apitest.DescSession{{Name: s.target.Session.Name, ID: s.target.Session.ID}})
		},
	},
	{
		name: "conflicting labels",
		arrange: func(t *testing.T, s *securityScene) {
			s.extraSess = s.e.seedOther(t, s.target.Socket,
				tmuxfix.SeedSession{Name: "dup-" + uuid.NewString()[:8], Label: s.target.current()})
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescConflictingLabels(apitest.ConflictingLabels{InstanceID: s.target.ID, NothingWasDone: true,
				Sessions: []apitest.DescSession{
					{Name: s.target.Session.Name, ID: s.target.Session.ID},
					{Name: s.extraSess.Name, ID: s.extraSess.ID},
				}})
		},
		disagree: true,
	},
	{
		name: "ours, success",
		arrange: func(t *testing.T, s *securityScene) {
			s.e.setAfterCall(tmux.CallKillPane, procfix.Gone(), s.target.AgentPID)
		},
	},
}

// securityEnv is the environment the planted sessions are created with.
func securityEnv() map[string]string { return map[string]string{"SECRET": securitySecret} }

// newSecurityScene seeds c's target row, another row, and on target's socket
// the no-id session and the other row's session, each carrying SECRET=xyz.
func newSecurityScene(t *testing.T, c securityCase) *securityScene {
	t.Helper()
	e := newKillEnv(t)
	s := &securityScene{e: e, target: e.seedRow(t, c.target)}
	s.other = e.seedRow(t, killRowSpec{NoSession: true})
	e.pc.Set(s.other.AgentPID, procfix.Alive(s.other.AgentStart).WithEnv(securityEnv()))
	e.ensureServer(&s.target)
	noIDName, otherName := "noid-"+uuid.NewString()[:8], s.other.Name
	switch c.holder {
	case securityHolderNoID:
		noIDName = s.target.Name
	case securityHolderOther:
		otherName = s.target.Name
	}
	// The no-id session: a create whose label step failed leaves it and its pane unlabelled.
	e.rec.Script(s.target.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate)
	s.noIDSess = securityCreate(t, e, s.target.Socket, noIDName, "", "")
	s.otherSess = securityCreate(t, e, s.target.Socket, otherName, s.other.Token, s.other.ID)
	e.syncServers()
	if c.arrange != nil {
		c.arrange(t, s)
	}
	return s
}

// securityCreate creates a session through the Recorder with SECRET=xyz in
// its environment and its pane process's, and returns its session id.
func securityCreate(t *testing.T, e *killEnv, socket, name, token, id string) string {
	t.Helper()
	reply, err := e.rec.NewSession(socket, name, t.TempDir(), securityEnv(), []string{"claude"}, token, id, e.storeID)
	if reply.SessionID == "" {
		t.Fatalf("NewSession(%s): no session created: %v", name, err)
	}
	e.pc.Set(reply.PanePID, procfix.Alive(apitest.LinuxProcStarttime).WithEnv(securityEnv()))
	return reply.SessionID
}

// securityAbsent fails when text carries the secret or the other row's id.
func securityAbsent(t *testing.T, what, text, otherID string) {
	t.Helper()
	for _, v := range []string{securitySecret, otherID} {
		if strings.Contains(text, v) {
			t.Errorf("%s carries %q: %s", what, v, text)
		}
	}
}

// TestSecuritySecretAndOtherRowID checks SR-15 for every verb in the table:
// no result, description, log line or trail record carries xyz or the other row's id.
func TestSecuritySecretAndOtherRowID(t *testing.T) {
	for _, v := range securityVerbs {
		for _, c := range v.cases {
			t.Run(v.verb+"/"+c.name, func(t *testing.T) {
				s := newSecurityScene(t, c)
				client, logs := s.e.client(t)
				s.e.rec.Reset()
				before := len(readAPITrailLines(t))

				res, err := v.call(client, s.target.ID)

				if !errors.Is(err, c.wantErr) {
					t.Fatalf("%s: err = %v; want %v", v.verb, err, c.wantErr)
				}
				if c.desc != nil {
					apitest.AssertDescription(t, err.Error(), c.desc(s), securitySecret, s.other.ID)
				}
				out, jerr := json.Marshal(res)
				if jerr != nil {
					t.Fatalf("marshal result: %v", jerr)
				}
				securityAbsent(t, "result", string(out), s.other.ID)
				securityAbsent(t, "client log", logs.String(), s.other.ID)
				securityCheckTrail(t, v.event, s, readAPITrailLines(t)[before:], c.disagree)
				securityCheckReads(t, s)
			})
		}
	}
}

// securityCheckTrail fails unless lines hold exactly one event record for the
// target (and a disagree record when wanted), none carrying xyz or the other id.
func securityCheckTrail(t *testing.T, event string, s *securityScene, lines []map[string]any, disagree bool) {
	t.Helper()
	called, disagrees := 0, 0
	for _, l := range lines {
		b, _ := json.Marshal(l)
		securityAbsent(t, "trail record", string(b), s.other.ID)
		if l["claude_instance_id"] != s.target.ID {
			continue
		}
		switch l["event"] {
		case event:
			called++
		case "ad.provenance.disagree":
			disagrees++
		}
	}
	if called != 1 {
		t.Errorf("%s records for %s = %d; want 1", event, s.target.ID, called)
	}
	if disagree && disagrees == 0 {
		t.Errorf("no ad.provenance.disagree record for %s; want one", s.target.ID)
	}
}

// securityCheckReads fails when the call read a process environment, made a
// tmux call carrying one or a name-based call, or removed a planted session.
func securityCheckReads(t *testing.T, s *securityScene) {
	t.Helper()
	if n := s.e.pc.EnvReads(); n != 0 {
		t.Errorf("process environment reads = %d; want 0", n)
	}
	for _, c := range s.e.rec.SocketCalls() {
		if c.Call == tmux.CallCreate || c.Envs != nil {
			t.Errorf("tmux call %v carries an environment (%v)", c.Call, c.Envs)
		}
	}
	if calls := s.e.rec.Calls(); len(calls) != 0 {
		t.Errorf("name-based tmux calls = %v; want none", calls)
	}
	held := map[string]bool{}
	for _, sess := range s.e.rec.Sessions(s.target.Socket) {
		held[sess.ID] = true
	}
	for _, id := range []string{s.noIDSess, s.otherSess} {
		if !held[id] {
			t.Errorf("planted session %s is gone after the call", id)
		}
	}
}
