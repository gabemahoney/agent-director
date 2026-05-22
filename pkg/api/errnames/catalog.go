package errnames

import (
	"errors"
	"strings"

	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Entry pairs an err_name string with the sentinel error it names.
// Catalog is walked via errors.Is, so %w-wrapped errors are matched
// correctly.
type Entry struct {
	Name string
	Err  error
}

// ErrUnknownTool is returned by the MCP dispatcher when the requested
// tool name is not in the registered verb list. Declared here (not in
// internal/mcp) so that internal/mcp can import pkg/api/errnames without
// a cycle: internal/mcp → pkg/api/errnames → pkg/api, but not back.
var ErrUnknownTool = errors.New("ErrUnknownTool")

// Catalog is the canonical err_name lookup table for all agent-director
// error paths. The CLI's Classify, the MCP server's classifyDispatchError,
// and (later) pkg/cabi's JSON encoder all consume this table — there is
// exactly one source of truth.
//
// Ordering is preserved from the original cmd/agent-director/errnames.go
// errCatalog for diff hygiene; no current entry wraps another, so
// first-match order is not load-bearing today.
//
// The sentinels here MUST match the exported error variables in pkg/api
// and the internal/* packages. There is intentionally no parallel list:
// this Catalog IS the single declaration point for err_name strings.
// Coherence with pkg/api sentinel variables is enforced by
// pkg/api/errnames/catalog_test.go and the full test suite (Task 6, ib).
// See also the cross-reference comment in pkg/api/errors.go.
//
// Design note (Task 6, subtask 9f): Option 1 (declare sentinels directly
// from Catalog rows) was evaluated and rejected — pkg/api/errnames imports
// pkg/api for the API-level sentinels; if pkg/api also imported
// pkg/api/errnames to re-export from the Catalog, the import cycle would be
// unresolvable. Option 2 (annotation + coherence test) is used instead.
var Catalog = []Entry{
	{Name: "ErrCwdMissing", Err: spawn.ErrCwdMissing},
	{Name: "ErrCwdNotAPath", Err: spawn.ErrCwdNotAPath},
	{Name: "ErrCwdNotFound", Err: spawn.ErrCwdNotFound},
	{Name: "ErrCwdNotADirectory", Err: spawn.ErrCwdNotADirectory},
	{Name: "ErrRelayModeInvalid", Err: spawn.ErrRelayModeInvalid},
	{Name: "ErrSpawnDeniedFlag", Err: spawn.ErrSpawnDeniedFlag},
	{Name: "ErrReservedEnvKey", Err: spawn.ErrReservedEnvKey},
	{Name: "ErrInstanceIdCollision", Err: spawn.ErrInstanceIdCollision},
	{Name: "ErrTmuxSessionNameEmpty", Err: spawn.ErrTmuxSessionNameEmpty},
	{Name: "ErrTmuxSessionNameInvalid", Err: spawn.ErrTmuxSessionNameInvalid},
	{Name: "ErrTmuxSessionNameTooLong", Err: spawn.ErrTmuxSessionNameTooLong},
	{Name: "ErrSpawnNotFound", Err: store.ErrSpawnNotFound},
	{Name: "ErrTmuxNotAvailable", Err: tmux.ErrTmuxNotAvailable},
	{Name: "ErrTmuxSessionCreate", Err: tmux.ErrTmuxSessionCreate},
	{Name: "ErrTmuxKillFailed", Err: tmux.ErrTmuxKillFailed},
	{Name: "ErrTmuxListPanesFailed", Err: tmux.ErrTmuxListPanesFailed},
	{Name: "ErrTmuxSendKeys", Err: tmux.ErrTmuxSendKeys},
	{Name: "ErrTmuxCaptureFailed", Err: tmux.ErrTmuxCaptureFailed},
	{Name: "ErrSchemaMismatch", Err: store.ErrSchemaMismatch},
	{Name: "ErrSpawnNotInteractive", Err: api.ErrSpawnNotInteractive},
	{Name: "ErrSendKeysWhileRelayed", Err: api.ErrSendKeysWhileRelayed},
	{Name: "ErrSpawnNotPausable", Err: api.ErrSpawnNotPausable},
	{Name: "ErrPauseTimeout", Err: api.ErrPauseTimeout},
	{Name: "ErrListInvalidLabel", Err: api.ErrListInvalidLabel},
	{Name: "ErrTemplateNameUnsafe", Err: config.ErrTemplateNameUnsafe},
	{Name: "ErrTemplateNotFound", Err: config.ErrTemplateNotFound},
	{Name: "ErrTemplateMalformed", Err: config.ErrTemplateMalformed},
	{Name: "ErrTemplateExists", Err: config.ErrTemplateExists},
	{Name: "ErrProbeUnsupported", Err: probe.ErrProbeUnsupported},
	{Name: "ErrSpawnNotResumable", Err: api.ErrSpawnNotResumable},
	{Name: "ErrNoSessionId", Err: api.ErrNoSessionId},
	{Name: "ErrJsonlMissing", Err: api.ErrJsonlMissing},
	{Name: "ErrRelayModeOff", Err: api.ErrRelayModeOff},
	{Name: "ErrInvalidDecision", Err: api.ErrInvalidDecision},
	{Name: "ErrNoOpenPermissionRequest", Err: store.ErrNoOpenPermissionRequest},
	{Name: "ErrAlreadyDecided", Err: store.ErrAlreadyDecided},
	// ErrUnknownTool is the MCP dispatcher's sentinel for unrecognized tool
	// names. It is declared in this package (not internal/mcp) to keep the
	// import graph cycle-free: internal/mcp → pkg/api/errnames, never back.
	{Name: "ErrUnknownTool", Err: ErrUnknownTool},
}

// Classify returns the canonical err_name and err_description for an error
// returned by a verb handler. It walks Catalog using errors.Is, returning
// the first matching entry's Name along with err.Error() as the description.
// Unrecognized errors collapse to "ErrInternal" — production paths should
// never reach this; tests pin the canonical names directly.
func Classify(err error) (name, description string) {
	if err == nil {
		return "", ""
	}
	for _, ec := range Catalog {
		if errors.Is(err, ec.Err) {
			return ec.Name, err.Error()
		}
	}
	return "ErrInternal", err.Error()
}

// TrimNamePrefix strips the redundant "ErrName: " prefix from description
// when present, returning the human-readable remainder. Used by the CLI's
// writeApiError so the envelope reads cleanly instead of carrying the
// err_name in both fields.
func TrimNamePrefix(name, description string) string {
	prefix := name + ":"
	if strings.HasPrefix(description, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(description, prefix))
	}
	return description
}
