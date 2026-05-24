//go:build cabi_dlopen

package main

// dlopen_error_test.go provides per-verb documented-error wire tests that
// exercise the C-ABI boundary via dlopen/dlsym.
//
// For each callable verb with len(ErrorNames) > 0:
//   - At minimum, one subtest drives the verb into ErrUnknownHandle by passing
//     an unregistered handle. This covers every handle-requiring verb.
//   - Additional subtests drive each verb into at least one of its documented
//     error states via params. The triggered err_name is asserted to be
//     present in errnames.Catalog (five-way coherence gate cross-check).
//
// Verbs without any ErrorNames (expire, delete) are skipped — they have no
// documented error states to drive.
//
// NOTE: import "C" is NOT used. All cgo is in dlopen_helper.go.

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// dlopenUnknownHandle is a syntactically valid 32-hex handle that is never
// registered in the .so's handle registry.
const dlopenUnknownHandle = "cafebabecafebabecafebabecafebabe"

// TestDlopenVerbsDocumentedErrors iterates every callable manifest verb that
// has documented error names and runs error-path wire tests through the loaded .so.
func TestDlopenVerbsDocumentedErrors(t *testing.T) {
	for _, v := range manifest.CallableVerbs() {
		v := v // capture
		if len(v.ErrorNames) == 0 {
			continue // no documented errors to drive
		}
		t.Run(v.Name, func(t *testing.T) {
			dlopenErrorVerb(t, v)
		})
	}
}

// assertErrInCatalog fails the test if errName is not present in errnames.Catalog.
// This is the dlopen-side equivalent of the five-way coherence gate: every
// err_name that crosses the C boundary must be registered in the catalog.
func assertErrInCatalog(t *testing.T, errName string) {
	t.Helper()
	for _, e := range errnames.Catalog {
		if e.Name == errName {
			return
		}
	}
	t.Errorf("err_name %q returned over C boundary but not in errnames.Catalog", errName)
}

// assertDlopenErrNameInCatalog is a combined assertion: verifies err_name equals
// want, err_description is non-empty, and want is registered in errnames.Catalog.
func assertDlopenErrNameInCatalog(t *testing.T, env []byte, want string) {
	t.Helper()
	assertDlopenErrName(t, env, want)
	assertErrInCatalog(t, want)
}

// unknownHandleParams builds a minimal params JSON with an unknown handle.
func unknownHandleParams() []byte {
	return []byte(fmt.Sprintf(`{"handle":%q}`, dlopenUnknownHandle))
}

// dlopenErrorVerb dispatches to the per-verb error helper.
func dlopenErrorVerb(t *testing.T, v manifest.VerbDef) {
	t.Helper()
	switch v.Name {
	case "spawn":
		dlopenErrorSpawn(t, v)
	case "status":
		dlopenErrorStatus(t, v)
	case "get":
		dlopenErrorGet(t, v)
	case "send-keys":
		dlopenErrorSendKeys(t, v)
	case "read-pane":
		dlopenErrorReadPane(t, v)
	case "kill":
		dlopenErrorKill(t, v)
	case "decide":
		dlopenErrorDecide(t, v)
	case "resume":
		dlopenErrorResume(t, v)
	case "find-missing":
		dlopenErrorFindMissing(t, v)
	case "pause":
		dlopenErrorPause(t, v)
	case "list":
		dlopenErrorList(t, v)
	case "make-template":
		dlopenErrorMakeTemplate(t, v)
	default:
		t.Errorf("unhandled verb %q with ErrorNames — add a dlopen error case", v.Name)
	}
}

// ─── per-verb error helpers ───────────────────────────────────────────────────

// dlopenErrorSpawn tests ErrUnknownHandle (unknown handle) and ErrCwdMissing
// (valid handle but no cwd supplied).
func dlopenErrorSpawn(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_spawn", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrCwdMissing", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		// Omit cwd entirely — ErrCwdMissing fires before any tmux call.
		params := fmt.Sprintf(`{"handle":%q}`, handle)
		raw := dlopenInvoke(t, "ad_spawn", []byte(params))
		assertDlopenErrNameInCatalog(t, raw, "ErrCwdMissing")
	})
}

// dlopenErrorStatus tests ErrUnknownHandle and ErrSpawnNotFound.
func dlopenErrorStatus(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_status", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id"})
		raw := dlopenInvoke(t, "ad_status", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
}

// dlopenErrorGet tests ErrUnknownHandle and ErrSpawnNotFound.
func dlopenErrorGet(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_get", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id"})
		raw := dlopenInvoke(t, "ad_get", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
}

// dlopenErrorSendKeys tests ErrUnknownHandle and ErrSpawnNotFound.
func dlopenErrorSendKeys(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_send_keys", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
			Text             string `json:"text"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id", Text: "x"})
		raw := dlopenInvoke(t, "ad_send_keys", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
}

// dlopenErrorReadPane tests ErrUnknownHandle and ErrSpawnNotFound.
func dlopenErrorReadPane(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_read_pane", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id"})
		raw := dlopenInvoke(t, "ad_read_pane", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
}

// dlopenErrorKill tests ErrUnknownHandle and ErrSpawnNotFound.
func dlopenErrorKill(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_kill", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id"})
		raw := dlopenInvoke(t, "ad_kill", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
}

// dlopenErrorDecide tests ErrUnknownHandle, ErrSpawnNotFound, and ErrRelayModeOff.
func dlopenErrorDecide(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_decide", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
			Decision         string `json:"decision"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id", Decision: "allow"})
		raw := dlopenInvoke(t, "ad_decide", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
	t.Run("ErrRelayModeOff", func(t *testing.T) {
		const id = "dl-decide-err-relay-01"
		home := t.TempDir()
		dbPath := fmt.Sprintf("%s/state.db", home)
		// seedRow sets relay_mode=off (default). Decide requires relay_mode=on.
		seedRow(t, dbPath, id, "dl-decide-err-sess", store.StateWaiting)
		handle := dlopenOpen(t, dbPath, "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
			Decision         string `json:"decision"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id, Decision: "allow"})
		raw := dlopenInvoke(t, "ad_decide", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrRelayModeOff")
	})
}

// dlopenErrorResume tests ErrUnknownHandle, ErrSpawnNotFound, and
// ErrSpawnNotResumable (waiting-state row is not in a terminal state).
func dlopenErrorResume(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_resume", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id"})
		raw := dlopenInvoke(t, "ad_resume", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
	t.Run("ErrSpawnNotResumable", func(t *testing.T) {
		const id = "dl-resume-err-nr-01"
		home := t.TempDir()
		dbPath := fmt.Sprintf("%s/state.db", home)
		// waiting is not a terminal state — resume requires ended/missing.
		seedRow(t, dbPath, id, "dl-resume-err-sess", store.StateWaiting)
		handle := dlopenOpen(t, dbPath, "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
		raw := dlopenInvoke(t, "ad_resume", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotResumable")
	})
}

// dlopenErrorFindMissing tests ErrUnknownHandle. ErrProbeUnsupported is only
// reachable on platforms without a probe implementation (not Linux); skipped
// here matching the same skip in verbs_test.go.
func dlopenErrorFindMissing(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_find_missing", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrProbeUnsupported", func(t *testing.T) {
		t.Skip("ErrProbeUnsupported not reachable on Linux (probe_linux.go reads /proc)")
	})
}

// dlopenErrorPause tests ErrUnknownHandle and ErrSpawnNotFound.
func dlopenErrorPause(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_pause", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: "no-such-id"})
		raw := dlopenInvoke(t, "ad_pause", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrSpawnNotFound")
	})
}

// dlopenErrorList tests ErrUnknownHandle and ErrListInvalidLabel.
func dlopenErrorList(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_list", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrListInvalidLabel", func(t *testing.T) {
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		// A label without '=' is invalid.
		type p struct {
			Handle string   `json:"handle"`
			Label  []string `json:"label"`
		}
		params, _ := json.Marshal(p{Handle: handle, Label: []string{"no-equals-sign"}})
		raw := dlopenInvoke(t, "ad_list", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrListInvalidLabel")
	})
}

// dlopenErrorMakeTemplate tests ErrUnknownHandle and ErrTemplateNameUnsafe.
func dlopenErrorMakeTemplate(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("ErrUnknownHandle", func(t *testing.T) {
		raw := dlopenInvoke(t, "ad_make_template", unknownHandleParams())
		assertDlopenErrNameInCatalog(t, raw, "ErrUnknownHandle")
	})
	t.Run("ErrTemplateNameUnsafe", func(t *testing.T) {
		// The .so's Go runtime uses the real HOME; no t.Setenv needed here
		// because ErrTemplateNameUnsafe is rejected before any filesystem write.
		home := t.TempDir()
		handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
		type p struct {
			Handle string `json:"handle"`
			Name   string `json:"name"`
		}
		// Path-traversal name is always rejected by ErrTemplateNameUnsafe.
		params, _ := json.Marshal(p{Handle: handle, Name: "../unsafe-name"})
		raw := dlopenInvoke(t, "ad_make_template", params)
		assertDlopenErrNameInCatalog(t, raw, "ErrTemplateNameUnsafe")
	})
}
