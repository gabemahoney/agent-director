package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

// TestValidateTemplateName accepts plain names and refuses empty, dot,
// separator and traversal names with ErrTemplateNameUnsafe.
func TestValidateTemplateName(t *testing.T) {
	for _, name := range []string{"dev", "prod-2", "a", "long_name_42"} {
		if err := config.ValidateTemplateName(name); err != nil {
			t.Errorf("ValidateTemplateName(%q) = %v; want nil", name, err)
		}
	}
	for _, name := range []string{"", ".", "..", ".hidden", "foo/bar", `foo\bar`, "foo..bar", "../escape"} {
		if err := config.ValidateTemplateName(name); !errors.Is(err, config.ErrTemplateNameUnsafe) {
			t.Errorf("ValidateTemplateName(%q) = %v; want ErrTemplateNameUnsafe", name, err)
		}
	}
}

func TestEnsureTemplatesDirIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first, err := config.EnsureTemplatesDir()
	if err != nil {
		t.Fatalf("first EnsureTemplatesDir: %v", err)
	}
	if info, err := os.Stat(first); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("stat %s: %v, %v; want mode 0700", first, info, err)
	}
	second, err := config.EnsureTemplatesDir()
	if err != nil || first != second {
		t.Errorf("second EnsureTemplatesDir = %q, %v; want %q", second, err, first)
	}
}

// TestLoadTemplate: a missing file is ErrTemplateNotFound and an unsafe name
// ErrTemplateNameUnsafe; an unknown key, a bad relay_mode, or a per-call key
// (SR-5.1, SR-10.1: named, so a future TemplateFile field cannot silently
// re-enable it) is ErrTemplateMalformed.
func TestLoadTemplate(t *testing.T) {
	cases := []struct {
		name, body string // body "" writes no file
		want       error
		inErr      string
	}{
		{"absent", "", config.ErrTemplateNotFound, ""},
		{"../escape", "", config.ErrTemplateNameUnsafe, ""},
		{"rogue", "cwd = \"/tmp\"\nmystery_field = \"wat\"\n", config.ErrTemplateMalformed, ""},
		{"bad", `relay_mode = "bogus"`, config.ErrTemplateMalformed, ""},
		{"session", "cwd = \"/tmp\"\ntmux_session_name = \"rogue-name\"\n", config.ErrTemplateMalformed, "tmux_session_name"},
		{"reuse", "cwd = \"/tmp\"\nreuse_finished = true\n", config.ErrTemplateMalformed, "reuse_finished"},
		{"reuse-dashed", "cwd = \"/tmp\"\nreuse-finished = true\n", config.ErrTemplateMalformed, "reuse-finished"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := templatesDir(t)
			if tc.body != "" {
				writeTemplate(t, dir, tc.name, tc.body)
			}
			_, err := config.LoadTemplate(tc.name)
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.inErr) {
				t.Fatalf("err = %v; want %v naming %q", err, tc.want, tc.inErr)
			}
		})
	}
}

func TestLoadTemplateValidFileDecodes(t *testing.T) {
	writeTemplate(t, templatesDir(t), "valid", `cwd = "/tmp"
relay_mode = "off"
claude_args = ["--model", "opus"]

[labels]
project = "foo"

[permissions]
allow = ["Bash(jq)"]
`)
	tf, err := config.LoadTemplate("valid")
	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}
	if tf.CWD != "/tmp" || tf.RelayMode != "off" {
		t.Errorf("scalars: %+v", tf)
	}
	if len(tf.ClaudeArgs) != 2 || tf.ClaudeArgs[0] != "--model" {
		t.Errorf("ClaudeArgs: %v", tf.ClaudeArgs)
	}
	if tf.AgentDirectorLabels["project"] != "foo" {
		t.Errorf("Labels: %v", tf.AgentDirectorLabels)
	}
	if tf.Permissions == nil || tf.Permissions.Allow[0] != "Bash(jq)" {
		t.Errorf("Permissions: %+v", tf.Permissions)
	}
}

// templatesDir points HOME at a temp dir and returns its templates dir.
func templatesDir(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir, err := config.EnsureTemplatesDir()
	if err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	return dir
}

// writeTemplate writes body as <dir>/<name>.toml.
func writeTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
}
