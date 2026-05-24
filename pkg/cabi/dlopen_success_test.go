//go:build cabi_dlopen

package main

// dlopen_success_test.go provides per-verb success-path wire tests that
// exercise the C-ABI boundary via dlopen/dlsym rather than the in-process
// string wrappers in verbs_test.go.
//
// For each verb in manifest.CallableVerbs():
//   - A fresh temp store is created (fresh-store-per-verb, SR-7.3).
//   - The verb is invoked through the .so loaded in TestMain.
//   - Every non-nullable ResultField listed in the manifest is asserted
//     present in the returned envelope (structural presence only — exact
//     values are not pinned here; business logic is covered by verbs_test.go).
//
// Verbs with no clean happy path (decide, resume) follow the same workaround
// as verbs_test.go: the nearest reachable documented error is used as the
// "success" subtest and the subtest name reflects this.
//
// NOTE on environment isolation: the .so loaded by TestMain has its own Go
// runtime whose environment was captured at dlopen time. t.Setenv changes do
// NOT propagate to the .so's os.Getenv calls. Tests that create filesystem
// artifacts (e.g. make-template) must therefore use unique names and register
// explicit t.Cleanup functions to delete those artifacts from the real home
// directory.
//
// NOTE: import "C" is NOT used. All cgo is in dlopen_helper.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// TestDlopenVerbsSuccess iterates every callable manifest verb and runs one
// success-path subtest per verb through the loaded .so.
func TestDlopenVerbsSuccess(t *testing.T) {
	for _, v := range manifest.CallableVerbs() {
		v := v // capture
		t.Run(v.Name, func(t *testing.T) {
			dlopenSuccessVerb(t, v)
		})
	}
}

// dlopenSuccessVerb dispatches to the per-verb success helper.
func dlopenSuccessVerb(t *testing.T, v manifest.VerbDef) {
	t.Helper()
	switch v.Name {
	case "version":
		dlopenSuccessVersion(t, v)
	case "spawn":
		dlopenSuccessSpawn(t, v)
	case "status":
		dlopenSuccessStatus(t, v)
	case "get":
		dlopenSuccessGet(t, v)
	case "list":
		dlopenSuccessList(t, v)
	case "send-keys":
		dlopenSuccessSendKeys(t, v)
	case "read-pane":
		dlopenSuccessReadPane(t, v)
	case "kill":
		dlopenSuccessKill(t, v)
	case "pause":
		dlopenSuccessPause(t, v)
	case "decide":
		dlopenSuccessDecide(t, v)
	case "resume":
		dlopenSuccessResume(t, v)
	case "find-missing":
		dlopenSuccessFindMissing(t, v)
	case "expire":
		dlopenSuccessExpire(t, v)
	case "delete":
		dlopenSuccessDelete(t, v)
	case "make-template":
		dlopenSuccessMakeTemplate(t, v)
	default:
		t.Errorf("unhandled verb %q — add a dlopen success case", v.Name)
	}
}

// assertResultFields checks that every non-nullable ResultField in def is
// present as a key in envelope. Nullable fields (ended_at, permission_request)
// are skipped because they appear only in specific states.
func assertResultFields(t *testing.T, def manifest.VerbDef, envelope map[string]any) {
	t.Helper()
	for _, f := range def.ResultFields {
		if f.Nullable {
			continue // optional — may be absent in a well-formed response
		}
		if _, has := envelope[f.Name]; !has {
			t.Errorf("result field %q missing from envelope (verb %q)", f.Name, def.Name)
		}
	}
}

// ─── per-verb success helpers ─────────────────────────────────────────────────

// dlopenSuccessVersion: handle-free; asserts version and commit keys.
func dlopenSuccessVersion(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	raw := dlopenInvoke(t, "ad_version", []byte(`{}`))
	assertDlopenSuccess(t, raw)
	assertResultFields(t, def, parseEnvelope(t, raw))
}

// dlopenSuccessSpawn: TmuxCommand="true" avoids a real tmux session.
//
// The .so's Go runtime captured AGENT_DIRECTOR_INSTANCE_ID at dlopen time (see
// dlopenParentID in dlopen_test.go). If non-empty, spawn.Launch will try to use
// it as parent_id and the FK constraint will fail on a fresh DB. We seed the DB
// with a pending row whose claude_instance_id equals dlopenParentID so the
// constraint is satisfied — matching the in-process workaround in verbs_test.go.
func dlopenSuccessSpawn(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)

	// If the .so will inject a parent_id, seed the DB with a matching row.
	if dlopenParentID != "" {
		seedRow(t, dbPath, dlopenParentID, "dlopen-spawn-parent-sess", store.StatePending)
	}

	cwd := t.TempDir()
	handle := dlopenOpen(t, dbPath, "true")
	type p struct {
		Handle     string `json:"handle"`
		CWD        string `json:"cwd"`
		NoPreTrust bool   `json:"no_pre_trust"`
	}
	params, _ := json.Marshal(p{Handle: handle, CWD: cwd, NoPreTrust: true})
	raw := dlopenInvoke(t, "ad_spawn", params)
	assertDlopenSuccess(t, raw)
	assertResultFields(t, def, parseEnvelope(t, raw))
}

// dlopenSuccessStatus: pre-seeds a pending row, then queries its state.
func dlopenSuccessStatus(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	const id = "dl-status-ok-01"
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)
	seedRow(t, dbPath, id, "dl-status-sess", store.StatePending)
	handle := dlopenOpen(t, dbPath, "")
	type p struct {
		Handle           string `json:"handle"`
		ClaudeInstanceID string `json:"claude_instance_id"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
	raw := dlopenInvoke(t, "ad_status", params)
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	if state, _ := m["state"].(string); state != store.StatePending {
		t.Errorf("state = %q; want %q", state, store.StatePending)
	}
}

// dlopenSuccessGet: pre-seeds a pending row and verifies full row is returned.
func dlopenSuccessGet(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	const id = "dl-get-ok-01"
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)
	seedRow(t, dbPath, id, "dl-get-sess", store.StatePending)
	handle := dlopenOpen(t, dbPath, "")
	type p struct {
		Handle           string `json:"handle"`
		ClaudeInstanceID string `json:"claude_instance_id"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
	raw := dlopenInvoke(t, "ad_get", params)
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	if got, _ := m["claude_instance_id"].(string); got != id {
		t.Errorf("claude_instance_id = %q; want %q", got, id)
	}
}

// dlopenSuccessList: empty store → spawns:[] (no filters).
func dlopenSuccessList(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	home := t.TempDir()
	handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
	params := fmt.Sprintf(`{"handle":%q}`, handle)
	raw := dlopenInvoke(t, "ad_list", []byte(params))
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	spawns, _ := m["spawns"].([]any)
	if len(spawns) != 0 {
		t.Errorf("spawns len = %d; want 0 for empty store", len(spawns))
	}
}

// dlopenSuccessSendKeys: waiting-state row + TmuxCommand="true" so tmux exits 0.
func dlopenSuccessSendKeys(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	const id = "dl-sk-ok-01"
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)
	seedRow(t, dbPath, id, "dl-sk-sess", store.StateWaiting)
	handle := dlopenOpen(t, dbPath, "true")
	type p struct {
		Handle           string `json:"handle"`
		ClaudeInstanceID string `json:"claude_instance_id"`
		Text             string `json:"text"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id, Text: "hello"})
	raw := dlopenInvoke(t, "ad_send_keys", params)
	assertDlopenSuccess(t, raw)
	// send-keys has no ResultFields; success = no err_name.
}

// dlopenSuccessReadPane: waiting-state row + TmuxCommand="true"; pane key present.
func dlopenSuccessReadPane(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	const id = "dl-rp-ok-01"
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)
	seedRow(t, dbPath, id, "dl-rp-sess", store.StateWaiting)
	handle := dlopenOpen(t, dbPath, "true")
	type p struct {
		Handle           string `json:"handle"`
		ClaudeInstanceID string `json:"claude_instance_id"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
	raw := dlopenInvoke(t, "ad_read_pane", params)
	assertDlopenSuccess(t, raw)
	assertResultFields(t, def, parseEnvelope(t, raw))
}

// dlopenSuccessKill: ended-state row is a no-op (terminal states are idempotent).
func dlopenSuccessKill(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	const id = "dl-kill-ok-01"
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)
	seedRow(t, dbPath, id, "dl-kill-sess", store.StateEnded)
	handle := dlopenOpen(t, dbPath, "")
	type p struct {
		Handle           string `json:"handle"`
		ClaudeInstanceID string `json:"claude_instance_id"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
	raw := dlopenInvoke(t, "ad_kill", params)
	assertDlopenSuccess(t, raw)
	// kill has no ResultFields; success = no err_name.
}

// dlopenSuccessPause: ended-state row is a no-op success per SRD §9.
func dlopenSuccessPause(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	const id = "dl-pause-ok-01"
	home := t.TempDir()
	dbPath := fmt.Sprintf("%s/state.db", home)
	seedRow(t, dbPath, id, "dl-pause-sess", store.StateEnded)
	handle := dlopenOpen(t, dbPath, "")
	type p struct {
		Handle           string `json:"handle"`
		ClaudeInstanceID string `json:"claude_instance_id"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
	raw := dlopenInvoke(t, "ad_pause", params)
	assertDlopenSuccess(t, raw)
	// pause has no ResultFields; success = no err_name.
}

// dlopenSuccessDecide: no clean success path without relay_mode=on + pending
// permission request. Following verbs_test.go, relay_mode=off triggers the
// nearest documented error: ErrRelayModeOff. The subtest name reflects this.
func dlopenSuccessDecide(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("expectedErrRelayModeOff", func(t *testing.T) {
		const id = "dl-decide-relay-01"
		home := t.TempDir()
		dbPath := fmt.Sprintf("%s/state.db", home)
		seedRow(t, dbPath, id, "dl-decide-sess", store.StateWaiting)
		handle := dlopenOpen(t, dbPath, "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
			Decision         string `json:"decision"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id, Decision: "allow"})
		raw := dlopenInvoke(t, "ad_decide", params)
		assertDlopenErrName(t, raw, "ErrRelayModeOff")
	})
}

// dlopenSuccessResume: no clean success path without a JSONL transcript.
// Following verbs_test.go, waiting-state row triggers ErrSpawnNotResumable.
func dlopenSuccessResume(t *testing.T, _ manifest.VerbDef) {
	t.Helper()
	t.Run("expectedErrSpawnNotResumable", func(t *testing.T) {
		const id = "dl-resume-nr-01"
		home := t.TempDir()
		dbPath := fmt.Sprintf("%s/state.db", home)
		seedRow(t, dbPath, id, "dl-resume-sess", store.StateWaiting)
		handle := dlopenOpen(t, dbPath, "")
		type p struct {
			Handle           string `json:"handle"`
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: id})
		raw := dlopenInvoke(t, "ad_resume", params)
		assertDlopenErrName(t, raw, "ErrSpawnNotResumable")
	})
}

// dlopenSuccessFindMissing: empty store → count=0, ids=[].
// ErrProbeUnsupported is only reachable on non-Linux platforms; that path is
// skipped here (matches verbs_test.go).
func dlopenSuccessFindMissing(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	home := t.TempDir()
	handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
	params := fmt.Sprintf(`{"handle":%q}`, handle)
	raw := dlopenInvoke(t, "ad_find_missing", []byte(params))
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	if count, _ := m["count"].(float64); count != 0 {
		t.Errorf("count = %v; want 0 for empty store", count)
	}
}

// dlopenSuccessExpire: older_than:"0d" on empty store → count=0.
func dlopenSuccessExpire(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	home := t.TempDir()
	handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
	type p struct {
		Handle    string `json:"handle"`
		OlderThan string `json:"older_than"`
	}
	params, _ := json.Marshal(p{Handle: handle, OlderThan: "0d"})
	raw := dlopenInvoke(t, "ad_expire", params)
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	if count, _ := m["count"].(float64); count != 0 {
		t.Errorf("count = %v; want 0 for empty store", count)
	}
}

// dlopenSuccessDelete: empty ID list → results map is empty.
func dlopenSuccessDelete(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	home := t.TempDir()
	handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
	type p struct {
		Handle           string   `json:"handle"`
		ClaudeInstanceID []string `json:"claude_instance_id"`
	}
	params, _ := json.Marshal(p{Handle: handle, ClaudeInstanceID: []string{}})
	raw := dlopenInvoke(t, "ad_delete", params)
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	results, _ := m["results"].(map[string]any)
	if len(results) != 0 {
		t.Errorf("results len = %d; want 0 for empty input", len(results))
	}
}

// dlopenSuccessMakeTemplate: creates a named template and asserts path is returned.
//
// The .so's Go runtime has its own environment copy captured at dlopen time;
// t.Setenv("HOME", …) does NOT redirect where the .so writes templates.
// Instead we use a name that is unique per test process (PID-based), and we
// register a t.Cleanup that deletes the resulting file from the real home
// directory so the test is self-contained across repeated runs.
func dlopenSuccessMakeTemplate(t *testing.T, def manifest.VerbDef) {
	t.Helper()
	// Unique name: avoids ErrTemplateExists if the process is re-run.
	name := fmt.Sprintf("dlopen-tmpl-%d", os.Getpid())

	// Register cleanup before the call so it always fires, even on t.Fatal.
	realHome, homeErr := os.UserHomeDir()
	if homeErr == nil {
		t.Cleanup(func() {
			tmplPath := filepath.Join(realHome, ".agent-director", "templates", name+".toml")
			_ = os.Remove(tmplPath) // best-effort; ignore error if absent
		})
	}

	home := t.TempDir()
	handle := dlopenOpen(t, fmt.Sprintf("%s/state.db", home), "")
	type p struct {
		Handle string `json:"handle"`
		Name   string `json:"name"`
	}
	params, _ := json.Marshal(p{Handle: handle, Name: name})
	raw := dlopenInvoke(t, "ad_make_template", params)
	assertDlopenSuccess(t, raw)
	m := parseEnvelope(t, raw)
	assertResultFields(t, def, m)
	if path, _ := m["path"].(string); path == "" {
		t.Errorf("path missing or empty in make_template envelope: %s", raw)
	}
}
