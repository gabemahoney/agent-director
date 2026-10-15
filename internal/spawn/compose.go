package spawn

import (
	"os"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/inputdelivery"
	"github.com/gabemahoney/agent-director/internal/store"
)

// envInstanceID names the env var ParentIDFromEnv reads for the new launch's
// parent_id (SRD §7.5). When set, the caller is itself a Spawn whose agent
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
//	claude --settings <inline-json> [agent args]
//
// for a claude agent (claude is [launch] claude_path when set), or
//
//	codex [agent args before --] <per-launch settings> [-- and what follows]
//
// for a codex agent (codex is [launch] codex_path when set; codexlaunch.go),
// and instance id; its Socket, Token and StoreID are zero, for the caller
// to fill with the launch's socket, token and this store's id.
//
// Row carries the request fields the row records (instance id, cwd, session
// name, agent args, relay mode, labels, extra env), the request's
// NoPreTrust (the choice the caller passes to RunPreTrust, so the row records
// what the launch did, SR-22.6) and the parent id from the caller's
// AGENT_DIRECTOR_INSTANCE_ID ("" is stored as NULL). Its launch start, start
// time and Identity (token, socket) are zero, for the caller to fill from
// the launch.
//
// PreTrust is the pre-trust outcome a codex launch was composed for (the folder
// trust and the hook-review bypass are in its argv only on PreTrustOK), decided
// by CodexPreTrust before composition; "" for a claude launch, whose pre-trust
// is a file write that follows composition. The caller hands it to RunPreTrust,
// so the outcome reported is the one the argv carries.
type ComposedLaunch struct {
	Create   CreateRequest
	Row      store.Spawn
	PreTrust PreTrustOutcome
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
//
// The argv starts with the agent's launch path (claudeProgram, codexProgram).
// A codex agent's composition also writes nothing in any Codex folder: its
// settings travel on the command line, and its pre-trust outcome is decided
// here (CodexPreTrust) because the folder trust is in the argv only on ok. An
// agent that is neither claude nor codex gets store.ErrRowAgentInvalid.
func ComposeLaunch(r Resolved, cfg config.Config) (ComposedLaunch, error) {
	if err := launchAgentError(r.Agent); err != nil {
		return ComposedLaunch{}, err
	}
	var command []string
	var carried PreTrustOutcome
	if r.Agent == store.AgentCodex {
		carried = CodexPreTrust(r.ExtraEnv, r.NoPreTrust)
		var err error
		command, err = composeCodexCommand(r, carried, cfg)
		if err != nil {
			return ComposedLaunch{}, err
		}
	} else {
		settings, err := synthesizeSettings(r, cfg)
		if err != nil {
			return ComposedLaunch{}, err
		}
		command = []string{claudeProgram(cfg), "--settings", settings}
		command = append(command, r.AgentArgs...)
	}

	return ComposedLaunch{
		PreTrust: carried,
		Create: CreateRequest{
			Agent:             r.Agent,
			OrdinaryInputSafe: inputdelivery.SafeArgs(r.AgentArgs),
			Name:              r.TmuxSessionName,
			CWD:               r.CWD,
			Env:               composeEnv(r),
			Command:           command,
			InstanceID:        r.InstanceID,
		},
		Row: store.Spawn{
			InstanceID:      r.InstanceID,
			ParentID:        ParentIDFromEnv(),
			CWD:             r.CWD,
			TmuxSessionName: r.TmuxSessionName,
			AgentArgs:       r.AgentArgs,
			RelayMode:       r.RelayMode,
			Labels:          r.AgentDirectorLabels,
			ExtraEnv:        r.ExtraEnv,
			NoPreTrust:      r.NoPreTrust,
			// The one place the row's agent is set, for spawn's insert
			// (insertPending) and reuse's reset (ResetForReuse) alike.
			Agent: r.Agent,
		},
	}, nil
}
