package installsh_test

// The agent-rename migration of config.toml and the templates, end to end
// (SR-11.2, SR-11.3, SR-11.7, SR-14.4): the rewrite, its backups, modes and
// links, a re-run, the gate on the store's version, and the migration while the
// move of AD's directory is due. What it refuses is migration_refusals_test.go's.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	migOldConfig = "# agent-director config\n[defaults]\ninject_help_hook = true\ndisable_askuserquestion = true # keep\n\n[store]\nbusy_timeout_ms = 5000\n"
	migNewConfig = "# agent-director config\n[defaults]\ninject_help_hook = true\ndisable_ask_user = true # keep\n\n[store]\nbusy_timeout_ms = 5000\n"
)

// migShape is a template before the migration and what it becomes.
type migShape struct{ name, old, want string }

var migShapes = []migShape{
	{"header", // claude_args renamed, agent added before the first header
		"name = \"header\"\nclaude_args = [\"--model\", \"opus\"] # keep\n\n[extra_env]\nA = \"1\"\n",
		"name = \"header\"\nagent_args = [\"--model\", \"opus\"] # keep\n\nagent = \"claude\"\n[extra_env]\nA = \"1\"\n"},
	{"nohdr", // no header: agent appended
		"name = \"nohdr\"\ncwd = \"/work\"\n",
		"name = \"nohdr\"\ncwd = \"/work\"\nagent = \"claude\"\n"},
	{"quoted", // tables with quoted keys, and a claude_args inside one, stay byte for byte
		"name = \"quoted\"\n\n[extra_env]\n\"claude_args\" = \"kept\"\n\"my key\" = \"v\"\n\n[labels]\nclaude_args = \"also kept\"\n",
		"name = \"quoted\"\n\nagent = \"claude\"\n[extra_env]\n\"claude_args\" = \"kept\"\n\"my key\" = \"v\"\n\n[labels]\nclaude_args = \"also kept\"\n"},
	{"named", // already names an agent: no change, no backup
		"name = \"named\"\nagent = \"codex\"\nagent_args = [\"-m\", \"x\"]\n",
		"name = \"named\"\nagent = \"codex\"\nagent_args = [\"-m\", \"x\"]\n"},
}

// migShapeFiles are the shapes as template files.
func migShapeFiles() []migFile {
	var out []migFile
	for _, s := range migShapes {
		out = append(out, migTemplate(s.name, s.old))
	}
	return out
}

// migMigrated is an install's config.toml and templates, by path under AD's
// directory, with the text each shape becomes.
func migMigrated() map[string]string {
	want := map[string]string{"config.toml": migNewConfig}
	for _, s := range migShapes {
		want["templates/"+s.name+".toml"] = s.want
	}
	return want
}

func TestMigrationEndToEnd(t *testing.T) {
	h := newMoveHome(t)
	h.migLay(h.New, migStoreBelow, migOldConfig, migShapeFiles()...)
	before := h.Snapshot()
	res := moveMustInstall(t, h)

	h.migFilesAre(h.New, migMigrated())
	if v, cur := h.migStoreVersion(filepath.Join(h.New, "state.db")), moveCurrentVersion(t, h); v != cur {
		t.Errorf("the store's user_version = %s, want the current %s", v, cur)
	}
	// A backup beside each changed file holds its original bytes in its mode;
	// a file the migration did not change has none.
	got := h.Snapshot()
	changed := []string{"config.toml", "templates/header.toml", "templates/nohdr.toml", "templates/quoted.toml"}
	for _, rel := range changed {
		orig := before[filepath.Join(moveNewRel, rel)]
		bak := migBackups(filepath.Join(h.New, rel))
		if len(bak) != 1 {
			t.Errorf("backups of %s = %v, want exactly one", rel, bak)
			continue
		}
		brel, _ := filepath.Rel(h.Home, bak[0])
		if got[brel] != orig {
			t.Errorf("backup of %s = %s, want the original %s", rel, got[brel], orig)
		}
		if !strings.Contains(res.Stdout, "  migrate : "+filepath.Join(h.New, rel)+" migrated for the agent rename (copy: "+bak[0]+")") {
			t.Errorf("stdout has no migrate line for %s:\n%s", rel, res.Stdout)
		}
	}
	if all := h.migBackupsUnder(h.New); len(all) != len(changed) {
		t.Errorf("backups under the install = %v, want one per changed file (%d)", all, len(changed))
	}
}

func TestMigrationBackupsBesideTheMergeBackup(t *testing.T) {
	h := newMoveHome(t)
	h.Flags = []string{"--no-symlink"} // hooks on: the inject_help_hook merge makes config.toml.bak.<ts>
	h.migLay(h.New, migStoreBelow, migOldConfig)
	moveMustInstall(t, h)

	cfg := filepath.Join(h.New, "config.toml")
	mig, bak := migBackups(cfg), mustGlob(t, cfg+".bak.*")
	if len(mig) != 1 || len(bak) != 1 {
		t.Fatalf("config.toml.pre-agent-rename.* = %v, config.toml.bak.* = %v; want one each", mig, bak)
	}
	if got := h.migRead(mig[0]); got != migOldConfig {
		t.Errorf("the migration's backup = %q, want the original config.toml", got)
	}
	// The merge runs after the migration, so its backup is the migrated file.
	if got := h.migRead(bak[0]); got != migNewConfig {
		t.Errorf("the merge's backup = %q, want the migrated config.toml", got)
	}
	if got := h.migRead(cfg); got != migNewConfig {
		t.Errorf("config.toml = %q, want the migrated bytes (the merge found the hook set)", got)
	}
}

func mustGlob(t *testing.T, pattern string) []string {
	t.Helper()
	m, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestMigrationModes(t *testing.T) {
	h := newMoveHome(t)
	private := migFile{Name: "templates/private.toml", Text: "name = \"private\"\n", Mode: 0o600}
	open := migFile{Name: "templates/open.toml", Text: "name = \"open\"\n", Mode: 0o640}
	h.migLay(h.New, migStoreBelow, migOldConfig, private, open)
	moveMustInstall(t, h)

	for _, f := range []migFile{private, open} {
		path := filepath.Join(h.New, f.Name)
		for _, p := range append([]string{path}, migBackups(path)...) {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != f.Mode {
				t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, f.Mode)
			}
		}
		if bak := migBackups(path); len(bak) != 1 {
			t.Errorf("backups of %s = %v, want one", f.Name, bak)
		}
		if got := h.migRead(path); got != f.Text+"agent = \"claude\"\n" {
			t.Errorf("%s = %q, want the agent appended", f.Name, got)
		}
	}
}

func TestMigrationThroughLinks(t *testing.T) {
	h := newMoveHome(t)
	dots := filepath.Join(h.Home, "dotfiles")
	h.Write(filepath.Join(dots, "config.toml"), migOldConfig, 0o640)
	h.Write(filepath.Join(dots, "linked.toml"), migShapes[1].old, 0o600)
	h.migLay(h.New, migStoreBelow, "")
	h.Link("../../dotfiles/config.toml", filepath.Join(h.New, "config.toml"))
	h.Link(filepath.Join(dots, "linked.toml"), filepath.Join(h.New, "templates", "linked.toml"))
	moveMustInstall(t, h)

	for link, want := range map[string]struct{ target, text string }{
		"config.toml":           {"../../dotfiles/config.toml", migNewConfig},
		"templates/linked.toml": {filepath.Join(dots, "linked.toml"), migShapes[1].want},
	} {
		p := filepath.Join(h.New, link)
		if tgt, err := os.Readlink(p); err != nil || tgt != want.target {
			t.Errorf("%s: link %q, %v; want it kept as %q", link, tgt, err, want.target)
		}
		if got := h.migRead(p); got != want.text {
			t.Errorf("%s reads %q through the link, want %q", link, got, want.text)
		}
		// The backup is a regular copy beside the link.
		bak := migBackups(p)
		if len(bak) != 1 {
			t.Errorf("backups of %s = %v, want one", link, bak)
			continue
		}
		if fi, err := os.Lstat(bak[0]); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("backup %s is not a regular file: %v, %v", bak[0], fi, err)
		}
	}
	// The targets keep their modes, and so do the backups of them.
	for p, mode := range map[string]os.FileMode{filepath.Join(dots, "config.toml"): 0o640, filepath.Join(dots, "linked.toml"): 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, mode)
		}
	}
	for p, mode := range map[string]os.FileMode{filepath.Join(h.New, "config.toml"): 0o640, filepath.Join(h.New, "templates", "linked.toml"): 0o600} {
		if fi, err := os.Stat(migBackups(p)[0]); err != nil || fi.Mode().Perm() != mode {
			t.Errorf("backup of %s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, mode)
		}
	}
}

// TestMigrationRerunOverMigratedFiles: files that are already migrated, over a
// store still below the rename (a first run that stopped before the store), are
// not read as changes: no write, no new backup.
func TestMigrationRerunOverMigratedFiles(t *testing.T) {
	h := newMoveHome(t)
	var files []migFile
	for _, s := range migShapes {
		files = append(files, migTemplate(s.name, s.want))
	}
	h.migLay(h.New, migStoreBelow, migNewConfig, files...)
	before := h.Snapshot()
	res := moveMustInstall(t, h)

	if !strings.Contains(res.Stdout, "  migrate : config.toml and the templates need no agent-rename change") {
		t.Errorf("stdout lacks the no-change line:\n%s", res.Stdout)
	}
	migUnchanged(t, before, h.Snapshot(), moveNewRel+"/config.toml", moveNewRel+"/templates")
	if got := h.migBackupsUnder(h.New); len(got) != 0 {
		t.Errorf("backups = %v, want none", got)
	}
	if v, cur := h.migStoreVersion(filepath.Join(h.New, "state.db")), moveCurrentVersion(t, h); v != cur {
		t.Errorf("the store's user_version = %s, want the current %s", v, cur)
	}
}

// migLater is a template made after the upgrade: no agent, a multi-line
// agent_args array. Read as top-level lines it would be refused.
const migLater = "name = \"later\"\nagent_args = [\n  \"--model\",\n  \"opus\",\n]\n"

func TestMigrationGate(t *testing.T) {
	later, old := migTemplate("later", migLater), migTemplate("old", migShapes[0].old)
	rows := []struct {
		name  string
		store migStoreKind
		files []migFile
	}{
		{"store-at-the-hop-leaves-a-template-made-after-the-upgrade", migStoreAt, []migFile{later}},
		{"store-at-the-hop-reads-no-template", migStoreAt, []migFile{later, old}},
		{"no-store-reads-and-changes-nothing", migNoStore, []migFile{later, old}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			h.migLay(h.New, row.store, migNewConfig, row.files...)
			before := h.Snapshot()
			res := moveMustInstall(t, h)
			migUnchanged(t, before, h.Snapshot(), moveNewRel+"/config.toml", moveNewRel+"/templates")
			if got := h.migBackupsUnder(h.New); len(got) != 0 {
				t.Errorf("backups = %v, want none", got)
			}
			if strings.Contains(res.Stdout, "migrate :") {
				t.Errorf("stdout reports a migration:\n%s", res.Stdout)
			}
		})
	}

	t.Run("no-store-and-the-old-key-stops-at-the-store-open", func(t *testing.T) {
		h := newMoveHome(t)
		h.migLay(h.New, migNoStore, migOldConfig, migTemplate("later", migLater), migTemplate("old", migShapes[0].old))
		before := h.Snapshot()
		res := h.Install()
		moveWant{Code: 5, Err: "ErrConfigMalformed", Stderr: []string{"disable_ask_user"}}.Check(t, h, res)
		migUnchanged(t, before, h.Snapshot(), moveNewRel+"/config.toml", moveNewRel+"/templates")
		if got := h.migBackupsUnder(h.New); len(got) != 0 {
			t.Errorf("backups = %v, want none", got)
		}
	})
}

func TestMigrationWhileTheMoveIsDue(t *testing.T) {
	t.Run("below-the-hop-reads-the-old-directory-and-migrates-the-new-one", func(t *testing.T) {
		h := newMoveHome(t)
		h.BuildOld(withMoveConfig(func(*moveHome) string { return migOldConfig }))
		h.migLay(h.Old, migNoStore, "", migShapeFiles()...)
		before := h.Snapshot()
		moveMustInstall(t, h)

		got := h.Snapshot()
		kept := moveKeptDir(t, h)
		if d := moveDiff(moveUnder(before, moveOldRel), moveUnder(got, kept)); d != "" {
			t.Errorf("the kept directory is not the original:\n%s", d)
		}
		h.migFilesAre(h.New, migMigrated())
		for _, rel := range []string{"config.toml", "templates/header.toml", "templates/nohdr.toml", "templates/quoted.toml"} {
			if bak := migBackups(filepath.Join(h.New, rel)); len(bak) != 1 {
				t.Errorf("backups of %s in the new directory = %v, want one", rel, bak)
			}
		}
		// BuildOld's own worker and reviewer templates are migrated too.
		if all := h.migBackupsUnder(h.New); len(all) != 6 {
			t.Errorf("backups in the new directory = %v, want six", all)
		}
		if all := h.migBackupsUnder(h.Home); len(all) != 6 {
			t.Errorf("backups under HOME = %v, want only the six in the new directory", all)
		}
		for _, p := range h.migBackupsUnder(filepath.Join(h.Home, kept)) {
			t.Errorf("the kept directory holds a backup: %s", p)
		}
	})

	t.Run("store-at-the-hop-outside-the-old-directory-migrates-nothing", func(t *testing.T) {
		h := newMoveHome(t)
		db := filepath.Join(h.Home, "data", "state.db")
		h.migMakeStore(db)
		cfg := moveConfig(db, 5000)
		h.BuildOld(withMoveNoStore(), withMoveConfig(func(*moveHome) string { return cfg }))
		h.migLay(h.Old, migNoStore, "", migShapeFiles()...)
		before := h.Snapshot()
		moveMustInstall(t, h)

		got := h.Snapshot()
		original := moveUnder(before, moveOldRel)
		for _, rel := range []string{"config.toml", "templates/header.toml", "templates/nohdr.toml", "templates/quoted.toml", "templates/named.toml", "templates/worker.toml", "templates/reviewer.toml"} {
			if got[moveNewRel+"/"+rel] != original[rel] {
				t.Errorf("new %s = %s, want the original's %s", rel, got[moveNewRel+"/"+rel], original[rel])
			}
		}
		if all := h.migBackupsUnder(h.Home); len(all) != 0 {
			t.Errorf("backups = %v, want none", all)
		}
	})
}
