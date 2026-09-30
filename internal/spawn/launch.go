package spawn

import (
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

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
//  3. Pre-trusts the cwd through PreTrust, off when NoPreTrust (best effort;
//     a failure never fails the spawn, SR-22.6).
//  4. INSERTs the pending row with the launch start (one read of now, in
//     milliseconds), the token, the socket and the NoPreTrust choice step 3
//     used, so the row records what the spawn did for its life and every
//     resume of that life follows it (SR-22.2, SR-3.3, SR-5.2, SR-22.6).
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
// On success Launch returns the instance id and the outcome of the step 3
// pre-trust exactly as PreTrust reported it: PreTrustSkipped when
// NoPreTrust is set (no file touched), otherwise PreTrustOK or
// PreTrustFailed (SR-22.6 "The field"). Every error path returns its error
// and no outcome.
//
// Launch does not wait for Claude to come up: the row stays pending until
// the first SessionStart hook moves it.
func Launch(s *store.Store, t LaunchTmux, pc tmux.ProcChecker, r Resolved, cfg config.Config, now func() time.Time, lg *log.Logger) (string, PreTrustOutcome, error) {
	socket, err := ResolveLaunchSocket()
	if err != nil {
		return "", "", err
	}
	token, err := NewLaunchToken()
	if err != nil {
		return "", "", err
	}

	envs := composeEnv(r)
	settings, err := synthesizeSettings(r, cfg)
	if err != nil {
		return "", "", err
	}

	// A failure never fails the spawn; the outcome is returned on success.
	preTrust := PreTrust(r.CWD, r.ExtraEnv, r.NoPreTrust)

	command := []string{claudeBinary, "--settings", settings}
	command = append(command, r.ClaudeArgs...)

	if err := insertPending(s, r, now().UnixMilli(), token, socket); err != nil {
		return "", "", err
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
		return "", "", err
	}
	if out.Kind == CreateLabelled {
		RecordLaunchIdentity(s, pc, lg, r.ClaudeInstanceID, insertRowVersion, token, out.Reply)
	}
	return r.ClaudeInstanceID, preTrust, nil
}

// insertRowVersion is the row_version the pending insert leaves, the version
// the identity write is conditional on (SR-5.2, SR-3.6).
const insertRowVersion = 0

// IdentityWriter is the one store write RecordLaunchIdentity needs: the
// conditional identity write (SR-3.6). *store.Store satisfies it.
type IdentityWriter interface {
	RecordLaunchIdentity(instanceID string, launchVersion int64, token string, id store.LaunchIdentity) (store.CondResult, error)
}

// RecordLaunchIdentity makes the one conditional identity write after a
// labelled create (SR-3.6), shared by plain spawn and resume: the reply's
// server pid and start_time and pane id and pid, with the server's and the
// pane's process start times from pc (an unreadable one is recorded as none),
// guarded on the launch's version and token: for a plain spawn the insert's
// version 0 and its token, for resume the version its move produced and the
// move's token. A write that does not apply (another write came first; no
// hook can, because a row that records no pane matches no hook, SR-22.9)
// records nothing; a store error gives one WARN line on lg naming the instance id and
// that recording the launch identity failed, with no token, label or
// environment value, and does not change the launch's result (SR-5.8). A nil
// lg logs nothing.
func RecordLaunchIdentity(w IdentityWriter, pc tmux.ProcChecker, lg *log.Logger, instanceID string, launchVersion int64, token string, reply tmux.CreateReply) {
	id := store.LaunchIdentity{
		ServerPID:       reply.ServerPID,
		ServerStart:     reply.ServerStart,
		ServerStarttime: knownStartTime(pc, reply.ServerPID),
		PaneID:          reply.PaneID,
		PanePID:         reply.PanePID,
		PaneStarttime:   knownStartTime(pc, reply.PanePID),
	}
	if _, err := w.RecordLaunchIdentity(instanceID, launchVersion, token, id); err != nil && lg != nil {
		lg.Printf("WARN: recording the launch identity of instance %s failed: %v", instanceID, err)
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

// insertPending inserts r's pending row with the launch start (milliseconds),
// the launch token, the launch socket (SR-22.2, SR-3.3, SR-3.5) and r's
// NoPreTrust, the choice Launch passed to PreTrust (SR-5.2, SR-22.6). parent_id
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
		NoPreTrust:            r.NoPreTrust,
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
