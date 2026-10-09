package spawn

import (
	"errors"
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// claudeBinary is the program tmux launches inside the new session. Held
// as a var so tests can swap it for a fake-claude helper without
// monkey-patching the spawn flow.
var claudeBinary = "claude"

// Launch is plain spawn's launch (SRD SR-3.3, SR-3.5, SR-3.6, SR-9.4,
// SR-22.2). The caller has already run the collision pre-check (and, for a
// caller-supplied id with no row, the label scan); minted is whether that
// pre-check minted r's instance id (ApplyDefaults returned IDMinted), which
// changes only the launch timeout's retry sentence. In order:
//
//  1. Resolves the launch socket as tmux would, creating a missing per-user
//     directory (ResolveLaunchSocket), and mints a new launch token
//     (NewLaunchToken). Both come before pre-trust and the insert, so a
//     refused socket directory (ErrTmuxNotAvailable) or a token failure
//     writes nothing and launches nothing (SR-20.6).
//  2. Composes the launch through ComposeLaunch: the session environment,
//     the synthesized --settings, the claude argv, the parent id and the
//     row's request fields. A composition failure writes nothing.
//  3. Pre-trusts the cwd through PreTrust, off when NoPreTrust, waiting for
//     a held lock on .claude.json for at most cfg's effective
//     pre_trust.lock_wait_seconds (best effort; a failure never fails the
//     spawn, SR-22.6).
//  4. INSERTs step 2's row with the launch start (one read of now, in
//     milliseconds), the token, the socket and the launch owner, this
//     process as CurrentLaunchOwner reads it through pc (b.kdf); the row
//     carries the NoPreTrust choice step 3 used, so it records what the
//     spawn did for its life and every resume of that life follows it
//     (SR-22.2, SR-3.3, SR-5.2, SR-22.6). Between the insert and the create
//     there is only in-process work (SR-13.4).
//  5. Creates the session and labels it through CreateAndLabel, on the
//     socket, with the token, the instance id and this store's id
//     (s.StoreID()): one create invocation with the chained label, at most
//     one label by id and one kill of a session that could not be labelled.
//     The create is bounded by the create timeout.
//  6. After a parsed reply with the label in place, reads the server's and
//     the pane's process start times through pc and makes one conditional
//     identity write (RecordLaunchIdentity, the insert's version 0 and the
//     token), which also ends the launch's hold. A write that does not apply
//     (another write came first) records nothing; a store error gives one
//     WARN line on lg and does not change the result (SR-3.6, SR-5.8).
//  7. When an owner was recorded and no identity write applied (a lost
//     reply, an identity write that did not apply or failed, and every
//     create failure but "duplicate session"), ends the launch's hold with
//     ReleaseLaunchOwner on the token, which retries a store error a bounded
//     number of times, then gives one WARN line on lg; it never changes the
//     result.
//
// On every create failure the row stays pending and nothing else is written
// but the release of step 7; the error is plainSpawnCreateError's:
// ErrTmuxUnresponsive for a timed-out create or a reply that does not parse
// with a non-zero exit (the launch-timeout rule, followed for a
// caller-supplied id, minted unset, by the opted-in retry),
// ErrTmuxNotAvailable for tmux unavailable, ErrTmuxSessionCreate for a
// session that could not be labelled and every other failure. "duplicate
// session" is not mapped to a verb error: Launch
// returns a *HeldNameError at once (no store write, file or network I/O,
// further tmux call or log line after the create) carrying the instance id,
// the requested name, the socket, the token and the launch start step 4
// wrote, and the caller's held-name path ends the row (ending the launch's
// hold itself, with ReleaseLaunchOwner, when that end write does not apply),
// re-looks the name up and classifies the holder (SR-9.4). The row is still
// pending at version 0
// when Launch returns it. A reply lost with exit 0 is a success with no
// identity recorded. On an insert collision ErrInstanceIdCollision surfaces
// (the TOCTOU fallback of the pre-check).
//
// On success Launch returns the instance id and the outcome of the step 3
// pre-trust exactly as PreTrust reported it: PreTrustSkipped when
// NoPreTrust is set (no file touched), otherwise PreTrustOK or
// PreTrustFailed (SR-22.6 "The field"). Every error path returns its error
// and no outcome.
//
// Launch does not wait for Claude to come up: the row stays pending until
// the first SessionStart hook moves it.
func Launch(s *store.Store, t LaunchTmux, pc tmux.ProcChecker, r Resolved, minted bool, cfg config.Config, now func() time.Time, lg *log.Logger) (string, PreTrustOutcome, error) {
	socket, err := ResolveLaunchSocket()
	if err != nil {
		return "", "", err
	}
	token, err := NewLaunchToken()
	if err != nil {
		return "", "", err
	}

	c, err := ComposeLaunch(r, cfg)
	if err != nil {
		return "", "", err
	}

	// A failure never fails the spawn; the outcome is returned on success.
	preTrust := PreTrust(r.CWD, r.ExtraEnv, r.NoPreTrust, cfg.PreTrust)

	// Read once, so a held-name outcome carries exactly what the insert wrote.
	launchStart := now().UnixMilli()
	row := c.Row
	row.LaunchStartedAtMillis = launchStart
	row.Identity = store.LaunchIdentity{Token: token, Socket: socket}
	row.LaunchOwner = CurrentLaunchOwner(pc)
	if err := insertPending(s, row); err != nil {
		return "", "", err
	}

	req := c.Create
	req.Socket = socket
	req.Token = token
	req.StoreID = s.StoreID()
	out := CreateAndLabel(t, req)
	recorded := false
	if out.Kind == CreateLabelled {
		recorded = RecordLaunchIdentity(s, pc, lg, r.ClaudeInstanceID, insertRowVersion, token, out.Reply)
	}
	// "duplicate session" is handed to the caller's held-name path, whose end
	// write is conditional on the insert's version 0: nothing is written
	// before it, and that path ends the hold itself when its end write does
	// not apply.
	if !recorded && out.Kind != CreateDuplicate && row.LaunchOwner.PID > 0 {
		ReleaseLaunchOwner(s, lg, r.ClaudeInstanceID, token)
	}
	if err := plainSpawnCreateError(out, req, launchStart, minted); err != nil {
		return "", "", err
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
// labelled create (SR-3.6), shared by plain spawn, resume and reuse: the
// reply's server pid and start_time and pane id and pid, with the server's
// and the pane's process start times from pc (an unreadable one is recorded
// as none), guarded on the launch's version and token: for a plain spawn the
// insert's version 0 and its token, for resume the version its move produced
// and the move's token, for reuse the version its reset produced and its
// token. The write also ends the launch's hold on the row (b.kdf). A write
// that does not apply (another write came first; no hook can, because a row
// that records no pane matches no hook, SR-22.9) records nothing; a store
// error gives one WARN line on lg naming the instance id and that recording
// the launch identity failed, with no token, label or environment value, and
// does not change the launch's result (SR-5.8). A nil lg logs nothing.
//
// It reports whether the write applied; the caller ends the launch's hold
// itself (ReleaseLaunchOwner) when it did not.
func RecordLaunchIdentity(w IdentityWriter, pc tmux.ProcChecker, lg *log.Logger, instanceID string, launchVersion int64, token string, reply tmux.CreateReply) bool {
	id := store.LaunchIdentity{
		ServerPID:       reply.ServerPID,
		ServerStart:     reply.ServerStart,
		ServerStarttime: tmux.KnownStartTime(pc, reply.ServerPID),
		PaneID:          reply.PaneID,
		PanePID:         reply.PanePID,
		PaneStarttime:   tmux.KnownStartTime(pc, reply.PanePID),
	}
	res, err := w.RecordLaunchIdentity(instanceID, launchVersion, token, id)
	if err != nil {
		if lg != nil {
			lg.Printf("WARN: recording the launch identity of instance %s failed: %v", instanceID, err)
		}
		return false
	}
	return res == store.CondApplied
}

// insertPending inserts row as the pending row: ComposeLaunch's row with the
// launch start (milliseconds), the launch token and the launch socket
// (SR-22.2, SR-3.3, SR-3.5). A primary-key collision maps to
// ErrInstanceIdCollision through rowExistsCollision, the text the pre-check
// gives a finished row. The pre-check in ApplyDefaults refuses every existing
// row a plain spawn may not use, so the collision here is a row inserted
// after the pre-check's read (a race), the only refusal of an existing row
// that follows pre-trust (store.ErrPrimaryKeyCollision is detected from the
// SQLite error code, not its text).
func insertPending(s *store.Store, row store.Spawn) error {
	if err := s.InsertPending(row); err != nil {
		if errors.Is(err, store.ErrPrimaryKeyCollision) {
			return rowExistsCollision(row.ClaudeInstanceID)
		}
		return err
	}
	return nil
}
