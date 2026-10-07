package api_test

// spawn_reuse_launch_argv_test.go covers reuse's create invocation through
// the production client and test/fake-tmux (SR-3.5, SR-3.8, SR-10.4,
// SR-22.2; AC-LKP-17's reuse half): the argv on -S <the row's socket> with
// its chained labels, a $ or \ name labelled by id, a # in the id doubled, and
// parity with a plain spawn of the same request.

import (
	"io"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// rlFakeServer gives r's socket in test/fake-tmux the row's recorded server
// holding one unlabelled bystander session, as seedReusable leaves the
// Recorder.
func (e *killEnv) rlFakeServer(t *testing.T, r reuseRow) {
	t.Helper()
	srv, _ := e.rec.Server(r.Socket)
	(faketmuxfix.Tables{}).Write(t, r.Socket, faketmuxfix.Table{Server: &faketmuxfix.Server{PID: srv.PID, Start: srv.Start},
		Sessions: []faketmuxfix.Session{{ID: "$0", Created: srv.Start, Name: "bystander",
			Panes: []faketmuxfix.Pane{{ID: "%0", PID: e.newPID()}}}}})
}

// rlFakeTmuxClient is a Client on e's store driving test/fake-tmux, r's
// socket holding its server (rlFakeServer), with e's clock and process fake;
// it returns the fake's argv log.
func (e *killEnv) rlFakeTmuxClient(t *testing.T, r reuseRow) (*api.Client, string) {
	t.Helper()
	e.rlFakeServer(t, r)
	logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
	t.Setenv(faketmuxfix.EnvLog, logPath)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxCommand: faketmuxfix.Binary(t),
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	api.SetClockForTest(c, e.clock.Now)
	api.SetProcCheckerForTest(c, e.pc)
	return c, logPath
}

// rlSocketArgvs returns the logged invocations that name a socket, in order.
func rlSocketArgvs(t *testing.T, logPath string) [][]string {
	t.Helper()
	var out [][]string
	for _, a := range fakeTmuxArgvs(t, logPath) {
		if len(a) > 2 && a[1] == "-S" {
			out = append(out, a)
		}
	}
	return out
}

// rlEnvEntries counts argv's elements that start with prefix.
func rlEnvEntries(argv []string, prefix string) int {
	n := 0
	for _, a := range argv {
		if strings.HasPrefix(a, prefix) {
			n++
		}
	}
	return n
}

// TestSpawnReuseCreateArgv: after the lookup on -S <socket>, one create there chains both labels
// (a # in the id doubled); a $ or \ name gets no chain and one label by id; the id's env entry appears once.
func TestSpawnReuseCreateArgv(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv.
	cases := []struct{ name, id, session string }{
		{"plain name", "", ""},
		{"dollar name", "", `a$b`},
		{"backslash name", "", `a\b`},
		{"# in the id", "reuse#x##" + uuid.NewString()[:8], ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
			var r reuseRow
			if tc.id == "" {
				r = e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
			} else {
				// reuseRowSpec has no id: the resumable seed with the id given.
				r = reuseRow{resumeRow: e.seedOnServer(t, killRowSpec{ID: tc.id, State: store.StateEnded, Agent: agentGone,
					NoSession: true, SessionID: "sess-" + uuid.NewString()[:8],
					Opts: []apitest.SpawnOption{apitest.WithEndedAt(e.ruleInstant().Add(-time.Hour))}}, e.seedRow)}
			}
			name := tc.session
			if name == "" {
				name = "argv-" + uuid.NewString()[:8]
			}
			c, logPath := e.rlFakeTmuxClient(t, r)
			if _, err := c.Spawn(reuseParams(t, r, reuseRequest{Name: name})); err != nil {
				t.Fatalf("reuse: %v", err)
			}
			tok, _ := e.columns(t, r.ID).LaunchToken.(string)
			if !spawnTokenRE.MatchString(tok) || tok == r.Token {
				t.Fatalf("launch_token = %q; want a new 16-hex token (old %q)", tok, r.Token)
			}

			argvs, byID := rlSocketArgvs(t, logPath), tmux.NeedsLabelByID(name)
			if want := map[bool]int{false: 2, true: 3}[byID]; len(argvs) != want {
				t.Fatalf("socket invocations = %q; want %d", argvs, want)
			}
			if !isLookupOn(argvs[0], r.Socket) {
				t.Errorf("first socket invocation %q; want the lookup on -S %s", argvs[0], r.Socket)
			}
			create, target := argvs[1], "="+name+":"
			if !containsRun(create, []string{"-u", "-S", r.Socket, "new-session"}) || !containsRun(create, []string{"-s", name}) {
				t.Errorf("create argv %q; want new-session -s %q on -u -S %s", create, name, r.Socket)
			}
			if dash := slices.Index(create, "--"); dash < 0 || len(create)-dash-1 < 2 || create[dash+1] != "claude" {
				t.Errorf("create argv %q: want claude and at least one more element after -- (SR-3.8)", create)
			}
			if n := rlEnvEntries(create, "AGENT_DIRECTOR_INSTANCE_ID="+r.ID); n != 1 || rlEnvEntries(create, "AGENT_DIRECTOR_INSTANCE_ID=") != 1 ||
				strings.Contains(strings.Join(create, "\n"), "AGENT_DIRECTOR_LAUNCH_STARTED_AT") {
				t.Errorf("create argv %q: want AGENT_DIRECTOR_INSTANCE_ID=%s exactly once and no launch start", create, r.ID)
			}
			if !byID {
				for _, seq := range [][]string{
					{";", "set-option", "-F", "-t", target, "@ad_owner", tmuxfix.ChainLabelValue(tok, r.ID, e.storeID)},
					{";", "set-option", "-p", "-F", "-t", target, "@ad_pane", tmuxfix.ChainPaneLabelValue(tok)},
				} {
					if !containsRun(create, seq) {
						t.Errorf("create argv %q lacks the chained %q", create, seq)
					}
				}
				return
			}
			if slices.Contains(create, "@ad_owner") || slices.Contains(create, "@ad_pane") {
				t.Errorf("create argv %q chains a label for %q; want none", create, name)
			}
			label, sid := argvs[2], ""
			if i := slices.Index(label, "-t"); i > 0 && i+1 < len(label) {
				sid = label[i+1]
			}
			var paneID string
			for _, s := range (faketmuxfix.Tables{}).Read(t, r.Socket).Sessions {
				if s.ID == sid && len(s.Panes) > 0 {
					paneID = s.Panes[0].ID
				}
			}
			for _, seq := range [][]string{
				{"-S", r.Socket, "set-option", "-t", sid, "@ad_owner", tmuxfix.LabelValue(tok, sid, r.ID, e.storeID)},
				{"set-option", "-p", "-t", paneID, "@ad_pane", tmuxfix.PaneLabelValue(tok, paneID)},
			} {
				if !strings.HasPrefix(sid, "$") || paneID == "" || !containsRun(label, seq) {
					t.Errorf("label-by-id argv %q lacks %q", label, seq)
				}
			}
		})
	}
}

// TestSpawnReuseCreateMatchesPlainSpawn: reuse's create argv (settings, environment and command) equals a
// plain spawn's for the same request, but for the id and token, with AGENT_DIRECTOR_INSTANCE_ID once.
func TestSpawnReuseCreateMatchesPlainSpawn(t *testing.T) {
	// Serial: it sets test/fake-tmux's log variable, TMUX_TMPDIR with t.Setenv.
	e := newKillEnv(t)
	e.ownSocketDir(t) // test/fake-tmux keeps its table beside the socket
	r := e.seedReusable(t, agentGone, reuseRowSpec{Age: time.Hour})
	c, logPath := e.rlFakeTmuxClient(t, r)
	q := reuseRequest{Name: "parity-" + uuid.NewString()[:8], Args: []string{"--model", "sonnet"},
		Env: map[string]string{"PARITY_VAR": "on"}, Labels: map[string]string{"team": "parity"}}
	plain := reuseParams(t, r, q)
	plain.ReuseFinished, plain.ClaudeInstanceID = false, "plain-"+uuid.NewString()[:8]
	if _, err := c.Spawn(plain); err != nil {
		t.Fatalf("plain spawn: %v", err)
	}
	e.rlFakeServer(t, r) // the plain spawn's session gone
	if _, err := c.Spawn(reuseParams(t, r, q)); err != nil {
		t.Fatalf("reuse: %v", err)
	}

	var creates [][]string
	for _, a := range rlSocketArgvs(t, logPath) {
		if slices.Contains(a, "new-session") {
			creates = append(creates, a)
		}
	}
	if len(creates) != 2 {
		t.Fatalf("creates = %q; want the plain spawn's, then the reuse's", creates)
	}
	// normalised is argv with id's and its launch token's text replaced.
	normalised := func(argv []string, id string) []string {
		tok, _ := e.columns(t, id).LaunchToken.(string)
		out := make([]string, len(argv))
		for i, a := range argv {
			out[i] = strings.NewReplacer(id, "<id>", tok, "<token>").Replace(a)
		}
		return out
	}
	if got, want := normalised(creates[1], r.ID), normalised(creates[0], plain.ClaudeInstanceID); !slices.Equal(got, want) {
		t.Errorf("reuse's create argv\n  %q\nwant the plain spawn's\n  %q", got, want)
	}
	if n := rlEnvEntries(creates[1], "AGENT_DIRECTOR_INSTANCE_ID="); n != 1 || rlEnvEntries(creates[1], "AGENT_DIRECTOR_INSTANCE_ID="+r.ID) != 1 {
		t.Errorf("reuse's create argv has %d AGENT_DIRECTOR_INSTANCE_ID entries; want one, %s", n, r.ID)
	}
	if strings.Contains(strings.Join(creates[1], "\n"), "AGENT_DIRECTOR_LAUNCH_STARTED_AT") {
		t.Errorf("reuse's create argv %q carries AGENT_DIRECTOR_LAUNCH_STARTED_AT; want none", creates[1])
	}
}
