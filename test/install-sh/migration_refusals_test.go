package installsh_test

// What install.sh refuses before it migrates config.toml and the templates, and
// what it says when a migration write fails (SR-11.2, SR-11.7, SR-14.4). A
// refusal exits 5 with its error name and leaves the whole home tree as it was;
// a write that fails after the binary swap exits 1. What the migration changes
// is migration_test.go's.

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestInstallerPrerequisites (b.ufx): one refusal names all missing commands
// for the selected mode, with install advice, before touching the home or staging files.
func TestInstallerPrerequisites(t *testing.T) {
	rows := []struct {
		name    string
		release bool
		hashes  bool
		missing []string
		want    []string
		absent  []string
	}{
		{"reported-pair", true, true, []string{"file", "sqlite3"}, []string{"file", "sqlite3"}, nil},
		{"common-commands", false, false, []string{"uname", "sed", "cp", "mktemp"}, []string{"uname", "sed", "cp", "mktemp"}, nil},
		{"release-commands", true, true, []string{"curl", "sleep", "sha256sum", "shasum"}, []string{"curl", "sleep", "sha256sum", "shasum"}, nil},
		{"local-needs-no-download-or-hash", false, false, []string{"file", "curl", "sleep", "sha256sum", "shasum"}, []string{"file"}, []string{"curl", "sleep", "sha256sum", "shasum"}},
		{"release-without-checksums", true, false, []string{"file", "sha256sum", "shasum"}, []string{"file"}, []string{"sha256sum", "shasum"}},
		{"checksum-alternative", true, true, []string{"file", "sha256sum"}, []string{"file"}, []string{"sha256sum", "shasum"}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			// Keep an existing installation, prior backup, config link and store
			// sentinel: a prerequisite refusal must not migrate or replace any of them.
			h.Write(filepath.Join(h.New, "bin", "agent-director"), "old binary\n", 0o755)
			h.Write(filepath.Join(h.New, "bin", "agent-director.prior"), "prior binary\n", 0o700)
			h.Write(filepath.Join(h.Home, "dotfiles", "config.toml"), "[defaults]\n", 0o640)
			h.Link("../../dotfiles/config.toml", filepath.Join(h.New, "config.toml"))
			h.Write(filepath.Join(h.New, "state.db"), "store must not be opened\n", 0o600)
			h.Flags = []string{"--keep-prior"}
			if row.release {
				h.Pair = movePair{}
				h.Flags = append(h.Flags, "--from-release", "v0.17.1")
				if row.hashes {
					h.Flags = append(h.Flags, "--sha256", strings.Repeat("0", 64), "--admin-sha256", strings.Repeat("0", 64))
				}
			}
			// Copy the existing toolbox's links, excluding only this case's missing
			// commands. Never append the ambient PATH, which could restore them.
			entries, err := os.ReadDir(h.S.Toolbox)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if !slices.Contains(row.missing, entry.Name()) {
					h.Link(filepath.Join(h.S.Toolbox, entry.Name()), filepath.Join(h.Tools, entry.Name()))
				}
			}
			// The shared toolbox omits curl; supply a presence-only executable.
			// A shasum stand-in makes the alternative deterministic on both images.
			for _, tool := range []string{"curl", "shasum"} {
				if !slices.Contains(row.missing, tool) {
					if err := os.Remove(filepath.Join(h.Tools, tool)); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					h.WriteTool(tool, "echo UNEXPECTED_PREREQUISITE_EXECUTION >&2; exit 99")
				}
			}
			h.Env["PATH"] = h.Tools
			before := h.Snapshot()
			res := h.Install()
			if res.Code != 2 || res.Stdout != "" {
				t.Errorf("prerequisite refusal: exit=%d stdout=%q; want exit=2 and empty stdout\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
			}
			for _, tool := range row.want {
				if !regexp.MustCompile(`\b` + regexp.QuoteMeta(tool) + `\b`).MatchString(res.Stderr) {
					t.Errorf("aggregate prerequisite diagnostic omits %q:\n%s", tool, res.Stderr)
				}
			}
			for _, tool := range row.absent {
				if regexp.MustCompile(`\b` + regexp.QuoteMeta(tool) + `\b`).MatchString(res.Stderr) {
					t.Errorf("diagnostic requires unneeded command %q:\n%s", tool, res.Stderr)
				}
			}
			if !strings.Contains(strings.ToLower(res.Stderr), "install") || !strings.Contains(strings.ToLower(res.Stderr), "package") {
				t.Errorf("missing prerequisite package installation advice:\n%s", res.Stderr)
			}
			for _, bad := range []string{"command not found", "UNEXPECTED_PREREQUISITE_EXECUTION", "err_name="} {
				if strings.Contains(res.Stderr, bad) {
					t.Errorf("preflight did not stop cleanly (%s):\n%s", bad, res.Stderr)
				}
			}
			if diff := moveDiff(before, h.Snapshot()); diff != "" {
				t.Errorf("missing prerequisites changed HOME:\n%s", diff)
			}
			if entries, err := os.ReadDir(h.Tmp); err != nil || len(entries) != 0 {
				t.Errorf("prerequisite refusal left staging files: %v, %v", entries, err)
			}
		})
	}
}

// migSetup lays a store below the rename's version, config and templates in the
// install's directory, and applies flags (nil keeps the fixture's --no-hooks).
func migSetup(config string, flags []string, files ...migFile) func(t *testing.T, h *moveHome) {
	return func(t *testing.T, h *moveHome) {
		if flags != nil {
			h.Flags = flags
		}
		h.migLay(h.New, migStoreBelow, config, files...)
	}
}

// migReadOnlyDir makes dir unwritable for the install (hold: Release undoes it).
func migReadOnlyDir(dir string) func(t *testing.T, h *moveHome) {
	return func(t *testing.T, h *moveHome) {
		if os.Geteuid() == 0 {
			t.Skip("a read-only directory stops nothing for root")
		}
		fi, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, fi.Mode()&^0o222); err != nil {
			t.Fatal(err)
		}
		h.OnRelease(func() { _ = os.Chmod(dir, fi.Mode()) })
	}
}

func TestMigrationPreflightRefusals(t *testing.T) {
	const hooksOn = "--no-symlink"
	configErr := func(stderr ...string) moveWant {
		return moveWant{Code: 5, Err: "ErrConfigMalformed", Stderr: append([]string{"Nothing was installed or changed."}, stderr...)}
	}
	templateErr := func(stderr ...string) moveWant {
		return moveWant{Code: 5, Err: "ErrTemplateMalformed", Stderr: append([]string{"Nothing was installed or changed."}, stderr...)}
	}
	defaults := func(lines ...string) string { return "[defaults]\n" + strings.Join(lines, "\n") + "\n" }
	twice := "sets the question-tool key more than once under [defaults]"
	inline := "sets defaults as a key"
	topLine := "holds a line the migration cannot read"
	dots := func(h *moveHome) string { return filepath.Join(h.Home, "dotfiles") }

	rows := []moveRefusal{
		{name: "config-old-and-new-key",
			setup: migSetup(defaults("disable_askuserquestion = true", "disable_ask_user = true"), nil),
			want:  configErr(twice, "line 2  : disable_askuserquestion = true", "line 3  : disable_ask_user = true")},
		{name: "config-old-key-twice",
			setup: migSetup(defaults("disable_askuserquestion = true", "disable_askuserquestion = false"), nil),
			want:  configErr(twice)},
		{name: "config-new-key-twice",
			setup: migSetup(defaults("disable_ask_user = true", "disable_ask_user = false"), nil),
			want:  configErr(twice)},
		{name: "config-inline-defaults-names-the-old-key",
			setup: migSetup("defaults = { disable_askuserquestion = true }\n", nil),
			want:  configErr(inline, "disable_askuserquestion", "line 1  : defaults = { disable_askuserquestion = true }")},
		{name: "config-inline-defaults-names-the-old-key-with-hooks-on",
			setup: migSetup("defaults = { disable_askuserquestion = true }\n", []string{hooksOn}),
			want:  configErr("defaults = { disable_askuserquestion = true }")},
		{name: "template-line-outside-the-form",
			setup: migSetup(migNewConfig, nil, migTemplate("bad", "name = \"bad\"\n\"quoted key\" = 1\n")),
			want:  templateErr(topLine, "line 2  : \"quoted key\" = 1")},
		{name: "template-multi-line-top-level-value",
			setup: migSetup(migNewConfig, nil, migTemplate("later", migLater)),
			want:  templateErr(topLine, "line 2  : agent_args = [")},
		{name: "template-claude-args-twice",
			setup: migSetup(migNewConfig, nil, migTemplate("dup", "name = \"dup\"\nclaude_args = [\"a\"]\nclaude_args = [\"b\"]\n")),
			want:  templateErr("sets claude_args more than once")},
		{name: "template-both-names",
			setup: migSetup(migNewConfig, nil, migTemplate("both", "name = \"both\"\nclaude_args = [\"a\"]\nagent_args = [\"b\"]\n")),
			want:  templateErr("sets both claude_args and agent_args")},
		{name: "store-version-unreadable",
			setup: func(t *testing.T, h *moveHome) {
				migSetup(migOldConfig, nil, migShapeFiles()...)(t, h)
				h.Write(filepath.Join(h.New, "state.db"), "this is not a database\n", 0o600)
			},
			want: moveWant{Code: 5, Err: "ErrVersionUnreadable", Stderr: []string{"reading state.db's schema version FAILED", "Nothing was installed or changed."}}},
	}

	// A link the migration cannot write through: its target's directory is not
	// writable. Each with and without --no-hooks, naming its own file's error.
	for _, hooks := range []struct {
		name  string
		flags []string
	}{{"no-hooks", nil}, {"hooks-on", []string{hooksOn}}} {
		rows = append(rows,
			moveRefusal{name: "config-link-" + hooks.name,
				setup: func(t *testing.T, h *moveHome) {
					migSetup("", hooks.flags)(t, h)
					h.Write(filepath.Join(dots(h), "config.toml"), migOldConfig, 0o600)
					h.Link(filepath.Join(dots(h), "config.toml"), filepath.Join(h.New, "config.toml"))
				},
				hold: func(t *testing.T, h *moveHome) { migReadOnlyDir(dots(h))(t, h) },
				// With hooks on the merge would refuse this link too; the migration's
				// refusal is the one given.
				want: moveWant{Code: 5, Err: "ErrConfigMalformed", Stderr: []string{"cannot migrate {HOME}/.config/agent-director/config.toml through its symlink; refusing to install."},
					NoStderr: []string{"cannot merge into", "--no-hooks, which edits neither"}}},
			moveRefusal{name: "template-link-" + hooks.name,
				setup: func(t *testing.T, h *moveHome) {
					migSetup(migNewConfig, hooks.flags)(t, h)
					h.Write(filepath.Join(dots(h), "t.toml"), migShapes[1].old, 0o644)
					h.Link(filepath.Join(dots(h), "t.toml"), filepath.Join(h.New, "templates", "t.toml"))
				},
				hold: func(t *testing.T, h *moveHome) { migReadOnlyDir(dots(h))(t, h) },
				want: moveWant{Code: 5, Err: "ErrTemplateMalformed", Stderr: []string{"cannot migrate {HOME}/.config/agent-director/templates/t.toml through its symlink; refusing to install."}}},
		)
	}
	runMoveRefusals(t, rows)
}

// TestMigrationRefusedWhileTheMoveIsDue: the pre-flight reads the old directory
// while the move is due, so a template there outside the form stops the install
// before any move: no new directory, the old one unchanged.
func TestMigrationRefusedWhileTheMoveIsDue(t *testing.T) {
	runMoveRefusals(t, []moveRefusal{{
		name: "old-directory-template-outside-the-form",
		setup: moveOldWith(func(t *testing.T, h *moveHome) {
			h.migLay(h.Old, migNoStore, "", migTemplate("bad", "name = \"bad\"\n\"quoted key\" = 1\n"))
		}),
		want: moveWant{Code: 5, Err: "ErrTemplateMalformed", Stderr: []string{
			"template {HOME}/.agent-director/templates/bad.toml holds a line the migration cannot read; refusing to install.",
			"Nothing was installed or changed."}},
	}})
}

// TestMigrationWriteFailsAfterTheBinarySwap: a backup copy that fails (a full
// disk) on the second template stops the install with exit 1, after the first
// two files are migrated and backed up, before the store is opened.
func TestMigrationWriteFailsAfterTheBinarySwap(t *testing.T) {
	h := newMoveHome(t)
	h.migLay(h.New, migStoreBelow, migOldConfig, migTemplate("a", migShapes[1].old), migTemplate("b", migShapes[1].old))
	h.WrapTool("cp", `if [[ "${!#}" == *templates/b.toml.pre-agent-rename.* ]]; then echo 'cp: error writing: No space left on device' >&2; exit 1; fi`)
	before := h.Snapshot()
	res := h.Install()

	file := filepath.Join(h.New, "templates", "b.toml")
	moveWant{Code: 1, Stderr: []string{
		"install.sh: the agent-rename migration could not write " + file + "; stopping before the store is opened.",
		"  file    : " + file,
		"No space left on device",
		"  " + file + " is as it was, and every copy this install made is kept.",
		"The new binaries are already installed, and the store is not opened.",
		"fix the cause, then run this release's install.sh again",
		"see \"Going back to the",
		"previous release\" and \"A migration write failed after the binary swap\" in",
		"docs/operator-actions.md.",
	}}.Check(t, h, res)
	// A failed backup copy stops the install at once: nothing then tries to
	// change the mode of a copy that was never made.
	if all := res.Stdout + res.Stderr; strings.Contains(all, "chmod: cannot access") {
		t.Errorf("a chmod of the missing backup ran:\n%s", all)
	}

	got := h.Snapshot()
	// b.toml is as it was and has no backup; config.toml and a.toml are migrated, each with its backup kept.
	migUnchanged(t, before, got, moveNewRel+"/templates/b.toml")
	if bak := migBackups(file); len(bak) != 0 {
		t.Errorf("b.toml has backups %v, want none", bak)
	}
	h.migFilesAre(h.New, map[string]string{"config.toml": migNewConfig, "templates/a.toml": migShapes[1].want})
	for _, rel := range []string{"config.toml", "templates/a.toml"} {
		bak := migBackups(filepath.Join(h.New, rel))
		if len(bak) != 1 {
			t.Errorf("backups of %s = %v, want one kept", rel, bak)
			continue
		}
		brel, _ := filepath.Rel(h.Home, bak[0])
		if got[brel] != before[filepath.Join(moveNewRel, rel)] {
			t.Errorf("backup of %s = %s, want the original %s", rel, got[brel], before[filepath.Join(moveNewRel, rel)])
		}
	}
	if left := mustGlob(t, filepath.Join(h.New, "templates", "*.new")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
	// The store was not opened: it is still below the rename, with no sentinel.
	migUnchanged(t, before, got, moveNewRel+"/state.db")
	if _, err := os.Lstat(filepath.Join(h.New, "migrate-authorized")); err == nil {
		t.Errorf("a migrate-authorized sentinel was written")
	}
	// The new binaries are in place.
	for path, want := range map[string]string{filepath.Join(h.New, "bin", "agent-director"): h.Pair.Main, filepath.Join(h.New, "admin", "agent-director-admin"): h.Pair.Admin} {
		if h.migRead(path) != h.migRead(want) {
			t.Errorf("%s is not the new binary", path)
		}
	}
}
