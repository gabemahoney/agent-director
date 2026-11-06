package api_test

// resume_missing_cwd_test.go covers b.vit (GitHub #37): tmux falls back to
// another directory when the create's -c path cannot be entered, so resume of
// a finished row whose saved cwd was removed, or replaced by a file, launched
// the agent elsewhere and reported success. Resume refuses it as spawn does,
// with ErrCwdNotFound or ErrCwdNotADirectory, before anything is written or
// launched. The Cursor case is in resume_cursor_test.go, the real-tmux one in
// test/realtmux.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestResumeRefusesUnusableCwd: an ended or missing claude or codex row whose
// saved cwd no longer exists is ErrCwdNotFound, and one whose cwd is now a
// regular file ErrCwdNotADirectory, before the transcript lookup: quoting the
// cwd, saying nothing was written or launched and naming the recourse; no tmux
// call, nothing written (row, history, trail), no .claude.json touched.
func TestResumeRefusesUnusableCwd(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	cases := []struct {
		name, agent, state string
		file               bool // the cwd is replaced by a regular file, not removed
		noTranscript       bool // the row's transcript is removed too, so the lookup would fail
	}{
		{name: "claude ended, cwd removed", agent: store.AgentClaude, state: store.StateEnded},
		{name: "claude missing, cwd removed", agent: store.AgentClaude, state: store.StateMissing},
		{name: "claude ended, cwd removed, transcript gone", agent: store.AgentClaude, state: store.StateEnded, noTranscript: true},
		{name: "claude ended, cwd is a file", agent: store.AgentClaude, state: store.StateEnded, file: true},
		{name: "claude missing, cwd is a file", agent: store.AgentClaude, state: store.StateMissing, file: true},
		{name: "codex ended, cwd removed", agent: store.AgentCodex, state: store.StateEnded},
		{name: "codex missing, cwd is a file", agent: store.AgentCodex, state: store.StateMissing, file: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newKillEnv(t)
			e.rowAgent = tc.agent
			cwd := filepath.Join(t.TempDir(), "ad-gone")
			if err := os.Mkdir(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			spec := e.resumableSpec(rlkSettled(e), agentGone)
			spec.State, spec.CWD = tc.state, cwd
			row := e.seedResumableRow(t, spec)
			removeCwd(t, cwd, tc.file)
			if tc.noTranscript {
				if err := os.Remove(row.JSONLPath); err != nil {
					t.Fatalf("remove transcript: %v", err)
				}
			}
			before := e.snapshotResume(t, row)

			_, err := e.resume(row.ID)

			want := "ErrCwdNotFound"
			if tc.file {
				want = "ErrCwdNotADirectory"
			}
			assertOneName(t, err, want)
			assertCwdRefusalText(t, errText(err), cwd, spawn.ReuseSpawn(tc.agent))
			e.assertKillCalls(t)
			e.assertResumeWroteNothing(t, before)
		})
	}
}

// removeCwd removes the directory cwd and, with file set, writes a regular
// file in its place.
func removeCwd(t *testing.T, cwd string, file bool) {
	t.Helper()
	if err := os.RemoveAll(cwd); err != nil {
		t.Fatalf("remove cwd: %v", err)
	}
	if file {
		if err := os.WriteFile(cwd, []byte("not a directory\n"), 0o600); err != nil {
			t.Fatalf("write file at cwd: %v", err)
		}
	}
}

// assertCwdRefusalText fails unless desc, a refused resume's description,
// quotes cwd, says nothing was written or launched, and names both ways on:
// recreating the directory, or recourse (a spawn of the id, or Cursor's fresh
// recovery).
func assertCwdRefusalText(t *testing.T, desc, cwd, recourse string) {
	t.Helper()
	for _, part := range []string{strconv.Quote(cwd), "nothing was written and nothing was launched", "recreate", recourse} {
		if !strings.Contains(desc, part) {
			t.Errorf("description %q\nwant it to contain %q", desc, part)
		}
	}
}
