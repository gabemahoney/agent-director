package api_test

// security_test.go is SR-15's secret and other-row-id test: beside the row a
// verb acts on, a no-id session and another row's session each carry
// SECRET=xyz (in their create's environment and their pane processes'), and
// neither xyz, a row's launch token (so no label value), the other row's id
// nor another store's id may appear in the verb's result, error description,
// client log or trail. It is a per-verb table (kill first, Epic 10; plain
// spawn's held name, Epic 13; then, each in its security_<verb>_test.go,
// find-missing's lookup, Epic 14; read-pane, which writes no trail event,
// Epic 11; send-keys on a live row and on a pending row with allow_pending,
// Epic 11; pause, which writes no call event, Epic 11; expire, Epic 15, as
// kept rows (ad.expire.kept) and deleted rows (no record); resume, Epic 16:
// its pre-launch refusals and its held name after "duplicate session"; reuse,
// Epic 17, run by TestSecurityReuse).

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
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

// securityScene is one SR-15 arrangement: the target row, another row, the
// ids of the no-id session and the other row's session on target's socket,
// subject, the instance id the call acts on (target's, or a launch's fresh
// id), and forbid, values the call learns that nothing may carry either.
type securityScene struct {
	e                   *killEnv
	target, other       killRow
	noIDSess, otherSess string
	extraSess           tmuxfix.SeedSession
	subject             string
	forbid              []string
}

// securityCase is one arrangement of a verb: the target row's spec, who holds
// its name, the socket's server before the planted sessions are created,
// extra setup, the expected error (nil: success) and description, the
// record the call writes once for the subject with field values it carries,
// records of the subject the arrangement writes during the call, and extra
// checks of the call's result (nil: none).
type securityCase struct {
	name     string
	target   killRowSpec
	holder   securityHolder
	server   func(e *killEnv, socket string) // nil: the target's recorded server
	arrange  func(t *testing.T, s *securityScene)
	wantErr  error
	desc     func(s *securityScene) apitest.DescCase
	disagree bool           // the call writes at least one ad.provenance.disagree record
	event    string         // the record written once for the subject; "": the verb's event
	fields   map[string]any // values that record carries (nil: not checked)
	others   []string       // events the arrangement writes for the subject during the call (the agent's own hooks)
	// check makes extra checks of the call's result (nil: none).
	check func(t *testing.T, s *securityScene, res any)
}

// securityVerb is one verb under SR-15: its call through the Client on the
// scene's subject, the trail event it writes once per call ("": none, and no
// record for the subject but the ad.provenance.disagree records a case
// expects; securityCheckTrail), extra checks of
// that record against the call's texts (description, client log, result;
// nil: none), and its arrangements. A launch verb acts on a fresh id on
// target's socket, requesting target's name, and makes one create.
type securityVerb struct {
	verb   string
	event  string
	launch bool
	call   func(t *testing.T, c *api.Client, s *securityScene) (any, error)
	record func(t *testing.T, s *securityScene, rec map[string]any, texts map[string]string)
	cases  []securityCase
}

// securityVerbs is the per-verb table; later Epics append their verbs.
var securityVerbs = append([]securityVerb{{
	verb:  "kill",
	event: "ad.kill.called",
	call: func(_ *testing.T, c *api.Client, s *securityScene) (any, error) {
		return c.Kill(api.KillParams{ClaudeInstanceID: s.subject})
	},
	cases: securityKillCases,
}, {
	verb:   "spawn",
	event:  "ad.launch.name_held",
	launch: true,
	call: func(t *testing.T, c *api.Client, s *securityScene) (any, error) {
		home := t.TempDir() // the trail is pinned by TestMain; the launch's files go here
		t.Setenv("HOME", home)
		if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte("{}"), 0o600); err != nil {
			t.Fatalf("write .claude.json: %v", err)
		}
		return c.Spawn(api.SpawnParams{CWD: t.TempDir(), ClaudeInstanceID: s.subject,
			TmuxSessionName: s.target.Name, TmuxSessionNameSupplied: true})
	},
	record: securityNameHeldRecord,
	cases:  securitySpawnCases,
}, {
	verb:   "find-missing",
	event:  "ad.launch.name_held",
	call:   securityFindMissingCall,
	record: securityFindMissingRecord,
	cases:  securityFindMissingCases,
}, {
	verb:  "read-pane",
	call:  securityReadPaneCall,
	cases: securityReadPaneCases,
}, {
	verb:  "send-keys",
	event: "ad.send_keys.called",
	call:  securitySendKeysCall(false),
	cases: securitySendKeysCases(store.StateWaiting),
}, {
	verb:  "send-keys allow_pending",
	event: "ad.send_keys.called",
	call:  securitySendKeysCall(true),
	cases: securitySendKeysCases(store.StatePending),
}, {
	verb:  "pause",
	call:  securityPauseCall,
	cases: securityPauseCases,
}, {
	verb: "expire", event: expireKeptEvent, call: securityExpireCall, record: securityExpireKeptRecord,
	cases: securityExpireKeptCases,
}, {
	verb: "expire, deleted rows", call: securityExpireCall, cases: securityExpireGoneCases,
}}, securityResumeVerbs...)

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

// securitySpawnCases meet plain spawn's "duplicate session" on the requested
// name held by another row's session, a no-id session, a leftover of the new
// id placed after the scan (SR-20.9), and another store's session (SR-9.4).
var securitySpawnCases = []securityCase{
	{
		name:    "held by the other row's session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderOther,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldDifferentID(s.held(s.otherSess)) },
	},
	{
		name:    "held by the no-id session",
		target:  killRowSpec{NoSession: true},
		holder:  securityHolderNoID,
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldNoValidID(s.held(s.noIDSess)) },
	},
	{
		name:   "held by a leftover of the new id",
		target: killRowSpec{NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			pid := s.e.newPID()
			s.e.pc.Set(pid, procfix.Alive(apitest.LinuxProcStarttime).WithEnv(securityEnv()))
			leftover := tmuxfix.SeedSession{Name: s.target.Name, Panes: []tmuxfix.SeedPane{{PID: pid}},
				Label: tmuxfix.Valid(tmuxfix.OtherToken, s.subject, s.e.storeID)}
			lookups := 0
			s.e.rec.AfterCall(tmux.CallLookup, func(tmuxfix.SocketCall, error) {
				if lookups++; lookups == 1 { // the scan's lookup: the leftover appears before the create
					s.extraSess = s.e.seedOther(t, s.target.Socket, leftover)
				}
			})
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc:    func(s *securityScene) apitest.DescCase { return apitest.DescHeldLeftover(s.held(s.extraSess.ID)) },
	},
	{
		name:   "held by another store's session",
		target: killRowSpec{NoSession: true},
		arrange: func(t *testing.T, s *securityScene) {
			id := securityCreate(t, s.e, s.target.Socket, s.target.Name, s.other.Token, s.other.ID,
				apitest.OtherStoreID(s.e.storeID))
			s.extraSess = tmuxfix.SeedSession{ID: id, Name: s.target.Name}
		},
		wantErr: api.ErrTmuxSessionConflict,
		desc: func(s *securityScene) apitest.DescCase {
			return apitest.DescHeldOtherStore(s.held(s.extraSess.ID), s.e.storeID)
		},
	},
}

// held is the held-name description parameter for target's name held by
// sessionID, after an applied end write.
func (s *securityScene) held(sessionID string) apitest.HeldName {
	return apitest.HeldName{Name: s.target.Name, SessionID: sessionID, Row: apitest.HeldRowEnded}
}

// securityEnv is the environment the planted sessions are created with.
func securityEnv() map[string]string { return map[string]string{"SECRET": securitySecret} }

// newSecurityScene seeds c's target row, another row, and on target's socket
// the no-id session and the other row's session, each carrying SECRET=xyz.
// For a launch verb target records the socket a launch resolves (TMUX
// unset) and the subject is a fresh id.
func newSecurityScene(t *testing.T, v securityVerb, c securityCase) *securityScene {
	t.Helper()
	e := newKillEnv(t)
	spec := c.target
	if v.launch {
		spec.Opts = append(append([]apitest.SpawnOption(nil), spec.Opts...), apitest.WithTmuxSocket(e.defaultSocket))
	}
	s := &securityScene{e: e, target: e.seedRow(t, spec)}
	s.subject = s.target.ID
	if v.launch {
		s.subject = heldID()
	}
	s.other = e.seedRow(t, killRowSpec{NoSession: true})
	e.pc.Set(s.other.AgentPID, procfix.Alive(s.other.AgentStart).WithEnv(securityEnv()))
	e.ensureServer(&s.target)
	if c.server != nil {
		c.server(e, s.target.Socket)
		e.syncServers()
	}
	noIDName, otherName := "noid-"+uuid.NewString()[:8], s.other.Name
	switch c.holder {
	case securityHolderNoID:
		noIDName = s.target.Name
	case securityHolderOther:
		otherName = s.target.Name
	}
	// The no-id session: a create whose label step failed leaves it and its pane unlabelled.
	e.rec.Script(s.target.Socket, tmuxfix.Script{Failure: tmux.FailLabel, Times: 1}, tmux.CallCreate)
	s.noIDSess = securityCreate(t, e, s.target.Socket, noIDName, "", "", e.storeID)
	s.otherSess = securityCreate(t, e, s.target.Socket, otherName, s.other.Token, s.other.ID, e.storeID)
	e.syncServers()
	if c.arrange != nil {
		c.arrange(t, s)
	}
	return s
}

// securityCreate creates a session through the Recorder, labelled for id by
// storeID, with SECRET=xyz in its environment and its pane process's, and
// returns its session id.
func securityCreate(t *testing.T, e *killEnv, socket, name, token, id, storeID string) string {
	t.Helper()
	reply, err := e.rec.NewSession(socket, name, t.TempDir(), securityEnv(), []string{"claude"}, token, id, storeID)
	if reply.SessionID == "" {
		t.Fatalf("NewSession(%s): no session created: %v", name, err)
	}
	e.pc.Set(reply.PanePID, procfix.Alive(apitest.LinuxProcStarttime).WithEnv(securityEnv()))
	return reply.SessionID
}

// securityForbidden are the values nothing may carry: the secret, both rows'
// launch tokens and an earlier launch's (the leftovers'; a label's content),
// the other row's id, another store's id and s.forbid.
func securityForbidden(s *securityScene) []string {
	return append([]string{securitySecret, s.target.Token, s.other.Token, tmuxfix.OtherToken, s.other.ID,
		apitest.OtherStoreID(s.e.storeID)}, s.forbid...)
}

// securityAbsent fails when text carries a securityForbidden value.
func securityAbsent(t *testing.T, what, text string, s *securityScene) {
	t.Helper()
	for _, v := range securityForbidden(s) {
		if strings.Contains(text, v) {
			t.Errorf("%s carries %q: %s", what, v, text)
		}
	}
}

// TestSecuritySecretAndOtherRowID checks SR-15 for every verb in the table:
// no result, description, log line or trail record carries a forbidden value.
func TestSecuritySecretAndOtherRowID(t *testing.T) { runSecurityVerbs(t, securityVerbs) }

// runSecurityVerbs runs every case of verbs (reuse's: TestSecurityReuse).
func runSecurityVerbs(t *testing.T, verbs []securityVerb) {
	for _, v := range verbs {
		for _, c := range v.cases {
			t.Run(v.verb+"/"+c.name, func(t *testing.T) {
				s := newSecurityScene(t, v, c)
				client, logs := s.e.client(t)
				s.e.rec.Reset()
				before := len(readAPITrailLines(t))

				res, err := v.call(t, client, s)

				if !errors.Is(err, c.wantErr) {
					t.Fatalf("%s: err = %v; want %v", v.verb, err, c.wantErr)
				}
				var desc string
				if c.desc != nil {
					desc = err.Error()
					apitest.AssertDescription(t, desc, c.desc(s), securityForbidden(s)...)
				}
				out, jerr := json.Marshal(res)
				if jerr != nil {
					t.Fatalf("marshal result: %v", jerr)
				}
				securityAbsent(t, "result", string(out), s)
				securityAbsent(t, "client log", logs.String(), s)
				if c.check != nil {
					c.check(t, s, res)
				}
				event := v.event
				if c.event != "" {
					event = c.event
				}
				if rec := securityCheckTrail(t, event, s, readAPITrailLines(t)[before:], c); rec != nil {
					for k, want := range c.fields {
						if got, ok := rec[k]; !ok || got != want {
							t.Errorf("%s: %s = %v (present %t); want %v", event, k, got, ok, want)
						}
					}
					if v.record != nil {
						v.record(t, s, rec, map[string]string{"description": desc, "client log": logs.String(), "result": string(out)})
					}
				}
				securityCheckReads(t, s, v.launch)
			})
		}
	}
}

// securityCheckTrail fails unless lines hold exactly one event record for the
// subject (and a disagree record when c wants one), none carrying a forbidden
// value; it returns that record (nil when not exactly one). With event ""
// (a verb that writes no call event: read-pane, SR-7.5; pause) it fails on
// any record for the subject but c.others', and on an ad.provenance.disagree
// record unless c wants one, and returns nil.
func securityCheckTrail(t *testing.T, event string, s *securityScene, lines []map[string]any, c securityCase) map[string]any {
	t.Helper()
	var called []map[string]any
	disagrees := 0
	for _, l := range lines {
		b, _ := json.Marshal(l)
		securityAbsent(t, "trail record", string(b), s)
		if l["claude_instance_id"] != s.subject || slices.Contains(c.others, fmt.Sprint(l["event"])) {
			continue
		}
		switch ev := l["event"]; {
		case event != "" && ev == event:
			called = append(called, l)
		case ev == "ad.provenance.disagree":
			disagrees++
		case event == "":
			t.Errorf("trail record %v for %s; want none", ev, s.subject)
		}
	}
	if c.disagree && disagrees == 0 {
		t.Errorf("no ad.provenance.disagree record for %s; want one", s.subject)
	}
	if event == "" {
		if !c.disagree && disagrees > 0 {
			t.Errorf("ad.provenance.disagree records for %s = %d; want none", s.subject, disagrees)
		}
		return nil
	}
	if len(called) != 1 {
		t.Errorf("%s records for %s = %d; want 1", event, s.subject, len(called))
		return nil
	}
	return called[0]
}

// securityNameHeldRecord checks an ad.launch.name_held record: this store's
// store_id, a boolean carries_this_id, and by-hand commands for the
// identified holder that none of texts (description, client log, result)
// carries.
func securityNameHeldRecord(t *testing.T, s *securityScene, rec map[string]any, texts map[string]string) {
	t.Helper()
	if rec["store_id"] != s.e.storeID {
		t.Errorf("store_id = %v; want this store's %q", rec["store_id"], s.e.storeID)
	}
	if _, ok := rec["carries_this_id"].(bool); !ok {
		t.Errorf("carries_this_id = %#v; want a boolean", rec["carries_this_id"])
	}
	leaks := []string{"attach-session", "kill-session"}
	for _, k := range []string{"attach_command", "end_command"} {
		cmd, _ := rec[k].(string)
		if cmd == "" {
			t.Errorf("%s = %#v; want the holder's by-hand command", k, rec[k])
			continue
		}
		leaks = append(leaks, cmd)
	}
	for what, text := range texts {
		for _, v := range leaks {
			if strings.Contains(text, v) {
				t.Errorf("%s carries the by-hand command text %q: %s", what, v, text)
			}
		}
	}
}

// securityCheckReads fails when the call read a process environment, made a
// tmux call carrying one (a launch's own create excepted, which must carry
// no forbidden value) or a name-based call, or removed a planted session.
func securityCheckReads(t *testing.T, s *securityScene, launch bool) {
	t.Helper()
	if n := s.e.pc.EnvReads(); n != 0 {
		t.Errorf("process environment reads = %d; want 0", n)
	}
	creates := 0
	for _, c := range s.e.rec.SocketCalls() {
		if c.Call == tmux.CallCreate && launch {
			creates++
			securityAbsent(t, "the launch's create environment", fmt.Sprint(c.Envs), s)
			continue
		}
		if c.Call == tmux.CallCreate || c.Envs != nil {
			t.Errorf("tmux call %v carries an environment (%v)", c.Call, c.Envs)
		}
	}
	if launch && creates != 1 {
		t.Errorf("creates = %d; want the launch's one", creates)
	}
	if calls := s.e.rec.Calls(); len(calls) != 0 {
		t.Errorf("name-based tmux calls = %v; want none", calls)
	}
	held := map[string]bool{}
	for _, sess := range s.e.rec.Sessions(s.target.Socket) {
		held[sess.ID] = true
	}
	for _, id := range []string{s.noIDSess, s.otherSess, s.extraSess.ID} {
		if id != "" && !held[id] {
			t.Errorf("planted session %s is gone after the call", id)
		}
	}
}
