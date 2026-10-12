package installsh_test

// What install.sh prints when the agent-rename migration stops an install after
// the move of AD's directory (SR-11.2, SR-11.7): the move's references into the
// old directory, which a re-run (with no move due) would never print again. A
// failed write (exit 1) and an apply-time refusal (exit 5) each print them on
// stderr after the stop's own description and before its ways on, which stay the
// last lines. migration_refusals_test.go has the stop's other contents.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	migStopConfigTail = "\n[launch]\nclaude_path = \"{HOME}/.agent-director/bin/claude\"\n"
	migStopTemplate   = "name = \"tooling\"\ncwd = \"{HOME}/.agent-director/work\"\n\n[extra_env]\nPLAIN = \"kept\"\n"
	migStopRefsHead   = "  references: these lines still name the old directory."
	migStopWaysFirst  = "  The new binaries are already installed, and the store is not opened. Either:"
	migStopWaysLast   = "      docs/operator-actions.md."
)

// migStopOld builds an old install whose config.toml and tooling.toml name the
// old directory (and whose config.toml and templates the migration changes),
// and returns the lines the references block must list, in order, as printed.
func migStopOld(h *moveHome) []string {
	cfg := h.expand(migOldConfig + migStopConfigTail)
	h.BuildOld(withMoveConfig(func(*moveHome) string { return cfg }))
	h.Write(filepath.Join(h.Old, "templates", "tooling.toml"), h.expand(migStopTemplate), 0o644)
	return []string{
		"    {HOME}/.config/agent-director/config.toml:" + migLineOf(cfg, "claude_path") + `:claude_path = "{HOME}/.agent-director/bin/claude"`,
		"    {HOME}/.config/agent-director/templates/tooling.toml:" + migLineOf(h.expand(migStopTemplate), "cwd") + `:cwd = "{HOME}/.agent-director/work"`,
	}
}

// migLineOf is the 1-based number, as text, of the first line of text holding sub.
func migLineOf(text, sub string) string {
	for i, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return strconv.Itoa(i + 1)
		}
	}
	panic("no line holds " + sub)
}

// migStopOrder fails the case unless stderr holds, in this order: the stop's
// own description (ending with the line before), the references block with each
// of refs in turn, then the ways on, whose last line is stderr's last but for
// the err_name an exit 5 ends with.
func migStopOrder(t *testing.T, h *moveHome, stderr, before string, refs []string, kept string) {
	t.Helper()
	at := strings.Index(stderr, before)
	if at < 0 {
		t.Fatalf("stderr lacks %q:\n%s", before, stderr)
	}
	for _, s := range append(append([]string{migStopRefsHead}, h.expandAll(refs)...), migStopWaysFirst, migStopWaysLast) {
		next := strings.Index(stderr[at:], s)
		if next < 0 {
			t.Fatalf("stderr lacks %q after the text before it (in order: %q, the references, the ways on):\n%s", s, before, stderr)
		}
		at += next + len(s)
	}
	// The block names the kept directory the references' fix is about.
	if !strings.Contains(stderr, "("+filepath.Join(h.Home, kept)+"):") {
		t.Errorf("the references block does not name the kept directory %s:\n%s", kept, stderr)
	}
	if n := strings.Count(stderr, "  references: "); n != 1 {
		t.Errorf("stderr holds %d references blocks, want one:\n%s", n, stderr)
	}
	block := stderr[strings.Index(stderr, "  references: "):strings.Index(stderr, migStopWaysFirst)]
	if strings.Contains(block, ".pre-agent-rename.") {
		t.Errorf("a migration backup is listed as a reference:\n%s", block)
	}
}

// failBackupCopy makes the cp that copies a file to its migration backup fail,
// as a full disk does. The move's own copies are not touched.
func failBackupCopy(h *moveHome) {
	h.WrapTool("cp", `if [[ "${!#}" == *.pre-agent-rename.* ]]; then echo 'cp: error writing: No space left on device' >&2; exit 1; fi`)
}

// TestMigrationStopAfterTheMovePrintsTheReferences: the move ran and the
// migration then stopped the install, so the references into the old directory
// are on stderr, before the stop's ways on.
func TestMigrationStopAfterTheMovePrintsTheReferences(t *testing.T) {
	t.Run("a-failed-write-exit-1", func(t *testing.T) {
		h := newMoveHome(t)
		refs := migStopOld(h)
		failBackupCopy(h)
		res := h.Install()

		cfg := filepath.Join(h.New, "config.toml")
		moveWant{Code: 1,
			Stderr:   []string{"install.sh: the agent-rename migration could not write " + cfg + "; stopping before the store is opened.", "No space left on device"},
			NoStdout: []string{"references:"},
		}.Check(t, h, res)
		migStopOrder(t, h, res.Stderr, "  "+cfg+" is as it was, and every copy this install made is kept.", refs, moveKeptDir(t, h))
		if got := strings.TrimRight(res.Stderr, "\n"); !strings.HasSuffix(got, "\n"+migStopWaysLast) {
			t.Errorf("stderr does not end with the ways on:\n%s", res.Stderr)
		}
	})

	t.Run("an-apply-time-refusal-exit-5", func(t *testing.T) {
		h := newMoveHome(t)
		refs := migStopOld(h)
		// The template is whole when pre-flight reads it in the old directory and
		// the move copies it; a second claude_args line appears before the migration
		// applies, as when something edits it in between. The stand-in cp does it as
		// the admin binary is copied, which comes after the move and before the
		// migration.
		dup := filepath.Join(h.New, "templates", "dup.toml")
		h.Write(filepath.Join(h.Old, "templates", "dup.toml"), "name = \"dup\"\nclaude_args = [\"a\"]\n", 0o644)
		h.WrapTool("cp", `if [[ "${!#}" == `+moveQuote(filepath.Join(h.New, "admin"))+`/* ]]; then echo 'claude_args = ["b"]' >> `+moveQuote(dup)+`; fi`)
		res := h.Install()

		moveWant{Code: 5, Err: "ErrTemplateMalformed",
			Stderr: []string{
				"install.sh: the agent-rename migration refuses " + dup + ", which pre-flight accepted; stopping before the store is opened.",
				"sets claude_args more than once",
			},
			NoStdout: []string{"references:"},
		}.Check(t, h, res)
		migStopOrder(t, h, res.Stderr, "  "+dup+" changed since pre-flight.", refs, moveKeptDir(t, h))
		if want := "install.sh: err_name=ErrTemplateMalformed"; res.Last() != want {
			t.Errorf("last stderr line = %q, want %q", res.Last(), want)
		}
		// The refused template is as it was: the ways on say nothing was changed in it.
		if got := h.migRead(dup); !strings.HasSuffix(got, "claude_args = [\"b\"]\n") || len(migBackups(dup)) != 0 {
			t.Errorf("dup.toml = %q, backups %v: want it left as it was, with no backup", got, migBackups(dup))
		}
	})

	t.Run("no-move-no-references", func(t *testing.T) {
		// The same failed write in an install with no move due: nothing to print,
		// even though config.toml names the old directory.
		h := newMoveHome(t)
		h.migLay(h.New, migStoreBelow, h.expand(migOldConfig+migStopConfigTail), migTemplate("tooling", h.expand(migStopTemplate)))
		failBackupCopy(h)
		res := h.Install()

		moveWant{Code: 1, Stderr: []string{"stopping before the store is opened.", migStopWaysFirst}, NoStderr: []string{"references:"}, NoStdout: []string{"references:"}}.Check(t, h, res)
		if _, err := os.Lstat(h.Old); err == nil {
			t.Errorf("%s exists: nothing was to move", h.Old)
		}
	})
}
