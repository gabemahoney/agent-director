package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/internal/config"
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
		AgentDirectorLabels: map[string]string{"project": "agent-director"},
		Permissions: &api.MakeTemplatePermissions{
			Allow: []string{"Bash(npm test)"},
		},
	})
	if err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	wantPath := filepath.Join(home, ".agent-director", "templates", "dev.toml")
	if res.Path != wantPath {
		t.Errorf("Path = %q; want %q", res.Path, wantPath)
	}

	// File exists with mode 0600 inside dir 0700.
	dirInfo, err := os.Stat(filepath.Join(home, ".agent-director", "templates"))
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
	if got.AgentDirectorLabels["project"] != "agent-director" {
		t.Errorf("Labels = %v", got.AgentDirectorLabels)
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
		AgentDirectorLabels: map[string]string{"env": "dev", "owner": "alice"},
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
	if got.AgentDirectorLabels["env"] != "dev" || got.AgentDirectorLabels["owner"] != "alice" {
		t.Errorf("Labels lost: %v", got.AgentDirectorLabels)
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
	dir := filepath.Join(home, ".agent-director", "templates")
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

// seedTemplateFile writes a valid TOML body for name to the templates
// dir and returns the absolute path. The body is intentionally distinct
// from anything the AC-1 / AC-3 test calls produce so a no-op write
// would be detectable. EnsureTemplatesDir handles dir creation.
func seedTemplateFile(t *testing.T, name string) string {
	t.Helper()
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("seedTemplateFile: EnsureTemplatesDir: %v", err)
	}
	path, err := config.TemplatePath(name)
	if err != nil {
		t.Fatalf("seedTemplateFile: TemplatePath: %v", err)
	}
	body := "cwd = \"/seed/dir\"\nrelay_mode = \"on\"\n\n[agent_director_labels]\n  origin = \"seed\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seedTemplateFile: WriteFile: %v", err)
	}
	return path
}

// assertNoOrphanTempfile fails the test if a sibling of name in the
// templates dir matches the ".<name>.toml.tmp.*" pattern that the
// atomic-rename write step uses for its in-flight tempfile.
func assertNoOrphanTempfile(t *testing.T, name string) {
	t.Helper()
	target, err := config.TemplatePath(name)
	if err != nil {
		t.Fatalf("assertNoOrphanTempfile: TemplatePath: %v", err)
	}
	dir := filepath.Dir(target)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("assertNoOrphanTempfile: ReadDir: %v", err)
	}
	prefix := "." + name + ".toml.tmp."
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) {
			t.Errorf("orphan tempfile remains in templates dir: %s", e.Name())
		}
	}
}

// envelopeJSONStrippingPath marshals res to JSON with the Path field
// zeroed so two envelopes can be byte-compared without their
// nondeterministic absolute paths fighting.
func envelopeJSONStrippingPath(t *testing.T, res api.MakeTemplateResult) []byte {
	t.Helper()
	res.Path = ""
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("envelopeJSONStrippingPath: Marshal: %v", err)
	}
	return b
}

func TestMakeTemplate_OverwriteTrue_ReplacesExisting(t *testing.T) {
	withTempHome(t)
	const name = "overw1"
	seedTemplateFile(t, name)

	params := api.MakeTemplateParams{
		Name:                "overw1",
		CWD:                 "/var/replaced",
		RelayMode:           "off",
		AgentDirectorLabels: map[string]string{"origin": "call"},
		Overwrite:           true,
	}
	if _, err := api.MakeTemplate(params); err != nil {
		t.Fatalf("MakeTemplate Overwrite=true: %v", err)
	}

	target, err := config.TemplatePath(name)
	if err != nil {
		t.Fatalf("TemplatePath: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read replaced template: %v", err)
	}
	var got config.TemplateFile
	if _, err := toml.Decode(string(body), &got); err != nil {
		t.Fatalf("toml.Decode: %v", err)
	}
	if got.CWD != "/var/replaced" {
		t.Errorf("CWD = %q; want /var/replaced (seed value leaked through)", got.CWD)
	}
	if got.RelayMode != "off" {
		t.Errorf("RelayMode = %q; want off (seed value leaked through)", got.RelayMode)
	}
	if got.AgentDirectorLabels["origin"] != "call" {
		t.Errorf("Labels[origin] = %q; want call (seed value leaked through)", got.AgentDirectorLabels["origin"])
	}

	assertNoOrphanTempfile(t, name)
}

func TestMakeTemplate_OverwriteFalse_StillErrorsOnCollision(t *testing.T) {
	cases := []struct {
		label  string
		params api.MakeTemplateParams
	}{
		{
			label: "explicit false",
			params: api.MakeTemplateParams{
				Name:      "overw2",
				CWD:       "/var/call",
				RelayMode: "off",
				Overwrite: false,
			},
		},
		{
			label: "unset zero value",
			params: api.MakeTemplateParams{
				Name:      "overw2",
				CWD:       "/var/call",
				RelayMode: "off",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			withTempHome(t)
			seedPath := seedTemplateFile(t, tc.params.Name)
			seedBody, err := os.ReadFile(seedPath)
			if err != nil {
				t.Fatalf("read seed: %v", err)
			}

			_, err = api.MakeTemplate(tc.params)
			if !errors.Is(err, config.ErrTemplateExists) {
				t.Fatalf("err = %v; want ErrTemplateExists", err)
			}
			// SRD §10: the absolute target path is embedded in the
			// error description so callers can surface it.
			target, perr := config.TemplatePath(tc.params.Name)
			if perr != nil {
				t.Fatalf("TemplatePath: %v", perr)
			}
			if !strings.Contains(err.Error(), target) {
				t.Errorf("err description %q missing target path %q", err.Error(), target)
			}

			postBody, err := os.ReadFile(seedPath)
			if err != nil {
				t.Fatalf("read post-call: %v", err)
			}
			if !bytes.Equal(seedBody, postBody) {
				t.Errorf("seed file was modified on collision\nseed:\n%s\npost:\n%s",
					string(seedBody), string(postBody))
			}
		})
	}
}

func TestMakeTemplate_OverwriteTrue_CreatesWhenAbsent(t *testing.T) {
	var absentEnv, replaceEnv []byte

	t.Run("create when absent", func(t *testing.T) {
		withTempHome(t)
		params := api.MakeTemplateParams{
			Name:                "overw3",
			CWD:                 "/var/fresh",
			RelayMode:           "off",
			AgentDirectorLabels: map[string]string{"origin": "call"},
			Overwrite:           true,
		}
		res, err := api.MakeTemplate(params)
		if err != nil {
			t.Fatalf("MakeTemplate Overwrite=true (absent): %v", err)
		}
		if _, err := os.Stat(res.Path); err != nil {
			t.Fatalf("expected file at %q: %v", res.Path, err)
		}
		absentEnv = envelopeJSONStrippingPath(t, res)
	})

	t.Run("replace existing", func(t *testing.T) {
		withTempHome(t)
		const name = "overw3"
		seedTemplateFile(t, name)
		params := api.MakeTemplateParams{
			Name:                "overw3",
			CWD:                 "/var/fresh",
			RelayMode:           "off",
			AgentDirectorLabels: map[string]string{"origin": "call"},
			Overwrite:           true,
		}
		res, err := api.MakeTemplate(params)
		if err != nil {
			t.Fatalf("MakeTemplate Overwrite=true (replace): %v", err)
		}
		replaceEnv = envelopeJSONStrippingPath(t, res)
	})

	if !bytes.Equal(absentEnv, replaceEnv) {
		t.Errorf("envelopes differ after stripping .path\nabsent: %s\nreplace: %s",
			string(absentEnv), string(replaceEnv))
	}
}
