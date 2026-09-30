package spawn

import (
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// RelaunchInput is what a resume launch is composed from (SR-8.1): the row
// resume read, the session to resume, and the launch's socket, token and
// store id.
//
// SessionID is the claude_session_id the `claude --resume <session id>` argv
// element names: the row's own, or an earlier session of the visible history
// whose transcript resume recovered. Socket is the launch's socket
// (ResolveRowLaunchSocket), Token the new launch token (NewLaunchToken) and
// StoreID this store's id ((*store.Store).StoreID(), read by the caller when
// the store opened; internal/spawn neither opens the store nor reads it).
type RelaunchInput struct {
	Row       store.Spawn
	SessionID string
	Socket    string
	Token     string
	StoreID   string
}

// ComposeRelaunch composes a resume launch without any tmux call or store
// write, so resume can run it before its move to pending and a composition
// failure writes nothing (SR-8.1, SR-8.3). It returns the create request
// Relaunch runs: the row's recorded session name verbatim ($ and \ names
// included), its cwd, the environment, and the argv
//
//	claude --resume <session id> --settings <inline-json> [row claude_args]
//
// on in.Socket, labelled with in.Token, the row's instance id and in.StoreID.
//
// The environment and the synthesized settings (the same hooks as a fresh
// spawn) are composed from the row. ExtraEnv is restored from the persisted
// row (in.Row.ExtraEnv, decoded by GetSpawn) so a resumed spawn keeps its
// original env — including CLAUDE_CONFIG_DIR and any auth vars. This is the
// persist-all posture: those values may sit at rest in the DB, but the store
// file is already forced 0600 in a 0700 dir on every open, so restoring them
// here opens no new exposure tier. Permissions, by contrast, is NOT stored,
// so it cannot be reconstructed and stays nil; the caller's shell env
// propagates auth via tmux's default behavior for anything not captured in
// ExtraEnv. AGENT_DIRECTOR_INSTANCE_ID reaches the create argv exactly once,
// as for a plain spawn (the client drops the environment's copy).
func ComposeRelaunch(in RelaunchInput, cfg config.Config) (CreateRequest, error) {
	// Synthesize a Resolved for the env/settings helpers. Permissions is
	// intentionally nil: it isn't stored on the row, so a resume can't carry
	// it over (matches SRD §8.1's contract).
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID:    in.Row.ClaudeInstanceID,
		CWD:                 in.Row.CWD,
		TmuxSessionName:     in.Row.TmuxSessionName,
		RelayMode:           in.Row.RelayMode,
		ClaudeArgs:          in.Row.ClaudeArgs,
		AgentDirectorLabels: in.Row.Labels,
		ExtraEnv:            in.Row.ExtraEnv,
	}}

	envs := composeEnv(r)
	settings, err := synthesizeSettings(r, cfg)
	if err != nil {
		return CreateRequest{}, err
	}

	command := []string{claudeBinary, "--resume", in.SessionID, "--settings", settings}
	command = append(command, r.ClaudeArgs...)

	return CreateRequest{
		Socket:     in.Socket,
		Name:       r.TmuxSessionName,
		CWD:        r.CWD,
		Env:        envs,
		Command:    command,
		Token:      in.Token,
		InstanceID: r.ClaudeInstanceID,
		StoreID:    in.StoreID,
	}, nil
}

// Relaunch is resume's session-creating step (SR-3.5, SR-8.1 step 6): the
// shared create-and-label step (CreateAndLabel) for a request ComposeRelaunch
// built, so the session is labelled at creation with the five-field
// "ad1 <token> <session id> <instance id> <store id>" label on the row's
// socket, a $ or \ name is labelled by id, and a failed label step is
// relabelled once by id or the session is killed by id. It returns the typed
// outcome unchanged: resume maps it to its errors, with the restore's result,
// in pkg/api. On CreateLabelled the caller makes the identity write
// (RecordLaunchIdentity) with the move's version and token.
//
// Relaunch does not wait for Claude to come up. The caller (resume) has
// moved the row to pending before the create, writing the parent id; the row
// stays pending until the resumed agent's first SessionStart takes it to
// waiting.
func Relaunch(t LaunchTmux, req CreateRequest) CreateOutcome {
	return CreateAndLabel(t, req)
}
