package main

// verbs_test.go provides one success-path test and one documented-error-path
// test for each of the 15 callable manifest verbs.
//
// Design:
//   - Each verb has a string-in / string-out wrapper that calls runVerb with
//     the same fn closure as the C export, minus the *C.char I/O boundary.
//   - Each test row builds its own fresh *pkgapi.Client and mints its own handle
//     (SR-7.3 fresh-store-per-verb) via newVerbHandle.
//   - No test uses real tmux; verbs that require a live tmux session use
//     TmuxCommand:"true" so the underlying exec.Command calls exit 0.
//   - import "C" is NOT used here.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/internal/store"
)

// ─── test helpers ─────────────────────────────────────────────────────────────

// newVerbHandle creates a fresh *pkgapi.Client backed by a temp-dir store,
// mints a handle in the global registry, and registers Cleanup for both.
//
// If seedFn is non-nil it is called with the DB path before the Client is
// opened, so the seeded rows are visible on startup (same pattern as
// newTestClientWithRows in pkg/api/methods_test.go).
//
// tmuxCmd overrides the tmux binary; pass "/usr/bin/true" to make every tmux
// call exit 0 without starting a real session.
func newVerbHandle(t *testing.T, tmuxCmd string, seedFn func(dbPath string)) (*pkgapi.Client, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "state.db")
	opts := pkgapi.Options{
		StorePath:   dbPath,
		TmuxCommand: tmuxCmd,
	}
	if seedFn != nil {
		seedFn(dbPath)
		// seedFn called OpenOrInit; Open (no create) suffices now.
	} else {
		opts.CreateIfMissing = true
	}
	c, err := pkgapi.New(opts)
	if err != nil {
		t.Fatalf("newVerbHandle: api.New: %v", err)
	}
	handle := registry.mint(c)
	t.Cleanup(func() {
		registry.delete(handle)
		_ = c.Close()
	})
	return c, handle
}

// seedRow opens the store at dbPath, inserts a pending Spawn row with the
// given id, sessName, and state, then closes the store. relay_mode is "off".
func seedRow(t *testing.T, dbPath, id, sessName, state string) {
	t.Helper()
	s, err := store.OpenOrInit(dbPath)
	if err != nil {
		t.Fatalf("seedRow OpenOrInit: %v", err)
	}
	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  sessName,
		RelayMode:        "off",
	}); err != nil {
		_ = s.Close()
		t.Fatalf("seedRow InsertPending(%q): %v", id, err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			_ = s.Close()
			t.Fatalf("seedRow ApplyHookTransition(%q→%q): %v", id, state, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("seedRow Close: %v", err)
	}
}

// assertSuccessEnvelope fails the test if env contains an err_name key.
func assertSuccessEnvelope(t *testing.T, env []byte) {
	t.Helper()
	m := unmarshalObj(t, env)
	if name, has := m["err_name"]; has {
		t.Errorf("expected success envelope; got err_name=%v — full: %s", name, env)
	}
}

// assertErrName fails the test if the parsed envelope's err_name does not equal
// want, or if err_description is empty.
func assertErrName(t *testing.T, env []byte, want string) {
	t.Helper()
	m := unmarshalObj(t, env)
	if got := m["err_name"]; got != want {
		t.Errorf("err_name = %v; want %q — full: %s", got, want, env)
	}
	if desc, _ := m["err_description"].(string); desc == "" {
		t.Errorf("err_description is empty for err_name=%q", want)
	}
}

// ─── string wrappers ──────────────────────────────────────────────────────────
//
// Each wrapper replicates the inner fn closure of the corresponding C export.
// The cgo *C.char I/O boundary is irrelevant here; runVerb is the shared
// dispatch layer under test. The wrappers are also used by unknown_handle_test.go.

func adVersionStr(paramsJSON string) string {
	return string(runVerb("ad_version", []byte(paramsJSON), func(_ *pkgapi.Client, _ []byte) (any, error) {
		return pkgapi.Version()
	}))
}

func adSpawnStr(paramsJSON string) string {
	return string(runVerb("ad_spawn", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			CWD              string            `json:"cwd"`
			Template         string            `json:"template"`
			ClaudeInstanceID string            `json:"claude_instance_id"`
			Label            []string          `json:"label"`
			Allow            []string          `json:"allow"`
			Deny             []string          `json:"deny"`
			Ask              []string          `json:"ask"`
			RelayMode        string            `json:"relay_mode"`
			ExtraEnv         map[string]string `json:"extra_env"`
			ClaudeArgs       []string          `json:"claude_args"`
			NoPreTrust       bool              `json:"no_pre_trust"`
			TmuxSessionName  string            `json:"tmux_session_name"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_spawn: invalid params: %w", err)
		}
		labels := make(map[string]string, len(raw.Label))
		for _, kv := range raw.Label {
			k, v, ok := splitKV(kv)
			if !ok {
				return nil, fmt.Errorf("ad_spawn: invalid label %q (want key=value)", kv)
			}
			labels[k] = v
		}
		p := pkgapi.SpawnParams{
			CWD:                 raw.CWD,
			Template:            raw.Template,
			ClaudeInstanceID:    raw.ClaudeInstanceID,
			ExtraEnv:            raw.ExtraEnv,
			AgentDirectorLabels: labels,
			ClaudeArgs:          raw.ClaudeArgs,
			RelayMode:           raw.RelayMode,
			NoPreTrust:          raw.NoPreTrust,
			TmuxSessionName:     raw.TmuxSessionName,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &pkgapi.Permissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return client.Spawn(p)
	}))
}

func adStatusStr(paramsJSON string) string {
	return string(runVerb("ad_status", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_status: invalid params: %w", err)
		}
		return client.Status(p.ClaudeInstanceID)
	}))
}

func adGetStr(paramsJSON string) string {
	return string(runVerb("ad_get", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p struct {
			ClaudeInstanceID string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_get: invalid params: %w", err)
		}
		return client.Get(p.ClaudeInstanceID)
	}))
}

func adListStr(paramsJSON string) string {
	return string(runVerb("ad_list", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			State           []string `json:"state"`
			Label           []string `json:"label"`
			Parent          string   `json:"parent"`
			Cwd             string   `json:"cwd"`
			TmuxSessionName string   `json:"tmux_session_name"`
			Limit           int      `json:"limit"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_list: invalid params: %w", err)
		}
		return client.List(pkgapi.ListParams{
			State:           raw.State,
			Labels:          raw.Label,
			Parent:          raw.Parent,
			Cwd:             raw.Cwd,
			TmuxSessionName: raw.TmuxSessionName,
			Limit:           raw.Limit,
		})
	}))
}

func adSendKeysStr(paramsJSON string) string {
	return string(runVerb("ad_send_keys", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.SendKeysParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_send_keys: invalid params: %w", err)
		}
		return client.SendKeys(p)
	}))
}

func adReadPaneStr(paramsJSON string) string {
	return string(runVerb("ad_read_pane", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.ReadPaneParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_read_pane: invalid params: %w", err)
		}
		return client.ReadPane(p)
	}))
}

func adKillStr(paramsJSON string) string {
	return string(runVerb("ad_kill", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.KillParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_kill: invalid params: %w", err)
		}
		return client.Kill(p)
	}))
}

func adPauseStr(paramsJSON string) string {
	return string(runVerb("ad_pause", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		ctx, cancel := contextFromParams(params)
		defer cancel()
		var p pkgapi.PauseParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_pause: invalid params: %w", err)
		}
		return client.Pause(ctx, p)
	}))
}

func adDecideStr(paramsJSON string) string {
	return string(runVerb("ad_decide", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.DecideParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_decide: invalid params: %w", err)
		}
		return client.Decide(p)
	}))
}

func adResumeStr(paramsJSON string) string {
	return string(runVerb("ad_resume", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var p pkgapi.ResumeParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("ad_resume: invalid params: %w", err)
		}
		return client.Resume(p)
	}))
}

func adFindMissingStr(paramsJSON string) string {
	return string(runVerb("ad_find_missing", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		ctx, cancel := contextFromParams(params)
		defer cancel()
		return client.FindMissing(ctx)
	}))
}

func adExpireStr(paramsJSON string) string {
	return string(runVerb("ad_expire", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			OlderThan string `json:"older_than"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_expire: invalid params: %w", err)
		}
		var older *time.Duration
		if raw.OlderThan != "" {
			dur, err := parseDuration(raw.OlderThan)
			if err != nil {
				return nil, fmt.Errorf("ad_expire: older_than: %w", err)
			}
			older = &dur
		}
		return client.Expire(older)
	}))
}

func adDeleteStr(paramsJSON string) string {
	return string(runVerb("ad_delete", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			ClaudeInstanceID []string `json:"claude_instance_id"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_delete: invalid params: %w", err)
		}
		return client.Delete(raw.ClaudeInstanceID)
	}))
}

func adMakeTemplateStr(paramsJSON string) string {
	return string(runVerb("ad_make_template", []byte(paramsJSON), func(client *pkgapi.Client, params []byte) (any, error) {
		var raw struct {
			Name       string            `json:"name"`
			CWD        string            `json:"cwd"`
			RelayMode  string            `json:"relay_mode"`
			ClaudeArgs []string          `json:"claude_args"`
			ExtraEnv   map[string]string `json:"extra_env"`
			Label      []string          `json:"label"`
			Allow      []string          `json:"allow"`
			Deny       []string          `json:"deny"`
			Ask        []string          `json:"ask"`
		}
		if err := json.Unmarshal(params, &raw); err != nil {
			return nil, fmt.Errorf("ad_make_template: invalid params: %w", err)
		}
		labels := make(map[string]string, len(raw.Label))
		for _, kv := range raw.Label {
			k, v, ok := splitKV(kv)
			if !ok {
				return nil, fmt.Errorf("ad_make_template: invalid label %q (want key=value)", kv)
			}
			labels[k] = v
		}
		p := pkgapi.MakeTemplateParams{
			Name:                raw.Name,
			CWD:                 raw.CWD,
			RelayMode:           raw.RelayMode,
			ClaudeArgs:          raw.ClaudeArgs,
			ExtraEnv:            raw.ExtraEnv,
			AgentDirectorLabels: labels,
		}
		if len(raw.Allow) > 0 || len(raw.Deny) > 0 || len(raw.Ask) > 0 {
			p.Permissions = &pkgapi.MakeTemplatePermissions{
				Allow: raw.Allow,
				Deny:  raw.Deny,
				Ask:   raw.Ask,
			}
		}
		return client.MakeTemplate(p)
	}))
}

// ─── per-verb tests ───────────────────────────────────────────────────────────
//
// Each Test function covers one manifest verb with at least one success-path
// subtest and one documented-error subtest. SR-7.3: every subtest calls
// newVerbHandle independently to get its own fresh store.

// TestAdVersionVerb: handle-free; success verifies version/commit keys present.
func TestAdVersionVerb(t *testing.T) {
	result := adVersionStr(`{}`)
	m := unmarshalObj(t, []byte(result))
	assertSuccessEnvelope(t, []byte(result))
	if _, has := m["version"]; !has {
		t.Errorf("missing version key in version envelope: %s", result)
	}
	if _, has := m["commit"]; !has {
		t.Errorf("missing commit key in version envelope: %s", result)
	}
}

// TestAdSpawnVerb: success uses TmuxCommand:"true" to avoid real tmux;
// error path uses empty CWD → ErrCwdMissing before any tmux call.
func TestAdSpawnVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		cwd := t.TempDir()
		// Clear AGENT_DIRECTOR_INSTANCE_ID so spawn.Launch inserts a NULL
		// parent_id; a non-empty value would fail the FK constraint in the
		// fresh store (the referenced id does not exist there).
		t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
		_, handle := newVerbHandle(t, "true", nil)
		params := fmt.Sprintf(`{"handle":%q,"cwd":%q,"no_pre_trust":true}`, handle, cwd)
		result := adSpawnStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		if id, _ := m["claude_instance_id"].(string); id == "" {
			t.Errorf("claude_instance_id missing or empty: %s", result)
		}
	})
	t.Run("ErrCwdMissing", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q}`, handle)
		assertErrName(t, []byte(adSpawnStr(params)), "ErrCwdMissing")
	})
}

// TestAdStatusVerb: success uses pre-seeded pending row; error uses unknown id.
func TestAdStatusVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const id = "st-ok-01"
		_, handle := newVerbHandle(t, "", func(dbPath string) {
			seedRow(t, dbPath, id, "st-ok-sess", store.StatePending)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q}`, handle, id)
		result := adStatusStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		if state, _ := m["state"].(string); state != store.StatePending {
			t.Errorf("state = %q; want %q", state, store.StatePending)
		}
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id"}`, handle)
		assertErrName(t, []byte(adStatusStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdGetVerb: success returns full row for known id; error for unknown id.
func TestAdGetVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const id = "get-ok-01"
		_, handle := newVerbHandle(t, "", func(dbPath string) {
			seedRow(t, dbPath, id, "get-ok-sess", store.StatePending)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q}`, handle, id)
		result := adGetStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		if got, _ := m["claude_instance_id"].(string); got != id {
			t.Errorf("claude_instance_id = %q; want %q", got, id)
		}
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id"}`, handle)
		assertErrName(t, []byte(adGetStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdListVerb: success against empty store returns spawns:[]; error uses
// invalid label (missing '=') → ErrListInvalidLabel.
func TestAdListVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q}`, handle)
		result := adListStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		spawns, _ := m["spawns"].([]any)
		if len(spawns) != 0 {
			t.Errorf("spawns len = %d; want 0 for empty store", len(spawns))
		}
	})
	t.Run("ErrListInvalidLabel", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		// A label without '=' is invalid.
		params := fmt.Sprintf(`{"handle":%q,"label":["no-equals-sign"]}`, handle)
		assertErrName(t, []byte(adListStr(params)), "ErrListInvalidLabel")
	})
}

// TestAdSendKeysVerb: success uses waiting-state row and TmuxCommand:"true";
// error uses unknown id → ErrSpawnNotFound before any tmux call.
func TestAdSendKeysVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const id = "sk-ok-01"
		_, handle := newVerbHandle(t, "true", func(dbPath string) {
			seedRow(t, dbPath, id, "sk-ok-sess", store.StateWaiting)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q,"text":"hello"}`, handle, id)
		assertSuccessEnvelope(t, []byte(adSendKeysStr(params)))
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id","text":"x"}`, handle)
		assertErrName(t, []byte(adSendKeysStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdReadPaneVerb: success uses a seeded row and TmuxCommand:"true" (returns
// empty pane); error uses unknown id → ErrSpawnNotFound.
func TestAdReadPaneVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const id = "rp-ok-01"
		_, handle := newVerbHandle(t, "true", func(dbPath string) {
			seedRow(t, dbPath, id, "rp-ok-sess", store.StateWaiting)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q}`, handle, id)
		result := adReadPaneStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		if _, has := m["pane"]; !has {
			t.Errorf("missing pane key in read_pane success envelope: %s", result)
		}
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id"}`, handle)
		assertErrName(t, []byte(adReadPaneStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdKillVerb: success on an ended-state row (terminal state is no-op, no
// tmux call needed); error for unknown id.
func TestAdKillVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const id = "kill-ok-01"
		_, handle := newVerbHandle(t, "", func(dbPath string) {
			seedRow(t, dbPath, id, "kill-ok-sess", store.StateEnded)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q}`, handle, id)
		assertSuccessEnvelope(t, []byte(adKillStr(params)))
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id"}`, handle)
		assertErrName(t, []byte(adKillStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdPauseVerb: success on an ended-state row (terminal state is no-op per
// SRD §9: "ended/missing → no-op success", no tmux send-keys needed);
// error for unknown id.
func TestAdPauseVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const id = "pause-ok-01"
		_, handle := newVerbHandle(t, "", func(dbPath string) {
			seedRow(t, dbPath, id, "pause-ok-sess", store.StateEnded)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q}`, handle, id)
		assertSuccessEnvelope(t, []byte(adPauseStr(params)))
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id"}`, handle)
		assertErrName(t, []byte(adPauseStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdDecideVerb: no clean success path without a full relay-mode setup, so the
// "success" subtest uses a row with relay_mode=off → ErrRelayModeOff (documented).
// Error: unknown id → ErrSpawnNotFound.
func TestAdDecideVerb(t *testing.T) {
	t.Run("expectedErrRelayModeOff", func(t *testing.T) {
		// relay_mode=off is set by seedRow; Decide requires relay_mode=on.
		const id = "decide-relay-01"
		_, handle := newVerbHandle(t, "", func(dbPath string) {
			seedRow(t, dbPath, id, "decide-relay-sess", store.StateWaiting)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q,"decision":"allow"}`, handle, id)
		assertErrName(t, []byte(adDecideStr(params)), "ErrRelayModeOff")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id","decision":"allow"}`, handle)
		assertErrName(t, []byte(adDecideStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdResumeVerb: no clean success path without a JSONL file + real tmux, so
// the "success" subtest uses a waiting-state row → ErrSpawnNotResumable
// (documented: resume requires ended/missing state, not waiting).
// Error: unknown id → ErrSpawnNotFound.
func TestAdResumeVerb(t *testing.T) {
	t.Run("expectedErrSpawnNotResumable", func(t *testing.T) {
		const id = "resume-not-res-01"
		_, handle := newVerbHandle(t, "", func(dbPath string) {
			seedRow(t, dbPath, id, "resume-not-res-sess", store.StateWaiting)
		})
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":%q}`, handle, id)
		assertErrName(t, []byte(adResumeStr(params)), "ErrSpawnNotResumable")
	})
	t.Run("ErrSpawnNotFound", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":"no-such-id"}`, handle)
		assertErrName(t, []byte(adResumeStr(params)), "ErrSpawnNotFound")
	})
}

// TestAdFindMissingVerb: success on empty store → count=0, ids=[].
// ErrProbeUnsupported is only returned by probe_unsupported.go on platforms
// without a probe implementation (not Linux, not macOS); that path is not
// reachable on this Linux target so it is covered by a skip-guarded subtest.
func TestAdFindMissingVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q}`, handle)
		result := adFindMissingStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		if count, _ := m["count"].(float64); count != 0 {
			t.Errorf("count = %v; want 0 for empty store", count)
		}
	})
	t.Run("ErrProbeUnsupported", func(t *testing.T) {
		// ErrProbeUnsupported is only possible on platforms using
		// probe_unsupported.go (build tag). On Linux this path is unreachable.
		t.Skip("ErrProbeUnsupported is not triggerable on Linux (probe_linux.go reads /proc)")
	})
}

// TestAdExpireVerb: success with older_than:"0d" on empty store → count=0.
// expire has no documented error names; no error subtest needed.
func TestAdExpireVerb(t *testing.T) {
	_, handle := newVerbHandle(t, "", nil)
	params := fmt.Sprintf(`{"handle":%q,"older_than":"0d"}`, handle)
	result := adExpireStr(params)
	assertSuccessEnvelope(t, []byte(result))
	m := unmarshalObj(t, []byte(result))
	if count, _ := m["count"].(float64); count != 0 {
		t.Errorf("count = %v; want 0 for empty store", count)
	}
}

// TestAdDeleteVerb: success with empty ID list → results map is empty.
// delete has no documented error names; no error subtest needed.
func TestAdDeleteVerb(t *testing.T) {
	_, handle := newVerbHandle(t, "", nil)
	params := fmt.Sprintf(`{"handle":%q,"claude_instance_id":[]}`, handle)
	result := adDeleteStr(params)
	assertSuccessEnvelope(t, []byte(result))
	m := unmarshalObj(t, []byte(result))
	results, _ := m["results"].(map[string]any)
	if len(results) != 0 {
		t.Errorf("results len = %d; want 0 for empty input", len(results))
	}
}

// TestAdMakeTemplateVerb: success creates a template and returns its path;
// error uses an unsafe name (path-traversal) → ErrTemplateNameUnsafe.
func TestAdMakeTemplateVerb(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"name":"test-tmpl-cabi"}`, handle)
		result := adMakeTemplateStr(params)
		assertSuccessEnvelope(t, []byte(result))
		m := unmarshalObj(t, []byte(result))
		if path, _ := m["path"].(string); path == "" {
			t.Errorf("path missing or empty in make_template success envelope: %s", result)
		}
	})
	t.Run("ErrTemplateNameUnsafe", func(t *testing.T) {
		_, handle := newVerbHandle(t, "", nil)
		params := fmt.Sprintf(`{"handle":%q,"name":"../unsafe-name"}`, handle)
		assertErrName(t, []byte(adMakeTemplateStr(params)), "ErrTemplateNameUnsafe")
	})
}
