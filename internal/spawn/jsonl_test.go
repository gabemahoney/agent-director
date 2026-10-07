package spawn_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
)

// TestJsonlPathSlugParity pins the slug rule byte for byte: it must match
// Claude Code's on-disk layout or resume's Stat pre-flight misses the JSONL.
// Every non-alphanumeric rune, '_' included, is one '-' (SRD §8.2: unlike
// SanitizeSessionName, which keeps '_'). JsonlPathIn (b.1ba) differs only in
// the config dir; neither touches the filesystem.
func TestJsonlPathSlugParity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct{ cwd, slug string }{
		{"/home/foo/projects/bar", "-home-foo-projects-bar"},
		{"/home/foo/my_repo", "-home-foo-my-repo"},
		{"/srv/multi-word-path", "-srv-multi-word-path"},
		{"/home/me/v1.2/site", "-home-me-v1-2-site"},
		{"/home/user/My Project", "-home-user-My-Project"},
		{"/résumé/π", "-r-sum---"},
		{"/", "-"},
		{"/var/tmp123", "-var-tmp123"},
	}
	for _, c := range cases {
		got, err := spawn.JsonlPath(c.cwd, "sid")
		if want := filepath.Join(home, ".claude", "projects", c.slug, "sid.jsonl"); err != nil || got != want {
			t.Errorf("JsonlPath(%q) = %q, %v; want %q", c.cwd, got, err, want)
		}
		if _, err := os.Stat(got); !os.IsNotExist(err) {
			t.Errorf("JsonlPath(%q) left something at %q: %v", c.cwd, got, err)
		}
		got, err = spawn.JsonlPathIn("/home/bot/.claude-infhub", c.cwd, "sid")
		if want := filepath.Join("/home/bot/.claude-infhub", "projects", c.slug, "sid.jsonl"); err != nil || got != want {
			t.Errorf("JsonlPathIn(%q) = %q, %v; want %q", c.cwd, got, err, want)
		}
	}
}

// TestJsonlPathRejectsEmptyArgs: an empty session id, or JsonlPathIn's empty
// config dir, is an error.
func TestJsonlPathRejectsEmptyArgs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := spawn.JsonlPath("/tmp", ""); err == nil {
		t.Error("JsonlPath with an empty session id: nil error")
	}
	if _, err := spawn.JsonlPathIn("/cfg", "/tmp", ""); err == nil {
		t.Error("JsonlPathIn with an empty session id: nil error")
	}
	if _, err := spawn.JsonlPathIn("", "/tmp", "sid"); err == nil {
		t.Error("JsonlPathIn with an empty config dir: nil error")
	}
}
