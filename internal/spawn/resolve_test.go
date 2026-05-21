package spawn_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/spawn"
)

// withTemplate seeds a TOML template under $HOME/.claude-director/templates/
// for the duration of a test. Returns the temp HOME directory in case
// the caller wants to introspect on-disk state.
func withTemplate(t *testing.T, name, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude-director", "templates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	return home
}

func TestResolveWithoutTemplateIsPassthrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	p := spawn.SpawnParams{
		CWD:       "/tmp",
		RelayMode: "off",
	}
	r, err := spawn.Resolve(p, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.CWD != "/tmp" || r.RelayMode != "off" {
		t.Errorf("passthrough lost: %+v", r)
	}
}

func TestResolvePureTemplateUsesAllTemplateFields(t *testing.T) {
	withTemplate(t, "base", `
cwd = "/tmp"
relay_mode = "off"
claude_args = ["--model", "opus"]

[labels]
project = "foo"

[permissions]
allow = ["Bash(jq)"]
`)
	r, err := spawn.Resolve(spawn.SpawnParams{Template: "base"}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.CWD != "/tmp" {
		t.Errorf("CWD = %q; want /tmp", r.CWD)
	}
	if r.RelayMode != "off" {
		t.Errorf("RelayMode = %q; want off", r.RelayMode)
	}
	if len(r.ClaudeArgs) != 2 || r.ClaudeArgs[0] != "--model" {
		t.Errorf("ClaudeArgs = %v", r.ClaudeArgs)
	}
	if r.ClaudeDirectorLabels["project"] != "foo" {
		t.Errorf("Labels = %v", r.ClaudeDirectorLabels)
	}
	if r.Permissions == nil || r.Permissions.Allow[0] != "Bash(jq)" {
		t.Errorf("Permissions = %+v", r.Permissions)
	}
}

func TestResolveScalarPerCallOverridesTemplate(t *testing.T) {
	// Per-call CWD replaces template CWD; RelayMode falls through.
	withTemplate(t, "base", `
cwd = "/tmp"
relay_mode = "off"
`)
	r, err := spawn.Resolve(spawn.SpawnParams{
		Template: "base",
		CWD:      "/var/data",
	}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.CWD != "/var/data" {
		t.Errorf("CWD = %q; want /var/data (per-call override)", r.CWD)
	}
	if r.RelayMode != "off" {
		t.Errorf("RelayMode = %q; want off (template fallback)", r.RelayMode)
	}
}

func TestResolveLabelMapsMergeWithPerCallWinning(t *testing.T) {
	// Template has project=foo + env=dev. Per-call adds owner=alice and
	// overrides project=bar. Expected: bar / dev / alice.
	withTemplate(t, "base", `
[labels]
project = "foo"
env = "dev"
`)
	r, err := spawn.Resolve(spawn.SpawnParams{
		Template: "base",
		ClaudeDirectorLabels: map[string]string{
			"owner":   "alice",
			"project": "bar",
		},
	}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[string]string{"project": "bar", "env": "dev", "owner": "alice"}
	if !mapsEqual(r.ClaudeDirectorLabels, want) {
		t.Errorf("Labels = %v; want %v", r.ClaudeDirectorLabels, want)
	}
}

func TestResolveExtraEnvMergesWithPerCallWinning(t *testing.T) {
	withTemplate(t, "base", `
[extra_env]
ANTHROPIC_API_KEY = "from-template"
EXTRA = "kept"
`)
	r, err := spawn.Resolve(spawn.SpawnParams{
		Template: "base",
		ExtraEnv: map[string]string{
			"ANTHROPIC_API_KEY": "from-call",
			"NEW":               "added",
		},
	}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := map[string]string{
		"ANTHROPIC_API_KEY": "from-call",
		"EXTRA":             "kept",
		"NEW":               "added",
	}
	if !mapsEqual(r.ExtraEnv, want) {
		t.Errorf("ExtraEnv = %v; want %v", r.ExtraEnv, want)
	}
}

func TestResolvePermissionsArraysConcatenate(t *testing.T) {
	// Template has allow=[A], deny=[X]. Per-call adds allow=[B], ask=[Q].
	// Expected: allow=[A,B], deny=[X], ask=[Q].
	withTemplate(t, "base", `
[permissions]
allow = ["A"]
deny = ["X"]
`)
	r, err := spawn.Resolve(spawn.SpawnParams{
		Template: "base",
		Permissions: &spawn.Permissions{
			Allow: []string{"B"},
			Ask:   []string{"Q"},
		},
	}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := r.Permissions.Allow, []string{"A", "B"}; !slicesEqual(got, want) {
		t.Errorf("Allow = %v; want %v", got, want)
	}
	if got, want := r.Permissions.Deny, []string{"X"}; !slicesEqual(got, want) {
		t.Errorf("Deny = %v; want %v", got, want)
	}
	if got, want := r.Permissions.Ask, []string{"Q"}; !slicesEqual(got, want) {
		t.Errorf("Ask = %v; want %v", got, want)
	}
}

func TestResolvePermissionsNilPerCallKeepsTemplate(t *testing.T) {
	withTemplate(t, "base", `
[permissions]
allow = ["A", "B"]
`)
	r, err := spawn.Resolve(spawn.SpawnParams{Template: "base"}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Permissions == nil || len(r.Permissions.Allow) != 2 {
		t.Fatalf("Permissions lost: %+v", r.Permissions)
	}
}

func TestResolveClaudeArgsPerCallReplacesWholesale(t *testing.T) {
	// Per-call non-nil ClaudeArgs replaces — does NOT concat.
	withTemplate(t, "base", `
claude_args = ["--model", "opus"]
`)
	r, err := spawn.Resolve(spawn.SpawnParams{
		Template:   "base",
		ClaudeArgs: []string{"--model", "sonnet"},
	}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := r.ClaudeArgs, []string{"--model", "sonnet"}; !slicesEqual(got, want) {
		t.Errorf("ClaudeArgs = %v; want %v (per-call replace)", got, want)
	}
}

func TestResolveClaudeArgsNilFallsBackToTemplate(t *testing.T) {
	withTemplate(t, "base", `
claude_args = ["--model", "opus"]
`)
	r, err := spawn.Resolve(spawn.SpawnParams{Template: "base"}, config.Default())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := r.ClaudeArgs, []string{"--model", "opus"}; !slicesEqual(got, want) {
		t.Errorf("ClaudeArgs = %v; want %v (template fallback)", got, want)
	}
}


func TestResolveTemplateNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := spawn.Resolve(spawn.SpawnParams{Template: "nope"}, config.Default())
	if !errors.Is(err, config.ErrTemplateNotFound) {
		t.Fatalf("err = %v; want ErrTemplateNotFound", err)
	}
}

func TestResolveBaseTemplateNotMutatedByResolve(t *testing.T) {
	// Mutating the resolved struct's maps/slices must not corrupt the
	// template's cached state. Pin this by mutating result.Labels and
	// re-loading the template: a second Resolve should still see the
	// unmodified template values.
	withTemplate(t, "base", `
[labels]
project = "foo"
`)
	r1, err := spawn.Resolve(spawn.SpawnParams{Template: "base"}, config.Default())
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	r1.ClaudeDirectorLabels["project"] = "corrupted"

	r2, err := spawn.Resolve(spawn.SpawnParams{Template: "base"}, config.Default())
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if r2.ClaudeDirectorLabels["project"] != "foo" {
		t.Errorf("template state was mutated: %v", r2.ClaudeDirectorLabels)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

