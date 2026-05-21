package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
)

func TestValidateTemplateNameAccepts(t *testing.T) {
	for _, name := range []string{"dev", "prod-2", "a", "long_name_42"} {
		t.Run(name, func(t *testing.T) {
			if err := config.ValidateTemplateName(name); err != nil {
				t.Errorf("ValidateTemplateName(%q) = %v; want nil", name, err)
			}
		})
	}
}

func TestValidateTemplateNameRejects(t *testing.T) {
	cases := map[string]string{
		"empty":         "",
		"dot":           ".",
		"dotdot":        "..",
		"leading-dot":   ".hidden",
		"slash":         "foo/bar",
		"backslash":     `foo\bar`,
		"dotdot-substr": "foo..bar",
		"traversal":     "../escape",
	}
	for label, name := range cases {
		t.Run(label, func(t *testing.T) {
			err := config.ValidateTemplateName(name)
			if !errors.Is(err, config.ErrTemplateNameUnsafe) {
				t.Errorf("ValidateTemplateName(%q) = %v; want ErrTemplateNameUnsafe", name, err)
			}
		})
	}
}

func TestEnsureTemplatesDirIsIdempotent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	first, err := config.EnsureTemplatesDir()
	if err != nil {
		t.Fatalf("first EnsureTemplatesDir: %v", err)
	}
	info, err := os.Stat(first)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("mode = %o; want 0700", info.Mode().Perm())
	}

	second, err := config.EnsureTemplatesDir()
	if err != nil {
		t.Fatalf("second EnsureTemplatesDir: %v", err)
	}
	if first != second {
		t.Errorf("path differs across calls: %q vs %q", first, second)
	}
}

func TestLoadTemplateMissingFileIsNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	_, err := config.LoadTemplate("absent")
	if !errors.Is(err, config.ErrTemplateNotFound) {
		t.Fatalf("err = %v; want ErrTemplateNotFound", err)
	}
}

func TestLoadTemplateRejectsUnknownKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	// Hand-write a file with a stray top-level key.
	body := `cwd = "/tmp"
mystery_field = "wat"
`
	if err := os.WriteFile(
		filepath.Join(home, ".claude-director", "templates", "rogue.toml"),
		[]byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := config.LoadTemplate("rogue")
	if !errors.Is(err, config.ErrTemplateMalformed) {
		t.Fatalf("err = %v; want ErrTemplateMalformed", err)
	}
}

// TestLoadTemplateRejectsReservedTmuxSessionNameKey pins SR-5.1: a
// template MUST NOT be allowed to set tmux_session_name. The existing
// meta.Undecoded() walk already rejects the key (TemplateFile has no
// matching field), but this test explicitly names tmux_session_name so
// a future struct addition can't silently re-enable it.
func TestLoadTemplateRejectsReservedTmuxSessionNameKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	body := `cwd = "/tmp"
tmux_session_name = "rogue-name"
`
	if err := os.WriteFile(
		filepath.Join(home, ".claude-director", "templates", "reserved.toml"),
		[]byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := config.LoadTemplate("reserved")
	if !errors.Is(err, config.ErrTemplateMalformed) {
		t.Fatalf("err = %v; want ErrTemplateMalformed (tmux_session_name is reserved)", err)
	}
	if !strings.Contains(err.Error(), "tmux_session_name") {
		t.Errorf("err message must name the reserved key, got: %v", err)
	}
}

func TestLoadTemplateRejectsBadRelayMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	body := `relay_mode = "bogus"`
	if err := os.WriteFile(
		filepath.Join(home, ".claude-director", "templates", "bad.toml"),
		[]byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := config.LoadTemplate("bad")
	if !errors.Is(err, config.ErrTemplateMalformed) {
		t.Fatalf("err = %v; want ErrTemplateMalformed", err)
	}
}

func TestLoadTemplateRejectsUnsafeName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := config.LoadTemplate("../escape")
	if !errors.Is(err, config.ErrTemplateNameUnsafe) {
		t.Fatalf("err = %v; want ErrTemplateNameUnsafe", err)
	}
}

func TestLoadTemplateValidFileDecodes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	body := `cwd = "/tmp"
relay_mode = "off"
claude_args = ["--model", "opus"]

[labels]
project = "foo"

[permissions]
allow = ["Bash(jq)"]
`
	if err := os.WriteFile(
		filepath.Join(home, ".claude-director", "templates", "valid.toml"),
		[]byte(body), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
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
	if tf.ClaudeDirectorLabels["project"] != "foo" {
		t.Errorf("Labels: %v", tf.ClaudeDirectorLabels)
	}
	if tf.Permissions == nil || tf.Permissions.Allow[0] != "Bash(jq)" {
		t.Errorf("Permissions: %+v", tf.Permissions)
	}
}
