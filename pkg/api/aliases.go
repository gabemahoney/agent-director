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

// Tmux sentinels — surface from spawn and resume when tmux is unavailable or
// fails to create a new session.

// ErrTmuxSessionCreate is returned by spawn (and resume) when tmux
// new-session exits non-zero. Check the system tmux installation and
// TMUX_TMPDIR if this surfaces in production.
var ErrTmuxSessionCreate = tmux.ErrTmuxSessionCreate

// ErrTmuxUnresponsive (class UNAVAILABLE, transient) is returned when tmux did
// not answer usably, so the outcome is unknown; the caller may retry later.
// It never means the agent is dead (SR-1.1, SR-1.6). spawn returns it when
// the session-creating call timed out or its reply does not parse with a
// non-zero exit (the row stays pending), and when the label scan for a caller-supplied instance id
// cannot read tmux's answer.
var ErrTmuxUnresponsive = tmux.ErrTmuxUnresponsive

// ErrTmuxSessionConflict (class CONFLICT) is returned when a tmux session
// conflicts with the request in a way waiting does not resolve: a human must
// look (SR-1.1, SR-1.6). It never means the agent is dead. spawn with a
// caller-supplied instance id returns it, before anything is written, when a
// session of this store labelled with that id is left over from an earlier
// life, or when tmux holds conflicting labels.
var ErrTmuxSessionConflict = tmux.ErrTmuxSessionConflict
