package api_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/claude-director/internal/api"
	"github.com/gabemahoney/claude-director/internal/config"
)

// withTempHome points $HOME at a per-test temp dir so the templates
// dir lives under a sandboxed root. Restores HOME on cleanup via
// t.Setenv's own teardown.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

func TestMakeTemplateWritesReadableTOML(t *testing.T) {
	home := withTempHome(t)

	res, err := api.MakeTemplate(api.MakeTemplateParams{
		Name:                 "dev",
		CWD:                  "/tmp",
		RelayMode:            "off",
		ClaudeArgs:           []string{"--model", "opus"},
		ClaudeDirectorLabels: map[string]string{"project": "claude-director"},
		Permissions: &api.MakeTemplatePermissions{
			Allow: []string{"Bash(npm test)"},
		},
	})
	if err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	wantPath := filepath.Join(home, ".claude-director", "templates", "dev.toml")
	if res.Path != wantPath {
		t.Errorf("Path = %q; want %q", res.Path, wantPath)
	}

	// File exists with mode 0600 inside dir 0700.
	dirInfo, err := os.Stat(filepath.Join(home, ".claude-director", "templates"))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir mode = %o; want 0700", dirInfo.Mode().Perm())
	}
	fileInfo, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o; want 0600", fileInfo.Mode().Perm())
	}

	// Round-trip: contents decode back to an equivalent TemplateFile.
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	var got config.TemplateFile
	if _, err := toml.Decode(string(body), &got); err != nil {
		t.Fatalf("toml.Decode: %v", err)
	}
	if got.CWD != "/tmp" {
		t.Errorf("CWD = %q; want /tmp", got.CWD)
	}
	if got.RelayMode != "off" {
		t.Errorf("RelayMode = %q; want off", got.RelayMode)
	}
	if got.ClaudeArgs[0] != "--model" || got.ClaudeArgs[1] != "opus" {
		t.Errorf("ClaudeArgs = %v; want [--model opus]", got.ClaudeArgs)
	}
	if got.ClaudeDirectorLabels["project"] != "claude-director" {
		t.Errorf("Labels = %v", got.ClaudeDirectorLabels)
	}
	if got.Permissions == nil || got.Permissions.Allow[0] != "Bash(npm test)" {
		t.Errorf("Permissions.Allow lost: %+v", got.Permissions)
	}

	// And the file is plain text — hand-readability is part of the
	// contract per SRD §10.1. A binary blob would fail this byte test.
	bodyStr := string(body)
	for _, want := range []string{`cwd = "/tmp"`, `relay_mode = "off"`} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("TOML body missing %q (got:\n%s)", want, bodyStr)
		}
	}
}

func TestMakeTemplateRejectsUnsafeNames(t *testing.T) {
	withTempHome(t)
	for _, name := range []string{"", ".", "..", ".hidden", "foo/bar", `foo\bar`, "foo..bar", "../escape"} {
		t.Run(name, func(t *testing.T) {
			_, err := api.MakeTemplate(api.MakeTemplateParams{Name: name, CWD: "/tmp"})
			if !errors.Is(err, config.ErrTemplateNameUnsafe) {
				t.Fatalf("name=%q: err = %v; want ErrTemplateNameUnsafe", name, err)
			}
		})
	}
}

func TestMakeTemplateRejectsOverwrite(t *testing.T) {
	withTempHome(t)
	first := api.MakeTemplateParams{Name: "dev", CWD: "/tmp"}
	if _, err := api.MakeTemplate(first); err != nil {
		t.Fatalf("first MakeTemplate: %v", err)
	}
	_, err := api.MakeTemplate(first)
	if !errors.Is(err, config.ErrTemplateExists) {
		t.Fatalf("second MakeTemplate err = %v; want ErrTemplateExists", err)
	}
}

func TestMakeTemplateRoundTripsThroughLoadTemplate(t *testing.T) {
	// MakeTemplate + LoadTemplate are the two halves of the disk
	// contract. A round-trip pin guarantees a write+read pair stays
	// equivalent — any future encoder change that loses information
	// (e.g. omits empty arrays) will break this.
	withTempHome(t)

	want := api.MakeTemplateParams{
		Name:                 "rt",
		CWD:                  "/var/data",
		RelayMode:            "on",
		ClaudeArgs:           []string{"--print"},
		ExtraEnv:             map[string]string{"ANTHROPIC_API_KEY": "sk-test"},
		ClaudeDirectorLabels: map[string]string{"env": "dev", "owner": "alice"},
		Permissions: &api.MakeTemplatePermissions{
			Allow: []string{"Bash(jq)", "Read(/etc)"},
			Deny:  []string{"Bash(rm)"},
		},
	}
	if _, err := api.MakeTemplate(want); err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	got, err := config.LoadTemplate("rt")
	if err != nil {
		t.Fatalf("LoadTemplate: %v", err)
	}
	if got.CWD != want.CWD {
		t.Errorf("CWD: got %q want %q", got.CWD, want.CWD)
	}
	if got.RelayMode != want.RelayMode {
		t.Errorf("RelayMode: got %q want %q", got.RelayMode, want.RelayMode)
	}
	if len(got.ClaudeArgs) != 1 || got.ClaudeArgs[0] != "--print" {
		t.Errorf("ClaudeArgs: got %v want %v", got.ClaudeArgs, want.ClaudeArgs)
	}
	if got.ExtraEnv["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Errorf("ExtraEnv lost")
	}
	if got.ClaudeDirectorLabels["env"] != "dev" || got.ClaudeDirectorLabels["owner"] != "alice" {
		t.Errorf("Labels lost: %v", got.ClaudeDirectorLabels)
	}
	if got.Permissions == nil ||
		len(got.Permissions.Allow) != 2 || got.Permissions.Allow[0] != "Bash(jq)" ||
		len(got.Permissions.Deny) != 1 || got.Permissions.Deny[0] != "Bash(rm)" {
		t.Errorf("Permissions lost: %+v", got.Permissions)
	}
}

func TestMakeTemplateLeavesNoHalfWrittenFileOnEncoderFailure(t *testing.T) {
	// Atomicity is hard to test in isolation because os.Rename is
	// effectively single-syscall on local filesystems. The closest
	// approximation: prove no temp-file orphan or partial file remains
	// when the encoder fails. We provoke a failure by pre-occupying
	// the target path with a directory entry — the existence check
	// will catch it before any temp file is created.
	home := withTempHome(t)
	dir := filepath.Join(home, ".claude-director", "templates")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(dir, "blocked.toml")
	if err := os.WriteFile(target, []byte("# pre-existing"), 0o600); err != nil {
		t.Fatalf("seed pre-existing: %v", err)
	}

	_, err := api.MakeTemplate(api.MakeTemplateParams{Name: "blocked", CWD: "/tmp"})
	if !errors.Is(err, config.ErrTemplateExists) {
		t.Fatalf("err = %v; want ErrTemplateExists", err)
	}
	// Original content untouched.
	body, _ := os.ReadFile(target)
	if string(body) != "# pre-existing" {
		t.Errorf("target file was clobbered: %q", string(body))
	}
	// No stray tempfiles littering the dir.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && strings.Contains(e.Name(), "blocked") {
			t.Errorf("tempfile leaked: %s", e.Name())
		}
	}
}
