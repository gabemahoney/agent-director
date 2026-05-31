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

// ErrSchemaMismatch is returned by api.New when the SQLite database was
// created by a different schema version. Callers should treat this as a
// fatal configuration error; the store cannot be used.
var ErrSchemaMismatch = store.ErrSchemaMismatch

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
