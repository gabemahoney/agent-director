package main_test

import (
	"encoding/json"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	admincmd "github.com/gabemahoney/agent-director/cmd/agent-director-admin"
	"github.com/gabemahoney/agent-director/internal/clisetup"
)

// b.51h: every single-value admin flag must fail before its handler runs.
// Use the real builders, omitting only the two documented repeatable flags.
func TestB51hAdminRepeatedFlag(t *testing.T) {
	for _, fs := range admincmd.FlagSetsForTest() {
		fs.VisitAll(func(f *flag.Flag) {
			if f.Name == "legacy-root" || fs.Name() == "delete" && f.Name == "instance-id" {
				return
			}
			isBool := false
			if b, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
				isBool = b.IsBoolFlag()
			}
			forms := []string{"separate", "equals", "mixed"}
			if isBool {
				forms = []string{"bare", "equals", "mixed"}
			}
			for _, form := range forms {
				t.Run(fs.Name()+"/"+f.Name+"/"+form, func(t *testing.T) {
					home, base := t.TempDir(), t.TempDir()
					values := map[string]string{
						"instance-id": "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa",
						"backup-dir":  filepath.Join(base, "backup"), "directory": filepath.Join(base, "snapshot"),
						"busy-timeout-ms": "1", "binary": filepath.Join(base, "candidate"), "version": "0.0.0",
					}
					value := func(name string) string {
						if v, ok := values[name]; ok {
							return v
						}
						return "true" // migrate-journal's booleans
					}
					args := []string{fs.Name()}
					if fs.Name() == "snapshot-store" {
						args = append([]string{"--store-path", filepath.Join(base, "source.db")}, args...)
					}
					fs.VisitAll(func(other *flag.Flag) {
						if other.Name != f.Name && other.Name != "legacy-root" {
							args = append(args, "--"+other.Name+"="+value(other.Name))
						}
					})
					v := value(f.Name)
					switch form {
					case "separate":
						args = append(args, "--"+f.Name, v, "--"+f.Name, v)
					case "equals":
						args = append(args, "--"+f.Name+"="+v, "--"+f.Name+"="+v)
					case "bare":
						args = append(args, "--"+f.Name, "-"+f.Name)
					case "mixed":
						args = append(args, "--"+f.Name+"="+v, "-"+f.Name)
						if !isBool {
							args = append(args, v)
						}
					}
					stdout, stderr, code := runAdmin(t, home, args...)
					env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
					if want := "flag --" + f.Name + " may only be specified once"; env.ErrDescription != want {
						t.Errorf("description=%q; want %q", env.ErrDescription, want)
					}
					if _, err := os.Lstat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
						t.Errorf("refused invocation touched HOME config: %v", err)
					}
					if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
						t.Errorf("refused invocation touched source/backup/install paths: %v (%v)", entries, err)
					}
					assertInvocationKinds(t, home)
				})
			}
		})
	}
}

// b.51h: globals share the refusal rule at both admin parsing stages.
func TestB51hAdminRepeatedGlobalFlag(t *testing.T) {
	for _, name := range clisetup.GlobalFlagNames() {
		for _, stage := range []string{"prefix", "verb", "across"} {
			t.Run(strings.TrimPrefix(name, "--")+"/"+stage, func(t *testing.T) {
				home, base := t.TempDir(), t.TempDir()
				a, b := filepath.Join(base, "first"), filepath.Join(base, "second")
				if name == "--create-if-missing" {
					a, b = "false", "true"
				}
				var args []string
				switch stage {
				case "prefix":
					args = []string{name, a, name + "=" + b, "version"}
				case "verb":
					args = []string{"version", name, a, name + "=" + b}
				case "across":
					args = []string{name, a, "version", name + "=" + b}
				}
				stdout, stderr, code := runAdmin(t, home, args...)
				env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
				if want := "flag " + name + " may only be specified once"; env.ErrDescription != want {
					t.Errorf("description=%q; want %q", env.ErrDescription, want)
				}
				if entries, err := os.ReadDir(home); err != nil || len(entries) != 1 || entries[0].Name() != "tmux-tmpdir" {
					t.Errorf("repeated global wrote under HOME: %v (%v)", entries, err)
				}
				if entries, err := os.ReadDir(base); err != nil || len(entries) != 0 {
					t.Errorf("repeated global touched override paths: %v (%v)", entries, err)
				}
				assertInvocationKinds(t, home)
			})
		}
	}
}

// b.51h: repeatable flags retain both values, including validation of each root.
func TestB51hAdminRepeatableControls(t *testing.T) {
	t.Run("delete", func(t *testing.T) {
		home := t.TempDir()
		stdout, stderr, code := runAdmin(t, home, "delete", "--instance-id", "first", "--instance-id=second")
		if code != 0 || stderr != "" {
			t.Fatalf("exit=%d stderr=%q; want success", code, stderr)
		}
		var res struct {
			Results map[string]string `json:"results"`
		}
		if err := json.Unmarshal([]byte(stdout), &res); err != nil || !maps.Equal(res.Results, map[string]string{"first": "ErrSpawnNotFound", "second": "ErrSpawnNotFound"}) {
			t.Errorf("results=%q (%v); want both ids", stdout, err)
		}
	})
	for _, invalid := range []string{"first", "second"} {
		t.Run("legacy-root/"+invalid, func(t *testing.T) {
			home, base := t.TempDir(), t.TempDir()
			a, b := filepath.Join(base, "first"), filepath.Join(base, "second")
			if invalid == "first" {
				a = "relative"
			} else {
				b = "relative"
			}
			args := append(checkOnlyArgs(a, filepath.Join(base, "backup")), "--legacy-root="+b)
			stdout, stderr, code := runAdmin(t, home, args...)
			if reason, _ := journalRefusal(t, stdout, stderr, code); reason != "legacy_root_not_absolute" {
				t.Errorf("reason=%q; want legacy_root_not_absolute", reason)
			}
		})
	}
}
