package api_test

// resume_pretrust_test.go covers resume's pre-trust (SR-22.6, SR-8.1 step 4,
// SR-5.1/5.2; AC-RES-19, AC-RES-20) on a per-test CLAUDE_CONFIG_DIR. A refusal
// writing no entry is the refusal tests'; which file a row names, and an
// unwritable one, are internal/spawn's pre-trust tests'.

import (
	"encoding/json"
	"maps"
	"os"
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
// opt-out (skipped) leaves the file, and each launches under the row's
// CLAUDE_CONFIG_DIR, reporting PreTrust (JSON pre_trust). A retry after a
// restored failure keeps no_pre_trust.
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
			if got, ok := cs[len(cs)-1].Envs["CLAUDE_CONFIG_DIR"]; !ok || got != c.dir {
				t.Errorf("create env CLAUDE_CONFIG_DIR = %q (set %v); want %q", got, ok, c.dir)
			}
		})
	}
}

// TestInheritedConfigDirFollowsTheRow pins b.uzn's resume rule: a claude spawn
// or reuse whose extra env lacks CLAUDE_CONFIG_DIR records the one it inherited
// (dir A) on the row beside the given keys, leaving the caller's map as it was,
// so a resume by a caller with it unset or set to dir B pre-trusts A's
// .claude.json and launches under A; B's and $HOME's files are untouched. A
// relative inherited value is recorded too, and both launches report failed.
// A spawn that inherits none records none, and its resume by a caller set to B
// pre-trusts $HOME's .claude.json and launches without one, as before b.uzn.
// The resumed row is seeded with the extra_env text the first launch stored.
func TestInheritedConfigDirFollowsTheRow(t *testing.T) {
	// Serial: it sets HOME and CLAUDE_CONFIG_DIR with t.Setenv, and the working directory (cwdfix.Temp) in the relative cell.
	cases := []struct {
		name     string
		reuse    bool // the first launch is a reuse of a finished row, not a plain spawn
		relative bool // A is "rel" (seedRelativeTrustConfig)
		resumerB bool // the resuming caller sets CLAUDE_CONFIG_DIR to B; else it is unset
		keyless  bool // the first launch inherits no CLAUDE_CONFIG_DIR, so the row records none
	}{
		{"spawn, resumed with it unset", false, false, false, false},
		{"reuse, resumed with B", true, false, true, false},
		{"relative spawn, resumed with it unset", false, true, false, false},
		{"spawn without it, resumed with B", false, false, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newReuseEnv(t)
			c, _ := e.client(t)
			home := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			t.Setenv("HOME", home.dir)
			a := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			want := "ok"
			if tc.relative {
				a, want = seedRelativeTrustConfig(t, trustLacksEntry), "failed"
			}
			b := seedTrustConfig(t, t.TempDir(), trustLacksEntry)
			wantRow := map[string]string{"KEEP": "1"}
			if !tc.keyless {
				t.Setenv("CLAUDE_CONFIG_DIR", a.dir)
				wantRow["CLAUDE_CONFIG_DIR"] = a.dir
			}

			given := map[string]string{"KEEP": "1"}
			p := api.SpawnParams{Agent: "claude", CWD: t.TempDir(), ExtraEnv: given}
			if tc.reuse {
				p = reuseParams(t, e.seedReusable(t, agentGone, reuseRowSpec{}), reuseRequest{})
				p.ExtraEnv = given
			}
			var res api.SpawnResult
			var err error
			captureStderr(t, func() {
				if tc.reuse {
					res, _, err = e.reuse(t, p)
				} else {
					res, err = c.Spawn(p)
				}
			})
			if err != nil {
				t.Fatalf("first launch: %v", err)
			}
			if res.PreTrust != want {
				t.Errorf("SpawnResult.PreTrust = %q; want %q", res.PreTrust, want)
			}
			if !maps.Equal(given, map[string]string{"KEEP": "1"}) {
				t.Errorf("caller's ExtraEnv = %v after the launch; want unchanged {KEEP: 1}", given)
			}
			stored, _ := e.columns(t, res.InstanceID).ExtraEnv.(string)
			var row map[string]string
			if err := json.Unmarshal([]byte(stored), &row); err != nil || !maps.Equal(row, wantRow) {
				t.Fatalf("stored extra_env = %q (%v); want %v", stored, err, wantRow)
			}

			if tc.resumerB {
				t.Setenv("CLAUDE_CONFIG_DIR", b.dir)
			} else {
				_ = os.Unsetenv("CLAUDE_CONFIG_DIR") // t.Setenv above restores it
			}
			r := e.seedResumable(t, rlkSettled(e), agentGone, apitest.WithRawExtraEnv(stored))
			before := len(e.rec.SocketCallsOf(tmux.CallCreate))
			var rr api.ResumeResult
			captureStderr(t, func() { rr, err = c.Resume(api.ResumeParams{InstanceID: r.ID}) })

			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if rr.PreTrust != want {
				t.Errorf("ResumeResult.PreTrust = %q; want %q", rr.PreTrust, want)
			}
			a.check(t, r.CWD, !tc.relative && !tc.keyless, "after the resume (A)")
			b.check(t, r.CWD, false, "after the resume (B)")
			home.check(t, r.CWD, tc.keyless, "after the resume ($HOME)")
			cs := e.rec.SocketCallsOf(tmux.CallCreate)
			if len(cs) != before+1 {
				t.Fatalf("creates = %d; want %d", len(cs), before+1)
			}
			got, ok := cs[before].Envs["CLAUDE_CONFIG_DIR"]
			if tc.keyless && ok {
				t.Errorf("resume create env CLAUDE_CONFIG_DIR = %q; want none", got)
			} else if !tc.keyless && (!ok || got != a.dir) {
				t.Errorf("resume create env CLAUDE_CONFIG_DIR = %q (set %v); want %q", got, ok, a.dir)
			}
		})
	}
}

// TestResumeCodexPreTrust (SR-5.4, SR-5.6): a resume of a Codex row decides
// pre_trust from the row's recorded no_pre_trust and its stored extra_env's
// CODEX_HOME, the call carrying neither: the same ok, skipped and failed as a
// spawn's, the failed warning printed once to the caller and the others none.
// The row is launched, and neither the Codex folder's tree nor the row's
// .claude.json is touched.
func TestResumeCodexPreTrust(t *testing.T) {
	// Serial: it sets HOME and the working directory with t.Setenv, and redirects stderr.
	for _, tc := range codexPreTrustCases() {
		t.Run(tc.name, func(t *testing.T) {
			e := newResumeEnv(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			homeJSON := filepath.Join(home, ".claude.json")
			if err := os.WriteFile(homeJSON, []byte("{}"), 0o600); err != nil {
				t.Fatalf("write home .claude.json: %v", err)
			}
			folder := tc.seed(t, home)
			env := apitest.WithExtraEnv(folder.extraEnv())
			if tc.give != nil {
				env = apitest.WithExtraEnv(tc.give)
			}
			opts := codexRowOpts(env)
			if tc.noPreTrust {
				opts = append(opts, apitest.WithNoPreTrust())
			}
			r := e.seedResumable(t, store.StateEnded, opts...)

			var res api.ResumeResult
			var err error
			warn := captureStderr(t, func() { res, err = e.resume(r.ID) })

			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if res.PreTrust != tc.wantPreTrust {
				t.Errorf("ResumeResult.PreTrust = %q; want %q", res.PreTrust, tc.wantPreTrust)
			}
			checkPreTrustJSON(t, res, tc.wantPreTrust)
			if warn != tc.warning() {
				t.Errorf("warning = %q; want %q", warn, tc.warning())
			}
			got := e.columns(t, r.ID)
			if got.State != store.StatePending || got.NoPreTrust != r.Before.NoPreTrust || got.Agent != "codex" {
				t.Errorf("row after the resume: state %v, no_pre_trust %#v, agent %q; want pending, %#v, codex",
					got.State, got.NoPreTrust, got.Agent, r.Before.NoPreTrust)
			}
			if n := len(e.rec.SocketCallsOf(tmux.CallCreate)); n != 1 {
				t.Errorf("creates = %d; want 1", n)
			}
			folder.assertUnchanged(t, "after the resume")
			if b, err := os.ReadFile(homeJSON); err != nil || string(b) != "{}" {
				t.Errorf("home .claude.json = %q (err %v); want untouched {}", b, err)
			}
		})
	}
}
