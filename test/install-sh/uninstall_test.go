package installsh_test

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// uninstallExit runs uninstall.sh with args under home, under umask unless it is
// "", with input on its stdin ("" is end of input) and pathDirs first on its
// PATH, and returns its output and exit code.
func uninstallExit(t *testing.T, home, umask, input string, args []string, pathDirs ...string) ([]byte, int) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director", "uninstall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{"-c", `[[ -z "$1" ]] || umask "$1" || exit; exec bash "$0" "${@:2}"`, script, umask}, args...)...)
	cmd.Env = []string{"HOME=" + home, "PATH=" + strings.Join(slices.Concat(pathDirs, []string{os.Getenv("PATH")}), ":")}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out, exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("uninstall.sh: %v\n%s", err, out)
	}
	return out, 0
}

// runUninstall is uninstallExit for a run that must exit 0; it returns the output.
func runUninstall(t *testing.T, home, umask string, pathDirs ...string) []byte {
	t.Helper()
	out, code := uninstallExit(t, home, umask, "", nil, pathDirs...)
	if code != 0 {
		t.Fatalf("uninstall.sh: exit %d\n%s", code, out)
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

// TestUninstallKeepsFileModes (b.ojn, b.nw5): uninstall.sh's edits of
// settings.json and config.toml, and their .bak copies, keep each file's mode
// under the umask, also over an earlier run's leftovers and through symlinks,
// which stay links with their targets edited; each is owner-only until its
// chmod, and no cp -p is needed (NFS homes).
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
		linkedWithLeftovers      bool // both files symlinks into dotfiles/, config.toml's absolute, settings.json's relative; 0666 junk at each .new and .bak name
	}{
		{"private settings, group-readable config", "022", 0o600, 0o640, false},
		{"read-only, symlinked files over leftovers", "000", 0o400, 0o400, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfg := filepath.Join(home, ".agent-director", "config.toml")
			settings := filepath.Join(home, ".claude", "settings.json")
			hook := filepath.Join(home, ".agent-director", "bin", "agent-director") + " help"
			files := []struct {
				path, before string
				mode         os.FileMode
				link         string // what path links to in the linked case
			}{
				{cfg, "[defaults]\nrelay_mode = \"off\"\ninject_help_hook = true\n", tc.configMode, filepath.Join(home, "dotfiles", "config.toml")},
				{settings, `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + hook + `"}]}]}}` + "\n", tc.settingsMode,
					filepath.Join("..", "dotfiles", "settings.json")},
			}
			// edited is the file uninstall.sh must edit: the link's target, or path itself.
			edited := func(path string) string {
				if tc.linkedWithLeftovers {
					return filepath.Join(home, "dotfiles", filepath.Base(path))
				}
				return path
			}
			for _, f := range files {
				writeMode(t, edited(f.path), f.before, f.mode)
				if !tc.linkedWithLeftovers {
					continue
				}
				if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.link, f.path); err != nil {
					t.Fatal(err)
				}
				// An earlier run's leftovers: its .new beside the link's target, its .bak beside the link.
				for _, p := range []string{edited(f.path) + ".new", f.path + ".bak." + stamp} {
					writeMode(t, p, "junk\n", 0o666)
				}
			}

			writeMode(t, chmodLog, "", 0o600)

			runUninstall(t, home, tc.umask, fakes)

			var wantChmods []string
			for _, f := range files {
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
				for _, p := range []string{f.path + ".new", edited(f.path) + ".new"} {
					if _, err := os.Lstat(p); !os.IsNotExist(err) {
						t.Errorf("%s left: %v", p, err)
					}
				}
				if tc.linkedWithLeftovers {
					if got, err := os.Readlink(f.path); err != nil || got != f.link {
						t.Errorf("%s link = %q, %v; want kept, naming %q", f.path, got, err, f.link)
					}
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
			if got, err := os.ReadFile(edited(cfg)); err != nil || string(got) != "[defaults]\nrelay_mode = \"off\"\n" {
				t.Errorf("%s after uninstall.sh = %q, %v; want the key cleared", edited(cfg), got, err)
			}
			if got, err := os.ReadFile(edited(settings)); err != nil || strings.Contains(string(got), hook) || !strings.Contains(string(got), "dark") {
				t.Errorf("%s after uninstall.sh = %q, %v; want the hook gone and the theme kept", edited(settings), got, err)
			}
		})
	}
}

// TestUninstallLeavesSettingsWithoutOneDocument (b.zbg): a settings.json holding
// no JSON document is left alone silently, one holding several, or one that is
// not valid JSON, with a note, and the uninstall still exits 0 and removes the
// binary.
func TestUninstallLeavesSettingsWithoutOneDocument(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	cases := []struct {
		name, settings string
		note           string // the one output line naming settings.json ("" none)
	}{
		{"empty", "", ""},
		{"only whitespace", " \n\t\n", ""},
		{"two documents", "{} {}\n", "uninstall.sh: ~/.claude/settings.json holds 2 JSON documents, not one; leaving it alone"},
		{"not valid JSON", "{\n", "uninstall.sh: ~/.claude/settings.json is not valid JSON; leaving it alone"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".agent-director", "bin", "agent-director")
			claude := filepath.Join(home, ".claude")
			writeMode(t, bin, "binary", 0o755)
			writeMode(t, filepath.Join(claude, "settings.json"), tc.settings, 0o600)
			before := treeSnap(t, claude)

			out := runUninstall(t, home, "")

			var named, want []string
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "settings.json") {
					named = append(named, line)
				}
			}
			if tc.note != "" {
				want = []string{tc.note}
			}
			if !slices.Equal(named, want) {
				t.Errorf("output lines naming settings.json = %q; want %q:\n%s", named, want, out)
			}
			if after := treeSnap(t, claude); !maps.Equal(after, before) {
				t.Errorf("uninstall.sh changed %s:\nbefore %v\nafter  %v", claude, before, after)
			}
			if _, err := os.Lstat(bin); !os.IsNotExist(err) {
				t.Errorf("%s after uninstall.sh: %v; want it removed", bin, err)
			}
		})
	}
}

// treeSnap maps each path under root to its mode, mtime, and contents or link target.
func treeSnap(t *testing.T, root string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		var body []byte
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			var target string
			target, err = os.Readlink(p)
			body = []byte("-> " + target)
		case fi.Mode().IsRegular():
			body, err = os.ReadFile(p)
		}
		snap[p] = fmt.Sprintf("%v %d %s", fi.Mode(), fi.ModTime().UnixNano(), body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// TestUninstallRefusesUnwritableLink (b.nw5): a settings.json holding its hooks,
// or a config.toml holding inject_help_hook, linked into a directory it cannot
// write in, makes uninstall.sh refuse (exit 2) and change nothing. Each way out
// the refusal gives, followed, lets the re-run finish: fixing the link, or
// removing the entry where the file comes from, after which the file is left alone.
func TestUninstallRefusesUnwritableLink(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write in the 0555 directory that stands in for a read-only one")
	}
	cli2TrackInputs(t)
	// {bin} is the installed agent-director.
	files := []struct {
		base, dir     string // the file, and its directory under HOME
		with, without string // its contents with and without uninstall.sh's entry
		entry, kept   string // in the contents with: the entry, and text that stays
		what, remedy  string // the refusal's "cannot <what>", and what it says to remove
	}{
		{"settings.json", ".claude",
			`{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"{bin} help"}]}]}}` + "\n",
			`{"theme":"dark","hooks":{"SessionStart":[]}}` + "\n",
			"{bin} help", "dark",
			"remove agent-director's hook entries from",
			"each SessionStart and SessionEnd entry holding a hook whose command starts with {bin}."},
		{"config.toml", ".agent-director",
			"[defaults]\nrelay_mode = \"off\"\ninject_help_hook = true\n",
			"[defaults]\nrelay_mode = \"off\"\n",
			"inject_help_hook", "relay_mode",
			"clear inject_help_hook from",
			"inject_help_hook from [defaults], and the [defaults] header too when only blank lines and comments are left under it."},
	}
	for _, f := range files {
		for _, follow := range []string{"fix the link", "remove the entry"} {
			t.Run(f.base+", "+follow, func(t *testing.T) {
				home := t.TempDir()
				bin := filepath.Join(home, ".agent-director", "bin", "agent-director")
				fill := strings.NewReplacer("{bin}", bin).Replace
				store := filepath.Join(home, "store")
				link, target := filepath.Join(home, f.dir, f.base), filepath.Join(store, f.base)
				writeMode(t, bin, "binary", 0o755)
				// Both files hold uninstall.sh's entry: f's linked into store/, the other plain.
				for _, g := range files {
					if g.base != f.base {
						writeMode(t, filepath.Join(home, g.dir, g.base), fill(g.with), 0o600)
					}
				}
				writeMode(t, target, fill(f.with), 0o600)
				if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(store, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
				before := treeSnap(t, home)

				out, code := uninstallExit(t, home, "", "", nil)

				want := "uninstall.sh: cannot " + f.what + " " + link + " through its symlink; refusing to uninstall." +
					" link : " + link + " target : " + target +
					" The target's directory, " + store + ", cannot be written in (a read-only file system, say, such as home-manager's /nix/store)." +
					" uninstall.sh writes its edits into the file a symlinked settings.json or config.toml resolves to, keeping the link." +
					" Fix the link so it reaches a file in a directory you can write in, or, where the file comes from" +
					" (your dotfiles or home-manager configuration, say), remove: " + fill(f.remedy) +
					" Nothing was removed or changed. Re-run this uninstall after the change."
				if got := strings.Join(strings.Fields(string(out)), " "); code != 2 || got != want {
					t.Fatalf("uninstall.sh exit %d, output:\n%s\nwant exit 2 and, spaces aside:\n%s", code, out, want)
				}
				if after := treeSnap(t, home); !maps.Equal(after, before) {
					t.Errorf("the refusal changed %s:\nbefore %v\nafter  %v", home, before, after)
				}

				switch follow {
				case "fix the link": // to a copy in a directory uninstall.sh can write in
					fixed := filepath.Join(home, "dotfiles", f.base)
					writeMode(t, fixed, fill(f.with), 0o600)
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(fixed, link); err != nil {
						t.Fatal(err)
					}
					target = fixed
				case "remove the entry": // as the read-only store is rebuilt from its source
					if err := os.Chmod(store, 0o755); err != nil {
						t.Fatal(err)
					}
					writeMode(t, target, f.without, 0o600)
					if err := os.Chmod(store, 0o555); err != nil {
						t.Fatal(err)
					}
				}
				out = runUninstall(t, home, "")

				if got, err := os.Readlink(link); err != nil || got != target {
					t.Errorf("%s link = %q, %v; want kept, naming %q", link, got, err, target)
				}
				got, err := os.ReadFile(target)
				if err != nil || strings.Contains(string(got), fill(f.entry)) || !strings.Contains(string(got), f.kept) {
					t.Errorf("%s after the re-run = %q, %v; want %q gone and %q kept", target, got, err, fill(f.entry), f.kept)
				}
				if follow == "remove the entry" && string(got) != f.without {
					t.Errorf("%s after the re-run = %q; want it left alone, %q", target, got, f.without)
				}
				note := "uninstall.sh: left " + link + " alone: it holds no agent-director hook entries, and its symlink cannot be written through"
				if wantNote := follow == "remove the entry" && f.base == "settings.json"; slices.Contains(strings.Split(string(out), "\n"), note) != wantNote {
					t.Errorf("re-run output has the line %q = %v; want %v:\n%s", note, !wantNote, wantNote, out)
				}
				if _, err := os.Lstat(bin); !os.IsNotExist(err) {
					t.Errorf("%s after the re-run: %v; want it removed", bin, err)
				}
			})
		}
	}
}

// TestUninstallPurge (b.nw5): --purge is confirmed after settings.json's link
// check and before config.toml's and any change. A confirmed purge ("y" at the
// prompt, or --force, which skips it) leaves an unwritable config.toml link's
// target alone, with a note, and edits a writable one's; a declined purge is a
// plain uninstall; no answer changes nothing. The unwritable link's row confirms
// with "y", not --force, so the link refusal is shown waived by the purge itself.
func TestUninstallPurge(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	const config = "[defaults]\nrelay_mode = \"off\"\ninject_help_hook = true\n"
	const cleared = "[defaults]\nrelay_mode = \"off\"\n"
	// In has: {config} and {settings} are the two files' paths, {target} the file
	// the linked one names.
	cases := []struct {
		name         string
		linked, into string // the file that is a link ("" none), and the directory under HOME holding its target: store (0555) or dotfiles
		input        string // the answer to the prompt ("" is end of input)
		force        bool
		code         int
		prompted     bool
		purged       bool   // the purge was confirmed, so ~/.agent-director is removed (checked only where configAfter is set)
		has, last    string // text the output holds, and its last line ("" either unchecked)
		configAfter  string // the file config.toml names, after; "" wants HOME unchanged
	}{
		{"unwritable config.toml link, answered y", "config.toml", "store", "y\n", false, 0, true, true,
			"uninstall.sh: left {config} alone: its symlink cannot be written through, and --purge removes the link; its target, {target}, keeps inject_help_hook",
			"uninstall.sh: done", config},
		{"unwritable config.toml link, answered n", "config.toml", "store", "n\n", false, 2, true, false,
			"uninstall.sh: cannot clear inject_help_hook from {config} through its symlink; refusing to uninstall.", "", ""},
		{"plain config.toml, answered n", "", "", "n\n", false, 0, true, false, "", "uninstall.sh: --purge aborted", cleared},
		{"writable config.toml link, --force", "config.toml", "dotfiles", "", true, 0, false, true, "", "uninstall.sh: done", cleared},
		{"unwritable settings.json link, answer y unasked", "settings.json", "store", "y\n", false, 2, false, false,
			"uninstall.sh: cannot remove agent-director's hook entries from {settings} through its symlink; refusing to uninstall.", "", ""},
		{"no answer", "", "", "", false, 1, true, false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.into == "store" && os.Geteuid() == 0 {
				t.Skip("root can write in the 0555 directory that stands in for a read-only one")
			}
			home := t.TempDir()
			root := filepath.Join(home, ".agent-director")
			bin := filepath.Join(root, "bin", "agent-director")
			cfg, settings := filepath.Join(root, "config.toml"), filepath.Join(home, ".claude", "settings.json")
			writeMode(t, bin, "binary", 0o755)
			target := ""
			for path, text := range map[string]string{
				cfg:      config,
				settings: `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + bin + ` help"}]}]}}` + "\n",
			} {
				if filepath.Base(path) != tc.linked {
					writeMode(t, path, text, 0o600)
					continue
				}
				target = filepath.Join(home, tc.into, tc.linked)
				writeMode(t, target, text, 0o600)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if tc.into == "store" {
				store := filepath.Join(home, "store")
				if err := os.Chmod(store, 0o555); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
			}
			fill := strings.NewReplacer("{config}", cfg, "{settings}", settings, "{target}", target).Replace
			before := treeSnap(t, home)
			args := []string{"--purge"}
			if tc.force {
				args = append(args, "--force")
			}

			raw, code := uninstallExit(t, home, "", tc.input, args)

			out := string(raw)
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if code != tc.code || !strings.Contains(out, fill(tc.has)) || (tc.last != "" && lines[len(lines)-1] != fill(tc.last)) {
				t.Fatalf("uninstall.sh exit %d, output:\n%s\nwant exit %d, holding %q, last line %q", code, out, tc.code, fill(tc.has), fill(tc.last))
			}
			if got := strings.Contains(out, "proceed? [y/N]"); got != tc.prompted {
				t.Errorf("output has the --purge prompt = %v; want %v:\n%s", got, tc.prompted, out)
			}
			if tc.configAfter == "" {
				if after := treeSnap(t, home); !maps.Equal(after, before) {
					t.Errorf("uninstall.sh changed %s:\nbefore %v\nafter  %v", home, before, after)
				}
				return
			}
			named := cfg
			if tc.linked == "config.toml" {
				named = target
			}
			if got, err := os.ReadFile(named); err != nil || string(got) != tc.configAfter {
				t.Errorf("%s after uninstall.sh = %q, %v; want %q", named, got, err, tc.configAfter)
			}
			if _, err := os.Lstat(root); os.IsNotExist(err) != tc.purged {
				t.Errorf("%s removed = %v; want %v (%v)", root, os.IsNotExist(err), tc.purged, err)
			}
			if got, err := os.ReadFile(settings); err != nil || strings.Contains(string(got), bin) || !strings.Contains(string(got), "dark") {
				t.Errorf("%s after uninstall.sh = %q, %v; want the hook gone and the theme kept", settings, got, err)
			}
		})
	}
}
