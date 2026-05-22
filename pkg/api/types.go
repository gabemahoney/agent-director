package api

import (
	internalapi "github.com/gabemahoney/agent-director/internal/api"
	"github.com/gabemahoney/agent-director/internal/spawn"
)

// Type aliases for verb input/output types.
//
// All aliases use the `type X = pkg.X` form so values are identical at runtime
// and callers can pass them to internal/api functions without copying. This is
// the sole public surface for these types — library callers import pkg/api only
// and never import internal/*.

// VerbSummary is the per-verb shape returned by Help.
type VerbSummary = internalapi.VerbSummary

// SpawnParams is the typed input for the spawn verb.
// Re-exported from internal/spawn so callers do not import internal/*.
type SpawnParams = spawn.SpawnParams

// Permissions holds the allow/deny/ask permission arrays for spawn.
// Re-exported from internal/spawn.
type Permissions = spawn.Permissions

// SpawnResult is the typed return shape of the spawn verb.
type SpawnResult = internalapi.SpawnResult

// StatusResult is the typed return shape of the status verb.
type StatusResult = internalapi.StatusResult

// SpawnRow is the full DB row shape returned by the get verb.
type SpawnRow = internalapi.SpawnRow

// PermissionRequestInfo is the nested shape of an open permission request
// embedded in SpawnRow when state == check_permission.
type PermissionRequestInfo = internalapi.PermissionRequestInfo

// ListParams is the typed input for the list verb.
type ListParams = internalapi.ListParams

// ListRow is one row in a ListResult.
type ListRow = internalapi.ListRow

// ListResult is the typed return shape of the list verb.
type ListResult = internalapi.ListResult

// SendKeysParams is the typed input for the send-keys verb.
type SendKeysParams = internalapi.SendKeysParams

// SendKeysResult is the typed return shape of the send-keys verb.
type SendKeysResult = internalapi.SendKeysResult

// ReadPaneParams is the typed input for the read-pane verb.
type ReadPaneParams = internalapi.ReadPaneParams

// ReadPaneResult is the typed return shape of the read-pane verb.
type ReadPaneResult = internalapi.ReadPaneResult

// DefaultReadPaneLines is the fallback line count for read-pane when
// NLines is 0 or omitted. Pinned here to match internal/api.
const DefaultReadPaneLines = internalapi.DefaultReadPaneLines

// KillParams is the typed input for the kill verb.
type KillParams = internalapi.KillParams

// KillResult is the typed return shape of the kill verb.
type KillResult = internalapi.KillResult

// PauseParams is the typed input for the pause verb.
type PauseParams = internalapi.PauseParams

// PauseResult is the typed return shape of the pause verb.
type PauseResult = internalapi.PauseResult

// DecideParams is the typed input for the decide verb.
type DecideParams = internalapi.DecideParams

// DecideResult is the typed return shape of the decide verb.
type DecideResult = internalapi.DecideResult

// ResumeParams is the typed input for the resume verb.
type ResumeParams = internalapi.ResumeParams

// ResumeResult is the typed return shape of the resume verb.
type ResumeResult = internalapi.ResumeResult

// FindMissingResult is the typed return shape of the find-missing verb.
type FindMissingResult = internalapi.FindMissingResult

// ExpireResult is the typed return shape of the expire verb.
type ExpireResult = internalapi.ExpireResult

// DeleteResult is the typed return shape of the delete verb.
type DeleteResult = internalapi.DeleteResult

// MakeTemplateParams is the typed input for the make-template verb.
type MakeTemplateParams = internalapi.MakeTemplateParams

// MakeTemplatePermissions holds permission arrays for make-template.
type MakeTemplatePermissions = internalapi.MakeTemplatePermissions

// MakeTemplateResult is the typed return shape of the make-template verb.
type MakeTemplateResult = internalapi.MakeTemplateResult

// VersionResult is the typed return shape of the version verb.
type VersionResult = internalapi.VersionResult
