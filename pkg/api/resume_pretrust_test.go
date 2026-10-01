package api_test

// resume_pretrust_test.go covers resume's pre-trust (SR-22.6, SR-8.1 step 4,
// SR-5.1/5.2; AC-RES-19, AC-RES-20): the trust entry is on disk when the move
// to pending runs, is never written over the row's opt-out (also on a retry
// after a restored failure) and is never written by a resume refused before
// its move; every resume that returns a result reports what its pre-trust did
// as ResumeResult.PreTrust (JSON pre_trust). Each row points CLAUDE_CONFIG_DIR
// at a per-test directory; the shared fixtures are in resume_fixture_test.go
// and pretrust_fixture_test.go.

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// rptResume resumes r and checks, when the move runs, that the file is as
// trusted says and that the move kept no_pre_trust; it returns Resume's result.
func rptResume(t *testing.T, e *resumeEnv, r resumableRow, c trustConfig, trusted bool, when string) (api.ResumeResult, error) {
	t.Helper()
	moved := false
	e.store.afterMove(func() {
		moved = true
		c.check(t, r.CWD, trusted, when+": when the move ran")
		if got := e.columns(t, r.ID).NoPreTrust; got != r.Before.NoPreTrust {
			t.Errorf("%s: no_pre_trust after the move = %#v; want %#v", when, got, r.Before.NoPreTrust)
		}
	})
	res, err := e.resume(r.ID)
	if err == nil && !moved {
		t.Errorf("%s: the resume succeeded without a move", when)
	}
	return res, err
}

// rptAssertPreTrust checks res reports want as PreTrust and as its JSON pre_trust.
func rptAssertPreTrust(t *testing.T, res api.ResumeResult, want, when string) {
	t.Helper()
	if res.PreTrust != want {
		t.Errorf("%s: ResumeResult.PreTrust = %q; want %q", when, res.PreTrust, want)
	}
	checkPreTrustJSON(t, res, want)
}

// rptAssertLaunched checks r is pending after creates creates, the last one
// resuming session.
func rptAssertLaunched(t *testing.T, e *resumeEnv, id, session string, creates int) {
	t.Helper()
	if st := e.columns(t, id).State; st != store.StatePending {
		t.Errorf("state = %v; want pending", st)
	}
	cs := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(cs) != creates {
		t.Fatalf("creates = %d; want %d", len(cs), creates)
	}
	cmd := cs[len(cs)-1].Command
	if i := slices.Index(cmd, "--resume"); i < 0 || i+1 >= len(cmd) || cmd[i+1] != session {
		t.Errorf("argv = %q; want --resume %s", cmd, session)
	}
}

// TestResumePreTrustBeforeMove: an allowed row's entry is on disk when the move
// runs (ok); a missing or unwritable file (failed) or the row's opt-out
// (skipped) leaves the file as it was, and every case launches. The
// archived-session path behaves the same.
func TestResumePreTrustBeforeMove(t *testing.T) {
	cases := []struct {
		name         string
		file         trustFile
		opts         []apitest.SpawnOption
		archived     bool // the current transcript is gone; resume takes the archived session
		trusted      bool // the entry is written; otherwise the file is left as seeded
		wantPreTrust string
	}{
		{"allowed row, entry lacking", trustLacksEntry, nil, false, true, "ok"},
		{".claude.json missing", trustMissing, nil, false, false, "failed"},
		{".claude.json unwritable", trustUnwritable, nil, false, false, "failed"},
		{"opted-out row", trustLacksEntry, []apitest.SpawnOption{apitest.WithNoPreTrust()}, false, false, "skipped"},
		{"opted-out row, unusual stored value", trustLacksEntry, []apitest.SpawnOption{apitest.WithRawNoPreTrust("sometimes")}, false, false, "skipped"},
		{"archived session, allowed row", trustLacksEntry, nil, true, true, "ok"},
		{"archived session, opted-out row", trustLacksEntry, []apitest.SpawnOption{apitest.WithNoPreTrust()}, true, false, "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			c := seedTrustConfig(t, t.TempDir(), tc.file)
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{c.env()}, tc.opts...)...)
			session := r.SessionID
			if tc.archived {
				if err := os.Remove(r.JSONLPath); err != nil {
					t.Fatalf("remove transcript: %v", err)
				}
				session = r.HistorySessionID
			}
			res, err := rptResume(t, e, r, c, tc.trusted, "resume")
			if err != nil {
				t.Fatalf("Resume: %v", err)
			}
			rptAssertPreTrust(t, res, tc.wantPreTrust, "resume")
			c.check(t, r.CWD, tc.trusted, "after the resume")
			rptAssertLaunched(t, e, r.ID, session, 1)
		})
	}
}

// TestResumePreTrustHomeWithoutConfigDir: a row with no CLAUDE_CONFIG_DIR gets
// its entry in $HOME/.claude.json and reports ok (a per-test HOME; not parallel).
func TestResumePreTrustHomeWithoutConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := newResumeEnv(t)
	c := seedTrustConfig(t, home, trustLacksEntry)
	r := e.seedResumable(t, store.StateMissing) // default extra env: no CLAUDE_CONFIG_DIR
	res, err := rptResume(t, e, r, c, true, "resume")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	rptAssertPreTrust(t, res, "ok", "resume")
	c.check(t, r.CWD, true, "after the resume")
	rptAssertLaunched(t, e, r.ID, r.SessionID, 1)
}

// TestResumePreTrustRetryAfterRestoredFailure: after a failed create and its
// restore, no_pre_trust is kept, and the retry pre-trusts again for an allowed
// row (ok) and still writes nothing for an opted-out one (skipped).
func TestResumePreTrustRetryAfterRestoredFailure(t *testing.T) {
	cases := []struct {
		name         string
		opts         []apitest.SpawnOption
		trusted      bool
		wantPreTrust string // the successful retry's
	}{
		{"allowed row pre-trusts again", nil, true, "ok"},
		{"opted-out row stays skipped", []apitest.SpawnOption{apitest.WithNoPreTrust()}, false, "skipped"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			c := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{c.env()}, tc.opts...)...)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)

			_, firstErr := rptResume(t, e, r, c, tc.trusted, "first resume")
			assertLaunchSentinel(t, firstErr, tmux.ErrTmuxSessionCreate)
			restored := e.columns(t, r.ID)
			if restored.State != r.Before.State || restored.NoPreTrust != r.Before.NoPreTrust {
				t.Fatalf("after the restore: state %v, no_pre_trust %#v; want %v, %#v",
					restored.State, restored.NoPreTrust, r.Before.State, r.Before.NoPreTrust)
			}
			if tc.trusted {
				c.reset(t) // so the retry's entry is its own
			}

			res, err := rptResume(t, e, r, c, tc.trusted, "retry")
			if err != nil {
				t.Fatalf("retry Resume: %v", err)
			}
			rptAssertPreTrust(t, res, tc.wantPreTrust, "retry")
			c.check(t, r.CWD, tc.trusted, "after the retry")
			rptAssertLaunched(t, e, r.ID, r.SessionID, 2)
		})
	}
}

// TestResumePreTrustRefusedBeforeMoveWritesNothing: every refusal before the
// move leaves an allowed row's .claude.json unchanged and creates nothing.
func TestResumePreTrustRefusedBeforeMoveWritesNothing(t *testing.T) {
	cases := []struct {
		name, wantName string
		seed           func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string
	}{
		{"live row", "ErrSpawnNotResumable", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			return e.seedResumable(t, store.StateWaiting, env).ID
		}},
		{"launch in progress", "ErrSpawnNotResumable", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			return e.seedResumable(t, store.StatePending, env).ID
		}},
		{"no session id", "ErrNoSessionId", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			return seedBareResumeRow(t, e, t.TempDir(), "", env)
		}},
		{"transcripts gone", "ErrJsonlMissing", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			r := e.seedResumable(t, store.StateEnded, env)
			for _, p := range []string{r.JSONLPath, r.HistoryJSONLPath} {
				if err := os.Remove(p); err != nil {
					t.Fatalf("remove transcript: %v", err)
				}
			}
			return r.ID
		}},
		{"transcript never written", "ErrJsonlNeverWritten", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			return seedBareResumeRow(t, e, t.TempDir(), "sess-"+uuid.NewString()[:8], env)
		}},
		{"control character in the id", "ErrInternal", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			suffix := uuid.NewString()[:8]
			return e.seedRow(t, resumableSpec{ID: "legacy\n" + suffix, SessionID: "sess-ctl-" + suffix,
				Opts: []apitest.SpawnOption{env}}).ID
		}},
		{"socket directory cannot be made", "ErrTmuxNotAvailable", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			sock := filepath.Join(userSocketDir(filepath.Join(t.TempDir(), "gone")), "default")
			return e.seedResumable(t, store.StateEnded, env, apitest.WithTmuxSocket(sock)).ID
		}},
		{"session name held by an unlabelled session", "ErrTmuxSessionConflict", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			// The row's recorded server answers the lookup with a session under
			// the row's name and no label (no valid instance id): Gone, held.
			r := e.seedResumable(t, store.StateEnded, env)
			e.rec.StartServer(e.socket, tmuxfix.Server{PID: r.Identity.ServerPID, Start: r.Identity.ServerStart,
				ProcStart: r.Identity.ServerStarttime})
			e.pc.Set(r.Identity.ServerPID, procfix.Alive(r.Identity.ServerStarttime))
			e.rec.SeedSessions(e.socket, tmuxfix.SeedSession{Name: r.Name})
			return r.ID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			c := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			id := tc.seed(t, e, c.env())
			_, err := e.resume(id)
			if name, _ := errnames.Classify(err); name != tc.wantName {
				t.Fatalf("Resume = %v (%s); want %s", err, name, tc.wantName)
			}
			c.check(t, "", false, "after the refusal")
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 0 {
				t.Errorf("creates = %d; want none", n)
			}
		})
	}
}
