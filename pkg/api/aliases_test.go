package api_test

// aliases_test.go covers the alias round-trip property introduced in the k7
// phase: api.Spawn, api.PermissionRow, and api.ListFilters are type aliases
// for internal/store types, so they are assignment-compatible and share
// identical field layouts without any explicit conversion.

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/internal/store"
)

// TestSpawnAliasRoundTrip proves that api.Spawn and store.Spawn are the same
// type: a value of one can be assigned directly to the other and all fields
// are preserved across the round-trip.
func TestSpawnAliasRoundTrip(t *testing.T) {
	orig := store.Spawn{
		ClaudeInstanceID: "alias-test-id",
		State:            store.StateWaiting,
		TmuxSessionName:  "alias-sess",
		RelayMode:        "off",
	}

	// store.Spawn → api.Spawn (no conversion syntax needed for type aliases)
	var asAPI api.Spawn = orig
	if asAPI.ClaudeInstanceID != orig.ClaudeInstanceID {
		t.Errorf("ClaudeInstanceID: got %q; want %q", asAPI.ClaudeInstanceID, orig.ClaudeInstanceID)
	}
	if asAPI.State != orig.State {
		t.Errorf("State: got %q; want %q", asAPI.State, orig.State)
	}

	// api.Spawn → store.Spawn (reverse direction)
	var asStore store.Spawn = asAPI
	if asStore.TmuxSessionName != orig.TmuxSessionName {
		t.Errorf("TmuxSessionName: got %q; want %q", asStore.TmuxSessionName, orig.TmuxSessionName)
	}
	if asStore.RelayMode != orig.RelayMode {
		t.Errorf("RelayMode: got %q; want %q", asStore.RelayMode, orig.RelayMode)
	}
}

// TestPermissionRowAliasRoundTrip proves api.PermissionRow ↔ store.PermissionRow
// interop: direct assignment works in both directions without conversion.
func TestPermissionRowAliasRoundTrip(t *testing.T) {
	orig := store.PermissionRow{
		ClaudeInstanceID: "perm-alias-id",
		ToolName:         "Bash",
		ToolInput:        `{"cmd":"echo"}`,
	}

	// store.PermissionRow → api.PermissionRow
	var asAPI api.PermissionRow = orig
	if asAPI.ClaudeInstanceID != orig.ClaudeInstanceID {
		t.Errorf("ClaudeInstanceID: got %q; want %q", asAPI.ClaudeInstanceID, orig.ClaudeInstanceID)
	}
	if asAPI.ToolName != orig.ToolName {
		t.Errorf("ToolName: got %q; want %q", asAPI.ToolName, orig.ToolName)
	}
	if asAPI.ToolInput != orig.ToolInput {
		t.Errorf("ToolInput: got %q; want %q", asAPI.ToolInput, orig.ToolInput)
	}

	// api.PermissionRow → store.PermissionRow (reverse direction)
	var asStore store.PermissionRow = asAPI
	if asStore.ClaudeInstanceID != orig.ClaudeInstanceID {
		t.Errorf("round-trip ClaudeInstanceID: got %q; want %q",
			asStore.ClaudeInstanceID, orig.ClaudeInstanceID)
	}
}

// TestListFiltersAliasRoundTrip proves api.ListFilters ↔ store.ListFilters
// interop: direct assignment works and field values are preserved.
func TestListFiltersAliasRoundTrip(t *testing.T) {
	orig := store.ListFilters{
		State:  []string{store.StateWaiting, store.StateWorking},
		Parent: "parent-id",
		Limit:  10,
	}

	// store.ListFilters → api.ListFilters
	var asAPI api.ListFilters = orig
	if asAPI.Parent != orig.Parent {
		t.Errorf("Parent: got %q; want %q", asAPI.Parent, orig.Parent)
	}
	if asAPI.Limit != orig.Limit {
		t.Errorf("Limit: got %d; want %d", asAPI.Limit, orig.Limit)
	}
	if len(asAPI.State) != len(orig.State) {
		t.Errorf("State len: got %d; want %d", len(asAPI.State), len(orig.State))
	}

	// api.ListFilters → store.ListFilters (reverse direction)
	var asStore store.ListFilters = asAPI
	if asStore.Parent != orig.Parent {
		t.Errorf("round-trip Parent: got %q; want %q", asStore.Parent, orig.Parent)
	}
	if asStore.Limit != orig.Limit {
		t.Errorf("round-trip Limit: got %d; want %d", asStore.Limit, orig.Limit)
	}
}
