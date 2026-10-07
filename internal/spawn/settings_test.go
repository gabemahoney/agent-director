package spawn

import (
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

// settingsShape is the part of the synthesized settings the permission tests read.
type settingsShape struct {
	Hooks       map[string]any `json:"hooks"`
	Permissions map[string]any `json:"permissions"`
}

// synthSettings synthesizes r's settings under cfg, failing the test on an error.
func synthSettings(t *testing.T, r Resolved, cfg config.Config) string {
	t.Helper()
	r.ClaudeInstanceID = "id"
	got, err := synthesizeSettings(r, cfg)
	if err != nil {
		t.Fatalf("synthesizeSettings: %v", err)
	}
	return got
}

// TestSynthesizeSettingsPermissions: the per-spawn overlay is written as given,
// disable_ask_user_question adds AskUserQuestion to deny first, and with
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
			cfg.Defaults.DisableAskUserQuestion = tc.disableAUQ
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

// TestSynthesizeSettingsRelayTimeout pins SR-1.3 and b.8q2: both relay hooks
// carry the effective relay timeout (86400 for 0 or a negative value), whose
// milliseconds fit Claude Code's 32-bit hook timer; SessionStart keeps 600 s.
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
