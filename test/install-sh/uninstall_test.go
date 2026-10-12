package installsh_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
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
			root := filepath.Join(home, ".config", "agent-director")
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
	cfg := filepath.Join(home, ".config", "agent-director", "config.toml")
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

// symlink makes link, creating its directory, naming target.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// linkIntoReadOnlyDir writes text to home/store/<link's name>, links link to it,
// and makes store 0555 for the rest of the test; it returns the target.
func linkIntoReadOnlyDir(t *testing.T, home, link, text string) string {
	t.Helper()
	store := filepath.Join(home, "store")
	target := filepath.Join(store, filepath.Base(link))
	writeMode(t, target, text, 0o600)
	symlink(t, target, link)
	if err := os.Chmod(store, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(store, 0o755) })
	return target
}

// standIn writes dir/name, a bash script that runs body and then the real name
// on PATH with its arguments.
func standIn(t *testing.T, dir, name, body string) {
	t.Helper()
	realPath, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	writeMode(t, filepath.Join(dir, name), "#!/bin/bash\n"+body+"\nexec '"+realPath+"' \"$@\"\n", 0o755)
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
		standIn(t, fakes, name, body)
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
			cfg := filepath.Join(home, ".config", "agent-director", "config.toml")
			settings := filepath.Join(home, ".claude", "settings.json")
			hook := filepath.Join(home, ".config", "agent-director", "bin", "agent-director") + " help"
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
				symlink(t, f.link, f.path)
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
	checkSettingsLeftAlone(t, []settingsLeftAlone{
		{name: "empty"},
		{name: "only whitespace", settings: " \n\t\n"},
		{name: "two documents", settings: "{} {}\n", note: "uninstall.sh: ~/.claude/settings.json holds 2 JSON documents, not one; leaving it alone"},
		{name: "not valid JSON", settings: "{\n", note: "uninstall.sh: ~/.claude/settings.json is not valid JSON; leaving it alone"},
	})
}

// TestUninstallLeavesSettingsItCannotEdit (b.ak5): a settings.json of another
// shape (also behind an unwritable link, not refused), one jq fails on, or an
// unwritable link holding none of agent-director's entries among odd values is
// left alone, with a note, and the uninstall goes on.
func TestUninstallLeavesSettingsItCannotEdit(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	const (
		help      = `{"hooks":[{"type":"command","command":"{bin} help"}]}`
		shapeNote = "uninstall.sh: ~/.claude/settings.json is valid JSON, but not an object whose hooks hold event lists; leaving it alone"
		jqNote    = "jq: error: b.ak5 stand-in fails here\n" +
			"uninstall.sh: cannot remove agent-director's hook entries from ~/.claude/settings.json (jq's error is above); leaving it alone"
	)
	checkSettingsLeftAlone(t, []settingsLeftAlone{
		{name: "an array", settings: "[]\n", note: shapeNote},
		{name: "hooks a list", settings: `{"hooks":[]}`, note: shapeNote},
		{name: "object-valued SessionStart holding the help hook", settings: `{"hooks":{"SessionStart":{"x":` + help + `}}}`, note: shapeNote},
		{name: "string SessionEnd beside the help hook", settings: `{"hooks":{"SessionStart":[` + help + `],"SessionEnd":"x"}}`, note: shapeNote},
		{name: "jq fails on the filter", settings: `{"hooks":{"SessionStart":[` + help + `]}}`, jqFails: `*'--arg prefix'*`, note: jqNote},
		{name: "unwritable link, object-valued SessionStart holding the help hook", linked: true,
			settings: `{"hooks":{"SessionStart":{"x":` + help + `}}}`, note: shapeNote},
		{name: "unwritable link, odd values and no help hook", linked: true, settings: `{"hooks":{"SessionStart":["x",{"hooks":[{"command":5}]}]}}`,
			note: "uninstall.sh: left {settings} alone: it holds no agent-director hook entries, and its symlink cannot be written through"},
		{name: "unwritable link, jq fails on the count", linked: true, settings: `{"hooks":{"SessionStart":[` + help + `]}}`,
			jqFails: `*'--arg prefix'*'| length'*`, note: jqNote},
	})
}

// settingsLeftAlone is a settings.json uninstall.sh leaves alone; {bin} in
// settings is the installed binary.
type settingsLeftAlone struct {
	name, settings string
	linked         bool   // settings.json links into a 0555 directory
	jqFails        string // a bash pattern over jq's arguments on which a stand-in jq fails ("" no stand-in)
	note           string // the output lines naming settings.json or starting "jq: ", "\n"-joined; {settings} is its path
}

// checkSettingsLeftAlone runs uninstall.sh per case, checking it exits 0 with the
// case's note, changes neither settings.json nor its link's target, and removes the binary.
func checkSettingsLeftAlone(t *testing.T, cases []settingsLeftAlone) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.linked && os.Geteuid() == 0 {
				t.Skip("root can write in the 0555 directory that stands in for a read-only one")
			}
			home := t.TempDir()
			bin := filepath.Join(home, ".config", "agent-director", "bin", "agent-director")
			claude := filepath.Join(home, ".claude")
			settings := filepath.Join(claude, "settings.json")
			fill := strings.NewReplacer("{bin}", bin, "{settings}", settings).Replace
			writeMode(t, bin, "binary", 0o755)
			dirs := []string{claude}
			if tc.linked {
				dirs = append(dirs, filepath.Dir(linkIntoReadOnlyDir(t, home, settings, fill(tc.settings))))
			} else {
				writeMode(t, settings, fill(tc.settings), 0o600)
			}
			var pathDirs []string
			if tc.jqFails != "" {
				fakes := t.TempDir()
				standIn(t, fakes, "jq", "if [[ \"$*\" == "+tc.jqFails+" ]]; then\n"+
					"  echo 'jq: error: b.ak5 stand-in fails here' >&2\n  exit 5\nfi")
				pathDirs = []string{fakes}
			}
			snap := func() map[string]string {
				all := map[string]string{}
				for _, d := range dirs {
					maps.Copy(all, treeSnap(t, d))
				}
				return all
			}
			before := snap()

			out := runUninstall(t, home, "", pathDirs...)

			var named []string
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(line, "settings.json") || strings.HasPrefix(line, "jq: ") {
					named = append(named, line)
				}
			}
			if got, want := strings.Join(named, "\n"), fill(tc.note); got != want {
				t.Errorf("output lines naming settings.json or from jq:\n%s\nwant:\n%s\nfull output:\n%s", got, want, out)
			}
			if after := snap(); !maps.Equal(after, before) {
				t.Errorf("uninstall.sh changed %q:\nbefore %v\nafter  %v", dirs, before, after)
			}
			if _, err := os.Lstat(bin); !os.IsNotExist(err) {
				t.Errorf("%s after uninstall.sh: %v; want it removed", bin, err)
			}
		})
	}
}

// TestUninstallRemovesHookEntriesBesideOddValues (b.ak5): uninstall.sh removes its
// entries from event lists holding values of any shape, keeping the rest verbatim,
// fills in a null document's lists as install.sh does, and keeps a .bak of each.
func TestUninstallRemovesHookEntriesBesideOddValues(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	const (
		help = `{"hooks":[{"type":"command","command":"{bin} help"}]}`
		odd  = `"x",{"hooks":["x"]},{"hooks":[{"command":5}]},{"hooks":{"a":{"command":"{bin} help"}}},{"hooks":[{"type":"command","command":"/usr/bin/true"}]}`
	)
	cases := []struct{ name, settings, want string }{
		{"odd values beside the help hooks",
			`{"theme":"dark","hooks":{"SessionStart":[` + odd + `,` + help + `,{"hooks":["x",{"type":"command","command":"{bin} help"}]}],` +
				`"SessionEnd":[5,null,{"matcher":"compact","hooks":[{"type":"command","command":"{bin} help"}]},{"hooks":"y"}]}}`,
			`{"theme":"dark","hooks":{"SessionStart":[` + odd + `],"SessionEnd":[5,null,{"hooks":"y"}]}}`},
		{"null SessionEnd filled in", `{"theme":"dark","hooks":{"SessionStart":[` + help + `],"SessionEnd":null}}`,
			`{"theme":"dark","hooks":{"SessionStart":[],"SessionEnd":[]}}`},
		{"null document filled in", "null\n", `{"hooks":{"SessionStart":[],"SessionEnd":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".config", "agent-director", "bin", "agent-director")
			settings := filepath.Join(home, ".claude", "settings.json")
			fill := strings.NewReplacer("{bin}", bin).Replace
			writeMode(t, bin, "binary", 0o755)
			writeMode(t, settings, fill(tc.settings), 0o600)

			runUninstall(t, home, "")

			raw, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("settings.json after uninstall.sh: %v\n%s", err, raw)
			}
			if err := json.Unmarshal([]byte(fill(tc.want)), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("settings.json after uninstall.sh = %s; want %s", raw, fill(tc.want))
			}
			baks, err := filepath.Glob(settings + ".bak.*")
			if err != nil {
				t.Fatal(err)
			}
			if len(baks) != 1 {
				t.Fatalf("settings.json backups = %q; want one", baks)
			}
			if bak, err := os.ReadFile(baks[0]); err != nil || string(bak) != fill(tc.settings) {
				t.Errorf("%s = %q, %v; want the original, %q", baks[0], bak, err, fill(tc.settings))
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

// TestUninstallRefusesUnwritableLink (b.nw5, b.ssz): a settings.json holding its hooks,
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
			"each SessionStart and SessionEnd entry holding a hook whose command starts with {bin} or {oldbin}."},
		{"config.toml", ".config/agent-director",
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
				bin := filepath.Join(home, ".config", "agent-director", "bin", "agent-director")
				fill := strings.NewReplacer("{bin}", bin, "{oldbin}", filepath.Join(home, ".agent-director", "bin", "agent-director")).Replace
				link := filepath.Join(home, f.dir, f.base)
				writeMode(t, bin, "binary", 0o755)
				// Both files hold uninstall.sh's entry: f's linked into store/, the other plain.
				for _, g := range files {
					if g.base != f.base {
						writeMode(t, filepath.Join(home, g.dir, g.base), fill(g.with), 0o600)
					}
				}
				target := linkIntoReadOnlyDir(t, home, link, fill(f.with))
				store := filepath.Dir(target)
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
					target = filepath.Join(home, "dotfiles", f.base)
					writeMode(t, target, fill(f.with), 0o600)
					if err := os.Remove(link); err != nil {
						t.Fatal(err)
					}
					symlink(t, target, link)
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
		purged       bool   // the purge was confirmed, so ~/.config/agent-director is removed (checked only where configAfter is set)
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
			root := filepath.Join(home, ".config", "agent-director")
			bin := filepath.Join(root, "bin", "agent-director")
			cfg, settings := filepath.Join(root, "config.toml"), filepath.Join(home, ".claude", "settings.json")
			writeMode(t, bin, "binary", 0o755)
			target := ""
			for path, text := range map[string]string{
				cfg:      config,
				settings: `{"theme":"dark","hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"` + bin + ` help"}]}]}}` + "\n",
			} {
				switch {
				case filepath.Base(path) != tc.linked:
					writeMode(t, path, text, 0o600)
				case tc.into == "store":
					target = linkIntoReadOnlyDir(t, home, path, text)
				default:
					target = filepath.Join(home, tc.into, tc.linked)
					writeMode(t, target, text, 0o600)
					symlink(t, target, path)
				}
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

// uninstallSeedCodex seeds HOME/.codex and returns a check that it is unchanged.
func uninstallSeedCodex(t *testing.T, home string) func() {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	writeMode(t, filepath.Join(dir, "config.toml"), "model = \"x\"\n", 0o600)
	writeMode(t, filepath.Join(dir, "sessions", "a.jsonl"), "{}\n", 0o600)
	before := treeSnap(t, dir)
	return func() {
		t.Helper()
		if after := treeSnap(t, dir); !maps.Equal(after, before) {
			t.Errorf("uninstall.sh changed %s:\nbefore %v\nafter  %v", dir, before, after)
		}
	}
}

// uninstallLinesWith returns the lines of out that start with any of prefixes.
func uninstallLinesWith(out []byte, prefixes ...string) []string {
	var got []string
	for _, line := range strings.Split(string(out), "\n") {
		for _, p := range prefixes {
			if strings.HasPrefix(line, p) {
				got = append(got, line)
				break
			}
		}
	}
	return got
}

// TestUninstallNeverMovedLayout (SR-11.4, b.ssz): with only ~/.agent-director
// present, uninstall.sh works on it as on the new directory: it removes the
// binaries, the admin tool, the PATH link, its old-path help hooks and
// inject_help_hook, keeps the directory without --purge, and makes no new one.
func TestUninstallNeverMovedLayout(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	modes := []struct {
		name   string
		args   []string
		input  string
		purged bool
	}{
		{"no --purge", nil, "", false},
		{"--purge --force", []string{"--purge", "--force"}, "", true},
		{"--purge answered y", []string{"--purge"}, "y\n", true},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".agent-director")
			bin := filepath.Join(root, "bin", "agent-director")
			cfg := filepath.Join(root, "config.toml")
			settings := filepath.Join(home, ".claude", "settings.json")
			link := filepath.Join(home, ".local", "bin", "agent-director")
			const user = `{"hooks":[{"type":"command","command":"/usr/bin/true"}]}`
			ours := `{"hooks":[{"type":"command","command":"` + bin + ` help"}]}`
			for _, f := range []string{"bin/agent-director", "bin/agent-director.prior", "bin/agent-director.20250101",
				"admin/agent-director-admin", "admin/agent-director-admin.prior", "state.db", "templates/t.toml"} {
				writeMode(t, filepath.Join(root, f), f, 0o755)
			}
			writeMode(t, cfg, "[defaults]\nrelay_mode = \"off\"\ninject_help_hook = true\n", 0o600)
			writeMode(t, settings, `{"theme":"dark","hooks":{"SessionStart":[`+ours+`,`+user+`],"SessionEnd":[`+ours+`]}}`+"\n", 0o600)
			symlink(t, bin, link)
			codexUnchanged := uninstallSeedCodex(t, home)

			out, code := uninstallExit(t, home, "", m.input, m.args)

			if code != 0 {
				t.Fatalf("uninstall.sh: exit %d\n%s", code, out)
			}
			lines := strings.Split(string(out), "\n")
			wantLines := []string{
				"uninstall.sh: removed binaries under " + filepath.Join(root, "bin"),
				"uninstall.sh: removed " + filepath.Join(root, "admin"),
				"uninstall.sh: removed symlink " + link,
				"uninstall.sh: removed help hook entries from " + settings,
			}
			if m.purged {
				wantLines = append(wantLines, "uninstall.sh: purged "+root)
			}
			for _, want := range wantLines {
				if !slices.Contains(lines, want) {
					t.Errorf("output lacks the line %q:\n%s", want, out)
				}
			}
			if len(uninstallLinesWith(out, "uninstall.sh: cleared inject_help_hook from "+cfg+" (backup ")) != 1 {
				t.Errorf("output lacks the line clearing inject_help_hook from %s:\n%s", cfg, out)
			}
			if got := uninstallLinesWith(out, "uninstall.sh: kept "); len(got) != 0 {
				t.Errorf("output names directories it kept, with no other AD directory present: %q", got)
			}
			if _, err := os.Lstat(link); !os.IsNotExist(err) {
				t.Errorf("%s after uninstall.sh: %v; want it removed", link, err)
			}
			if _, err := os.Lstat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
				t.Errorf("%s after uninstall.sh: %v; want none made", filepath.Join(home, ".config"), err)
			}
			raw, err := os.ReadFile(settings)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("settings.json after uninstall.sh: %v\n%s", err, raw)
			}
			if err := json.Unmarshal([]byte(`{"theme":"dark","hooks":{"SessionStart":[`+user+`],"SessionEnd":[]}}`), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("settings.json after uninstall.sh = %s; want only the user's hook left", raw)
			}
			if m.purged {
				if _, err := os.Lstat(root); !os.IsNotExist(err) {
					t.Errorf("%s after a confirmed --purge: %v; want it removed", root, err)
				}
				if !strings.Contains(string(out), "uninstall.sh: --purge will rm -rf "+root+" — proceed? [y/N] ") != (m.input == "") {
					t.Errorf("--purge prompt wrong for input %q:\n%s", m.input, out)
				}
			} else {
				for f, text := range map[string]string{"state.db": "state.db", "templates/t.toml": "templates/t.toml",
					"config.toml": "[defaults]\nrelay_mode = \"off\"\n"} {
					if got, err := os.ReadFile(filepath.Join(root, f)); err != nil || strings.TrimSuffix(string(got), "\n") != strings.TrimSuffix(text, "\n") {
						t.Errorf("%s after uninstall.sh = %q, %v; want %q", f, got, err, text)
					}
				}
				var left []string
				for _, sub := range []string{"bin", "admin"} {
					_ = filepath.WalkDir(filepath.Join(root, sub), func(p string, d os.DirEntry, err error) error {
						if err == nil && !d.IsDir() {
							left = append(left, p)
						}
						return nil
					})
				}
				if len(left) != 0 {
					t.Errorf("files left under bin and admin: %q; want none", left)
				}
			}
			codexUnchanged()
		})
	}
}

// TestUninstallRemovesOldDirectoryHookEntries (SR-11.4, b.ssz): uninstall.sh
// removes the help hooks whose command starts with the old directory's
// bin/agent-director beside the new ones, keeping every other hook, with the
// .bak copy and the symlink rules as for the new ones: an old-path entry alone
// counts, so a link it cannot write through is refused, not left alone.
func TestUninstallRemovesOldDirectoryHookEntries(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	const (
		oldHelp   = `{"hooks":[{"type":"command","command":"{oldbin} help"}]}`
		newHelp   = `{"hooks":[{"type":"command","command":"{bin} help"}]}`
		user      = `{"hooks":[{"type":"command","command":"/usr/bin/true"}]}`
		lookalike = `{"hooks":[{"type":"command","command":"{home}/.agent-director-other/bin/agent-director help"}]}`
	)
	cases := []struct {
		name, settings, want string
		link                 string // "" a plain file, or "writable" or "unwritable": a link to a file in a directory uninstall.sh can or cannot write in
	}{
		{name: "new and old entries beside others",
			settings: `{"theme":"dark","hooks":{"SessionStart":[` + oldHelp + `,` + newHelp + `,` + user + `,` + lookalike + `],` +
				`"SessionEnd":[{"matcher":"x","hooks":[{"type":"command","command":"{oldbin} help"}]},` + newHelp + `]}}`,
			want: `{"theme":"dark","hooks":{"SessionStart":[` + user + `,` + lookalike + `],"SessionEnd":[]}}`},
		{name: "old entries only",
			settings: `{"hooks":{"SessionStart":[` + oldHelp + `],"SessionEnd":[` + oldHelp + `]}}`,
			want:     `{"hooks":{"SessionStart":[],"SessionEnd":[]}}`},
		{name: "old entry only, writable link", link: "writable",
			settings: `{"hooks":{"SessionStart":[` + oldHelp + `,` + user + `],"SessionEnd":[]}}`,
			want:     `{"hooks":{"SessionStart":[` + user + `],"SessionEnd":[]}}`},
		{name: "old entry only, unwritable link", link: "unwritable",
			settings: `{"hooks":{"SessionStart":[` + oldHelp + `],"SessionEnd":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.link == "unwritable" && os.Geteuid() == 0 {
				t.Skip("root can write in the 0555 directory that stands in for a read-only one")
			}
			home := t.TempDir()
			bin := filepath.Join(home, ".config", "agent-director", "bin", "agent-director")
			oldBin := filepath.Join(home, ".agent-director", "bin", "agent-director")
			settings := filepath.Join(home, ".claude", "settings.json")
			fill := strings.NewReplacer("{bin}", bin, "{oldbin}", oldBin, "{home}", home).Replace
			writeMode(t, bin, "binary", 0o755)
			target := settings
			switch tc.link {
			case "":
				writeMode(t, settings, fill(tc.settings), 0o600)
			case "writable":
				target = filepath.Join(home, "dotfiles", "settings.json")
				writeMode(t, target, fill(tc.settings), 0o600)
				symlink(t, target, settings)
			case "unwritable":
				target = linkIntoReadOnlyDir(t, home, settings, fill(tc.settings))
			}
			if tc.link == "unwritable" {
				before := treeSnap(t, home)

				out, code := uninstallExit(t, home, "", "", nil)

				want := "uninstall.sh: cannot remove agent-director's hook entries from " + settings + " through its symlink; refusing to uninstall."
				if got := strings.Join(strings.Fields(string(out)), " "); code != 2 || !strings.HasPrefix(got, want) ||
					!strings.Contains(got, "starts with "+bin+" or "+oldBin+".") {
					t.Errorf("uninstall.sh exit %d, output:\n%s\nwant exit 2, starting %q and naming both prefixes", code, out, want)
				}
				if after := treeSnap(t, home); !maps.Equal(after, before) {
					t.Errorf("the refusal changed %s:\nbefore %v\nafter  %v", home, before, after)
				}
				return
			}

			runUninstall(t, home, "")

			raw, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("settings.json after uninstall.sh: %v\n%s", err, raw)
			}
			if err := json.Unmarshal([]byte(fill(tc.want)), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("settings.json after uninstall.sh = %s; want %s", raw, fill(tc.want))
			}
			baks, err := filepath.Glob(settings + ".bak.*")
			if err != nil {
				t.Fatal(err)
			}
			if len(baks) != 1 {
				t.Fatalf("settings.json backups = %q; want one", baks)
			}
			if bak, err := os.ReadFile(baks[0]); err != nil || string(bak) != fill(tc.settings) {
				t.Errorf("%s = %q, %v; want the original, %q", baks[0], bak, err, fill(tc.settings))
			}
			if tc.link != "" {
				if got, err := os.Readlink(settings); err != nil || got != target {
					t.Errorf("%s link = %q, %v; want kept, naming %q", settings, got, err, target)
				}
			}
		})
	}
}

// uninstallOther is an AD directory beside the one uninstall.sh works on.
type uninstallOther struct {
	rel    string // under HOME
	kind   string // the text a purge names it with in parentheses; "" for an aside
	linkTo string // "" a directory holding state.db, else the link's target ({new} is the new directory)
}

// TestUninstallOtherADirectories (SR-11.4, b.ssz): without a confirmed --purge
// uninstall.sh keeps every other AD directory (each pre-move-*, after-upgrade-*
// and recreated-* one, an old directory beside the new one, a compatibility link)
// and names each but the link, with the delete-once-confirmed advice; a confirmed
// --purge, its prompt naming each, removes them (a link as a link, its target
// untouched) but leaves each *.aside-* directory, and names it either way. A
// seeded ~/.codex is never touched.
func TestUninstallOtherADirectories(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	const (
		preMove    = "the old directory an upgrade kept"
		afterUp    = "the directory the way back set aside"
		recreated  = "a recreated old directory the way back set aside"
		beside     = "an old directory recreated beside the new one"
		compat     = "a compatibility link to the new directory"
		stamp      = "20260101-000000"
		asideStamp = "20260104-000000"
	)
	worlds := []struct {
		name   string
		others []uninstallOther // in the order uninstall.sh names them: pre-move, after-upgrade, recreated, beside, compat, then asides
	}{
		{"every kind of directory and both asides", []uninstallOther{
			{".agent-director.pre-move-" + stamp, preMove, ""},
			{".agent-director.pre-move-" + stamp + "-2", preMove, ""},
			{".config/agent-director.after-upgrade-" + stamp, afterUp, ""},
			{".agent-director.recreated-" + stamp, recreated, ""},
			{".agent-director", beside, ""},
			{".agent-director.aside-" + asideStamp, "", ""},
			{".config/agent-director.aside-" + asideStamp, "", ""}}},
		{"a kept directory that is a link elsewhere", []uninstallOther{
			{".agent-director.pre-move-" + stamp, preMove, "elsewhere"}}},
		{"a relative compatibility link", []uninstallOther{{".agent-director", compat, ".config/agent-director"}}},
		{"an absolute compatibility link", []uninstallOther{{".agent-director", compat, "{new}"}}},
		{"no other directory", nil},
	}
	modes := []struct {
		name     string
		args     []string
		input    string
		purged   bool
		prompted bool
	}{
		{"no --purge", nil, "", false, false},
		{"--purge answered n", []string{"--purge"}, "n\n", false, true},
		{"--purge answered y", []string{"--purge"}, "y\n", true, true},
		{"--purge --force", []string{"--purge", "--force"}, "", true, false},
	}
	for _, w := range worlds {
		for _, m := range modes {
			t.Run(w.name+", "+m.name, func(t *testing.T) {
				home := t.TempDir()
				root := filepath.Join(home, ".config", "agent-director")
				writeMode(t, filepath.Join(root, "bin", "agent-director"), "binary", 0o755)
				writeMode(t, filepath.Join(root, "state.db"), "db", 0o600)
				writeMode(t, filepath.Join(home, "elsewhere", "state.db"), "elsewhere", 0o600)
				var extras, asides []uninstallOther
				for _, o := range w.others {
					p := filepath.Join(home, o.rel)
					if o.linkTo == "" {
						writeMode(t, filepath.Join(p, "state.db"), o.rel, 0o600)
					} else {
						symlink(t, strings.ReplaceAll(o.linkTo, "{new}", root), p)
					}
					if o.kind == "" {
						asides = append(asides, o)
					} else {
						extras = append(extras, o)
					}
				}
				codexUnchanged := uninstallSeedCodex(t, home)
				// What must stay as it is: each aside and the link's target always, each kept directory too unless purged.
				stays := []string{filepath.Join(home, "elsewhere")}
				for _, o := range asides {
					stays = append(stays, filepath.Join(home, o.rel))
				}
				if !m.purged {
					for _, o := range extras {
						stays = append(stays, filepath.Join(home, o.rel))
					}
				}
				snap := func() map[string]string {
					all := map[string]string{}
					for _, p := range stays {
						maps.Copy(all, treeSnap(t, p))
					}
					return all
				}
				before := snap()

				out, code := uninstallExit(t, home, "", m.input, m.args)

				if code != 0 {
					t.Fatalf("uninstall.sh: exit %d\n%s", code, out)
				}
				// uninstall.sh names the kept directories, then the asides, then what it purged.
				var wantNamed, wantPurged []string
				for _, o := range extras {
					p := filepath.Join(home, o.rel)
					switch {
					case m.purged:
						wantPurged = append(wantPurged, "uninstall.sh: purged "+p+" ("+o.kind+")")
					case o.kind != compat:
						wantNamed = append(wantNamed, "uninstall.sh: kept "+p+" ("+o.kind+"); delete it once the upgrade is confirmed, or run --purge")
					}
				}
				for _, o := range asides {
					wantNamed = append(wantNamed, "uninstall.sh: left "+filepath.Join(home, o.rel)+
						" (set aside to settle a conflict; it may hold data you did not choose, so --purge leaves it); delete it yourself once you are sure it is not needed")
				}
				if m.purged {
					wantNamed = append(wantNamed, "uninstall.sh: purged "+root)
					wantNamed = append(wantNamed, wantPurged...)
				}
				named := uninstallLinesWith(out, "uninstall.sh: kept ", "uninstall.sh: purged ", "uninstall.sh: left ")
				if !slices.Equal(named, wantNamed) {
					t.Errorf("lines naming directories:\n%s\nwant:\n%s\nfull output:\n%s", strings.Join(named, "\n"), strings.Join(wantNamed, "\n"), out)
				}

				prompt := "uninstall.sh: --purge will rm -rf " + root
				if len(extras) > 0 {
					prompt += ", and also remove these other AD directories (a link is removed as a link, never what it points to):"
					for i, o := range extras {
						prompt += " " + filepath.Join(home, o.rel) + " (" + o.kind + ")"
						if i < len(extras)-1 {
							prompt += ","
						}
					}
				}
				prompt += " — proceed? [y/N] "
				if got := strings.Contains(string(out), prompt); got != m.prompted {
					t.Errorf("output has the prompt %q = %v; want %v:\n%s", prompt, got, m.prompted, out)
				}
				if got := strings.Contains(string(out), "proceed? [y/N]"); got != m.prompted {
					t.Errorf("output has a --purge prompt = %v; want %v:\n%s", got, m.prompted, out)
				}
				if last := strings.Split(strings.TrimRight(string(out), "\n"), "\n"); m.input == "n\n" != (last[len(last)-1] == "uninstall.sh: --purge aborted") {
					t.Errorf("last output line = %q for input %q", last[len(last)-1], m.input)
				}

				if after := snap(); !maps.Equal(after, before) {
					t.Errorf("uninstall.sh changed what it keeps:\nbefore %v\nafter  %v", before, after)
				}
				for _, o := range extras {
					p := filepath.Join(home, o.rel)
					if _, err := os.Lstat(p); os.IsNotExist(err) != m.purged {
						t.Errorf("%s removed = %v; want %v (%v)", p, os.IsNotExist(err), m.purged, err)
					}
				}
				if _, err := os.Lstat(filepath.Join(root, "state.db")); os.IsNotExist(err) != m.purged {
					t.Errorf("%s removed = %v; want %v (%v)", root, os.IsNotExist(err), m.purged, err)
				}
				codexUnchanged()
			})
		}
	}
}

// uninstallMCPRun runs uninstall.sh under home with the stand-in claude (the
// copy in tools, first on PATH) and env, and returns its stdout, stderr and
// exit code separately.
func uninstallMCPRun(t *testing.T, home, tools, input string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "skills", "install-agent-director", "uninstall.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{script}, args...)...)
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + tools + ":" + os.Getenv("PATH")}, env...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	var ee *exec.ExitError
	if err := cmd.Run(); errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("uninstall.sh: %v", err)
	}
	return out.String(), errb.String(), code
}

// TestUninstallMCPAlso (b.fom): --mcp-also runs `claude mcp remove
// agent-director` and tells what came of it. A removal is "deregistered"; a name
// registered nowhere ("No MCP server named ...") is "nothing to deregister",
// not a failure; any other failure (the name in several scopes, which Claude
// Code refuses to remove without -s, or a write that failed) is reported with
// the command run, its exit status and stderr and the command to run by hand,
// every later step still runs (also --purge), and the exit status is 6.
func TestUninstallMCPAlso(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	const failed = "uninstall.sh: deregistering agent-director from MCP failed; nothing was removed from Claude Code."
	const doneFailed = "uninstall.sh: done, except the MCP deregistration failed (see above); exit 6"
	const byHand = "  Remove it by hand, in the scope that 'mcp get agent-director' shows (a name registered in several scopes needs -s):\n    claude mcp remove agent-director -s <scope>\n"
	rows := []struct {
		name     string
		args     []string
		input    string
		env      []string // CLAUDE_STANDIN_* settings
		code     int
		calls    []string // the claude calls (every one is an `mcp remove`, none with -s)
		stdout   []string
		noStdout []string
		stderr   []string // each must be in stderr; "" in the list is ignored
		purged   bool     // ~/.config/agent-director is gone
		lastErr  string   // stderr's last line ("" stderr empty)
	}{
		{name: "no --mcp-also runs no claude", args: nil, code: 0, calls: nil,
			noStdout: []string{"from MCP", "deregist"}},
		{name: "deregistered", args: []string{"--mcp-also"}, code: 0, calls: []string{"mcp remove agent-director"},
			stdout: []string{"uninstall.sh: deregistered agent-director from MCP\n", "uninstall.sh: done\n"}},
		{name: "none found is not a failure", args: []string{"--mcp-also"}, env: []string{"CLAUDE_STANDIN_REMOVE_NONE=1"}, code: 0,
			calls:    []string{"mcp remove agent-director"},
			stdout:   []string{"uninstall.sh: no agent-director registration found from this directory; nothing to deregister\n", "uninstall.sh: done\n"},
			noStdout: []string{"deregistered agent-director"}},
		{name: "the name in several scopes", args: []string{"--mcp-also"}, env: []string{"CLAUDE_STANDIN_REMOVE_MULTI=1"}, code: 6,
			calls:    []string{"mcp remove agent-director"},
			stdout:   []string{"uninstall.sh: removed binaries under "},
			noStdout: []string{"deregistered agent-director", "nothing to deregister", "uninstall.sh: done\n"},
			stderr: []string{failed,
				"  command run: claude mcp remove agent-director\n  exit status: 1\n  stderr:\n" +
					"    MCP server \"agent-director\" exists in multiple scopes:\n" +
					"      - Local config (private to you in this project) ({HOME}/.claude.json [project: {HOME}/w/a])\n" +
					"      - User config (available in all your projects) ({HOME}/.claude.json)\n" +
					"    \n    To remove from a specific scope, use:\n      claude mcp remove agent-director -s local\n      claude mcp remove agent-director -s user\n",
				byHand},
			lastErr: doneFailed},
		{name: "any other failure, stderr empty", args: []string{"--mcp-also"}, env: []string{"CLAUDE_STANDIN_REMOVE_FAIL=1"}, code: 6,
			calls:    []string{"mcp remove agent-director"},
			noStdout: []string{"deregistered agent-director", "nothing to deregister"},
			stderr:   []string{failed, "  exit status: 1\n  stderr:\n    Error: could not write the MCP configuration\n", byHand},
			lastErr:  doneFailed},
		{name: "a failure does not stop --purge", args: []string{"--mcp-also", "--purge", "--force"}, env: []string{"CLAUDE_STANDIN_REMOVE_FAIL=1"}, code: 6,
			calls:   []string{"mcp remove agent-director"},
			stdout:  []string{"uninstall.sh: purged {HOME}/.config/agent-director\n"},
			stderr:  []string{failed, byHand},
			purged:  true,
			lastErr: doneFailed},
		{name: "a failure and a declined --purge", args: []string{"--mcp-also", "--purge"}, input: "n\n", env: []string{"CLAUDE_STANDIN_REMOVE_FAIL=1"}, code: 6,
			calls:    []string{"mcp remove agent-director"},
			stdout:   []string{"uninstall.sh: --purge aborted\n"},
			noStdout: []string{"purged"},
			stderr:   []string{failed, byHand},
			lastErr:  "    claude mcp remove agent-director -s <scope>"},
	}
	for _, set := range ownedSets(t) {
		capture, err := filepath.Abs(filepath.Join(ownedClaudeDir, set, "old-plain-local"))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			t.Run(set+"/"+row.name, func(t *testing.T) {
				home := t.TempDir()
				tools := filepath.Join(home, "tools")
				script, err := os.ReadFile(filepath.Join(ownedClaudeDir, "claude.sh"))
				if err != nil {
					t.Fatal(err)
				}
				writeMode(t, filepath.Join(tools, "claude"), string(script), 0o755)
				root := filepath.Join(home, ".config", "agent-director")
				writeMode(t, filepath.Join(root, "bin", "agent-director"), "binary", 0o755)
				writeMode(t, filepath.Join(root, "state.db"), "state", 0o600)
				log := filepath.Join(home, "claude-calls.log")
				env := append([]string{"CLAUDE_STANDIN_LOG=" + log, "CLAUDE_STANDIN_CAPTURE=" + capture}, row.env...)
				fill := strings.NewReplacer("{HOME}", home).Replace

				stdout, stderr, code := uninstallMCPRun(t, home, tools, row.input, env, row.args...)

				if code != row.code {
					t.Errorf("exit = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, row.code, stdout, stderr)
				}
				for _, s := range row.stdout {
					if !strings.Contains(stdout, fill(s)) {
						t.Errorf("stdout lacks %q:\n%s", fill(s), stdout)
					}
				}
				for _, s := range row.noStdout {
					if strings.Contains(stdout, s) {
						t.Errorf("stdout holds %q:\n%s", s, stdout)
					}
				}
				for _, s := range row.stderr {
					if !strings.Contains(stderr, fill(s)) {
						t.Errorf("stderr lacks %q:\n%s", fill(s), stderr)
					}
				}
				if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); row.lastErr == "" && stderr != "" && row.code == 0 {
					t.Errorf("stderr = %q, want it empty", stderr)
				} else if row.lastErr != "" && lines[len(lines)-1] != row.lastErr {
					t.Errorf("last stderr line = %q, want %q", lines[len(lines)-1], row.lastErr)
				}
				var calls []string
				for _, c := range ownedCalls(t, &moveHome{Env: map[string]string{"CLAUDE_STANDIN_LOG": log}}) {
					calls = append(calls, c.line())
				}
				if !slices.Equal(calls, row.calls) {
					t.Errorf("claude calls = %q, want %q", calls, row.calls)
				}
				// The binaries go whatever came of the deregistration; the directory only under --purge.
				if _, err := os.Lstat(filepath.Join(root, "bin", "agent-director")); !os.IsNotExist(err) && !row.purged {
					t.Errorf("the binary is still there after uninstall.sh: %v", err)
				}
				if _, err := os.Lstat(root); os.IsNotExist(err) != row.purged {
					t.Errorf("%s removed = %v, want %v (%v)", root, os.IsNotExist(err), row.purged, err)
				}
			})
		}
	}
}

// uninstallMCPWrapper writes path, a Claude Code program (executable unless
// mode says otherwise) that records its calls in log through the shared
// stand-in claude, so each program's calls are told apart by its own log.
func uninstallMCPWrapper(t *testing.T, path, log string, mode os.FileMode) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(ownedClaudeDir, "claude.sh"))
	if err != nil {
		t.Fatal(err)
	}
	writeMode(t, path, "#!/bin/bash\nexport CLAUDE_STANDIN_LOG='"+log+"'\nexec bash '"+script+"' \"$@\"\n", mode)
}

// TestUninstallMCPAlsoProgram (SR-11.4, SR-11.1, b.ssz): --mcp-also runs `mcp
// remove agent-director` with the program [launch] claude_path names when it
// names an executable file, else with claude from PATH. A claude_path line
// outside the reader's form, or one naming no executable file, gets a note
// naming it and the run uses PATH's claude; with no program at all the step is
// skipped, with a line saying so. None of it changes the exit status (0) or
// stops the rest of the run, and without --mcp-also no program is run. A
// layout never moved gets claude_path from the old config.toml.
func TestUninstallMCPAlsoProgram(t *testing.T) {
	if os.Getenv(sandboxguard.EnvVar) != "1" {
		t.Skipf("uninstall.sh runs only in the sandbox (%s=1)", sandboxguard.EnvVar)
	}
	cli2TrackInputs(t)
	if p, err := exec.LookPath("claude"); err == nil {
		t.Skipf("a claude is on PATH here (%s); the rows with none need a PATH without one", p)
	}
	const (
		removeCall = "mcp remove agent-director"
		deregister = "uninstall.sh: deregistered agent-director from MCP\n"
		skipped    = "uninstall.sh: --mcp-also skipped: no Claude Code program was found (claude is not on PATH, and "
		unreadable = "uninstall.sh: could not read [launch] claude_path from {config}; using claude from PATH.\n"
		namesNone  = "uninstall.sh: [launch] claude_path = \"{none}\" in {config} names no executable file; using claude from PATH\n"
	)
	// A claude_path value is the program in {launch}, a path that names nothing
	// ({none}) or a file that is not executable ({plain}).
	const (
		progNone = "none" // the program run: nothing
		progPath = "path" // the claude on PATH
		progConf = "launch"
	)
	rows := []struct {
		name       string
		oldLayout  bool   // only ~/.agent-director, with its config.toml
		keys       string // claude_path written by apitest.WriteKeysConfig: "" none, else a path with {launch}, {none} or {plain}
		raw        string // else config.toml's text as written by hand, with the same placeholders
		onPath     bool   // a claude on PATH
		args       []string
		ran        string   // the program that ran: progConf, progPath or progNone
		stdout     []string // each in stdout, with {config}, {none} and {home} filled in
		stderr     []string // each in stderr
		stderrNone bool     // stderr is empty
	}{
		{name: "claude_path beside another claude on PATH", keys: "{launch}", onPath: true, args: []string{"--mcp-also"},
			ran: progConf, stdout: []string{deregister}, stderrNone: true},
		{name: "claude_path unset, claude on PATH", onPath: true, args: []string{"--mcp-also"},
			ran: progPath, stdout: []string{deregister}, stderrNone: true},
		{name: "claude_path names no executable file, claude on PATH", keys: "{none}", onPath: true, args: []string{"--mcp-also"},
			ran: progPath, stdout: []string{deregister}, stderr: []string{namesNone}},
		{name: "claude_path names a file that is not executable, claude on PATH", keys: "{plain}", onPath: true, args: []string{"--mcp-also"},
			ran: progPath, stdout: []string{deregister}, stderr: []string{strings.ReplaceAll(namesNone, "{none}", "{plain}")}},
		{name: "[Launch] header, claude on PATH", raw: "[Launch]\nclaude_path = \"{launch}\"\n", onPath: true, args: []string{"--mcp-also"},
			ran: progPath, stdout: []string{deregister}, stderr: []string{unreadable, "  config  : {config}\n  line 1  : [Launch]\n"}},
		{name: "backslash escape in claude_path, claude on PATH", raw: "[launch]\nclaude_path = \"{launch}\\n\"\n", onPath: true, args: []string{"--mcp-also"},
			ran: progPath, stdout: []string{deregister}, stderr: []string{unreadable, "  config  : {config}\n  line 2  : claude_path = \"{launch}\\n\"\n"}},
		{name: "claude_path names no executable file, no claude on PATH", keys: "{none}", args: []string{"--mcp-also"},
			ran: progNone, stdout: []string{skipped + "[launch] claude_path in {config} names no executable file), so agent-director was not deregistered from Claude Code\n", "uninstall.sh: done\n"},
			stderr: []string{namesNone}},
		{name: "claude_path unset, no claude on PATH", args: []string{"--mcp-also"},
			ran: progNone, stdout: []string{skipped + "[launch] claude_path is not set), so agent-director was not deregistered from Claude Code\n", "uninstall.sh: done\n"},
			stderrNone: true},
		{name: "[Launch] header, no claude on PATH", raw: "[Launch]\nclaude_path = \"{launch}\"\n", args: []string{"--mcp-also"},
			ran: progNone, stdout: []string{skipped + "[launch] claude_path is not set), so agent-director was not deregistered from Claude Code\n"},
			stderr: []string{unreadable, "  line 1  : [Launch]\n"}},
		{name: "layout never moved, claude on PATH", oldLayout: true, keys: "{launch}", onPath: true, args: []string{"--mcp-also"},
			ran: progConf, stdout: []string{deregister}, stderrNone: true},
		{name: "layout never moved, no claude on PATH", oldLayout: true, keys: "{launch}", args: []string{"--mcp-also"},
			ran: progConf, stdout: []string{deregister}, stderrNone: true},
		{name: "claude_path set, no --mcp-also", keys: "{launch}", onPath: true,
			ran: progNone, stderrNone: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			home := t.TempDir()
			root := filepath.Join(home, ".config", "agent-director")
			if row.oldLayout {
				root = filepath.Join(home, ".agent-director")
			}
			config := filepath.Join(root, "config.toml")
			launch := filepath.Join(home, "launch", "claude-code")
			none := filepath.Join(home, "nowhere", "claude")
			plain := filepath.Join(home, "launch", "claude-plain")
			launchLog, pathLog := filepath.Join(home, "launch-calls.log"), filepath.Join(home, "path-calls.log")
			tools := filepath.Join(home, "tools")
			if err := os.MkdirAll(tools, 0o700); err != nil {
				t.Fatal(err)
			}
			uninstallMCPWrapper(t, launch, launchLog, 0o755)
			uninstallMCPWrapper(t, plain, launchLog, 0o644)
			if row.onPath {
				uninstallMCPWrapper(t, filepath.Join(tools, "claude"), pathLog, 0o755)
			}
			writeMode(t, filepath.Join(root, "bin", "agent-director"), "binary", 0o755)
			fill := strings.NewReplacer("{launch}", launch, "{none}", none, "{plain}", plain, "{config}", config, "{home}", home).Replace
			switch {
			case row.raw != "":
				writeMode(t, config, fill(row.raw), 0o600)
			case row.keys != "":
				apitest.WriteKeysConfig(t, config, apitest.ConfigKeys{ClaudePath: fill(row.keys)})
			default:
				apitest.WriteKeysConfig(t, config, apitest.ConfigKeys{CodexPath: "/opt/codex/codex"})
			}
			configBefore, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}

			stdout, stderr, code := uninstallMCPRun(t, home, tools, "", nil, row.args...)

			if code != 0 {
				t.Errorf("exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
			}
			for _, s := range row.stdout {
				if !strings.Contains(stdout, fill(s)) {
					t.Errorf("stdout lacks %q:\n%s", fill(s), stdout)
				}
			}
			for _, s := range row.stderr {
				if !strings.Contains(stderr, fill(s)) {
					t.Errorf("stderr lacks %q:\n%s", fill(s), stderr)
				}
			}
			if row.stderrNone && stderr != "" {
				t.Errorf("stderr = %q, want it empty", stderr)
			}
			// Each program's calls are in its own log; only the program that ran has any.
			wantCalls := map[string][]string{launchLog: nil, pathLog: nil}
			switch row.ran {
			case progConf:
				wantCalls[launchLog] = []string{removeCall}
			case progPath:
				wantCalls[pathLog] = []string{removeCall}
			}
			for log, want := range wantCalls {
				var got []string
				for _, c := range ownedCalls(t, &moveHome{Env: map[string]string{"CLAUDE_STANDIN_LOG": log}}) {
					got = append(got, c.line())
				}
				if !slices.Equal(got, want) {
					t.Errorf("calls recorded in %s = %q, want %q", filepath.Base(log), got, want)
				}
			}
			if row.ran == progNone && len(row.args) == 0 {
				for _, s := range []string{"from MCP", "deregist", "--mcp-also skipped"} {
					if strings.Contains(stdout, s) {
						t.Errorf("stdout of a run without --mcp-also holds %q:\n%s", s, stdout)
					}
				}
			}
			if after, err := os.ReadFile(config); err != nil || string(after) != string(configBefore) {
				t.Errorf("config.toml after uninstall.sh = %q, %v; want it unchanged, %q", after, err, configBefore)
			}
			if _, err := os.Lstat(filepath.Join(root, "bin", "agent-director")); !os.IsNotExist(err) {
				t.Errorf("the binary is still there after uninstall.sh: %v", err)
			}
		})
	}
}
