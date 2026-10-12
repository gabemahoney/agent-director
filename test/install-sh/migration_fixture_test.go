package installsh_test

// The agent-rename migration's home builder (SR-11.2, SR-14.4): config.toml and
// templates laid down in a directory beside a store at a chosen state. It builds
// on the move fixture (move_fixture_test.go), which it only reads. The cases are
// in migration_test.go and migration_refusals_test.go.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// migStoreKind is the store migLay leaves beside the files.
type migStoreKind int

const (
	migNoStore    migStoreKind = iota // none made: the directory's own, or none
	migStoreBelow                     // a v0.11.1 store, below the rename's schema version
	migStoreAt                        // a store the built binary made, at the current version
)

// migFile is a file under a migLay directory; Mode 0 means 0644.
type migFile struct {
	Name, Text string
	Mode       os.FileMode
}

// migTemplate is a migFile under templates/.
func migTemplate(name, text string) migFile {
	return migFile{Name: "templates/" + name + ".toml", Text: text}
}

// migLay writes config.toml (0600, when config is not empty) and files into dir,
// and the store of kind at dir/state.db. A store the binary makes is made first,
// before any config.toml it might refuse.
func (h *moveHome) migLay(dir string, store migStoreKind, config string, files ...migFile) {
	h.t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	db := filepath.Join(dir, "state.db")
	switch store {
	case migStoreAt:
		h.migMakeStore(db)
	case migStoreBelow:
		h.WriteStore(db)
	}
	if config != "" {
		h.Write(filepath.Join(dir, "config.toml"), config, 0o600)
	}
	for _, f := range files {
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		h.Write(filepath.Join(dir, f.Name), f.Text, mode)
	}
}

// migMakeStore makes a store at db at the version the pair built from this tree
// makes, with the install's home and no config.toml in view.
func (h *moveHome) migMakeStore(db string) {
	h.t.Helper()
	if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if res := h.Run(h.Pair.Main, "list", "--store-path", db); res.Code != 0 {
		h.t.Fatalf("list --store-path %s: exit %d: %s", db, res.Code, res.Stderr)
	}
}

// migRead returns the bytes of path; a file that cannot be read fails the case.
func (h *moveHome) migRead(path string) string {
	h.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

// migBackups lists the files beside path that the migration made of it
// (<path>.pre-agent-rename.<ts>), sorted.
func migBackups(path string) []string {
	m, _ := filepath.Glob(path + ".pre-agent-rename.*")
	sort.Strings(m)
	return m
}

// migBackupsUnder lists every migration backup under dir, relative to dir.
func (h *moveHome) migBackupsUnder(dir string) []string {
	h.t.Helper()
	var out []string
	for p := range h.Snapshot() {
		if strings.Contains(p, ".pre-agent-rename.") {
			if rel, err := filepath.Rel(dir, filepath.Join(h.Home, p)); err == nil && !strings.HasPrefix(rel, "..") {
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out
}

// migStoreVersion is the user_version of the store at db.
func (h *moveHome) migStoreVersion(db string) string {
	h.t.Helper()
	return moveUserVersion(h.t, h, db)
}

// migFilesAre fails the case unless each file under dir that want names
// (relative paths) holds exactly the text it maps to.
func (h *moveHome) migFilesAre(dir string, want map[string]string) {
	h.t.Helper()
	for rel, text := range want {
		if got := h.migRead(filepath.Join(dir, rel)); got != text {
			h.t.Errorf("%s =\n%q\nwant\n%q", rel, got, text)
		}
	}
}

// migUnchanged fails the case unless every entry of before at or under rels
// (paths relative to HOME) is as it was in after, and after adds none under them.
func migUnchanged(t *testing.T, before, after moveTree, rels ...string) {
	t.Helper()
	pick := func(tr moveTree) moveTree {
		out := moveTree{}
		for p, v := range tr {
			for _, r := range rels {
				if p == r || strings.HasPrefix(p, r+"/") || strings.HasPrefix(p, r+".") {
					out[p] = v
				}
			}
		}
		return out
	}
	if d := moveDiff(pick(before), pick(after)); d != "" {
		t.Errorf("the install changed what it must not:\n%s", d)
	}
}
