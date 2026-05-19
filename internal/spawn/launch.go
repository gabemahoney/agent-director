package spawn

import (
	"fmt"
	"os"
	"strings"

	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/store"
)

// TmuxClient is the narrow tmux surface Launch needs. *tmux.Client
// satisfies it; tests pass a fake that records argv without launching
// real tmux.
type TmuxClient interface {
	NewSession(name, cwd string, envs map[string]string, command []string) error
}

// claudeBinary is the program tmux launches inside the new session. Held
// as a var so tests can swap it for a fake-claude helper without
// monkey-patching the spawn flow.
var claudeBinary = "claude"

// envInstanceID names the env var Launch reads to populate parent_id on
// the new row (SRD §7.5). When set, the caller is itself a Spawn whose
// Claude shell is invoking us; the value becomes the new row's parent.
const envInstanceID = "CLAUDE_DIRECTOR_INSTANCE_ID"

// Launch is the fire-and-forget half of `spawn` (SRD §7.4). The function:
//
//  1. Composes the env-var map for the tmux session.
//  2. Synthesizes --settings inline JSON.
//  3. Builds the claude argv (claude, --settings, <json>, ...user args).
//  4. INSERTs the pending row.
//  5. Calls TmuxClient.NewSession.
//
// On tmux failure the row remains `pending` (find-missing sweeps it in
// Epic 8). On INSERT failure ErrInstanceIdCollision surfaces if the
// underlying error mentions UNIQUE/PRIMARY KEY; otherwise the raw error
// wraps through.
//
// The function does not wait for Claude to come up. The row state stays
// `pending` until the first SessionStart hook flips it (SRD §7.4
// fire-and-forget contract).
func Launch(s *store.Store, tmuxClient TmuxClient, r Resolved, cfg config.Config) (string, error) {
	envs := composeEnv(r)
	settings, err := synthesizeSettings(r, cfg)
	if err != nil {
		return "", err
	}

	command := []string{claudeBinary, "--settings", settings}
	command = append(command, r.ClaudeArgs...)

	// parent_id is auto-detected from our own env (SRD §7.5). Empty is
	// fine; InsertPending writes NULL in that case.
	parent := os.Getenv(envInstanceID)

	row := store.Spawn{
		ClaudeInstanceID: r.ClaudeInstanceID,
		ParentID:         parent,
		CWD:              r.CWD,
		TmuxSessionName:  r.TmuxSessionName,
		ClaudeArgs:       r.ClaudeArgs,
		RelayMode:        r.RelayMode,
		Labels:           r.ClaudeDirectorLabels,
	}
	if err := s.InsertPending(row); err != nil {
		// SQLite's UNIQUE / PRIMARY KEY collision text is driver-specific;
		// matching the substring keeps the surface stable across driver
		// versions. The pre-check in ApplyDefaults catches most races; this
		// is the belt-and-suspenders fallback for racing callers.
		if isPrimaryKeyCollision(err) {
			return "", fmt.Errorf("%w: %s", ErrInstanceIdCollision, r.ClaudeInstanceID)
		}
		return "", err
	}

	if err := tmuxClient.NewSession(r.TmuxSessionName, r.CWD, envs, command); err != nil {
		// On tmux failure the row stays pending — find-missing (Epic 8)
		// will reconcile. Surface the tmux error to the caller so the
		// CLI exits non-zero with a typed error envelope.
		return "", err
	}

	return r.ClaudeInstanceID, nil
}

// isPrimaryKeyCollision matches SQLite's driver-emitted error text for
// PRIMARY KEY / UNIQUE violations. modernc/sqlite does not surface a
// machine-readable code through the standard error chain, so substring
// match is pragmatic and stable enough for the surface mapping.
func isPrimaryKeyCollision(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, sub := range []string{
		"UNIQUE constraint failed",
		"PRIMARY KEY",
		"constraint failed: spawns.claude_instance_id",
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}
