package api

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/dbpathfix"
)

// loadAs reads content as the file the loaders read for kind: config.Load for
// a config, config.LoadTemplate for a template stored under HOME. It returns
// the values the file gives, or whether the loader refused it. HOME is a
// temp dir (t.Setenv by the caller).
func loadAs(t *testing.T, kind dbpathfix.Kind, content string) (got dbpathfix.Want) {
	t.Helper()
	home := os.Getenv("HOME")
	switch kind {
	case dbpathfix.KindConfig:
		path := filepath.Join(home, "config-under-test.toml")
		writeRenameFile(t, path, content)
		cfg, err := config.Load(path)
		if err != nil {
			return dbpathfix.Want{LoaderRefuses: true}
		}
		return dbpathfix.Want{AskUser: cfg.Defaults.DisableAskUser}
	default:
		path := filepath.Join(home, ".config", "agent-director", "templates", "t.toml")
		writeRenameFile(t, path, content)
		tf, err := config.LoadTemplate("t")
		if err != nil {
			return dbpathfix.Want{LoaderRefuses: true}
		}
		return dbpathfix.Want{Agent: tf.Agent, Args: tf.AgentArgs, Labels: tf.AgentDirectorLabels}
	}
}

func writeRenameFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sameWant reports whether the loaders' reading got is the case's want. A
// loader's nil and empty slice or map are the same reading.
func sameWant(got, want dbpathfix.Want) bool {
	return got.LoaderRefuses == want.LoaderRefuses && got.AskUser == want.AskUser && got.Agent == want.Agent &&
		slices.Equal(got.Args, want.Args) && maps.Equal(got.Labels, want.Labels)
}

// TestInstallShAgentRenameMatchesLoaders is the drift guard for install.sh's
// agent-rename migration block (SR-11.2, SR-14.4), beside the db_path guards:
// for every case of the dbpathfix matrix the extracted block writes exactly
// the bytes the case names, or leaves the file, or refuses it with the
// message the case names; and config.Load and config.LoadTemplate read the
// migrated file with the values the case names (so the block's output is what
// the new loaders accept, agent = "claude" added to a template naming none
// included), and the file as it was only when the case says they refuse it.
func TestInstallShAgentRenameMatchesLoaders(t *testing.T) {
	// Serial: it sets HOME with t.Setenv.
	r := dbpathfix.NewMigrationReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	for i, c := range dbpathfix.RenameCases {
		t.Run(c.Name, func(t *testing.T) {
			file := filepath.Join(dir, fmt.Sprintf("%03d.toml", i))
			if !c.Missing {
				writeRenameFile(t, file, c.In)
			}
			res := r.Migrate(t, c.Kind, file)
			if !c.Missing {
				if data, err := os.ReadFile(file); err != nil || string(data) != c.In {
					t.Errorf("the migration changed the file itself: now %q, err %v", data, err)
				}
			}
			switch c.Outcome {
			case dbpathfix.Migrated:
				if res.Code != 0 || res.Stdout != c.Out || res.Stderr != "" {
					t.Fatalf("status %d, stderr %q, stdout %q; want status 0, no stderr, stdout %q", res.Code, res.Stderr, res.Stdout, c.Out)
				}
				if got := loadAs(t, c.Kind, c.Out); !sameWant(got, c.Want) {
					t.Errorf("loader reads the migrated file as %+v; want %+v", got, c.Want)
				}
				if c.InRefused && !loadAs(t, c.Kind, c.In).LoaderRefuses {
					t.Errorf("loader accepts the file as it was; the case expects it refused, so the migration is what makes it load")
				}
				again := filepath.Join(dir, fmt.Sprintf("%03d-again.toml", i))
				writeRenameFile(t, again, res.Stdout)
				if res := r.Migrate(t, c.Kind, again); res.Code != 3 || res.Stdout != "" {
					t.Errorf("migrating the migrated file: status %d, stdout %q; want status 3 and nothing on stdout", res.Code, res.Stdout)
				}
			case dbpathfix.Unchanged:
				if res.Code != 3 || res.Stdout != "" || res.Stderr != "" {
					t.Fatalf("status %d, stderr %q, stdout %q; want status 3 and nothing on either", res.Code, res.Stderr, res.Stdout)
				}
				if c.Missing {
					return
				}
				if got := loadAs(t, c.Kind, c.In); !sameWant(got, c.Want) {
					t.Errorf("loader reads the file as %+v; want %+v", got, c.Want)
				}
			case dbpathfix.Refused:
				if res.Code != 2 || res.Stdout != "" {
					t.Fatalf("status %d, stdout %q, stderr %q; want status 2 and nothing on stdout", res.Code, res.Stdout, res.Stderr)
				}
				lines := strings.Split(strings.TrimSuffix(res.Stderr, "\n"), "\n")
				if want := strings.ReplaceAll(c.Headline, "%s", file); lines[0] != want {
					t.Errorf("first stderr line %q; want %q", lines[0], want)
				}
				if n := len(lines); n < 2 || !slices.Equal(lines[n-2:], dbpathfix.RefusalTail) {
					t.Errorf("stderr does not end with the refusal tail:\n%s", res.Stderr)
				}
				for _, show := range c.Show {
					if !slices.Contains(lines, show) {
						t.Errorf("stderr has no line %q:\n%s", show, res.Stderr)
					}
				}
			}
		})
	}
}

// TestInstallShAgentRenameErrNames: the err_name install.sh reports for a
// refused file is the catalogued name Go reports for a refused file of that
// kind. config's sentinel is config.ErrTemplateMalformed; a config's is
// internal/clisetup's, which pkg/api's tests cannot import (an import cycle),
// so its name is written here.
func TestInstallShAgentRenameErrNames(t *testing.T) {
	t.Parallel()
	r := dbpathfix.NewMigrationReader(t, filepath.Join("..", "..", "skills", "install-agent-director", "install.sh"))
	for kind, want := range map[dbpathfix.Kind]string{
		dbpathfix.KindConfig:   "ErrConfigMalformed",
		dbpathfix.KindTemplate: config.ErrTemplateMalformed.Error(),
	} {
		if got := r.MigrationErrName(t, kind); got != want {
			t.Errorf("err_name for a refused %s = %q; want %q", kind, got, want)
		}
	}
}
