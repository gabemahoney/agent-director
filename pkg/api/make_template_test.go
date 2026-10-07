package api_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// withTempHome points $HOME at a per-test temp dir (t.Setenv), so the
// templates dir lives under a sandboxed root.
func withTempHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// seedTemplateBody is a valid template no test call writes, so a no-op write shows.
const seedTemplateBody = "cwd = \"/seed/dir\"\nrelay_mode = \"on\"\n\n[labels]\n  origin = \"seed\"\n"

// seedTemplateFile writes seedTemplateBody as name's template and returns its path.
func seedTemplateFile(t *testing.T, name string) string {
	t.Helper()
	if _, err := config.EnsureTemplatesDir(); err != nil {
		t.Fatalf("EnsureTemplatesDir: %v", err)
	}
	path, err := config.TemplatePath(name)
	if err != nil {
		t.Fatalf("TemplatePath: %v", err)
	}
	if err := os.WriteFile(path, []byte(seedTemplateBody), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// assertNoOrphanTempfile fails if the templates dir holds an atomic-rename
// temp file ".<name>.toml.tmp.*".
func assertNoOrphanTempfile(t *testing.T, name string) {
	t.Helper()
	target, err := config.TemplatePath(name)
	if err != nil {
		t.Fatalf("TemplatePath: %v", err)
	}
	orphans, err := filepath.Glob(filepath.Join(filepath.Dir(target), "."+name+".toml.tmp.*"))
	if err != nil || len(orphans) != 0 {
		t.Errorf("temp files %v (%v); want none left", orphans, err)
	}
}

// TestMakeTemplateWritesReadableTOML pins SRD §10.1: the template lands at
// ~/.agent-director/templates/<name>.toml, mode 0600 in a 0700 dir, as plain
// TOML that config.LoadTemplate reads back field for field.
func TestMakeTemplateWritesReadableTOML(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	home := withTempHome(t)
	p := api.MakeTemplateParams{Name: "dev", CWD: "/var/data", RelayMode: "on", ClaudeArgs: []string{"--model", "opus"},
		ExtraEnv: map[string]string{"ANTHROPIC_API_KEY": "sk-test"}, AgentDirectorLabels: map[string]string{"env": "dev", "owner": "alice"},
		Permissions: &api.MakeTemplatePermissions{Allow: []string{"Bash(jq)", "Read(/etc)"}, Deny: []string{"Bash(rm)"}}}
	res, err := api.MakeTemplate(p)
	if err != nil {
		t.Fatalf("MakeTemplate: %v", err)
	}
	dir := filepath.Join(home, ".agent-director", "templates")
	if res.Path != filepath.Join(dir, "dev.toml") {
		t.Errorf("Path = %q; want %q", res.Path, filepath.Join(dir, "dev.toml"))
	}
	for path, mode := range map[string]os.FileMode{dir: 0o700, res.Path: 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != mode {
			t.Errorf("%s: %v; want mode %o", path, err, mode)
		}
	}
	body, _ := os.ReadFile(res.Path)
	for _, want := range []string{`cwd = "/var/data"`, `relay_mode = "on"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("TOML body missing %q:\n%s", want, body)
		}
	}
	want := config.TemplateFile{CWD: p.CWD, RelayMode: p.RelayMode, ClaudeArgs: p.ClaudeArgs, ExtraEnv: p.ExtraEnv,
		AgentDirectorLabels: p.AgentDirectorLabels, Permissions: &config.TemplatePermissions{Allow: p.Permissions.Allow, Deny: p.Permissions.Deny}}
	if got, err := config.LoadTemplate("dev"); err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("LoadTemplate = %+v, %v; want %+v", got, err, want)
	}
}

// TestMakeTemplateRefusals: an unsafe name is ErrTemplateNameUnsafe; without
// Overwrite an existing name is ErrTemplateExists naming the target path
// (SRD §10), its file untouched and no temp file left.
func TestMakeTemplateRefusals(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	withTempHome(t)
	for _, name := range []string{"", ".", "..", ".hidden", "foo/bar", `foo\bar`, "foo..bar", "../escape"} {
		if _, err := api.MakeTemplate(api.MakeTemplateParams{Name: name, CWD: "/tmp"}); !errors.Is(err, config.ErrTemplateNameUnsafe) {
			t.Errorf("name %q: err = %v; want ErrTemplateNameUnsafe", name, err)
		}
	}
	target := seedTemplateFile(t, "taken")
	_, err := api.MakeTemplate(api.MakeTemplateParams{Name: "taken", CWD: "/var/call", RelayMode: "off"})
	if !errors.Is(err, config.ErrTemplateExists) || !strings.Contains(err.Error(), target) {
		t.Errorf("err = %v; want ErrTemplateExists naming %s", err, target)
	}
	if body, err := os.ReadFile(target); err != nil || string(body) != seedTemplateBody {
		t.Errorf("existing template = %q, %v; want it untouched", body, err)
	}
	assertNoOrphanTempfile(t, "taken")
}

// TestMakeTemplateOverwrite: Overwrite replaces an existing template with the
// call's body, or creates an absent one, leaving no temp file.
func TestMakeTemplateOverwrite(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	withTempHome(t)
	seedTemplateFile(t, "existing")
	for _, name := range []string{"existing", "absent"} {
		res, err := api.MakeTemplate(api.MakeTemplateParams{Name: name, CWD: "/var/replaced", RelayMode: "off",
			AgentDirectorLabels: map[string]string{"origin": "call"}, Overwrite: true})
		if err != nil {
			t.Fatalf("MakeTemplate(%s, Overwrite): %v", name, err)
		}
		got, err := config.LoadTemplate(name)
		if err != nil || got.CWD != "/var/replaced" || got.RelayMode != "off" || got.AgentDirectorLabels["origin"] != "call" {
			t.Errorf("%s at %s = %+v, %v; want the call's cwd, relay_mode and labels", name, res.Path, got, err)
		}
		assertNoOrphanTempfile(t, name)
	}
}

// TestMakeTemplate_OverwriteTrue_ConcurrentAtomicity pins SR-1.7: after N
// concurrent Overwrite writers of one name the file is exactly one writer's
// body, never empty, partial or spliced. N=4, 3 iterations, as the Docker
// testplan case overwrite-4.
func TestMakeTemplate_OverwriteTrue_ConcurrentAtomicity(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	withTempHome(t)
	const name = "concur"
	writers := []string{"writer-0", "writer-1", "writer-2", "writer-3"}
	target := seedTemplateFile(t, name)
	for iter := 0; iter < 3; iter++ {
		_ = os.Remove(target) // each iteration races to create it
		release := make(chan struct{})
		var wg sync.WaitGroup
		for _, w := range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-release
				if _, err := api.MakeTemplate(api.MakeTemplateParams{Name: name, CWD: "/tmp", RelayMode: "off",
					AgentDirectorLabels: map[string]string{"writer": w}, Overwrite: true}); err != nil {
					t.Errorf("iter %d %s: %v", iter, w, err)
				}
			}()
		}
		close(release)
		wg.Wait()
		got, err := config.LoadTemplate(name) // a torn or partial body fails the TOML decode
		if body, _ := os.ReadFile(target); err != nil || !slices.Contains(writers, got.AgentDirectorLabels["writer"]) {
			t.Fatalf("iter %d: template %q (%v); want exactly one writer's body", iter, body, err)
		}
	}
}
