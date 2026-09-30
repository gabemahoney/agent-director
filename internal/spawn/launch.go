package spawn

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// preTrustWarn is where pre-trust warnings land — missing-file or
// best-effort failures. Held as a var so tests can capture without
// touching os.Stderr (which the test harness drains for the JSON error
// envelope).
var preTrustWarn io.Writer = os.Stderr

// TmuxClient is the name-based tmux surface Relaunch (resume) still uses
// until resume moves to the socket-taking create. Launch uses LaunchTmux.
// *tmux.Client satisfies it.
type TmuxClient interface {
	NewSessionByName(name, cwd string, envs map[string]string, command []string) error
}

// claudeBinary is the program tmux launches inside the new session. Held
// as a var so tests can swap it for a fake-claude helper without
// monkey-patching the spawn flow.
var claudeBinary = "claude"

// envInstanceID names the env var Launch reads to populate parent_id on
// the new row (SRD §7.5). When set, the caller is itself a Spawn whose
// Claude shell is invoking us; the value becomes the new row's parent.
const envInstanceID = "AGENT_DIRECTOR_INSTANCE_ID"

// Launch is plain spawn's launch (SRD SR-3.3, SR-3.5, SR-3.6, SR-9.4,
// SR-22.2). The caller has already run the collision pre-check (and, for a
// caller-supplied id with no row, the label scan). In order:
//
//  1. Resolves the launch socket as tmux would, creating a missing per-user
//     directory (ResolveLaunchSocket), and mints a new launch token
//     (NewLaunchToken). Both come before pre-trust and the insert, so a
//     refused socket directory (ErrTmuxNotAvailable) or a token failure
//     writes nothing and launches nothing (SR-20.6).
//  2. Composes the session environment, synthesizes --settings and builds
//     the claude argv.
//  3. Pre-trusts the cwd unless NoPreTrust (best effort).
//  4. INSERTs the pending row with the launch start (one read of now, in
//     milliseconds), the token and the socket (SR-22.2, SR-3.3).
//  5. Creates the session and labels it through CreateAndLabel, on the
//     socket, with the token, the instance id and this store's id
//     (s.StoreID()): one create invocation with the chained label, at most
//     one label by id and one kill of a session that could not be labelled.
//     The create is bounded by the create timeout.
//  6. After a parsed reply with the label in place, reads the server's and
//     the pane's process start times through pc and makes one conditional
//     identity write (RecordLaunchIdentity, the insert's version 0 and the
//     token). A write that does not apply (a hook wrote first) records
//     nothing; a store error gives one WARN line on lg and does not change
//     the result (SR-3.6, SR-5.8).
//
// On every create failure the row stays pending and nothing else is
// written; the error is plainSpawnCreateError's: ErrTmuxUnresponsive for a
// timed-out create or a reply that does not parse with a non-zero exit (the
// launch-timeout rule), ErrTmuxNotAvailable for tmux unavailable,
// ErrTmuxSessionCreate for a session that could not be labelled, "duplicate
// session" and every other failure. A reply lost with exit 0 is a success
// with no identity recorded. On an insert collision ErrInstanceIdCollision
// surfaces (the TOCTOU fallback of the pre-check).
//
// Launch does not wait for Claude to come up: the row stays pending until
// the first SessionStart hook moves it.
func Launch(s *store.Store, t LaunchTmux, pc tmux.ProcChecker, r Resolved, cfg config.Config, now func() time.Time, lg *log.Logger) (string, error) {
	socket, err := ResolveLaunchSocket()
	if err != nil {
		return "", err
	}
	token, err := NewLaunchToken()
	if err != nil {
		return "", err
	}

	envs := composeEnv(r)
	settings, err := synthesizeSettings(r, cfg)
	if err != nil {
		return "", err
	}

	if !r.NoPreTrust {
		preTrustBestEffort(r)
	}

	command := []string{claudeBinary, "--settings", settings}
	command = append(command, r.ClaudeArgs...)

	if err := insertPending(s, r, now().UnixMilli(), token, socket); err != nil {
		return "", err
	}

	// The create's own -e entry carries AGENT_DIRECTOR_INSTANCE_ID once; the
	// client drops any such key from envs (tmux.NewSession).
	req := CreateRequest{
		Socket:     socket,
		Name:       r.TmuxSessionName,
		CWD:        r.CWD,
		Env:        envs,
		Command:    command,
		Token:      token,
		InstanceID: r.ClaudeInstanceID,
		StoreID:    s.StoreID(),
	}
	out := CreateAndLabel(t, req)
	if err := plainSpawnCreateError(out, req); err != nil {
		return "", err
	}
	if out.Kind == CreateLabelled {
		recordLaunchIdentity(s, pc, lg, r.ClaudeInstanceID, token, out.Reply)
	}
	return r.ClaudeInstanceID, nil
}

// insertRowVersion is the row_version the pending insert leaves, the version
// the identity write is conditional on (SR-5.2, SR-3.6).
const insertRowVersion = 0

// recordLaunchIdentity makes the one conditional identity write after a
// labelled create (SR-3.6): the reply's server pid and start_time and pane
// id and pid, with the server's and the pane's process start times from pc.
// A write that does not apply records nothing; a store error gives one WARN
// line naming the instance id only.
func recordLaunchIdentity(s *store.Store, pc tmux.ProcChecker, lg *log.Logger, instanceID, token string, reply tmux.CreateReply) {
	id := store.LaunchIdentity{
		ServerPID:       reply.ServerPID,
		ServerStart:     reply.ServerStart,
		ServerStarttime: knownStartTime(pc, reply.ServerPID),
		PaneID:          reply.PaneID,
		PanePID:         reply.PanePID,
		PaneStarttime:   knownStartTime(pc, reply.PanePID),
	}
	if _, err := s.RecordLaunchIdentity(instanceID, insertRowVersion, token, id); err != nil {
		lg.Printf("WARN: spawn: recording the launch identity of spawn %s failed: %v", instanceID, err)
	}
}

// knownStartTime returns pid's process start time when the reader answers
// alive and known, and "" (none recorded) otherwise (SR-3.6, SR-3.8).
func knownStartTime(pc tmux.ProcChecker, pid int) string {
	if pid <= 0 {
		return ""
	}
	start, alive, known := pc.StartTime(pid)
	if !alive || !known {
		return ""
	}
	return start
}

// preTrustBestEffort pre-trusts r's cwd in ~/.claude.json so the spawned
// Claude Code skips its workspace-trust dialog (bug b.f75). Best-effort: any
// failure (missing file on truly-fresh machines, parse error, perm issue) is
// surfaced as a soft warning on preTrustWarn but does not block the spawn;
// the operator will see the trust dialog in that case and can dismiss it.
func preTrustBestEffort(r Resolved) {
	err := preTrustCwd(r.CWD, r.ExtraEnv)
	switch {
	case err == nil:
	case errors.Is(err, ErrClaudeJSONMissing):
		fmt.Fprintf(preTrustWarn, "agent-director: pre-trust skipped (%v); spawn may block on Claude Code's trust dialog\n", err)
	default:
		fmt.Fprintf(preTrustWarn, "agent-director: pre-trust failed: %v\n", err)
	}
}

// insertPending inserts r's pending row with the launch start (milliseconds),
// the launch token and the launch socket (SR-22.2, SR-3.3, SR-3.5). parent_id
// is auto-detected from our own environment (SRD §7.5); empty is stored as
// NULL. A primary-key collision, the TOCTOU fallback of the pre-check in
// ApplyDefaults, maps to ErrInstanceIdCollision (store.ErrPrimaryKeyCollision
// is detected from the SQLite error code, not its text).
func insertPending(s *store.Store, r Resolved, launchStartMillis int64, token, socket string) error {
	row := store.Spawn{
		ClaudeInstanceID:      r.ClaudeInstanceID,
		ParentID:              os.Getenv(envInstanceID),
		CWD:                   r.CWD,
		TmuxSessionName:       r.TmuxSessionName,
		ClaudeArgs:            r.ClaudeArgs,
		RelayMode:             r.RelayMode,
		Labels:                r.AgentDirectorLabels,
		ExtraEnv:              r.ExtraEnv,
		LaunchStartedAtMillis: launchStartMillis,
		Identity:              store.LaunchIdentity{Token: token, Socket: socket},
	}
	if err := s.InsertPending(row); err != nil {
		if errors.Is(err, store.ErrPrimaryKeyCollision) {
			return fmt.Errorf("%w: %s", ErrInstanceIdCollision, r.ClaudeInstanceID)
		}
		return err
	}
	return nil
}
