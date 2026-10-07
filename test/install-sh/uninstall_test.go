package installsh_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// runUninstall runs uninstall.sh under home, under umask unless it is "", with
// pathDirs first on its PATH, and returns its output.
func runUninstall(t *testing.T, home, umask string, pathDirs ...string) []byte {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director", "uninstall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `[[ -z "$1" ]] || umask "$1" || exit; exec bash "$0"`, script, umask)
	cmd.Env = []string{"HOME=" + home, "PATH=" + strings.Join(slices.Concat(pathDirs, []string{os.Getenv("PATH")}), ":")}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("uninstall.sh: %v\n%s", err, out)
	}
	return out
}

// TestUninstallRemovesAdminBinary (b.vqr): uninstall.sh removes
// agent-director-admin, its .prior and install tempfiles, and the admin/
// directory, which it leaves, with a note, only when it holds other files.
func TestUninstallRemovesAdminBinary(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	cases := []struct {
		name     string
		other    bool // a file of someone else's in admin/
		wantLine string
	}{
		{"admin dir removed", false, "uninstall.sh: removed {admin}"},
		{"admin dir holding another file kept", true, "uninstall.sh: removed agent-director-admin; left {admin}, which holds other files"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".agent-director")
			admin := filepath.Join(root, "admin")
			files := []string{"bin/agent-director", "bin/agent-director.prior", "admin/agent-director-admin",
				"admin/agent-director-admin.prior", "admin/agent-director-admin.tmp.123", "state.db"}
			if tc.other {
				files = append(files, "admin/notes.txt")
			}
			for _, f := range files {
				p := filepath.Join(root, f)
				if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(f), 0o755); err != nil {
					t.Fatal(err)
				}
			}

			out := runUninstall(t, home, "")

			want := strings.ReplaceAll(tc.wantLine, "{admin}", admin)
			if !slices.Contains(strings.Split(string(out), "\n"), want) {
				t.Errorf("output lacks the line %q:\n%s", want, out)
			}
			var left []string
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(root, p)
					left = append(left, rel)
				}
				return nil
			})
			wantLeft := []string{"state.db"}
			if tc.other {
				wantLeft = []string{"admin/notes.txt", "state.db"}
			}
			if !slices.Equal(left, wantLeft) {
				t.Errorf("files left = %q; want %q", left, wantLeft)
			}
			if _, err := os.Stat(admin); os.IsNotExist(err) == tc.other {
				t.Errorf("%s exists = %v; want %v", admin, !os.IsNotExist(err), tc.other)
			}
		})
	}
}

// TestUninstallClearsInjectHelpHookOnlyUnderDefaults (b.onv, b.hhk):
// uninstall.sh drops inject_help_hook in any letter case under a [defaults]
// header spelled with blanks, a comment or in any letter case, and keeps it
// under [defaults.x], [defaultsx] and [DEFAULTS.x], and keeps INJECT_HELP_HOOKS.
func TestUninstallClearsInjectHelpHookOnlyUnderDefaults(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	home := t.TempDir()
	cfg := filepath.Join(home, ".agent-director", "config.toml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o700); err != nil {
		t.Fatal(err)
	}
	near := "[defaults.x]\ninject_help_hook = true\n[defaultsx]\ninject_help_hook = true\n[DEFAULTS.x]\nINJECT_HELP_HOOK = true\n"
	config := near + "\t[ defaults ]  # mine\nrelay_mode = \"off\"\ninject_help_hook = true\n" +
		"[Defaults]\nINJECT_HELP_HOOKS = true\nInject_Help_Hook = true\n"
	if err := os.WriteFile(cfg, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	runUninstall(t, home, "")

	got, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := near + "\t[ defaults ]  # mine\nrelay_mode = \"off\"\n[Defaults]\nINJECT_HELP_HOOKS = true\n"; string(got) != want {
		t.Errorf("config.toml after uninstall.sh = %q; want %q", got, want)
	}
}

// writeMode writes text to path, creating its directory, and sets its mode.
func writeMode(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// TestUninstallKeepsFileModes (b.ojn): uninstall.sh's edits of settings.json
// and config.toml, and their .bak copies, keep each file's mode under the umask,
// through a symlinked settings.json and over an earlier run's leftovers; each is
// owner-only until its chmod, and no cp -p is needed (NFS homes).
func TestUninstallKeepsFileModes(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	// Stand-ins, each then running the real command: a date that names every
	// .bak copy with stamp, a chmod that logs "<name> <mode>" of each .new or .bak
	// file before changing it, and a cp that fails on -p or -a, as on an NFS home.
	const stamp = "20260101-000000"
	fakes := t.TempDir()
	chmodLog := filepath.Join(fakes, "chmod.log")
	for name, body := range map[string]string{
		"date":  `[[ "$*" == '+%Y%m%d-%H%M%S' ]] && exec echo ` + stamp,
		"chmod": `t="${!#}"; case "$t" in *.new|*.bak.*) echo "${t##*/} $(stat -c %a "$t")" >>'` + chmodLog + `' ;; esac`,
		"cp":    `for a; do [[ "$a" =~ ^(-[^-]*[ap]|--preserve|--archive) ]] && { echo "cp: b.ojn stand-in cannot preserve attributes here" >&2; exit 1; }; done`,
	} {
		realPath, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		writeMode(t, filepath.Join(fakes, name), "#!/bin/bash\n"+body+"\nexec '"+realPath+"' \"$@\"\n", 0o755)
	}

	cases := []struct {
		name                     string
		umask                    string
		settingsMode, configMode os.FileMode
		linkedWithLeftovers      bool // settings.json a symlink; 0666 junk at both .new and .bak names
	}{
		{"private settings, group-readable config", "022", 0o600, 0o640, false},
		{"read-only, symlinked settings.json over leftovers", "000", 0o400, 0o400, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfg := filepath.Join(home, ".agent-director", "config.toml")
			settings := filepath.Join(home, ".claude", "settings.json")
			hook := filepath.Join(home, ".agent-director", "bin", "agent-director") + " help"
			cfgText := "[defaults]\nrelay_mode = \"off\"\ninject_help_hook = true\n"
			settingsText := `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + hook + `"}]}]}}` + "\n"
			writeMode(t, cfg, cfgText, tc.configMode)
			if tc.linkedWithLeftovers {
				target := filepath.Join(home, "dotfiles", "settings.json")
				writeMode(t, target, settingsText, tc.settingsMode)
				if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, settings); err != nil {
					t.Fatal(err)
				}
				for _, f := range []string{cfg + ".new", cfg + ".bak." + stamp, settings + ".new", settings + ".bak." + stamp} {
					writeMode(t, f, "junk\n", 0o666)
				}
			} else {
				writeMode(t, settings, settingsText, tc.settingsMode)
			}

			writeMode(t, chmodLog, "", 0o600)

			runUninstall(t, home, tc.umask, fakes)

			var wantChmods []string
			for _, f := range []struct {
				path, before string
				mode         os.FileMode
			}{{cfg, cfgText, tc.configMode}, {settings, settingsText, tc.settingsMode}} {
				base := filepath.Base(f.path)
				wantChmods = append(wantChmods, base+".new 600", fmt.Sprintf("%s.bak.%s %o", base, stamp, f.mode&0o700))
				for _, p := range []string{f.path, f.path + ".bak." + stamp} {
					if fi, err := os.Stat(p); err != nil {
						t.Error(err)
					} else if got := fi.Mode().Perm(); got != f.mode {
						t.Errorf("%s mode = %v; want %v", p, got, f.mode)
					}
				}
				if got, err := os.ReadFile(f.path + ".bak." + stamp); err != nil || string(got) != f.before {
					t.Errorf("%s.bak.%s = %q, %v; want %q", f.path, stamp, got, err, f.before)
				}
				if _, err := os.Lstat(f.path + ".new"); !os.IsNotExist(err) {
					t.Errorf("%s.new left: %v", f.path, err)
				}
			}
			logged, err := os.ReadFile(chmodLog)
			if err != nil {
				t.Fatal(err)
			}
			gotChmods := strings.Split(strings.TrimSpace(string(logged)), "\n")
			slices.Sort(gotChmods)
			slices.Sort(wantChmods)
			if !slices.Equal(gotChmods, wantChmods) {
				t.Errorf("modes before chmod = %q; want %q (each .new 600, each .bak its original's owner bits)", gotChmods, wantChmods)
			}
			if got, err := os.ReadFile(cfg); err != nil || string(got) != "[defaults]\nrelay_mode = \"off\"\n" {
				t.Errorf("config.toml after uninstall.sh = %q, %v; want the key cleared", got, err)
			}
			if got, err := os.ReadFile(settings); err != nil || strings.Contains(string(got), hook) || !strings.Contains(string(got), "dark") {
				t.Errorf("settings.json after uninstall.sh = %q, %v; want the hook gone and the theme kept", got, err)
			}
		})
	}
}
