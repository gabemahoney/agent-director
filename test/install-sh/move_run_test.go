package installsh_test

// The move itself (SR-11.7, SR-14.4): an upgrade that moves ~/.agent-director
// to ~/.config/agent-director and keeps the original, what it does to the path
// keys and other references, a copy that fails (a file it cannot copy, a full
// disk, a signal), recovery from the marker a failed move leaves, and a re-run.
// What happens before the move is move_preflight_test.go's.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	moveKeptRE    = `\.agent-director\.pre-move-\d{8}-\d{6}(-\d+)?`
	moveNewRel    = ".config/agent-director"
	moveOldRel    = ".agent-director"
	moveMarkerRel = moveNewRel + "/.move-incomplete"
)

// moveUnder returns the entries of tree at or under dir (relative to HOME), keyed
// relative to dir: "" is dir itself.
func moveUnder(tree moveTree, dir string) moveTree {
	out := moveTree{}
	for p, v := range tree {
		switch {
		case p == dir:
			out[""] = v
		case strings.HasPrefix(p, dir+"/"):
			out[p[len(dir)+1:]] = v
		}
	}
	return out
}

// moveWithout returns tree less each name and everything under it.
func moveWithout(tree moveTree, names ...string) moveTree {
	out := moveTree{}
	for p, v := range tree {
		keep := true
		for _, n := range names {
			if p == n || strings.HasPrefix(p, n+"/") {
				keep = false
			}
		}
		if keep {
			out[p] = v
		}
	}
	return out
}

// moveKeptDir is the one ~/.agent-director.pre-move-* directory, relative to HOME.
func moveKeptDir(t *testing.T, h *moveHome) string {
	t.Helper()
	kept, _ := filepath.Glob(filepath.Join(h.Home, ".agent-director.pre-move-*"))
	if len(kept) != 1 {
		t.Fatalf("kept directories = %v, want exactly one", kept)
	}
	rel, _ := filepath.Rel(h.Home, kept[0])
	return rel
}

// moveCheckMoved fails the case unless the home tree is what a move leaves, by
// SR-14.3's rule: the kept directory is the original; the new one holds every
// file of the original but bin/, admin/ and the store, the same; the rest of HOME
// (~/.config aside) is as it was. before is the snapshot taken before the move.
func moveCheckMoved(t *testing.T, h *moveHome, before moveTree) {
	t.Helper()
	got := h.Snapshot()
	original := moveUnder(before, moveOldRel)
	if d := moveDiff(original, moveUnder(got, moveKeptDir(t, h))); d != "" {
		t.Errorf("the kept directory is not the original:\n%s", d)
	}
	// The install's own migration appends to the trail, so the new trail only
	// starts with the original's.
	skip := []string{"bin", "admin", "state.db", "state.db-wal", "ad-trail.jsonl"}
	wantNew := moveWithout(original, skip...)
	wantNew[""] = "dir drwx------"
	if d := moveDiff(wantNew, moveWithout(moveUnder(got, moveNewRel), skip...)); d != "" {
		t.Errorf("the new directory differs from the original's files:\n%s", d)
	}
	oldTrail, _ := os.ReadFile(filepath.Join(h.Home, moveKeptDir(t, h), "ad-trail.jsonl"))
	if newTrail, err := os.ReadFile(filepath.Join(h.New, "ad-trail.jsonl")); err != nil || !strings.HasPrefix(string(newTrail), string(oldTrail)) {
		t.Errorf("the new trail does not start with the original's: %q, %v", newTrail, err)
	}
	rest := func(tr moveTree) moveTree {
		kept, _ := filepath.Glob(filepath.Join(h.Home, ".agent-director.pre-move-*"))
		names := []string{moveOldRel, ".config"}
		for _, k := range kept {
			rel, _ := filepath.Rel(h.Home, k)
			names = append(names, rel)
		}
		return moveWithout(tr, names...)
	}
	if d := moveDiff(rest(before), rest(got)); d != "" {
		t.Errorf("the move changed the rest of the home tree:\n%s", d)
	}
}

// moveUserVersion reads a store's user_version without changing it.
func moveUserVersion(t *testing.T, h *moveHome, db string) string {
	t.Helper()
	return strings.TrimSpace(h.Sqlite(db, "PRAGMA user_version;", "-readonly"))
}

// moveCurrentVersion is the schema version the pair built from this tree makes a
// fresh store at.
func moveCurrentVersion(t *testing.T, h *moveHome) string {
	t.Helper()
	ref := filepath.Join(h.Root, "reference.db")
	if res := h.Run(h.Pair.Main, "list", "--store-path", ref); res.Code != 0 {
		t.Fatalf("list --store-path: exit %d: %s", res.Code, res.Stderr)
	}
	return moveUserVersion(t, h, ref)
}

// moveListHas fails the case unless the installed binary's list names each id.
func moveListHas(t *testing.T, h *moveHome, ids ...string) {
	t.Helper()
	res := h.Run(filepath.Join(h.New, "bin", "agent-director"), "list")
	if res.Code != 0 {
		t.Errorf("list: exit %d, stderr %q", res.Code, res.Stderr)
	}
	for _, id := range ids {
		if !strings.Contains(res.Stdout, id) {
			t.Errorf("list lacks %q:\n%s", id, res.Stdout)
		}
	}
}

// moveMustInstall runs install.sh and fails the case unless it exits 0.
func moveMustInstall(t *testing.T, h *moveHome, args ...string) moveResult {
	t.Helper()
	res := h.Install(args...)
	if res.Code != 0 {
		t.Fatalf("install.sh: exit %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	return res
}

// moveRichOld builds an old install and adds the shapes SR-11.7 says the copy
// keeps: a 0700 directory, an empty 0750 one, and a relative and an absolute link.
func moveRichOld(t *testing.T, h *moveHome, opts ...moveOldOpt) {
	t.Helper()
	h.BuildOld(opts...)
	h.Write(filepath.Join(h.Old, "private", "key.txt"), "secret\n", 0o600)
	for dir, mode := range map[string]os.FileMode{"private": 0o700, "empty": 0o750} {
		if err := os.MkdirAll(filepath.Join(h.Old, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(h.Old, dir), mode); err != nil {
			t.Fatal(err)
		}
	}
	h.Link("ad-trail.jsonl", filepath.Join(h.Old, "trail-latest"))
	h.Link(filepath.Join(h.Home, "elsewhere", "note"), filepath.Join(h.Old, "note-link"))
}

func TestMoveSuccessful(t *testing.T) {
	rows := []struct {
		name  string
		args  []string
		prior bool
	}{
		{name: "plain"},
		{name: "keep-prior", args: []string{"--keep-prior"}, prior: true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			moveRichOld(t, h)
			before := h.Snapshot()
			res := moveMustInstall(t, h, row.args...)

			for _, s := range []string{
				"  move    : {HOME}/.agent-director -> {HOME}/.config/agent-director",
				"  moved   : {HOME}/.agent-director -> {HOME}/.config/agent-director (store, config.toml, templates and the rest, checked)",
				"(the old directory as it was before the move, old binaries included; delete it once the upgrade is confirmed)",
			} {
				if !strings.Contains(res.Stdout, h.expand(s)) {
					t.Errorf("stdout lacks %q:\n%s", s, res.Stdout)
				}
			}
			if !regexp.MustCompile(`(?m)^  kept    : ` + regexp.QuoteMeta(h.Home) + `/` + moveKeptRE + ` \(`).MatchString(res.Stdout) {
				t.Errorf("stdout has no kept line naming ~/.agent-director.pre-move-<timestamp>:\n%s", res.Stdout)
			}
			// The no-snapshot line prints only when --keep-prior was passed.
			noSnapshot := "--keep-prior made no snapshot on this install"
			if has := strings.Contains(res.Stdout, noSnapshot); has != row.prior {
				t.Errorf("stdout holds the no-snapshot line = %v, want %v:\n%s", has, row.prior, res.Stdout)
			}
			for _, prior := range []string{"bin/agent-director.prior", "admin/agent-director-admin.prior"} {
				if _, err := os.Lstat(filepath.Join(h.New, prior)); err == nil {
					t.Errorf("%s exists: --keep-prior made a snapshot on a moving install", prior)
				}
			}

			moveCheckMoved(t, h, before)
			if got := h.Snapshot()[".config"]; got != "dir drwx------" {
				t.Errorf("~/.config = %q, want a 0700 directory", got)
			}

			// The new store is migrated and holds the old store's rows, one of which
			// lived only in the old -wal; the kept store is not migrated.
			kept := filepath.Join(h.Home, moveKeptDir(t, h))
			newV, keptV, current := moveUserVersion(t, h, filepath.Join(h.New, "state.db")), moveUserVersion(t, h, filepath.Join(kept, "state.db")), moveCurrentVersion(t, h)
			if newV != current || keptV == current {
				t.Errorf("user_version: new store %s, kept store %s, current schema %s; want new = current != kept", newV, keptV, current)
			}
			moveListHas(t, h, "ended-1", "ended-2", "missing-1", "ended-3")
		})
	}
}

// TestMoveNoProcessCheckSaidOnce: the live check runs in pre-flight and again as
// the move's first step; when no process check can run, the note says so once
// on stdout across the two runs, not once per run.
func TestMoveNoProcessCheckSaidOnce(t *testing.T) {
	h := newMoveHome(t)
	h.Env["INSTALL_SH_TEST_PROC_CHECK"] = "none"
	h.BuildOld(withMoveNoStore())
	res := moveMustInstall(t, h)
	const note = "process check: did not run (this host has no /proc and no usable ps or lsof), so no agent-director process was looked for"
	if n := strings.Count(res.Stdout, "process check: did not run"); n != 1 {
		t.Errorf("stdout says the process check did not run %d times, want once:\n%s", n, res.Stdout)
	}
	if !strings.Contains(res.Stdout, note) {
		t.Errorf("stdout lacks %q:\n%s", note, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "moved   :") {
		t.Errorf("the move did not run, so the second check was not exercised:\n%s", res.Stdout)
	}
}

func TestMoveKeptNameTaken(t *testing.T) {
	h := newMoveHome(t)
	h.WrapTool("date", `[[ "$1" == +%Y%m%d-%H%M%S ]] && { echo 20240101-120000; exit 0; }`)
	h.BuildOld(withMoveNoStore())
	taken := filepath.Join(h.Home, ".agent-director.pre-move-20240101-120000")
	h.Write(filepath.Join(taken, "mine.txt"), "an earlier upgrade's directory\n", 0o644)
	h.Link(filepath.Join(h.Home, "nowhere"), taken+"-2")
	before := h.Snapshot()
	res := moveMustInstall(t, h)
	if want := taken + "-3 ("; !strings.Contains(res.Stdout, "  kept    : "+want) {
		t.Errorf("stdout lacks a kept line naming %s:\n%s", taken+"-3", res.Stdout)
	}
	after := h.Snapshot()
	for _, name := range []string{".agent-director.pre-move-20240101-120000", ".agent-director.pre-move-20240101-120000-2"} {
		if d := moveDiff(moveUnder(before, name), moveUnder(after, name)); d != "" {
			t.Errorf("%s changed:\n%s", name, d)
		}
	}
	if d := moveDiff(moveUnder(before, moveOldRel), moveUnder(after, ".agent-director.pre-move-20240101-120000-3")); d != "" {
		t.Errorf("the kept directory is not the original:\n%s", d)
	}
}

func TestMoveSpecialFilesRefused(t *testing.T) {
	special := func(name string, mode uint32) moveRefusal {
		return moveRefusal{
			name: name,
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				if err := syscall.Mknod(filepath.Join(h.Old, "templates", name), mode|0o600, 0); err != nil {
					t.Skipf("this sandbox cannot make a %s: %v", name, err)
				}
			},
			want: moveWant{Code: 5, Err: "ErrMoveFailed", Stderr: []string{
				"install.sh: moving {HOME}/.agent-director to {HOME}/.config/agent-director failed: cannot copy {HOME}/.agent-director/templates/" + name + ": it is not a regular file, a directory or a link (a fifo, a socket or a device, say).",
				"The partial {HOME}/.config/agent-director was removed (and so was ~/.config, when the move had created it); {HOME}/.agent-director was not changed.",
			}},
		}
	}
	runMoveRefusals(t, []moveRefusal{
		special("fifo", syscall.S_IFIFO),
		special("socket", syscall.S_IFSOCK),
		special("device", syscall.S_IFCHR),
	})
}

// movePathKeyRow is one row of the path-key table. store and log are the TOML
// values as written (quotes and a comment included); wantStore and wantLog are
// the new config.toml's, "" when the line stays as written.
type movePathKeyRow struct {
	name               string
	store, log         string
	wantStore, wantLog string
	// storeAt and logAt are where each file lives before, relative to HOME.
	storeAt, logAt string
	// links are made after the old install: link (relative to HOME) -> target.
	links map[string]string
	// crlf, bom and noEOL change the config.toml's bytes.
	crlf, bom, noEOL bool
	note             string // a line the install prints
}

func (r movePathKeyRow) config(h *moveHome, store, log string) string {
	s := "# agent-director config\n[defaults]\ninject_help_hook = true\n\n[store]\ndb_path = " + store + "\nbusy_timeout_ms = 5000\n"
	if log != "" {
		s += "\n[log]\nerror_log_path = " + log + "\n"
	}
	s = h.expand(s)
	if r.noEOL {
		s = strings.TrimSuffix(s, "\n")
	}
	if r.crlf {
		s = strings.ReplaceAll(s, "\n", "\r\n")
	}
	if r.bom {
		s = "\xef\xbb\xbf" + s
	}
	return s
}

// movedTo is the place of a file that lived at rel before the move.
func movedTo(rel string) string {
	if strings.HasPrefix(rel, moveOldRel+"/") {
		return moveNewRel + rel[len(moveOldRel):]
	}
	return rel
}

func TestMovePathKeys(t *testing.T) {
	const logText = "2024-03-01T10:00:00Z ErrExample: an old error\n"
	inOld, inNew := "{HOME}/"+moveOldRel, "{HOME}/"+moveNewRel
	abs := func(rows ...movePathKeyRow) []movePathKeyRow { return rows }
	rows := abs(
		movePathKeyRow{name: "relative-inside",
			store: `"x"`, storeAt: ".agent-director/x", log: `"logs/e.log"`, logAt: ".agent-director/logs/e.log"},
		movePathKeyRow{name: "relative-leaving-the-old-dir",
			store: `"../x"`, wantStore: `"{HOME}/x"`, storeAt: "x", log: `"../e.log"`, wantLog: `"{HOME}/e.log"`, logAt: "e.log"},
		movePathKeyRow{name: "tilde-inside",
			store: `"~/.agent-director/x"`, wantStore: `"~/.config/agent-director/x"`, storeAt: ".agent-director/x",
			log: `"~/.agent-director/logs/e.log"`, wantLog: `"~/.config/agent-director/logs/e.log"`, logAt: ".agent-director/logs/e.log"},
		movePathKeyRow{name: "tilde-outside",
			store: `"~/data/x"`, storeAt: "data/x", log: `"~/data/e.log"`, logAt: "data/e.log"},
		movePathKeyRow{name: "absolute-inside",
			store: `"` + inOld + `/x"`, wantStore: `"` + inNew + `/x"`, storeAt: ".agent-director/x",
			log: `"` + inOld + `/logs/e.log"`, wantLog: `"` + inNew + `/logs/e.log"`, logAt: ".agent-director/logs/e.log"},
		movePathKeyRow{name: "absolute-outside",
			store: `"{HOME}/data/x"`, storeAt: "data/x", log: `"{HOME}/data/e.log"`, logAt: "data/e.log"},
		movePathKeyRow{name: "reaches-the-old-dir-only-through-a-link",
			store: `"{HOME}/via/x"`, wantStore: `"` + inNew + `/x"`, storeAt: ".agent-director/x",
			log: `"{HOME}/via/logs/e.log"`, wantLog: `"` + inNew + `/logs/e.log"`, logAt: ".agent-director/logs/e.log",
			links: map[string]string{"via": "{HOME}/.agent-director"}},
		// A relative value that leaves the old directory through a link inside
		// it is written as the absolute physical path it reaches today (the
		// copied link would point one level deeper), with no warning.
		movePathKeyRow{name: "relative-value-leaves-through-a-link-inside",
			store: `"out/x"`, wantStore: `"{HOME}/elsewhere/x"`, storeAt: "elsewhere/x", log: `"out/e.log"`, wantLog: `"{HOME}/elsewhere/e.log"`, logAt: "elsewhere/e.log",
			links: map[string]string{".agent-director/out": "{HOME}/elsewhere"}},
		movePathKeyRow{name: "relative-value-leaves-through-a-link-with-a-relative-target",
			store: `"out/x"`, wantStore: `"{HOME}/elsewhere/x"`, storeAt: "elsewhere/x", log: `"out/e.log"`, wantLog: `"{HOME}/elsewhere/e.log"`, logAt: "elsewhere/e.log",
			links: map[string]string{".agent-director/out": "../elsewhere"}},
		movePathKeyRow{name: "relative-value-through-a-link-keeps-quotes-comment-bom-and-crlf",
			store: `'out/x' # my store`, wantStore: `'{HOME}/elsewhere/x' # my store`, storeAt: "elsewhere/x",
			log: `"out/e.log"`, wantLog: `"{HOME}/elsewhere/e.log"`, logAt: "elsewhere/e.log",
			links: map[string]string{".agent-director/out": "../elsewhere"}, bom: true, crlf: true},
		movePathKeyRow{name: "single-quotes-and-a-comment-kept",
			store: `'` + inOld + `/x' # my store`, wantStore: `'` + inNew + `/x' # my store`, storeAt: ".agent-director/x"},
		movePathKeyRow{name: "bom-crlf-and-no-final-newline-kept",
			store: `"` + inOld + `/x"`, wantStore: `"` + inNew + `/x"`, storeAt: ".agent-director/x",
			log: `"` + inOld + `/e.log"`, wantLog: `"` + inNew + `/e.log"`, logAt: ".agent-director/e.log", bom: true, crlf: true, noEOL: true},
	)
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			cfg := row.config(h, row.store, row.log)
			h.BuildOld(withMoveConfig(func(*moveHome) string { return cfg }),
				withMoveStore(func(h *moveHome) string { return filepath.Join(h.Home, row.storeAt) }))
			for link, target := range row.links {
				h.Link(h.expand(target), filepath.Join(h.Home, link))
			}
			if row.log != "" {
				h.Write(filepath.Join(h.Home, row.logAt), logText, 0o640)
			}
			res := moveMustInstall(t, h)

			want := row.config(h, orElse(row.wantStore, row.store), orElse(row.wantLog, row.log))
			got, err := os.ReadFile(filepath.Join(h.New, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want {
				t.Errorf("new config.toml:\n%q\nwant:\n%q", got, want)
			}
			if row.note != "" && !strings.Contains(res.Stdout, h.expand(row.note)) {
				t.Errorf("stdout lacks %q:\n%s", row.note, res.Stdout)
			}
			if row.note == "" && strings.Contains(res.Stdout, "warning:") {
				t.Errorf("stdout warns:\n%s", res.Stdout)
			}

			// The installed binary opens the store at the key's place, and no default
			// store was made beside it.
			if _, err := os.Lstat(filepath.Join(h.New, "state.db")); err == nil {
				t.Errorf("the new directory holds a default state.db: the installed binary opened another store")
			}
			if _, err := os.Stat(filepath.Join(h.Home, movedTo(row.storeAt))); err != nil {
				t.Errorf("the store is not at %s: %v", movedTo(row.storeAt), err)
			}
			moveListHas(t, h, "ended-1", "ended-3")
			if row.log != "" {
				if b, err := os.ReadFile(filepath.Join(h.Home, movedTo(row.logAt))); err != nil || string(b) != logText {
					t.Errorf("the error log at %s = %q, %v; want its old text", movedTo(row.logAt), b, err)
				}
			}
		})
	}
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

func TestMovePathKeyUnresolvableLog(t *testing.T) {
	runMoveRefusals(t, []moveRefusal{{
		name: "error-log-in-a-missing-directory",
		setup: moveOldWith(nil, withMoveConfig(func(h *moveHome) string {
			return moveConfig("", 5000) + fmt.Sprintf("\n[log]\nerror_log_path = %q\n", filepath.Join(h.Home, "nowhere", "e.log"))
		})),
		want: moveWant{Code: 5, Err: "ErrMoveConflict", Stderr: []string{
			"cannot place [log] error_log_path {HOME}/nowhere/e.log against the old directory; refusing to install.",
			"mkdir -p -- {HOME}/nowhere", "Nothing was installed or changed."}},
	}})
}

// TestMovePathKeyOutwardRefused: an absolute or ~/ path key that is inside the
// old directory by its text but reaches outside it through a link inside it
// cannot be left as written (the rename would leave it naming nothing), so
// pre-flight refuses it with ErrMoveConflict, names the key, its value as
// written and the physical place it reaches, and gives the key's remedy; nothing
// is changed (runMoveRefusals compares the tree).
func TestMovePathKeyOutwardRefused(t *testing.T) {
	const cite = "operator-actions.md, \"`install.sh` refuses with `ErrMoveConflict`\""
	// outward builds an old install whose [store] db_path / [log] error_log_path
	// is value (read by the config), with the link .agent-director/out leading to target.
	outward := func(table, value, target string) func(t *testing.T, h *moveHome) {
		return func(t *testing.T, h *moveHome) {
			cfg := h.expand(moveConfig("", 5000))
			opts := []moveOldOpt{}
			if table == "store" {
				cfg = h.expand("# agent-director config\n[defaults]\ninject_help_hook = true\n\n[store]\ndb_path = \"" + value + "\"\nbusy_timeout_ms = 5000\n")
				opts = append(opts, withMoveStore(func(h *moveHome) string { return filepath.Join(h.Home, "elsewhere", "x") }))
			} else {
				cfg += h.expand("\n[log]\nerror_log_path = \"" + value + "\"\n")
			}
			opts = append(opts, withMoveConfig(func(*moveHome) string { return cfg }))
			h.BuildOld(opts...)
			h.Write(filepath.Join(h.Home, "elsewhere", "e.log"), "an old error\n", 0o640)
			h.Link(h.expand(target), filepath.Join(h.Old, "out"))
		}
	}
	refused := func(key, value, remedy string) moveWant {
		table := "store"
		if key == "error_log_path" {
			table = "log"
		}
		return moveWant{Code: 5, Err: "ErrMoveConflict", Stderr: []string{
			"install.sh: [" + table + "] " + key + " " + value + " reaches outside {HOME}/.agent-director through a link inside it; refusing to install.",
			"  " + key + ` = "` + remedy + `"`,
			"Nothing was installed or changed. Re-run this install after the change.",
			cite,
		}, NoStdout: []string{"move    :"}}
	}
	runMoveRefusals(t, []moveRefusal{
		{
			name:  "absolute-db-path-through-an-inner-link",
			setup: outward("store", "{HOME}/.agent-director/out/x", "{HOME}/elsewhere"),
			want:  refused("db_path", "{HOME}/.agent-director/out/x", "{HOME}/elsewhere/x"),
		},
		{
			name:  "tilde-db-path-through-an-inner-link",
			setup: outward("store", "~/.agent-director/out/x", "{HOME}/elsewhere"),
			want:  refused("db_path", "~/.agent-director/out/x", "{HOME}/elsewhere/x"),
		},
		{
			name:  "absolute-db-path-through-a-link-with-a-relative-target",
			setup: outward("store", "{HOME}/.agent-director/out/x", "../elsewhere"),
			want:  refused("db_path", "{HOME}/.agent-director/out/x", "{HOME}/elsewhere/x"),
		},
		{
			name:  "absolute-error-log-through-an-inner-link",
			setup: outward("log", "{HOME}/.agent-director/out/e.log", "{HOME}/elsewhere"),
			want:  refused("error_log_path", "{HOME}/.agent-director/out/e.log", "{HOME}/elsewhere/e.log"),
		},
		{
			name:  "tilde-error-log-through-an-inner-link",
			setup: outward("log", "~/.agent-director/out/e.log", "{HOME}/elsewhere"),
			want:  refused("error_log_path", "~/.agent-director/out/e.log", "{HOME}/elsewhere/e.log"),
		},
	})
}

func TestMoveOtherReferencesPrinted(t *testing.T) {
	h := newMoveHome(t)
	cfg := moveOldConfig + "\n[launch]\nclaude_path = \"{HOME}/.agent-director/bin/claude\"\n"
	h.BuildOld(withMoveConfig(func(h *moveHome) string { return h.expand(cfg) }))
	tmpl := "name = \"tooling\"\ncwd = \"{HOME}/.agent-director/work\"\n\n[extra_env]\nTOOLS = \"~/.agent-director/tools\"\nPLAIN = \"kept\"\n"
	h.Write(filepath.Join(h.Old, "templates", "tooling.toml"), h.expand(tmpl), 0o644)
	res := moveMustInstall(t, h)
	for _, s := range []string{
		"  references: these lines still name the old directory. They were left as written;",
		"    {HOME}/.config/agent-director/config.toml:9:claude_path = \"{HOME}/.agent-director/bin/claude\"",
		"    {HOME}/.config/agent-director/templates/tooling.toml:2:cwd = \"{HOME}/.agent-director/work\"",
		"    {HOME}/.config/agent-director/templates/tooling.toml:5:TOOLS = \"~/.agent-director/tools\"",
	} {
		if !strings.Contains(res.Stdout, h.expand(s)) {
			t.Errorf("stdout lacks %q:\n%s", s, res.Stdout)
		}
	}
	if strings.Contains(res.Stdout, "PLAIN") {
		t.Errorf("stdout names a line that does not reach the old directory:\n%s", res.Stdout)
	}
	// Left as written: the new files are the old ones, byte for byte.
	for _, f := range []string{"config.toml", "templates/tooling.toml", "templates/worker.toml"} {
		a, errA := os.ReadFile(filepath.Join(h.New, f))
		b, errB := os.ReadFile(filepath.Join(h.Home, moveKeptDir(t, h), f))
		if errA != nil || errB != nil || string(a) != string(b) {
			t.Errorf("%s changed in the move: %v %v", f, errA, errB)
		}
	}
}

// moveBlockCopy puts a stand-in cp first on PATH that touches the returned flag
// file and then sleeps, when its destination lies under the new directory.
func moveBlockCopy(h *moveHome) string {
	flag := filepath.Join(h.Root, "copying")
	h.WrapTool("cp", `if [[ "${!#}" == `+moveQuote(h.New)+`/* ]]; then : > `+moveQuote(flag)+`; sleep 60; fi`)
	return flag
}

// moveAwait waits for path to exist.
func moveAwait(t *testing.T, path string) {
	t.Helper()
	for deadline := time.Now().Add(moveWait); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	t.Fatalf("%s did not appear within %v", path, moveWait)
}

func TestMoveInterrupted(t *testing.T) {
	rows := []struct {
		name   string
		sig    syscall.Signal
		signal string
		setup  func(t *testing.T, h *moveHome) // before the snapshot
	}{
		{name: "int", sig: syscall.SIGINT, signal: "SIGINT"},
		{name: "term", sig: syscall.SIGTERM, signal: "SIGTERM"},
		{name: "hup", sig: syscall.SIGHUP, signal: "SIGHUP"},
		{name: "config-dir-holding-other-files-is-kept", sig: syscall.SIGTERM, signal: "SIGTERM",
			setup: func(t *testing.T, h *moveHome) {
				h.Write(filepath.Join(h.Home, ".config", "other", "keep.txt"), "mine\n", 0o644)
			}},
		{name: "empty-config-dir-that-was-there-is-kept", sig: syscall.SIGINT, signal: "SIGINT",
			setup: func(t *testing.T, h *moveHome) {
				if err := os.MkdirAll(filepath.Join(h.Home, ".config"), 0o755); err != nil {
					t.Fatal(err)
				}
			}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			h.BuildOld()
			if row.setup != nil {
				row.setup(t, h)
			}
			flag := moveBlockCopy(h)
			before := h.Snapshot()
			run := h.StartInstall()
			moveAwait(t, flag)
			if _, err := os.Lstat(filepath.Join(h.Home, moveMarkerRel)); err != nil {
				t.Errorf("no marker while the copy runs: %v", err)
			}
			run.Signal(row.sig)
			res := run.Wait(t)
			h.Release()
			moveWant{Code: 5, Err: "ErrMoveFailed", Stderr: []string{
				"install.sh: moving {HOME}/.agent-director to {HOME}/.config/agent-director failed: the install was interrupted (" + row.signal + ")",
				"The partial {HOME}/.config/agent-director was removed (and so was ~/.config, when the move had created it); {HOME}/.agent-director was not changed.",
				"A full disk is the usual cause",
			}}.Check(t, h, res)
			if d := moveDiff(before, h.Snapshot()); d != "" {
				t.Errorf("the interrupted install left changes:\n%s", d)
			}
		})
	}
}

// TestMoveStoppedAfterTheRename: a signal that arrives after the rename has
// finished and before the move's traps are put back must remove nothing: the
// new directory is the complete copy and the kept directory the original. The
// stand-in mv does the real rename of the old directory and then signals the
// install (its parent), so the trap runs as soon as mv returns, every time.
// The message says the move finished, and a re-run continues with no move due.
func TestMoveStoppedAfterTheRename(t *testing.T) {
	for _, sig := range []string{"TERM", "INT", "HUP"} {
		t.Run(sig, func(t *testing.T) {
			h := newMoveHome(t)
			moveRichOld(t, h)
			h.WriteTool("mv", `if [[ "$1" == -- && "$2" == `+moveQuote(h.Old)+` ]]; then
`+moveQuote(h.RealTool("mv"))+` "$@"
rc=$?
kill -`+sig+` "$PPID"
exit "$rc"
fi
exec `+moveQuote(h.RealTool("mv"))+` "$@"`)
			before := h.Snapshot()
			res := h.Install()
			moveWant{Code: 5, Err: "ErrMoveFailed",
				Stderr: []string{
					"install.sh: the install was stopped after the move had renamed {HOME}/.agent-director: the install was interrupted (SIG" + sig + ")",
					"Nothing was removed.",
					"Re-run this install: it continues from {HOME}/.config/agent-director, and no move is due.",
					"operator-actions.md, \"`install.sh` stops with `ErrMoveFailed`\"",
				},
				NoStderr: []string{"The partial", "was removed (and so was", "A full disk is the usual cause"}}.Check(t, h, res)

			// Nothing was removed: the kept directory is the original and the new one is the copy.
			got := h.Snapshot()
			if d := moveDiff(moveUnder(before, moveOldRel), moveUnder(got, moveKeptDir(t, h))); d != "" {
				t.Errorf("the kept directory is not the original:\n%s", d)
			}
			if _, err := os.Lstat(h.Old); err == nil {
				t.Errorf("%s is still there after the rename", h.Old)
			}
			// The marker is still there (the re-run removes it), now naming the kept directory.
			skip := []string{"bin", "admin", "state.db", "state.db-wal", "ad-trail.jsonl", ".move-incomplete"}
			wantNew := moveWithout(moveUnder(before, moveOldRel), skip...)
			wantNew[""] = "dir drwx------"
			if d := moveDiff(wantNew, moveWithout(moveUnder(got, moveNewRel), skip...)); d != "" {
				t.Errorf("the new directory is not the complete copy:\n%s", d)
			}
			if b, err := os.ReadFile(filepath.Join(h.Home, moveMarkerRel)); err != nil || strings.TrimSpace(string(b)) != filepath.Join(h.Home, moveKeptDir(t, h)) {
				t.Errorf("the marker = %q, %v; want the kept directory's path", b, err)
			}

			// The re-run (the real mv back in place) continues from the new directory.
			h.WriteTool("mv", `exec `+moveQuote(h.RealTool("mv"))+` "$@"`)
			res = moveMustInstall(t, h)
			for _, s := range []string{"move    :", "moved   :", "kept    :"} {
				if strings.Contains(res.Stdout, s) {
					t.Errorf("the re-run prints %q:\n%s", s, res.Stdout)
				}
			}
			if _, err := os.Lstat(filepath.Join(h.Home, moveMarkerRel)); err == nil {
				t.Errorf("the re-run left the marker")
			}
			moveCheckMoved(t, h, before)
			moveListHas(t, h, "ended-1", "ended-2", "missing-1", "ended-3")
		})
	}
}

func TestMoveCopyFailsLikeAFullDisk(t *testing.T) {
	failed := func(cause string, more ...string) moveWant {
		return moveWant{Code: 5, Err: "ErrMoveFailed", Stderr: append([]string{
			"install.sh: moving {HOME}/.agent-director to {HOME}/.config/agent-director failed: " + cause,
			"A full disk is the usual cause: free some space (or fix the cause above), then re-run this install.",
			"operator-actions.md, \"`install.sh` stops with `ErrMoveFailed`\""}, more...)}
	}
	noSpace := "echo 'cp: No space left on device' >&2; exit 1"
	rows := []moveRefusal{
		{
			name: "copy-of-a-file",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				h.WriteTool("cp", `if [[ "${!#}" == `+moveQuote(h.New)+`/* ]]; then `+noSpace+`; fi
exec `+moveQuote(h.RealTool("cp"))+` "$@"`)
			},
			want: failed("cannot copy ", "The partial {HOME}/.config/agent-director was removed (and so was ~/.config, when the move had created it); {HOME}/.agent-director was not changed.",
				"cp: No space left on device"),
		},
		{
			name: "copy-of-a-binary-to-a-temp-file",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				h.WriteTool("cp", noSpace)
			},
			want: failed("cannot copy ", "Nothing was changed: {HOME}/.agent-director is as it was.", "cp: No space left on device"),
		},
		{
			name: "sqlite-backup-of-the-store",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				h.WrapTool("sqlite3", `for a in "$@"; do if [[ "$a" == .backup* ]]; then echo 'Error: database or disk is full' >&2; exit 1; fi; done`)
			},
			want: failed("SQLite's backup of the store {HOME}/.agent-director/state.db failed: ", "The partial {HOME}/.config/agent-director was removed", "database or disk is full"),
		},
	}
	runMoveRefusals(t, rows)
}

// TestMoveBackupVerificationFails: the store's copy is checked after SQLite's
// backup, and a copy that does not show the original's user_version or schema
// hash ends the install with ErrMoveFailed, the old directory untouched and no
// new one. A stand-in sqlite3 answers the check's reads of the copy (a path
// under the new directory) wrongly and passes every other call to the real one.
func TestMoveBackupVerificationFails(t *testing.T) {
	standIn := func(h *moveHome, sql, answer string) {
		h.WrapTool("sqlite3", `if [[ "${@: -1}" == `+moveQuote(sql)+` && "${@: -2:1}" == `+moveQuote(h.New)+`/* ]]; then echo `+moveQuote(answer)+`; exit 0; fi`)
	}
	failed := func(cause string) moveWant {
		return moveWant{Code: 5, Err: "ErrMoveFailed", Stderr: []string{
			"install.sh: moving {HOME}/.agent-director to {HOME}/.config/agent-director failed: " + cause,
			"The partial {HOME}/.config/agent-director was removed (and so was ~/.config, when the move had created it); {HOME}/.agent-director was not changed.",
		}}
	}
	runMoveRefusals(t, []moveRefusal{
		{
			name: "copy-has-another-user-version",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				standIn(h, "PRAGMA user_version;", "99")
			},
			want: failed("the copy {HOME}/.config/agent-director/state.db of the store has user_version '99', the original's is '"),
		},
		{
			name: "copy-has-another-schema-hash",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				standIn(h, ".sha3sum --schema", strings.Repeat("ab", 32))
			},
			want: failed("the schema of the copy {HOME}/.config/agent-director/state.db of the store could not be shown to match the original's (.sha3sum --schema gave '" + strings.Repeat("ab", 32) + "' for the copy"),
		},
		{
			name: "copy-has-no-schema-hash",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				standIn(h, ".sha3sum --schema", "Error: file is not a database")
			},
			want: failed("the schema of the copy {HOME}/.config/agent-director/state.db of the store could not be shown to match the original's"),
		},
	})
}

// moveMarkerStale writes the marker a move leaves: content is what it holds.
func moveMarkerStale(h *moveHome, content string) {
	h.Write(filepath.Join(h.Home, moveMarkerRel), content, 0o600)
}

func TestMoveMarkerRecovery(t *testing.T) {
	for name, content := range map[string]string{
		"empty-marker-with-both-directories-moves-again":                     "",
		"marker-naming-an-absent-kept-dir-with-both-directories-moves-again": "{HOME}/.agent-director.pre-move-19990101-000000\n",
	} {
		t.Run(name, func(t *testing.T) {
			h := newMoveHome(t)
			moveRichOld(t, h)
			if err := os.MkdirAll(h.New, 0o700); err != nil {
				t.Fatal(err)
			}
			h.Write(filepath.Join(h.New, "partial.txt"), "a half-made copy\n", 0o600)
			moveMarkerStale(h, h.expand(content))
			before := h.Snapshot()
			moveMustInstall(t, h)
			moveCheckMoved(t, h, before)
			if _, err := os.Lstat(filepath.Join(h.New, "partial.txt")); err == nil {
				t.Errorf("the partial copy was kept")
			}
		})
	}

	t.Run("marker-naming-a-kept-dir-while-the-old-dir-is-back", func(t *testing.T) {
		h := newMoveHome(t)
		h.BuildOld(withMoveNoStore())
		moveMustInstall(t, h)
		kept := filepath.Join(h.Home, moveKeptDir(t, h))
		h.Write(filepath.Join(h.Old, "config.toml"), moveOldConfig, 0o600)
		moveMarkerStale(h, kept+"\n")
		before := h.Snapshot()
		res := h.Install()
		moveWant{Code: 5, Err: "ErrMoveConflict", Stderr: []string{
			"both {HOME}/.agent-director and {HOME}/.config/agent-director exist; refusing to install.", "Nothing was installed or changed."}}.Check(t, h, res)
		// Only the marker went.
		if d := moveDiff(moveWithout(before, moveMarkerRel), h.Snapshot()); d != "" {
			t.Errorf("the refused install changed more than the marker:\n%s", d)
		}
	})

	t.Run("marker-with-no-old-directory", func(t *testing.T) {
		h := newMoveHome(t)
		h.BuildOld(withMoveNoStore())
		moveMustInstall(t, h)
		kept := filepath.Join(h.Home, moveKeptDir(t, h))
		if err := os.RemoveAll(kept); err != nil {
			t.Fatal(err)
		}
		moveMarkerStale(h, kept+"\n")
		res := moveMustInstall(t, h)
		if strings.Contains(res.Stdout, "move    :") {
			t.Errorf("a move ran:\n%s", res.Stdout)
		}
		if _, err := os.Lstat(filepath.Join(h.Home, moveMarkerRel)); err == nil {
			t.Errorf("the marker was left")
		}
		if left, _ := filepath.Glob(filepath.Join(h.Home, ".agent-director*")); len(left) > 0 {
			t.Errorf("the install made a kept or old directory: %v", left)
		}
	})
}

func TestMoveRerunChangesNothing(t *testing.T) {
	h := newMoveHome(t)
	moveRichOld(t, h)
	moveMustInstall(t, h)
	before := h.Snapshot()
	res := moveMustInstall(t, h)
	for _, s := range []string{"move    :", "moved   :", "kept    :", "references:"} {
		if strings.Contains(res.Stdout, s) {
			t.Errorf("a re-run prints %q:\n%s", s, res.Stdout)
		}
	}
	if d := moveDiff(before, h.Snapshot()); d != "" {
		t.Errorf("a re-run changed the home tree:\n%s", d)
	}
}
