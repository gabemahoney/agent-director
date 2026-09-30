package api_test

// resume_pending_launch_test.go covers resume's launch after the move to
// pending (SR-3.3, SR-3.5, SR-3.6, SR-3.13, SR-20.6; AC-LKP-05, AC-LKP-09,
// AC-LKP-17, AC-LKP-20): the one labelled create on the row's socket, the
// identity write, $ and \ names labelled by id, socket resolution and the
// control-character id refusal. The resumeEnv fixture is in
// resume_fixture_test.go.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// rplResumed runs resume on r, fails on an error, and returns the row's raw
// columns and its new launch token, which must differ from the seeded one.
func rplResumed(t *testing.T, e *resumeEnv, r resumableRow) (apitest.SpawnColumns, string) {
	t.Helper()
	res, err := e.resume(r.ID)
	if err != nil || res.ClaudeInstanceID != r.ID {
		t.Fatalf("resume(%s) = %+v, %v; want success", r.ID, res, err)
	}
	cols := e.columns(t, r.ID)
	tok, _ := cols.LaunchToken.(string)
	if cols.State != store.StatePending || !spawnTokenRE.MatchString(tok) || tok == r.Identity.Token {
		t.Fatalf("row {state %v, token %q}; want pending with a new 16-hex token (old %q)", cols.State, tok, r.Identity.Token)
	}
	return cols, tok
}

// rplSessionNamed returns the session on socket whose stored name is name.
func rplSessionNamed(t *testing.T, rec *tmuxfix.Recorder, socket, name string) tmuxfix.SeedSession {
	t.Helper()
	for _, s := range rec.Sessions(socket) {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no session named %q on %s: %+v", name, socket, rec.Sessions(socket))
	return tmuxfix.SeedSession{}
}

// rplTrailCount counts the trail lines of event for instance id.
func rplTrailCount(t *testing.T, event, id string) int {
	t.Helper()
	n := 0
	for _, row := range readAPITrailLines(t) {
		if row["event"] == event && row["claude_instance_id"] == id {
			n++
		}
	}
	return n
}

// TestResumeCreatesLabelledSessionOnRecordedSocket: one create on the recorded
// socket carries the new token, the id and this store's id; the session holds the five-field label.
func TestResumeCreatesLabelledSessionOnRecordedSocket(t *testing.T) {
	for _, state := range []string{store.StateEnded, store.StateMissing} {
		t.Run(state, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, state)
			_, tok := rplResumed(t, e, r)
			storeID, err := apitest.ReadStoreID(e.dbPath)
			if err != nil || storeID != e.storeID {
				t.Fatalf("ReadStoreID = %q, %v; want the store's id %q", storeID, err, e.storeID)
			}

			calls := e.rec.SocketCalls()
			if len(calls) != 1 || calls[0].Call != tmux.CallCreate {
				t.Fatalf("socket calls = %+v; want exactly one create", calls)
			}
			c := calls[0]
			if c.Socket != e.socket || c.Target != r.Name || c.Cwd != r.CWD {
				t.Errorf("create {socket %q, name %q, cwd %q}; want {%q %q %q}", c.Socket, c.Target, c.Cwd, e.socket, r.Name, r.CWD)
			}
			if c.Token != tok || c.InstanceID != r.ID || c.StoreID != storeID {
				t.Errorf("create label {%q %q %q}; want {%q %q %q}", c.Token, c.InstanceID, c.StoreID, tok, r.ID, storeID)
			}
			if len(c.Command) < 2 || c.Command[0] != "claude" || !containsRun(c.Command, []string{"--resume", r.SessionID}) {
				t.Errorf("create command %q; want claude --resume %s and at least two elements (SR-3.8)", c.Command, r.SessionID)
			}
			s := rplSessionNamed(t, e.rec, e.socket, r.Name)
			if s.Label != tmuxfix.Valid(tok, r.ID, storeID) || s.Panes[0].AdPane != tok {
				t.Errorf("session label %+v, pane label %q; want ad1 %s <$N> %s %s, pane %s", s.Label, s.Panes[0].AdPane, tok, r.ID, storeID, tok)
			}
		})
	}
}

// TestResumeCreateArgvChainsLabelOnRecordedSocket: through the production client,
// a plain name gets one create on -S <socket> with both chained labels; a $ or \ name gets no chain and one label by id.
func TestResumeCreateArgvChainsLabelOnRecordedSocket(t *testing.T) {
	bin := faketmuxfix.Binary(t)
	for _, name := range []string{"", `a$b`, `a\b`} {
		t.Run("name "+name, func(t *testing.T) {
			e := newResumeEnv(t)
			logPath := filepath.Join(t.TempDir(), "fake-tmux.log")
			t.Setenv(faketmuxfix.EnvLog, logPath)
			var opts []apitest.SpawnOption
			if name != "" {
				opts = append(opts, apitest.WithTmuxSessionName(name))
			}
			r := e.seedResumable(t, "", opts...)
			if name == "" {
				name = r.Name
			}
			cfgPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			fc, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxCommand: bin, Logger: e.lg})
			if err != nil {
				t.Fatalf("api.New: %v", err)
			}
			t.Cleanup(func() { _ = fc.Close() })
			if _, err := api.Resume(e.store, api.TmuxClientOf(fc), e.pc, e.cfg, e.storeID, e.clock.Now, e.lg,
				api.ResumeParams{ClaudeInstanceID: r.ID}); err != nil {
				t.Fatalf("resume: %v", err)
			}
			cols := e.columns(t, r.ID)
			tok, _ := cols.LaunchToken.(string)
			paneID, _ := cols.PaneID.(string)

			var argvs [][]string // the socket-taking invocations
			for _, a := range fakeTmuxArgvs(t, logPath) {
				if len(a) > 2 && a[1] == "-S" {
					argvs = append(argvs, a)
				}
			}
			byID := tmux.NeedsLabelByID(name)
			if want := map[bool]int{false: 1, true: 2}[byID]; len(argvs) != want {
				t.Fatalf("socket invocations = %q; want %d", argvs, want)
			}
			create, target := argvs[0], "="+name+":"
			if !containsRun(create, []string{"-u", "-S", e.socket, "new-session"}) || !containsRun(create, []string{"-s", name}) {
				t.Errorf("create argv %q; want new-session -s %q on -S %s", create, name, e.socket)
			}
			dash := slices.Index(create, "--")
			if dash < 0 || len(create)-dash-1 < 2 || create[dash+1] != "claude" {
				t.Errorf("create argv %q: want claude and at least one more element after -- (SR-3.8)", create)
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
			label := argvs[1]
			sid := ""
			if i := slices.Index(label, "-t"); i > 0 && i+1 < len(label) {
				sid = label[i+1]
			}
			for _, seq := range [][]string{
				{"-S", e.socket, "set-option", "-t", sid, "@ad_owner", tmuxfix.LabelValue(tok, sid, r.ID, e.storeID)},
				{"set-option", "-p", "-t", paneID, "@ad_pane", tmuxfix.PaneLabelValue(tok, paneID)},
			} {
				if !strings.HasPrefix(sid, "$") || !containsRun(label, seq) {
					t.Errorf("label-by-id argv %q lacks %q", label, seq)
				}
			}
		})
	}
}

// TestResumeRecordsLaunchIdentity: the identity write stores the reply's server and pane with
// their start times; a lost reply and a write that lost to another versioned write record none;
// hooks before it are ignored and it records the pane.
func TestResumeRecordsLaunchIdentity(t *testing.T) {
	// recorded is the identity of the reply's server and pane.
	recorded := func(e *resumeEnv, s tmuxfix.SeedSession) []any {
		srv, _ := e.rec.Server(e.socket)
		return []any{int64(srv.PID), srv.Start, apitest.LinuxProcStarttime, s.Panes[0].ID,
			int64(s.Panes[0].PID), apitest.DarwinProcStarttime}
	}
	cases := []struct {
		name  string
		setup func(t *testing.T, e *resumeEnv, r resumableRow) (parent any)
		want  func(e *resumeEnv, s tmuxfix.SeedSession) []any
	}{
		{"labelled create", func(*testing.T, *resumeEnv, resumableRow) any { return nil }, recorded},
		{"lost reply", func(_ *testing.T, e *resumeEnv, _ resumableRow) any {
			e.rec.Script(e.socket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 0, Applied: true}, tmux.CallCreate)
			return nil
		}, func(*resumeEnv, tmuxfix.SeedSession) []any { return noIdentity }},
		{"identity write not applied", func(t *testing.T, e *resumeEnv, r resumableRow) any {
			other := e.seedResumable(t, "")
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				if err := e.st.SetParentID(r.ID, other.ID); err != nil {
					t.Errorf("SetParentID: %v", err)
				}
			})
			return other.ID
		}, func(*resumeEnv, tmuxfix.SeedSession) []any { return noIdentity }},
		// SR-22.9 (decision A7): the moved row records no pane until the
		// identity write, so hooks before it are ignored and it applies.
		{"hooks before the identity write ignored", func(t *testing.T, e *resumeEnv, r resumableRow) any {
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				for _, ev := range []string{"Stop", "SessionStart"} {
					got := apitest.ApplyAgentHook(t, e.dbPath, r.ID, ev, r.SessionID, apitest.HookTranscript(r.JSONLPath, true))
					if got.Applied || got.Reason != store.HookReasonNoPaneRecorded {
						t.Errorf("%s before the identity write = %+v; want not applied, %s", ev, got, store.HookReasonNoPaneRecorded)
					}
				}
			})
			return nil
		}, recorded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			r := e.seedResumable(t, "")
			e.rec.AfterCall(tmux.CallCreate, func(tmuxfix.SocketCall, error) {
				srv, _ := e.rec.Server(e.socket)
				e.pc.Set(srv.PID, procfix.Alive(apitest.LinuxProcStarttime))
				for _, s := range e.rec.Sessions(e.socket) {
					e.pc.Set(s.Panes[0].PID, procfix.Alive(apitest.DarwinProcStarttime))
				}
			})
			wantParent := tc.setup(t, e, r)
			cols, tok := rplResumed(t, e, r)

			s := rplSessionNamed(t, e.rec, e.socket, r.Name)
			if s.Label != tmuxfix.Valid(tok, r.ID, e.storeID) {
				t.Errorf("session label %+v; want ad1 %s <$N> %s <store id>", s.Label, tok, r.ID)
			}
			if got, want := identityCols(cols), tc.want(e, s); !reflect.DeepEqual(got, want) {
				t.Errorf("identity columns = %#v; want %#v", got, want)
			}
			if cols.ParentID != wantParent || cols.TmuxSocket != e.socket {
				t.Errorf("row {parent %v, socket %v}; want {%v, %s}", cols.ParentID, cols.TmuxSocket, wantParent, e.socket)
			}
			if n := len(e.rec.SocketCalls()); n != 1 || strings.Contains(e.logs.String(), "WARN") {
				t.Errorf("socket calls = %d, log %q; want the create only and no WARN", n, e.logs.String())
			}
		})
	}
}

// TestResumeLabelsDollarAndBackslashNamesByID: every catalogued $ or \ name gets one label by id
// on the reply's session and pane; the session whose id "$7" spells keeps its label.
func TestResumeLabelsDollarAndBackslashNamesByID(t *testing.T) {
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID {
			continue
		}
		t.Run(n.Raw, func(t *testing.T) {
			e := newResumeEnv(t)
			otherTok := strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{ID: "$7", Name: "bystander",
				Label: tmuxfix.Valid(otherTok, "bystander", e.storeID), Panes: []tmuxfix.SeedPane{{AdPane: otherTok}}})
			bystander := rplSessionNamed(t, e.rec, e.socket, "bystander")
			r := e.seedResumable(t, "", apitest.WithTmuxSessionName(n.Raw))
			cols, tok := rplResumed(t, e, r)

			s := rplSessionNamed(t, e.rec, e.socket, n.Stored)
			if s.Label != tmuxfix.Valid(tok, r.ID, e.storeID) || s.Panes[0].AdPane != tok {
				t.Errorf("session %s label %+v, pane label %q; want ad1 %s %s %s <store id>, pane %s",
					s.ID, s.Label, s.Panes[0].AdPane, tok, s.ID, r.ID, tok)
			}
			if got := rplSessionNamed(t, e.rec, e.socket, "bystander"); !reflect.DeepEqual(got, bystander) {
				t.Errorf("bystander $7 = %+v; want unchanged %+v", got, bystander)
			}
			creates, labels := e.rec.SocketCallsOf(tmux.CallCreate), e.rec.SocketCallsOf(tmux.CallSetLabel)
			if len(creates) != 1 || creates[0].Target != n.Raw || len(labels) != 1 || len(e.rec.SocketCalls()) != 2 {
				t.Fatalf("socket calls = %+v; want one create of %q and one label by id", e.rec.SocketCalls(), n.Raw)
			}
			l := labels[0]
			if l.Socket != e.socket || l.Target != s.ID || l.PaneID != s.Panes[0].ID ||
				l.Token != tok || l.InstanceID != r.ID || l.StoreID != e.storeID {
				t.Errorf("label by id %+v; want session %s pane %s on %s with {%s %s <store id>}",
					l, s.ID, s.Panes[0].ID, e.socket, tok, r.ID)
			}
			if cols.PaneID != s.Panes[0].ID {
				t.Errorf("row pane_id = %v; want the reply's %s", cols.PaneID, s.Panes[0].ID)
			}
		})
	}
}

// TestResumeLaunchSocket: resume launches on the recorded socket (its vanished per-user directory
// made again 0700), refuses a socket whose parent vanished before the move, and records a resolved one for a pre-release row.
func TestResumeLaunchSocket(t *testing.T) {
	cases := []struct {
		name  string
		made  bool // the per-user directory must be made again, 0700
		setup func(t *testing.T, e *resumeEnv) (opt apitest.SpawnOption, socket string, refuse *tmux.SocketDirError)
	}{
		{"recorded socket in an existing directory", false, func(t *testing.T, _ *resumeEnv) (apitest.SpawnOption, string, *tmux.SocketDirError) {
			sock := filepath.Join(t.TempDir(), "recorded.sock")
			return apitest.WithTmuxSocket(sock), sock, nil
		}},
		{"per-user directory vanished", true, func(t *testing.T, _ *resumeEnv) (apitest.SpawnOption, string, *tmux.SocketDirError) {
			sock := filepath.Join(userSocketDir(t.TempDir()), "default")
			return apitest.WithTmuxSocket(sock), sock, nil
		}},
		{"parent directory vanished", false, func(t *testing.T, _ *resumeEnv) (apitest.SpawnOption, string, *tmux.SocketDirError) {
			sock := filepath.Join(userSocketDir(filepath.Join(t.TempDir(), "gone")), "default")
			return apitest.WithTmuxSocket(sock), sock,
				&tmux.SocketDirError{Socket: sock, Dir: filepath.Dir(sock), Reason: tmux.SocketDirNotCreatable}
		}},
		{"no recorded socket", false, func(_ *testing.T, e *resumeEnv) (apitest.SpawnOption, string, *tmux.SocketDirError) {
			return apitest.WithNoLaunchToken(), e.socket, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			opt, sock, refuse := tc.setup(t, e)
			r := e.seedResumable(t, "", opt)
			if refuse != nil {
				_, err := e.resume(r.ID)
				assertLaunchSentinel(t, err, tmux.ErrTmuxNotAvailable)
				apitest.AssertDescription(t, err.Error(), apitest.DescSocketDir(sock, refuse.Dir, refuse.Error()))
				assertNoTmuxCalls(t, e.rec)
				if got := e.columns(t, r.ID); !reflect.DeepEqual(got, r.Before) {
					t.Errorf("row = %+v; want unchanged %+v", got, r.Before)
				}
				if _, err := os.Stat(filepath.Dir(refuse.Dir)); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("stat %s: %v; want still missing", filepath.Dir(refuse.Dir), err)
				}
				if n := rplTrailCount(t, "ad.resume.moved_to_pending", r.ID); n != 0 {
					t.Errorf("ad.resume.moved_to_pending lines = %d; want 0", n)
				}
				return
			}
			cols, _ := rplResumed(t, e, r)
			calls := e.rec.SocketCalls()
			if len(calls) == 0 || cols.TmuxSocket != sock {
				t.Errorf("socket calls %d, row socket %v; want calls and the row on %s", len(calls), cols.TmuxSocket, sock)
			}
			for _, c := range calls {
				if c.Socket != sock {
					t.Errorf("%s on socket %q; want %q", c.Call, c.Socket, sock)
				}
			}
			if fi, err := os.Stat(filepath.Dir(sock)); tc.made && (err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700) {
				t.Errorf("socket directory %s: %v; want a 0700 directory", filepath.Dir(sock), err)
			}
		})
	}
}

// TestResumeRefusesControlCharacterID: a legacy id with a control character is ErrInternal
// before anything: no tmux call, the row (version, parent id) unchanged, no move trail.
func TestResumeRefusesControlCharacterID(t *testing.T) {
	for name, ctl := range map[string]string{"newline": "\n", "escape": "\x1b"} {
		t.Run(name, func(t *testing.T) {
			e := newResumeEnv(t)
			parent := e.seedResumable(t, "")
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", parent.ID)
			suffix := uuid.NewString()[:8]
			r := e.seedRow(t, resumableSpec{ID: "legacy" + ctl + suffix, SessionID: "sess-ctl-" + suffix})

			_, err := e.resume(r.ID)
			if err == nil {
				t.Fatal("resume err = nil; want ErrInternal")
			}
			name, desc := errnames.Classify(err)
			if name != "ErrInternal" {
				t.Errorf("Classify name = %q (%v); want ErrInternal", name, err)
			}
			apitest.AssertDescription(t, desc, apitest.DescResumeInstanceIDControlChar(r.ID))
			assertNoTmuxCalls(t, e.rec)
			if got := e.columns(t, r.ID); !reflect.DeepEqual(got, r.Before) {
				t.Errorf("row = %+v; want unchanged %+v", got, r.Before)
			}
			if n := rplTrailCount(t, "ad.resume.moved_to_pending", r.ID); n != 0 {
				t.Errorf("ad.resume.moved_to_pending lines = %d; want 0", n)
			}
		})
	}
}
