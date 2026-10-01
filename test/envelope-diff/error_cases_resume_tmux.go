// error_cases_resume_tmux.go holds the resume error rows whose error comes
// from tmux (SR-8.1 step 3, SR-20.5): each seeds a resumable ended row, with
// its transcript, recording a private socket whose fake-tmux table the row
// writes, so both runners see the same tmux and nothing is written.
package envelope_diff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	// resumeHeldID is the ended row whose recorded name is held on its
	// socket by a session with no valid label.
	resumeHeldID = "id-err-rsc-1"
	// resumeHeldSessID is the row's claude session id, whose transcript
	// the seed writes.
	resumeHeldSessID = "session-uuid-err-rsc-1"
	// resumeHeldName, resumeHeldSessionID and resumeHeldCreated describe
	// the unlabelled session holding the row's recorded name.
	resumeHeldName      = "err-rsc-held"
	resumeHeldSessionID = "$6"
	resumeHeldCreated   = 1790549184
	// ctxLaunchToken is the ctx key under which the seed passes the row's
	// launch token on to its desc (a value the description must not carry).
	ctxLaunchToken = "launch_token"
)

// resumeParams and resumeArgv are the resume call on id for each runner.
func resumeParams(id string) map[string]any { return map[string]any{"claude_instance_id": id} }
func resumeArgv(id string) []string         { return []string{"resume", "--claude-instance-id", id} }

// resumeTmuxErrorCases are appended to errorCases.
var resumeTmuxErrorCases = []errorCase{

	// ── resume / ErrTmuxSessionConflict ───────────────────────────────────
	// Gone with no recorded agent process, and a session with no valid
	// label holds the row's recorded name: resume refuses before the move,
	// with the pre-launch no-valid-id holder description (SR-1.4, SR-3.10).
	{
		verb:    "resume",
		errName: "ErrTmuxSessionConflict",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			socket, tables := usePrivateFakeTmux(t)
			jsonl := apitest.SeedJsonlUnder(t, t.TempDir(), "/tmp", resumeHeldSessID)
			dbPath := filepath.Join(t.TempDir(), "state.db")
			if _, err := apitest.SeedSpawn(dbPath, resumeHeldID, store.StateEnded, "", "", resumeHeldSessID, true,
				apitest.WithTmuxSessionName(resumeHeldName),
				apitest.WithTmuxSocket(socket),
				apitest.WithJsonlPath(jsonl)); err != nil {
				t.Fatalf("seed resume row: %v", err)
			}
			storeID, err := apitest.ReadStoreID(dbPath)
			if err != nil {
				t.Fatalf("read store id: %v", err)
			}
			cols, err := apitest.ReadSpawnColumns(dbPath, resumeHeldID)
			if err != nil {
				t.Fatalf("read resume row: %v", err)
			}
			token, _ := cols.LaunchToken.(string)
			tables.Write(t, socket, faketmuxfix.Table{
				Server: &faketmuxfix.Server{PID: os.Getpid(), Start: resumeHeldCreated},
				Sessions: []faketmuxfix.Session{{
					ID: resumeHeldSessionID, Created: resumeHeldCreated, Name: resumeHeldName,
					Panes: []faketmuxfix.Pane{{ID: "%6", PID: os.Getpid()}},
				}},
			})
			return filepath.Dir(dbPath), map[string]any{ctxStoreID: storeID, ctxLaunchToken: token}
		},
		params:  func(_ map[string]any) map[string]any { return resumeParams(resumeHeldID) },
		cliArgv: func(_ map[string]any) []string { return resumeArgv(resumeHeldID) },
		desc: func(ctx map[string]any) (apitest.DescCase, []string) {
			c := apitest.DescHeldNoValidID(apitest.HeldName{
				Name: resumeHeldName, SessionID: resumeHeldSessionID, BeforeLaunch: true,
			})
			var forbid []string
			for _, k := range []string{ctxStoreID, ctxLaunchToken} {
				if v, _ := ctx[k].(string); v != "" {
					forbid = append(forbid, v)
				}
			}
			return c, forbid
		},
	},
}
