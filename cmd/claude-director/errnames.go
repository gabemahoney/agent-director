package main

import (
	"errors"
	"strings"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/spawn"
	"github.com/gabemahoney/claude-director/internal/store"
	"github.com/gabemahoney/claude-director/internal/tmux"
)

// errCatalog is the lookup table the CLI consults to translate a wrapped
// error chain into the canonical err_name / err_description envelope per
// SRD §13.1. The chain walk uses errors.Is, which respects %w wrapping;
// the err_description is the wrapped error's full message.
//
// Adding a new sentinel: append it here and to the appropriate VerbDef's
// ErrorNames slice in internal/api/manifest. The mismatch is caught by
// the doc-drift CI gate if either side falls behind.
var errCatalog = []struct {
	name string
	err  error
}{
	{"ErrCwdMissing", spawn.ErrCwdMissing},
	{"ErrCwdNotAPath", spawn.ErrCwdNotAPath},
	{"ErrCwdNotFound", spawn.ErrCwdNotFound},
	{"ErrCwdNotADirectory", spawn.ErrCwdNotADirectory},
	{"ErrRelayModeInvalid", spawn.ErrRelayModeInvalid},
	{"ErrSpawnDeniedFlag", spawn.ErrSpawnDeniedFlag},
	{"ErrReservedEnvKey", spawn.ErrReservedEnvKey},
	{"ErrInstanceIdCollision", spawn.ErrInstanceIdCollision},
	{"ErrSpawnNotFound", store.ErrSpawnNotFound},
	{"ErrTmuxNotAvailable", tmux.ErrTmuxNotAvailable},
	{"ErrTmuxSessionCreate", tmux.ErrTmuxSessionCreate},
	{"ErrTmuxKillFailed", tmux.ErrTmuxKillFailed},
	{"ErrTmuxListPanesFailed", tmux.ErrTmuxListPanesFailed},
	{"ErrTmuxSendKeys", tmux.ErrTmuxSendKeys},
	{"ErrTmuxCaptureFailed", tmux.ErrTmuxCaptureFailed},
	{"ErrSchemaMismatch", store.ErrSchemaMismatch},
	{"ErrSpawnNotInteractive", api.ErrSpawnNotInteractive},
	{"ErrSendKeysWhileRelayed", api.ErrSendKeysWhileRelayed},
}

// classifyError returns the canonical err_name and an err_description for
// an error returned by a verb handler. Unrecognized errors collapse to
// "ErrInternal" — production paths should never hit this; tests pin the
// canonical names directly.
func classifyError(err error) (name, description string) {
	if err == nil {
		return "", ""
	}
	for _, ec := range errCatalog {
		if errors.Is(err, ec.err) {
			return ec.name, err.Error()
		}
	}
	return "ErrInternal", err.Error()
}

// errMessageStartsWithName strips the redundant `ErrName: ` prefix from
// the description when present, leaving the human-readable remainder.
// Used by writeApiError so the envelope reads cleanly instead of carrying
// the err_name in both fields.
func errMessageStartsWithName(name, description string) string {
	prefix := name + ":"
	if strings.HasPrefix(description, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(description, prefix))
	}
	return description
}
