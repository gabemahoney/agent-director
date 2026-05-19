package spawn

import (
	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/store"
)

// RelaunchInput captures the SRD §8.1 inputs Resume needs to relaunch
// a terminated Spawn. Most fields are pulled from the stored row by
// the caller; `Parent` is freshly re-derived from the resume caller's
// env (SRD §7.5 — `parent_id` records "who currently owns this
// Spawn," not the original creator).
//
// `SessionID` is the existing claude_session_id used to compose the
// `claude --resume <session_id>` argv element.
type RelaunchInput struct {
	Row       store.Spawn
	Parent    string
	SessionID string
}

// Relaunch is the resume-time analogue of Launch. It composes env +
// synthesized settings (same hooks as a fresh spawn — Permissions and
// ExtraEnv are NOT stored, so they cannot be reconstructed; the
// caller's shell env propagates auth via tmux's default behavior),
// then `tmux new-session -d` with the resume argv:
//
//	claude --resume <session_id> --settings <inline-json> [user claude_args]
//
// The function is fire-and-forget: it returns after tmux acknowledges
// the session creation. The first SessionStart hook from the
// resurrected Claude is what flips the row's state back to `waiting`
// and rotates `claude_session_id` to whatever Claude Code mints on
// the new run.
//
// On tmux error the function returns the wrapped tmux error. The
// caller (api.Resume) has already done the state guards and the
// session-name collision check, so the only expected tmux failure is
// a transient one or a corrupted server state.
func Relaunch(in RelaunchInput, tmuxClient TmuxClient, cfg config.Config) error {
	// Synthesize a Resolved for the env/settings helpers. Permissions
	// is intentionally nil — they aren't stored on the row, so a
	// resume can't carry them over (matches SRD §8.1's contract).
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID:     in.Row.ClaudeInstanceID,
		CWD:                  in.Row.CWD,
		TmuxSessionName:      in.Row.TmuxSessionName,
		RelayMode:            in.Row.RelayMode,
		ClaudeArgs:           in.Row.ClaudeArgs,
		ClaudeDirectorLabels: in.Row.Labels,
	}}

	envs := composeEnv(r)
	settings, err := synthesizeSettings(r, cfg)
	if err != nil {
		return err
	}

	command := []string{claudeBinary, "--resume", in.SessionID, "--settings", settings}
	command = append(command, r.ClaudeArgs...)

	return tmuxClient.NewSession(r.TmuxSessionName, r.CWD, envs, command)
}
