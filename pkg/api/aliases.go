package api

import (
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// ── Type aliases ─────────────────────────────────────────────────────────────
//
// These aliases re-export internal types so external consumers can name them
// (e.g. in interface implementations or struct fields) without importing any
// internal/* package directly. pkg/api's runtime dep graph still references
// internal/store; the aliases just surface the names at api.X.

// Spawn is re-exported from internal/store so external consumers can name
// the type in interface implementations without importing internal/store
// directly. pkg/api's runtime graph still references internal/store (that is
// expected and acceptable per Epic 4 Task 1); the alias just means a caller
// can write api.Spawn rather than store.Spawn.
type Spawn = store.Spawn

// PermissionRow is re-exported from internal/store for the same reason.
// It appears in the GetStore and DecideStore interface method signatures.
type PermissionRow = store.PermissionRow

// ListFilters is re-exported from internal/store for the same reason.
// It appears in the ListStore interface method signature.
type ListFilters = store.ListFilters

// LiveSpawnIdentity is re-exported from internal/store for the same reason.
// It appears in the FindMissingStore interface method signature.
type LiveSpawnIdentity = store.LiveSpawnIdentity

// ExpireCandidate is re-exported from internal/store for the same reason.
// It appears in the ExpireStore interface method signature: one finished row
// expire selected, with its recorded session name, SessionStart identity,
// launch identity and row snapshot (SR-12.1, SR-16.1).
type ExpireCandidate = store.ExpireCandidate

// RowSnapshot is re-exported from internal/store for the same reason.
// It is the type of api.Spawn's Snapshot field: the row's change-detection
// key, values exactly as stored (SR-5.3).
type RowSnapshot = store.RowSnapshot

// LaunchIdentity is re-exported from internal/store for the same reason.
// It is the type of api.Spawn's Identity field: the launch token, socket,
// server identity and pane (SR-3.3 to SR-3.6); zero values mean NULL.
type LaunchIdentity = store.LaunchIdentity

// ResumePrior is re-exported from internal/store for the same reason. It holds
// what resume's move to pending clears, exactly as stored, so the restore after
// a failed launch writes it back byte for byte (SR-8.5); zero values mean NULL.
type ResumePrior = store.ResumePrior

// CondResult is re-exported from internal/store for the same reason. It
// reports the outcome of a conditional write (SR-5.3): CondApplied,
// CondChanged or CondAbsent.
type CondResult = store.CondResult

// The outcomes of a conditional write (SR-5.3), re-exported from
// internal/store; each is identical to its original.
const (
	// CondApplied is store.CondApplied: the row met the condition and the
	// write was applied.
	CondApplied = store.CondApplied
	// CondChanged is store.CondChanged: the row exists but no longer meets
	// the condition; nothing was written.
	CondChanged = store.CondChanged
	// CondAbsent is store.CondAbsent: the row no longer exists; nothing was
	// written.
	CondAbsent = store.CondAbsent
)

// ── Error sentinel re-exports ─────────────────────────────────────────────────
//
// These var declarations re-export internal error sentinels under the pkg/api
// package name so external consumers can do errors.Is(err, api.ErrX) without
// importing any internal/* package themselves. The underlying sentinel values
// are identical to the originals; errors.Is and errors.As work across both
// names interchangeably.
//
// Store sentinels — surface from verb calls and from api.New.

// ErrSpawnNotFound is returned by most verbs when the requested
// claude_instance_id does not exist in the store.
var ErrSpawnNotFound = store.ErrSpawnNotFound

// ErrNoOpenPermissionRequest is returned by decide when the target Spawn has
// no outstanding permission request to resolve.
var ErrNoOpenPermissionRequest = store.ErrNoOpenPermissionRequest

// ErrAlreadyDecided is returned by decide when the outstanding permission
// request has already been answered (by a parallel caller or a prior call).
var ErrAlreadyDecided = store.ErrAlreadyDecided

// ErrPermissionRequestNotFound is returned by get-permission when no
// permission_requests row exists for the supplied request_token. Re-exported
// from internal/store so external consumers can do errors.Is(err, api.X)
// without importing internal/store directly.
var ErrPermissionRequestNotFound = store.ErrPermissionRequestNotFound

// ErrSchemaMismatch is returned by api.New when this binary cannot use the
// store. For a store newer than the binary, install the matching binary. For
// a store at the current version without a valid store id (store_meta's
// store_id, SR-5.1; only a hand edit causes this), restore the copy of
// state.db taken before the install. Never delete state.db. See
// docs/architecture.md "ErrSchemaMismatch recovery".
var ErrSchemaMismatch = store.ErrSchemaMismatch

// ErrSchemaMigrationRequired is returned by api.New when the SQLite database
// is older than the schema version this binary understands and no valid
// administrator authorization was presented. The store never auto-migrates on
// open; the upgrade must be performed by an administrator via the
// agent-director install process. Callers should treat this as a fatal
// configuration error; the store cannot be used until migrated.
var ErrSchemaMigrationRequired = store.ErrSchemaMigrationRequired

// ErrStoreNotInitialized is returned by api.New when CreateIfMissing is false
// and the database file does not exist. Initialize the store first or set
// CreateIfMissing: true.
var ErrStoreNotInitialized = store.ErrStoreNotInitialized

// Tmux sentinels (SR-1.1, SR-1.6): the tmux-caused refusals of the verbs
// that run tmux (kill, read-pane, send-keys, pause, resume and spawn), each
// of one class. Match them with errors.Is; every tmux-caused error matches
// exactly one of them (SR-1.5). Only the GONE class (ErrTmuxSendKeys,
// ErrTmuxCaptureFailed) may be read as "the row's session is not there";
// UNAVAILABLE, CONFLICT and ENVIRONMENT never mean the agent is dead.

// ErrTmuxNotAvailable (class ENVIRONMENT) is returned when this caller cannot
// reach the agent's tmux server as launched: the tmux binary cannot be run,
// tmux refuses the socket to this user, the launch's socket directory is
// unusable, or the lookup finds that this is not the tmux server the agent
// was launched on (SR-1.2). It does not always mean nothing was done: a
// plain spawn whose session-creating call hit it keeps its new pending row
// (SR-9.4, SR-18.1); a plain spawn that met it at the re-lookup of the
// requested name after "duplicate session" has ended its new row (the
// description says if it could not be; SR-9.4); and when the follow-up lookup
// after a kill, send or capture call returns it, that call may or may not
// have taken effect (SR-2.5, SR-6.1). The caller must run as the agents' user
// in their tmux environment. kill, read-pane, send-keys, pause, resume and
// spawn return it.
var ErrTmuxNotAvailable = tmux.ErrTmuxNotAvailable

// ErrTmuxSessionCreate (class LAUNCH FAILURE) is returned by spawn (and
// resume) when the session-creating call fails other than by timing out, by
// tmux being unavailable or by "duplicate session"; for "duplicate session"
// only when the re-lookup finds no session holding the name; or when a
// created session could not be labelled (SR-1.2). Check the system tmux
// installation and TMUX_TMPDIR if this surfaces in production.
var ErrTmuxSessionCreate = tmux.ErrTmuxSessionCreate

// ErrTmuxUnresponsive (class UNAVAILABLE, transient) is returned when tmux did
// not answer usably, so the outcome is unknown; the caller may retry later.
// It never means the agent is dead (SR-1.1, SR-1.6). spawn returns it when
// the session-creating call timed out or its reply does not parse with a
// non-zero exit (the row stays pending), and when the label scan for a
// caller-supplied instance id cannot read tmux's answer, and when the
// re-lookup after "duplicate session" cannot read it (the new row is ended;
// the description says if it could not be).
// kill returns it when
// its lookup cannot read tmux's answer, and when a kill was sent but the
// agent process cannot be checked and the follow-up lookup cannot read
// tmux's answer.
var ErrTmuxUnresponsive = tmux.ErrTmuxUnresponsive

// ErrTmuxSessionConflict (class CONFLICT) is returned when a tmux session
// conflicts with the request in a way waiting does not resolve: a human must
// look (SR-1.1, SR-1.6). It never means the agent is dead. spawn with a
// caller-supplied instance id returns it, before anything is written, when a
// session of this store labelled with that id is left over from an earlier
// life, or when tmux holds conflicting labels. Plain spawn also returns it,
// after "duplicate session" and with its new row ended (the description says
// if it could not be), when the requested name is held by a session left over
// from an earlier life of the id, by another row's session, by a session of
// another agent-director store or by one with no valid instance id, or when
// tmux holds conflicting labels. kill returns it, with no kill sent, when the
// session its lookup finds is not this launch's session, or when tmux holds
// conflicting labels for the row.
var ErrTmuxSessionConflict = tmux.ErrTmuxSessionConflict

// ErrTmuxKillFailed (class UNAVAILABLE) is returned by kill only: the agent
// process still runs after kill (SR-1.1, SR-1.2). A kill was sent and the
// agent process, or another process of a pane of the agent's session, was
// still running after the kill exit wait; or a kill was sent, the agent
// process cannot be checked and its labelled session is still there; or no
// session or pane of the launch was found while the agent process still
// runs, and no kill was sent. kill never changes the row's state (SR-6.1).
// It never means the agent is dead.
var ErrTmuxKillFailed = tmux.ErrTmuxKillFailed

// ErrTmuxSendKeys (class GONE) is returned by send-keys and pause when the
// row's own session or pane is not there: the lookup finds no session of the
// launch (nothing sent), or a keys call failed and the follow-up lookup
// finds the launch's session gone (SR-1.1, SR-1.2).
var ErrTmuxSendKeys = tmux.ErrTmuxSendKeys

// ErrTmuxCaptureFailed (class GONE) is returned by read-pane when the row's
// own session or pane is not there: the lookup finds no session of the
// launch (nothing read), or the capture failed and the follow-up lookup
// finds the launch's session gone (SR-1.1, SR-1.2).
var ErrTmuxCaptureFailed = tmux.ErrTmuxCaptureFailed
