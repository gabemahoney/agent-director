package spawn

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/claude-director/internal/config"
)

// withStubExe redirects executablePath for the duration of a test so JSON
// assertions don't depend on the test binary's actual location on disk.
func withStubExe(t *testing.T, path string) {
	t.Helper()
	saved := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = saved })
}

// settingsShape is the minimal JSON shape we assert on. Keeping it loose
// (any-typed value for nested objects) lets the tests focus on the
// presence-and-structure invariants rather than re-spec'ing Claude Code's
// settings schema.
type settingsShape struct {
	Hooks       map[string]any `json:"hooks"`
	Permissions map[string]any `json:"permissions"`
}

func TestSynthesizeSettingsContainsAllEightHooks(t *testing.T) {
	withStubExe(t, "/usr/local/bin/claude-director")
	jsonStr, err := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		config.Default(),
	)
	if err != nil {
		t.Fatalf("synthesizeSettings: %v", err)
	}
	var got settingsShape
	if err := json.Unmarshal([]byte(jsonStr), &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, jsonStr)
	}
	wantEvents := []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"Stop", "Notification", "SessionEnd", "PermissionRequest",
	}
	for _, evt := range wantEvents {
		if _, ok := got.Hooks[evt]; !ok {
			t.Errorf("hooks missing event %q", evt)
		}
	}
	if got.Permissions != nil {
		t.Errorf("permissions should be omitted when no overlay supplied; got %v", got.Permissions)
	}
}

func TestSynthesizeSettingsMatcherFields(t *testing.T) {
	withStubExe(t, "/usr/local/bin/claude-director")
	jsonStr, _ := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		config.Default(),
	)
	var top map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &top); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	hooks, _ := top["hooks"].(map[string]any)
	check := func(evt string, wantMatcher bool) {
		entries, _ := hooks[evt].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s: expected 1 entry, got %d", evt, len(entries))
		}
		entry, _ := entries[0].(map[string]any)
		_, hasMatcher := entry["matcher"]
		if hasMatcher != wantMatcher {
			t.Errorf("%s: matcher present = %v; want %v", evt, hasMatcher, wantMatcher)
		}
		// Hook command structure: [{type:command, command:"<bin> hook"}]
		hooksList, _ := entry["hooks"].([]any)
		if len(hooksList) != 1 {
			t.Fatalf("%s: expected 1 hook command, got %d", evt, len(hooksList))
		}
		cmdEntry, _ := hooksList[0].(map[string]any)
		if cmdEntry["type"] != "command" {
			t.Errorf("%s: type = %v; want command", evt, cmdEntry["type"])
		}
		cmdStr, _ := cmdEntry["command"].(string)
		if !strings.HasSuffix(cmdStr, " hook") {
			t.Errorf("%s: command = %q; want suffix ' hook'", evt, cmdStr)
		}
	}
	check("PreToolUse", true)
	check("PermissionRequest", true)
	check("SessionStart", false)
	check("Stop", false)
	check("SessionEnd", false)
}

func TestSynthesizeSettingsBinaryPathIsAbsolute(t *testing.T) {
	withStubExe(t, "/opt/claude-director/bin/claude-director")
	jsonStr, _ := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		config.Default(),
	)
	if !strings.Contains(jsonStr, "/opt/claude-director/bin/claude-director hook") {
		t.Fatalf("settings JSON does not embed the absolute path: %s", jsonStr)
	}
}

func TestSynthesizeSettingsQuotesPathWithWhitespace(t *testing.T) {
	withStubExe(t, "/opt/with space/claude-director")
	jsonStr, _ := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		config.Default(),
	)
	// Parse the JSON to see the command's *decoded* value — that's where
	// the defensive quoting must show up. Comparing the raw JSON string
	// would hit JSON's own backslash-escaping and produce a brittle check.
	var top map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &top); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	hooks, _ := top["hooks"].(map[string]any)
	entries, _ := hooks["SessionStart"].([]any)
	entry, _ := entries[0].(map[string]any)
	hl, _ := entry["hooks"].([]any)
	cmdEntry, _ := hl[0].(map[string]any)
	cmd, _ := cmdEntry["command"].(string)
	want := `"/opt/with space/claude-director" hook`
	if cmd != want {
		t.Fatalf("command = %q; want %q (path with whitespace should be defensively quoted)", cmd, want)
	}
}

func TestSynthesizeSettingsPermissionsBlock(t *testing.T) {
	withStubExe(t, "/bin/x")
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID: "id",
		Permissions: &Permissions{
			Allow: []string{"Bash(go test)"},
			Deny:  []string{"Bash(rm -rf)"},
			Ask:   []string{"WebFetch"},
		},
	}}
	jsonStr, _ := synthesizeSettings(r, config.Default())
	var top map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &top); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	perm, ok := top["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("permissions block missing")
	}
	if !equalStringList(perm["allow"], []string{"Bash(go test)"}) {
		t.Errorf("allow = %v; want [Bash(go test)]", perm["allow"])
	}
	if !equalStringList(perm["deny"], []string{"Bash(rm -rf)"}) {
		t.Errorf("deny = %v; want [Bash(rm -rf)]", perm["deny"])
	}
	if !equalStringList(perm["ask"], []string{"WebFetch"}) {
		t.Errorf("ask = %v; want [WebFetch]", perm["ask"])
	}
}

func TestSynthesizeSettingsDisableAskUserQuestionAlone(t *testing.T) {
	withStubExe(t, "/bin/x")
	cfg := config.Default()
	cfg.Defaults.DisableAskUserQuestion = true
	jsonStr, _ := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		cfg,
	)
	var top map[string]any
	_ = json.Unmarshal([]byte(jsonStr), &top)
	perm, _ := top["permissions"].(map[string]any)
	if !equalStringList(perm["deny"], []string{"AskUserQuestion"}) {
		t.Fatalf("deny = %v; want [AskUserQuestion]", perm["deny"])
	}
}

func TestSynthesizeSettingsDisableAskUserQuestionAdditive(t *testing.T) {
	withStubExe(t, "/bin/x")
	cfg := config.Default()
	cfg.Defaults.DisableAskUserQuestion = true
	r := Resolved{SpawnParams: SpawnParams{
		ClaudeInstanceID: "id",
		Permissions:      &Permissions{Deny: []string{"Bash(rm -rf)"}},
	}}
	jsonStr, _ := synthesizeSettings(r, cfg)
	var top map[string]any
	_ = json.Unmarshal([]byte(jsonStr), &top)
	perm, _ := top["permissions"].(map[string]any)
	want := []string{"AskUserQuestion", "Bash(rm -rf)"}
	if !equalStringList(perm["deny"], want) {
		t.Fatalf("deny = %v; want %v", perm["deny"], want)
	}
}

func TestSynthesizeSettingsDisabledFalseLeavesDenyAlone(t *testing.T) {
	withStubExe(t, "/bin/x")
	cfg := config.Default()
	cfg.Defaults.DisableAskUserQuestion = false
	jsonStr, _ := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		cfg,
	)
	if strings.Contains(jsonStr, "AskUserQuestion") {
		t.Fatalf("settings JSON should not mention AskUserQuestion when disabled-flag=false: %s", jsonStr)
	}
}

// equalStringList accepts either nil/[]string or []any (json.Unmarshal's
// default for arrays) and reports element-by-element equality. Used by
// permissions tests to compare against tightly-typed expectations.
func equalStringList(got any, want []string) bool {
	switch v := got.(type) {
	case nil:
		return len(want) == 0
	case []string:
		return reflect.DeepEqual(v, want)
	case []any:
		if len(v) != len(want) {
			return false
		}
		for i := range v {
			s, ok := v[i].(string)
			if !ok || s != want[i] {
				return false
			}
		}
		return true
	}
	return false
}

// TestSynthesizeSettingsRoundTrip parses the JSON back into Go and confirms
// every event's command points at the abs path passed in. This guards
// against regressions where a hook gets dropped or the command is rendered
// without the full path.
func TestSynthesizeSettingsRoundTrip(t *testing.T) {
	abs, err := filepath.Abs("/usr/local/bin/claude-director")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	withStubExe(t, abs)
	jsonStr, _ := synthesizeSettings(
		Resolved{SpawnParams: SpawnParams{ClaudeInstanceID: "id"}},
		config.Default(),
	)
	var top map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &top); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	hooks, _ := top["hooks"].(map[string]any)
	for _, evt := range hookEvents {
		entries, _ := hooks[string(evt)].([]any)
		entry, _ := entries[0].(map[string]any)
		hooksList, _ := entry["hooks"].([]any)
		cmdEntry, _ := hooksList[0].(map[string]any)
		cmd, _ := cmdEntry["command"].(string)
		if !strings.Contains(cmd, abs) {
			t.Errorf("event %s command %q does not contain abs path %q", evt, cmd, abs)
		}
	}
}
