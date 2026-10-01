package spawn

import (
	"os"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// envInstanceID names the env var ParentIDFromEnv reads for the new life's
// parent_id (SRD §7.5). When set, the caller is itself a Spawn whose Claude
// shell is invoking us; the value becomes the row's parent.
const envInstanceID = "AGENT_DIRECTOR_INSTANCE_ID"

// ParentIDFromEnv returns the parent id a launch records: the caller's
// AGENT_DIRECTOR_INSTANCE_ID, unchanged ("" is stored as NULL). It is the one
// derivation of the parent id, used by a spawn's insert and a reuse's reset
// (through ComposeLaunch) and by resume's move to pending.
func ParentIDFromEnv() string {
	return os.Getenv(envInstanceID)
}

// ComposedLaunch is everything a launch of a resolved request needs before
// the write that begins it: the create request and the row's request fields.
//
// Create carries the session name, cwd, composed environment, argv
//
//	claude --settings <inline-json> [claude args]
//
// and instance id; its Socket, Token and StoreID are zero, for the caller
// to fill with the launch's socket, token and this store's id.
//
// Row carries the request fields the row records (instance id, cwd, session
// name, Claude args, relay mode, labels, extra env), the request's
// NoPreTrust (the choice the caller passes to PreTrust, so the row records
// what the launch did, SR-22.6) and the parent id from the caller's
// AGENT_DIRECTOR_INSTANCE_ID ("" is stored as NULL). Its launch start, start
// time and Identity (token, socket) are zero, for the caller to fill from
// the launch.
type ComposedLaunch struct {
	Create CreateRequest
	Row    store.Spawn
}

// ComposeLaunch composes the launch of r, a resolved, validated and
// defaulted request, for plain spawn's insert and for reuse's reset
// (SR-10.3, SR-22.2). It makes no store write, tmux call, file or network
// I/O; it reads only the process environment for the parent id. So every
// composition failure (a settings synthesis error) comes before the write
// that begins the launch and writes nothing, and between that write and the
// create there is only in-process work (SR-13.4).
//
// The environment carries AGENT_DIRECTOR_INSTANCE_ID; the create's own -e
// entry carries it too and the client drops the environment's copy, so it
// reaches the create argv exactly once (tmux.NewSession).
func ComposeLaunch(r Resolved, cfg config.Config) (ComposedLaunch, error) {
	settings, err := synthesizeSettings(r, cfg)
	if err != nil {
		return ComposedLaunch{}, err
	}
	command := []string{claudeBinary, "--settings", settings}
	command = append(command, r.ClaudeArgs...)

	return ComposedLaunch{
		Create: CreateRequest{
			Name:       r.TmuxSessionName,
			CWD:        r.CWD,
			Env:        composeEnv(r),
			Command:    command,
			InstanceID: r.ClaudeInstanceID,
		},
		Row: store.Spawn{
			ClaudeInstanceID: r.ClaudeInstanceID,
			ParentID:         ParentIDFromEnv(),
			CWD:              r.CWD,
			TmuxSessionName:  r.TmuxSessionName,
			ClaudeArgs:       r.ClaudeArgs,
			RelayMode:        r.RelayMode,
			Labels:           r.AgentDirectorLabels,
			ExtraEnv:         r.ExtraEnv,
			NoPreTrust:       r.NoPreTrust,
		},
	}, nil
}
