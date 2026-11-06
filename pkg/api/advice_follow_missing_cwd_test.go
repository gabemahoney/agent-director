package api_test

// advice_follow_missing_cwd_test.go follows, literally, the advice resume's
// refusal of a row whose saved cwd is gone gives (b.vit; B16): trigger the
// refusal, pin the advice, do what it says, check the promised outcome. The
// refusals themselves are covered in resume_missing_cwd_test.go, Cursor's
// follow in advice_follow_cursor_resume_test.go.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// The advice B16 pins, exactly as the description or Go doc words it.
const (
	advCwdRecreate = "nothing was written and nothing was launched; recreate that directory and resume again"
	advCwdGoDoc    = "Recreate the directory at that path and resume again, or spawn the same id with the reuse opt-in, the row's agent and an existing cwd, which starts a fresh conversation in the retained life"
)

// advCwdReuse is B16's spawn-again advice for a row of agent: the reuse opt-in and that agent word, then an existing cwd.
func advCwdReuse(agent string) string {
	return "or spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and agent " + agent +
		" (--agent " + agent + " on the CLI) and an existing cwd (same-agent reuse starts a fresh conversation in the retained life; older sessions remain listed for inspection only)"
}

// TestAdviceFollow_B16_MissingCwdRecreateOrSpawnWithReuse: while the saved cwd
// is gone, resume is refused the same way each time and writes nothing; once
// the directory is recreated at that path, resume launches there. On another
// such row, a spawn of the id with the reuse opt-in, the row's agent and an
// existing cwd launches in that cwd.
// B16 (b.vit): "nothing was written and nothing was launched; recreate that directory and resume again, or spawn the id with the reuse opt-in reuse_finished (--reuse-finished on the CLI) and agent claude (--agent claude on the CLI) and an existing cwd (...)"
func TestAdviceFollow_B16_MissingCwdRecreateOrSpawnWithReuse(t *testing.T) {
	// Serial: it checks every record written to the shared trail since its mark.
	adviceAssertGoDoc(t, "resume.go", "Resume", advCwdGoDoc)
	for _, agent := range []string{"claude", "codex"} {
		t.Run(agent+" row, recreate the directory", func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), Bare: true, Opts: agentRowOpts(agent)})
			if err := os.RemoveAll(r.CWD); err != nil {
				t.Fatalf("remove cwd: %v", err)
			}
			before := e.snapshotResume(t, r.resumeRow)

			_, refused := e.resume(r.ID)

			adviceAssertAdvice(t, refused, spawn.ErrCwdNotFound, advCwdRecreate)
			adviceAssertPhrase(t, refused, advCwdReuse(agent))
			e.assertResumeWroteNothing(t, before)

			before = e.snapshotResume(t, r.resumeRow)
			if _, err := e.resume(r.ID); err == nil || err.Error() != refused.Error() {
				t.Errorf("resume while the cwd is still gone = %v; want the same refusal %q", err, refused)
			}
			e.assertResumeWroteNothing(t, before)

			if err := os.Mkdir(r.CWD, 0o700); err != nil {
				t.Fatalf("recreate cwd: %v", err)
			}
			advResumeLaunches(t, e, r.ID)
			advCwdAssertCreateIn(t, e.rec.SocketCallsOf(tmux.CallCreate), r.CWD)
		})
		t.Run(agent+" row, spawn with the reuse opt-in", func(t *testing.T) {
			e := newKillEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Age: rlkSettled(e), Opts: agentRowOpts(agent)})
			if err := os.RemoveAll(r.CWD); err != nil {
				t.Fatalf("remove cwd: %v", err)
			}
			before := e.snapshotResume(t, r.resumeRow)

			_, err := e.resume(r.ID)

			adviceAssertAdvice(t, err, spawn.ErrCwdNotFound, advCwdReuse(agent))
			e.assertResumeWroteNothing(t, before)

			cwd, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatalf("EvalSymlinks: %v", err)
			}
			p := reuseParams(t, r, reuseRequest{CWD: cwd})
			p.Agent = agent
			res, logs, err := e.reuse(t, p)
			if err != nil {
				t.Fatalf("spawn of %s with the reuse opt-in, agent %s and an existing cwd = %v (log %q); want it launched", r.ID, agent, err, logs)
			}
			creates := e.rec.SocketCallsOf(tmux.CallCreate)
			if agent == "codex" {
				assertCodexLaunch(t, creates, e.dbPath, r.ID, codexProgram)
			} else {
				advSpawnLaunched(t, e.dbPath, r.ID, advSpawnNextLife(before.cols), res, err)
			}
			advCwdAssertCreateIn(t, creates, cwd)
			if got := e.columns(t, r.ID).CWD; got != cwd {
				t.Errorf("row cwd = %v; want %q", got, cwd)
			}
		})
	}
}

// advCwdAssertCreateIn fails unless creates holds exactly one create, starting its session in cwd (-c).
func advCwdAssertCreateIn(t *testing.T, creates []tmuxfix.SocketCall, cwd string) {
	t.Helper()
	if len(creates) != 1 || creates[0].Cwd != cwd {
		t.Fatalf("creates = %+v; want one, in %q", creates, cwd)
	}
}
