package spawn

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// withStubExe redirects executablePath for the duration of a test so JSON
// assertions don't depend on the test binary's actual location on disk.
func withStubExe(t *testing.T, path string) {
	t.Helper()
	saved := executablePath
	executablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { executablePath = saved })
}

// TestExecutablePathResolvesSymlink pins SR-1.8 and b.ue3: the real
// executablePath of a binary run through a symlink is the binary's own
// resolved path, never the link, so hook commands name the installed file.
func TestExecutablePathResolvesSymlink(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "agent-director")
	if err := os.Symlink(want, link); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(link, "-test.run=^$") //nolint:gosec // the test binary itself, through the link
	cmd.Env = append(os.Environ(), envExePathChild+"=1")
	out, err := cmd.Output()
	if got := string(out); err != nil || got != want || got == link {
		t.Errorf("executablePath() run through %s = %q, %v; want %q", link, got, err, want)
	}
}

// withStubHelpBin redirects helpHookBinPath for the duration of a test.
func withStubHelpBin(t *testing.T, path string) {
	t.Helper()
	saved := helpHookBinPath
	helpHookBinPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { helpHookBinPath = saved })
}

// TestHelpHookBinPathUnderConfigDir pins the unstubbed help-hook binary path:
// under a given $HOME it is <HOME>/.config/agent-director/bin/agent-director,
// both from helpHookBinPath itself and in the SessionStart help entry the
// settings carry. It sets $HOME, so it is serial.
func TestHelpHookBinPathUnderConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "agent-director", "bin", "agent-director")

	got, err := helpHookBinPath()
	if err != nil || got != want {
		t.Fatalf("helpHookBinPath() = %q, %v; want %q", got, err, want)
	}

	withStubExe(t, "/opt/ad/bin/agent-director")
	cfg := config.Default()
	cfg.Defaults.InjectHelpHook = true
	settings, err := synthesizeSettings(Resolved{SpawnParams: SpawnParams{InstanceID: "id-help"}}, cfg)
	if err != nil {
		t.Fatalf("synthesizeSettings: %v", err)
	}
	assertExecFormSettings(t, settings, "/opt/ad/bin/agent-director", want+" help", cfg)
}

// settingsShape is the part of the synthesized settings the permission tests read.
type settingsShape struct {
	Hooks       map[string]any `json:"hooks"`
	Permissions map[string]any `json:"permissions"`
}

// synthSettings synthesizes r's settings under cfg, failing the test on an error.
func synthSettings(t *testing.T, r Resolved, cfg config.Config) string {
	t.Helper()
	r.InstanceID = "id"
	got, err := synthesizeSettings(r, cfg)
	if err != nil {
		t.Fatalf("synthesizeSettings: %v", err)
	}
	return got
}

// TestSynthesizeSettingsPermissions: the per-spawn overlay is written as given,
// disable_ask_user adds AskUserQuestion to deny first, and with
// neither the permissions block is omitted.
func TestSynthesizeSettingsPermissions(t *testing.T) {
	withStubExe(t, "/bin/x")
	overlay := &Permissions{Allow: []string{"Bash(go test)"}, Deny: []string{"Bash(rm -rf)"}, Ask: []string{"WebFetch"}}
	cases := []struct {
		name       string
		perms      *Permissions
		disableAUQ bool
		want       map[string]any
	}{
		{name: "none"},
		{name: "overlay", perms: overlay,
			want: map[string]any{"allow": []any{"Bash(go test)"}, "deny": []any{"Bash(rm -rf)"}, "ask": []any{"WebFetch"}}},
		{name: "AskUserQuestion alone", disableAUQ: true, want: map[string]any{"deny": []any{"AskUserQuestion"}}},
		{name: "AskUserQuestion before the overlay's deny", disableAUQ: true, perms: &Permissions{Deny: []string{"Bash(rm -rf)"}},
			want: map[string]any{"deny": []any{"AskUserQuestion", "Bash(rm -rf)"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Defaults.DisableAskUser = tc.disableAUQ
			var got settingsShape
			if err := json.Unmarshal([]byte(synthSettings(t, Resolved{SpawnParams: SpawnParams{Permissions: tc.perms}}, cfg)), &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got.Permissions, tc.want) {
				t.Errorf("permissions = %v; want %v", got.Permissions, tc.want)
			}
		})
	}
}

// claudeConfigWithStatusLine returns an ExtraEnv whose CLAUDE_CONFIG_DIR holds
// a settings.json with the user statusLine command "user-gauge".
func claudeConfigWithStatusLine(t *testing.T) map[string]string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"statusLine":{"type":"command","command":"user-gauge"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	return map[string]string{"CLAUDE_CONFIG_DIR": dir}
}

// assertContextStatuslines checks settings' status lines for the user command
// claudeConfigWithStatusLine writes: both wrapped in exe's context callback
// when disabled is false, neither key present when it is true. It returns the
// decoded settings.
func assertContextStatuslines(t *testing.T, settings, exe string, disabled bool) map[string]any {
	t.Helper()
	var top map[string]any
	if err := json.Unmarshal([]byte(settings), &top); err != nil {
		t.Fatalf("settings %q: %v", settings, err)
	}
	prefix := "exec '" + exe + "' hook --context-source "
	want := map[string]string{
		"statusLine":         prefix + "statusLine --context-forward " + base64.RawStdEncoding.EncodeToString([]byte("user-gauge")),
		"subagentStatusLine": prefix + "subagentStatusLine",
	}
	for key, command := range want {
		got, present := top[key]
		if disabled {
			if present {
				t.Errorf("disable_context_fullness on: settings carry %s = %v; want no %s key", key, got, key)
			}
			continue
		}
		if fields, _ := got.(map[string]any); fields["type"] != "command" || fields["command"] != command {
			t.Errorf("disable_context_fullness off: %s = %v; want command %q", key, got, command)
		}
	}
	return top
}

// TestSynthesizeSettingsContextFullnessSwitch (b.kh7): with
// disable_context_fullness on, a Claude launch's settings carry neither status
// line key, so Claude's own status line is left alone, and are otherwise the
// settings it carries with the switch off.
func TestSynthesizeSettingsContextFullnessSwitch(t *testing.T) {
	const exe = "/opt/ad/bin/agent-director"
	withStubExe(t, exe)
	t.Setenv("HOME", t.TempDir())
	r := Resolved{SpawnParams: SpawnParams{CWD: t.TempDir(), ExtraEnv: claudeConfigWithStatusLine(t),
		Permissions: &Permissions{Allow: []string{"Bash(go test)"}}}}
	cfg := config.Default()
	cfg.Defaults.DisableAskUser = true
	off := assertContextStatuslines(t, synthSettings(t, r, cfg), exe, false)
	cfg.Defaults.DisableContextFullness = true
	on := assertContextStatuslines(t, synthSettings(t, r, cfg), exe, true)
	delete(off, "statusLine")
	delete(off, "subagentStatusLine")
	if !reflect.DeepEqual(on, off) {
		t.Errorf("settings with the switch on = %v; want the switch-off settings without the status lines, %v", on, off)
	}
}

// TestSynthesizeSettingsRelayTimeout pins SR-1.3 and b.8q2: both relay hooks
// carry the effective relay timeout (86400 for 0 or a negative value), whose
// milliseconds fit Claude Code's 32-bit hook timer, and the PermissionRequest
// hook's --timeout argument is the same value (b.146 rule 4); SessionStart
// keeps 600 s.
func TestSynthesizeSettingsRelayTimeout(t *testing.T) {
	const exe = "/opt/ad/bin/agent-director"
	withStubExe(t, exe)
	for _, tc := range []struct{ configured, want int }{
		{config.DefaultRelayTimeoutSeconds, 86400}, {3600, 3600}, {config.MaxRelayTimeoutSeconds, 2147483}, {0, 86400}, {-5, 86400},
	} {
		cfg := config.Default()
		cfg.Relay.TimeoutSeconds = tc.configured
		if got := cfg.Relay.EffectiveTimeoutSeconds(); got != tc.want || float64(got)*1000 > math.MaxInt32 {
			t.Errorf("timeout_seconds %d: effective %d; want %d, within a 32-bit millisecond timer", tc.configured, got, tc.want)
		}
		assertExecFormSettings(t, synthSettings(t, Resolved{}, cfg), exe, "", cfg)
	}
}

// TestSettingsSessionStartHookTimeoutIs600 pins the SessionStart hook timeout
// at the SRD-mandated 600 s (SR-22.9); other tests compare against the constant.
func TestSettingsSessionStartHookTimeoutIs600(t *testing.T) {
	if sessionStartHookTimeoutSeconds != 600 {
		t.Fatalf("sessionStartHookTimeoutSeconds = %d; want 600 (SR-22.9)", sessionStartHookTimeoutSeconds)
	}
}
