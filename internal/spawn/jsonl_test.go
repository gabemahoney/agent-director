package spawn_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
)

// TestJsonlPathSlugParity pins the slug rule byte-for-byte. Any
// future change here is expected to be intentional — the slug must
// match Claude Code's own on-disk layout exactly or resume's Stat
// pre-flight will miss the real JSONL.
func TestJsonlPathSlugParity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cases := []struct {
		name    string
		cwd     string
		session string
		want    string
	}{
		{
			name:    "simple path with slashes",
			cwd:     "/home/foo/projects/bar",
			session: "abc",
			want:    filepath.Join(home, ".claude", "projects", "-home-foo-projects-bar", "abc.jsonl"),
		},
		{
			name:    "underscores become dashes (parity with Claude Code, divergence from tmux sanitizer)",
			cwd:     "/home/foo/my_repo",
			session: "s",
			want:    filepath.Join(home, ".claude", "projects", "-home-foo-my-repo", "s.jsonl"),
		},
		{
			name:    "existing dashes survive verbatim",
			cwd:     "/srv/multi-word-path",
			session: "id",
			want:    filepath.Join(home, ".claude", "projects", "-srv-multi-word-path", "id.jsonl"),
		},
		{
			name:    "dots become dashes",
			cwd:     "/home/me/v1.2/site",
			session: "x",
			want:    filepath.Join(home, ".claude", "projects", "-home-me-v1-2-site", "x.jsonl"),
		},
		{
			name:    "spaces become dashes",
			cwd:     "/home/user/My Project",
			session: "y",
			want:    filepath.Join(home, ".claude", "projects", "-home-user-My-Project", "y.jsonl"),
		},
		{
			name:    "non-ASCII collapses to one dash per rune",
			cwd:     "/résumé/π",
			session: "z",
			// `/` `r` `é` `s` `u` `m` `é` `/` `π` →
			// `-` `r` `-` `s` `u` `m` `-` `-` `-` = "-r-sum---".
			// Each rune (single or multi-byte) yields exactly one dash.
			want: filepath.Join(home, ".claude", "projects", "-r-sum---", "z.jsonl"),
		},
		{
			name:    "root cwd",
			cwd:     "/",
			session: "id",
			want:    filepath.Join(home, ".claude", "projects", "-", "id.jsonl"),
		},
		{
			name:    "digits preserved",
			cwd:     "/var/tmp123",
			session: "id",
			want:    filepath.Join(home, ".claude", "projects", "-var-tmp123", "id.jsonl"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := spawn.JsonlPath(c.cwd, c.session)
			if err != nil {
				t.Fatalf("JsonlPath: %v", err)
			}
			if got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestJsonlPathEmptySessionRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := spawn.JsonlPath("/tmp", ""); err == nil {
		t.Fatalf("expected error for empty sessionID; got nil")
	}
}

func TestJsonlPathDoesNotTouchFilesystem(t *testing.T) {
	// The resolver is a pure path computation. JsonlPath on a path
	// that doesn't exist must still return the composed path, not an
	// error.
	t.Setenv("HOME", t.TempDir())
	got, err := spawn.JsonlPath("/nonexistent/totally/fake", "session-x")
	if err != nil {
		t.Fatalf("JsonlPath should be pure path comp; got %v", err)
	}
	if !strings.Contains(got, "session-x.jsonl") {
		t.Errorf("got %q; expected to contain session-x.jsonl", got)
	}
	// The composed path must NOT exist on disk (we never created it).
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Errorf("JsonlPath wrongly created a file at %q: %v", got, err)
	}
}

// TestSlugDivergenceFromTmuxSanitizer locks in the SRD §8.2 invariant
// that the JSONL slug intentionally differs from the tmux session-name
// sanitizer. SanitizeSessionName allows '_'; slugifyCwd (inside
// JsonlPath) replaces it with '-'. Any change that unifies them
// breaks resume, so this test exists to catch the accidental
// "tidy-up" merger.
func TestSlugDivergenceFromTmuxSanitizer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Tmux sanitizer on `my_repo` keeps the underscore.
	sanitized := spawn.SanitizeSessionName("my_repo")
	if sanitized != "my_repo" {
		t.Fatalf("SanitizeSessionName changed; this test's premise is broken: got %q", sanitized)
	}

	// JsonlPath slug on `/my_repo` replaces it with `-`.
	got, err := spawn.JsonlPath("/my_repo", "id")
	if err != nil {
		t.Fatalf("JsonlPath: %v", err)
	}
	if !strings.Contains(got, "-my-repo") {
		t.Errorf("JSONL slug must replace _ with -; got %q", got)
	}
	if strings.Contains(got, "my_repo") {
		t.Errorf("JSONL slug must NOT contain _ (would break Claude Code parity); got %q", got)
	}
}
