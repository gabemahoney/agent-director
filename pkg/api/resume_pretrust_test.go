package api_test

// resume_pretrust_test.go covers resume's pre-trust (SR-22.6, SR-8.1 step 4,
// SR-5.1/5.2; AC-RES-19, AC-RES-20) on a per-test CLAUDE_CONFIG_DIR. A refusal
// writing no entry is the refusal tests'; which file a row names, and an
// unwritable one, are internal/spawn's pre-trust tests'.

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
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

// TestResumePreTrust: an allowed row's entry is on disk when the move runs
// (ok); a missing file or a relative CLAUDE_CONFIG_DIR (failed; b.nje) or the
// opt-out (skipped) leaves the file, and each launches, reporting PreTrust
// (JSON pre_trust). A retry after a restored failure keeps no_pre_trust.
func TestResumePreTrust(t *testing.T) {
	// Serial: it changes the working directory and PWD (cwdfix.Temp).
	optOut := []apitest.SpawnOption{apitest.WithNoPreTrust()}
	cases := []struct {
		name         string
		file         trustFile
		opts         []apitest.SpawnOption
		trusted      bool // the entry is written; otherwise the file is left as seeded
		wantPreTrust string
		relative     bool // CLAUDE_CONFIG_DIR is "rel" (seedRelativeTrustConfig)
		retry        bool // the first create fails and is restored; the retry launches
	}{
		{"allowed row, entry lacking", trustLacksEntry, nil, true, "ok", false, false},
		{".claude.json missing", trustMissing, nil, false, "failed", false, false},
		{"opted-out row", trustLacksEntry, optOut, false, "skipped", false, false},
		{"opted-out row, unusual stored value", trustLacksEntry, []apitest.SpawnOption{apitest.WithRawNoPreTrust("sometimes")},
			false, "skipped", false, false},
		{"relative CLAUDE_CONFIG_DIR, persisted path rotted", trustLacksEntry,
			[]apitest.SpawnOption{apitest.WithJsonlPath(filepath.Join(t.TempDir(), "gone", "rotted.jsonl"))}, false, "failed", true, false},
		{"allowed row pre-trusts again on the retry", trustLacksEntry, nil, true, "ok", false, true},
		{"opted-out row stays skipped on the retry", trustLacksEntry, optOut, false, "skipped", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			var c trustConfig
			if tc.relative {
				c = seedRelativeTrustConfig(t, tc.file)
			} else {
				c = seedTrustConfig(t, t.TempDir(), tc.file)
			}
			r := e.seedResumable(t, store.StateEnded, append([]apitest.SpawnOption{c.env()}, tc.opts...)...)
			when, creates := "resume", 1
			if tc.retry {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
				_, err := rptResume(t, e, r, c, tc.trusted, "first resume")
				assertLaunchSentinel(t, err, tmux.ErrTmuxSessionCreate)
				if got := e.columns(t, r.ID); got.State != r.Before.State || got.NoPreTrust != r.Before.NoPreTrust {
					t.Fatalf("after the restore: state %v, no_pre_trust %#v; want %v, %#v", got.State, got.NoPreTrust, r.Before.State, r.Before.NoPreTrust)
				}
				if tc.trusted {
					c.reset(t) // so the retry's entry is its own
				}
				when, creates = "retry", 2
			}

			res, err := rptResume(t, e, r, c, tc.trusted, when)

			if err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			if res.PreTrust != tc.wantPreTrust {
				t.Errorf("%s: ResumeResult.PreTrust = %q; want %q", when, res.PreTrust, tc.wantPreTrust)
			}
			checkPreTrustJSON(t, res, tc.wantPreTrust)
			c.check(t, r.CWD, tc.trusted, "after the "+when)
			if st := e.columns(t, r.ID).State; st != store.StatePending {
				t.Errorf("state = %v; want pending", st)
			}
			cs := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(cs) != creates {
				t.Fatalf("creates = %d; want %d", len(cs), creates)
			}
			cmd := cs[len(cs)-1].Command
			if i := slices.Index(cmd, "--resume"); i < 0 || i+1 >= len(cmd) || cmd[i+1] != r.SessionID {
				t.Errorf("argv = %q; want --resume %s", cmd, r.SessionID)
			}
		})
	}
}
