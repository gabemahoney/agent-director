package api_test

// resume_pretrust_test.go covers resume's pre-trust (SR-22.6, SR-8.1 step 4,
// SR-5.1/5.2; AC-RES-19, AC-RES-20): the trust entry is on disk when the move
// to pending runs, is never written over the row's opt-out (also on a retry
// after a restored failure) and is never written by a resume refused before
// its move. Each row points CLAUDE_CONFIG_DIR at a per-test directory; the
// shared fixture is in resume_fixture_test.go.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// rptFile is the state of the .claude.json a test's config directory holds.
type rptFile int

const (
	rptLacksEntry rptFile = iota // present, trusting another folder only
	rptMissing                   // absent
	rptUnwritable                // present, in a directory no temp file can be created in
)

// rptSeedJSON is the compact .claude.json a config directory starts with, so
// any rewrite (which indents) changes its bytes.
const rptSeedJSON = `{"numStartups":7,"projects":{"/elsewhere":{"hasTrustDialogAccepted":true}}}`

// rptConfig is a config directory and its .claude.json as seeded (before is
// nil when the file is absent).
type rptConfig struct {
	dir, path string
	before    []byte
}

// rptSeedConfig puts a .claude.json in state file into dir. The unwritable
// state (a 0500 directory) is skipped as root, where permissions do not bite.
func rptSeedConfig(t *testing.T, dir string, file rptFile) rptConfig {
	t.Helper()
	c := rptConfig{dir: dir, path: filepath.Join(dir, ".claude.json")}
	if file == rptMissing {
		return c
	}
	c.before = []byte(rptSeedJSON)
	if err := os.WriteFile(c.path, c.before, 0o600); err != nil {
		t.Fatalf("write %s: %v", c.path, err)
	}
	if file == rptUnwritable {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions; the unwritable .claude.json cannot be simulated")
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatalf("chmod %s: %v", dir, err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	}
	return c
}

// env is the seed option pointing a row's CLAUDE_CONFIG_DIR at c.
func (c rptConfig) env() apitest.SpawnOption {
	return apitest.WithExtraEnv(map[string]string{"CLAUDE_CONFIG_DIR": c.dir})
}

// reset writes the seeded bytes back, removing an entry a resume wrote.
func (c rptConfig) reset(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(c.path, c.before, 0o600); err != nil {
		t.Fatalf("reset %s: %v", c.path, err)
	}
}

// check asserts, at when, that the file trusts cwd (trusted) or is exactly as
// seeded (still absent when it was).
func (c rptConfig) check(t *testing.T, cwd string, trusted bool, when string) {
	t.Helper()
	got, err := os.ReadFile(c.path)
	if !trusted {
		if c.before == nil {
			if !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s: %s = %q (%v); want still absent", when, c.path, got, err)
			}
		} else if err != nil || string(got) != string(c.before) {
			t.Errorf("%s: %s = %q (%v); want unchanged %q", when, c.path, got, err, c.before)
		}
		return
	}
	var top struct {
		Projects map[string]map[string]any `json:"projects"`
	}
	if err != nil || json.Unmarshal(got, &top) != nil || top.Projects[cwd]["hasTrustDialogAccepted"] != true {
		t.Errorf("%s: %s = %q (%v); want projects[%q].hasTrustDialogAccepted true", when, c.path, got, err, cwd)
	}
}

// rptResume resumes r and checks, when the move runs, that the file is as
// trusted says and that the move kept no_pre_trust.
func rptResume(t *testing.T, e *resumeEnv, r resumableRow, c rptConfig, trusted bool, when string) error {
	t.Helper()
	moved := false
	e.store.afterMove(func() {
		moved = true
		c.check(t, r.CWD, trusted, when+": when the move ran")
		if got := e.columns(t, r.ID).NoPreTrust; got != r.Before.NoPreTrust {
			t.Errorf("%s: no_pre_trust after the move = %#v; want %#v", when, got, r.Before.NoPreTrust)
		}
	})
	_, err := e.resume(r.ID)
	if err == nil && !moved {
		t.Errorf("%s: the resume succeeded without a move", when)
	}
	return err
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
// runs; a missing or unwritable file, or the row's opt-out, leaves the file as
// it was, and every case launches. The archived-session path behaves the same.
func TestResumePreTrustBeforeMove(t *testing.T) {
	cases := []struct {
		name     string
		file     rptFile
		opts     []apitest.SpawnOption
		archived bool // the current transcript is gone; resume takes the archived session
		trusted  bool // the entry is written; otherwise the file is left as seeded
	}{
		{"allowed row, entry lacking", rptLacksEntry, nil, false, true},
		{".claude.json missing", rptMissing, nil, false, false},
		{".claude.json unwritable", rptUnwritable, nil, false, false},
		{"opted-out row", rptLacksEntry, []apitest.SpawnOption{apitest.WithNoPreTrust()}, false, false},
		{"opted-out row, unusual stored value", rptLacksEntry, []apitest.SpawnOption{apitest.WithRawNoPreTrust("sometimes")}, false, false},
		{"archived session, allowed row", rptLacksEntry, nil, true, true},
		{"archived session, opted-out row", rptLacksEntry, []apitest.SpawnOption{apitest.WithNoPreTrust()}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			c := rptSeedConfig(t, t.TempDir(), tc.file)
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{c.env()}, tc.opts...)...)
			session := r.SessionID
			if tc.archived {
				if err := os.Remove(r.JSONLPath); err != nil {
					t.Fatalf("remove transcript: %v", err)
				}
				session = r.HistorySessionID
			}
			if err := rptResume(t, e, r, c, tc.trusted, "resume"); err != nil {
				t.Fatalf("Resume: %v", err)
			}
			c.check(t, r.CWD, tc.trusted, "after the resume")
			rptAssertLaunched(t, e, r.ID, session, 1)
		})
	}
}

// TestResumePreTrustHomeWithoutConfigDir: a row with no CLAUDE_CONFIG_DIR gets
// its entry in $HOME/.claude.json (a per-test HOME; not parallel).
func TestResumePreTrustHomeWithoutConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := newResumeEnv(t)
	c := rptSeedConfig(t, home, rptLacksEntry)
	r := e.seedResumable(t, store.StateMissing) // default extra env: no CLAUDE_CONFIG_DIR
	if err := rptResume(t, e, r, c, true, "resume"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	c.check(t, r.CWD, true, "after the resume")
	rptAssertLaunched(t, e, r.ID, r.SessionID, 1)
}

// TestResumePreTrustRetryAfterRestoredFailure: after a failed create and its
// restore, no_pre_trust is kept, and the retry pre-trusts again for an allowed
// row and still writes nothing for an opted-out one.
func TestResumePreTrustRetryAfterRestoredFailure(t *testing.T) {
	cases := []struct {
		name    string
		opts    []apitest.SpawnOption
		trusted bool
	}{
		{"allowed row pre-trusts again", nil, true},
		{"opted-out row stays skipped", []apitest.SpawnOption{apitest.WithNoPreTrust()}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			c := rptSeedConfig(t, t.TempDir(), rptLacksEntry)
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{c.env()}, tc.opts...)...)
			e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)

			assertLaunchSentinel(t, rptResume(t, e, r, c, tc.trusted, "first resume"), tmux.ErrTmuxSessionCreate)
			restored := e.columns(t, r.ID)
			if restored.State != r.Before.State || restored.NoPreTrust != r.Before.NoPreTrust {
				t.Fatalf("after the restore: state %v, no_pre_trust %#v; want %v, %#v",
					restored.State, restored.NoPreTrust, r.Before.State, r.Before.NoPreTrust)
			}
			if tc.trusted {
				c.reset(t) // so the retry's entry is its own
			}

			if err := rptResume(t, e, r, c, tc.trusted, "retry"); err != nil {
				t.Fatalf("retry Resume: %v", err)
			}
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
		{"session name already taken", "ErrTmuxSessionCreate", func(t *testing.T, e *resumeEnv, env apitest.SpawnOption) string {
			e.rec.WithHasSession(true)
			return e.seedResumable(t, store.StateEnded, env).ID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			c := rptSeedConfig(t, t.TempDir(), rptLacksEntry)
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
