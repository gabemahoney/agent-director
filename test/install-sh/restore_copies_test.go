package installsh_test

// The restore's copies (SR-2.16, Subtask t3.7tz.ab.hh.as): on a crossing
// upgrade whose previous release predates the cutover, pre-flight copies each
// item a restore puts back (the installed pair, the sentinel beside the
// store, the managed PATH link) beside it as
// <name>.restore-<YYYYmmdd-HHMMSS>.<suffix>, never in TMPDIR and, with the
// move due, never inside AD's directories: a managed link outside them is
// copied at its directory's physical place, and a --symlink-dir inside them
// gets no link and no copy. A file's copy is a file; a link's is a 0700
// directory, made by mktemp -d under the pattern, holding a link named as the
// item with the same raw target. Nothing already at a name is written
// through or removed. Success and the non-cutover stops tested here remove
// their own copies, never another run's; outcome tests cover retained copies.
// A copy that
// cannot be made for want of room, access or a regular file is J37's
// (crossing_preflight_test.go).

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// restoreCopyPattern matches a restore copy's name; its group is the run's
// timestamp.
var restoreCopyPattern = regexp.MustCompile(`\.restore-(\d{8}-\d{6})\.[A-Za-z0-9]{6}$`)

// restoreCopyNames lists every path under h's HOME whose name has the
// copies' pattern, relative to HOME, sorted. Tests that enumerate
// restore-copy names under the fixture HOME after a run must use
// restoreCopyNames; inspect other copy locations and in-run state
// explicitly.
func restoreCopyNames(t *testing.T, h *moveHome) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(h.Home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if restoreCopyPattern.MatchString(d.Name()) {
			rel, _ := filepath.Rel(h.Home, p)
			names = append(names, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// restoreDescribe is what a copy of p keeps: a link's raw target, or a file's
// mode and sha256, in recordCopiesAtSwap's form; for a directory, its mode
// and how many entries it holds.
func restoreDescribe(t *testing.T, p string) string {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		entries, err := os.ReadDir(p)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("dir\t%o\t%d", fi.Mode().Perm(), len(entries))
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(p)
		if err != nil {
			t.Fatal(err)
		}
		return "link\t" + target
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%o\t%x", fi.Mode().Perm(), sha256.Sum256(b))
}

// recordCopiesAtSwap makes cp, when the swap copies the new agent-director
// into place, record every copy under h.Root (HOME, TMPDIR and any directory
// a case makes beside them), one "<path>\t<restoreDescribe>" line each, and
// for a copy that is a directory each entry in it too, in the returned file;
// it leaves every other cp alone, the copies' own included.
func recordCopiesAtSwap(h *moveHome) string {
	rec := filepath.Join(h.Root, "copies-at-swap")
	h.WrapTool("cp", `if [[ "${!#}" == */bin/agent-director.tmp.* ]]; then
    describe() {
        if [[ -L "$1" ]]; then
            printf 'link\t%s' "$(readlink "$1")"
        elif [[ -d "$1" ]]; then
            printf 'dir\t%s\t%s' "$(stat -c %a "$1")" "$(($(find "$1" -mindepth 1 -maxdepth 1 | wc -l)))"
        else
            printf '%s\t%s' "$(stat -c %a "$1")" "$(sha256sum <"$1" | cut -d' ' -f1)"
        fi
    }
    : >>`+moveQuote(rec)+`
    find `+moveQuote(h.Root)+` -name '*.restore-*' -print0 2>/dev/null | while IFS= read -r -d '' p; do
        printf '%s\t%s\n' "$p" "$(describe "$p")"
        if [[ -d "$p" && ! -L "$p" ]]; then
            find "$p" -mindepth 1 -maxdepth 1 -print0 | while IFS= read -r -d '' e; do
                printf '%s\t%s\n' "$e" "$(describe "$e")"
            done
        fi
    done >>`+moveQuote(rec)+`
fi`)
	return rec
}

// readCopiesAtSwap reads recordCopiesAtSwap's file, failing when the swap
// never ran.
func readCopiesAtSwap(t *testing.T, rec string) map[string]string {
	t.Helper()
	f, err := os.Open(rec)
	if err != nil {
		t.Fatalf("the swap's cp never ran: %v", err)
	}
	defer f.Close()
	at := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		path, desc, _ := strings.Cut(sc.Text(), "\t")
		at[path] = desc
	}
	return at
}

// newCopiesMoveHome is an old layout from v0.11.1 with the move due, its
// store outside the old directory (reached through the link
// ~/.agent-director/out, so it is physically ~/elsewhere/x) and a sentinel
// an earlier run left beside it. linkDir sets up the --symlink-dir and
// returns it with the physical path the managed link's copy belongs at, ""
// when the swap makes no link. It returns the sentinel's physical path and
// that path.
func newCopiesMoveHome(t *testing.T, linkDir func(h *moveHome) (string, string)) (*moveHome, string, string) {
	t.Helper()
	cfg := "# agent-director config\n[defaults]\ninject_help_hook = true\n\n[store]\ndb_path = \"{HOME}/.agent-director/out/x\"\nbusy_timeout_ms = 5000\n"
	h := newGenuineOldHome(t, withMoveConfig(func(h *moveHome) string { return h.expand(cfg) }),
		withMoveStore(func(h *moveHome) string { return filepath.Join(h.Home, "elsewhere", "x") }))
	h.Link(filepath.Join(h.Home, "elsewhere"), filepath.Join(h.Old, "out"))
	sentinel := filepath.Join(h.Home, "elsewhere", "migrate-authorized")
	h.Write(sentinel, "{\"from\": 4, \"to\": 5}\n", 0o600)
	dir, link := linkDir(h)
	linkDirAt(h, dir)
	return h, sentinel, link
}

// linksInHome is newCopiesMoveHome's usual link directory, ~/links, holding
// the managed link into the old bin/.
func linksInHome(h *moveHome) (string, string) {
	dir := withLinkDir(h)
	link := filepath.Join(dir, "agent-director")
	h.Link(filepath.Join(h.Old, "bin", "agent-director"), link)
	return dir, link
}

// skippedLinkDir is a --symlink-dir inside the old directory (dir, spelt
// through the old root or an alias of it), for which the swap makes no link.
func skippedLinkDir(dir func(h *moveHome) string) func(h *moveHome) (string, string) {
	return func(h *moveHome) (string, string) { return dir(h), "" }
}

// plantAtLinkCopy makes mktemp plant an entry, by plant, at the name the
// managed link's copy (a mktemp -u name beside link) gets, and list it in
// the returned file. The link's copy is a directory mktemp -d makes for that
// template, so the stand-in plants at a name the real mktemp -u gives for it
// and then lets the real mktemp -d choose.
func plantAtLinkCopy(h *moveHome, link, plant string) string {
	list := filepath.Join(h.Root, "planted")
	real := moveQuote(h.RealTool("mktemp"))
	h.WriteTool("mktemp", `if [[ "$1" == -d && "${!#}" == `+moveQuote(link)+`.restore-*.XXXXXX ]]; then
    name="$(`+real+` -u "${!#}")" || exit
    `+plant+` "$name" || exit
    echo "$name" >>`+moveQuote(list)+`
fi
exec `+real+` "$@"`)
	return list
}

// copiesCase is one run of TestRestoreCopies.
type copiesCase struct {
	h     *moveHome
	flags []string
	// items are the paths the run copies, one copy each, beside them; none
	// means it makes no copy.
	items []string
	// inside are path prefixes no copy may lie under at the swap, besides
	// TMPDIR.
	inside []string
	// kept are copies an earlier run left; stdout holds each of stdout.
	kept, stdout []string
	// planted is the file listing entries planted at a copy's name, each
	// of which restoreDescribe must find as plantedWant after the run.
	planted, plantedWant string
	// fail is how the run ends; nil means it passes.
	fail *moveWant
	// after makes the row's own checks once the run has ended; with
	// ownLeft, they include what copies are left under HOME.
	after   func(t *testing.T, res moveResult)
	ownLeft bool
}

// linkCopyPlanted is a row whose planted list names an entry planted at a
// name the managed link's copy could get (restoreDescribe finds it as want):
// mktemp -d never takes an existing name, so the run makes its copy at
// another and passes, leaving the entry alone; check, if any, looks at what
// the entry leads to.
func linkCopyPlanted(h *moveHome, flags []string, p copyPaths, planted, want string, check func(t *testing.T)) copiesCase {
	return copiesCase{h: h, flags: flags, items: []string{p.bin, p.admin, p.sentinel, p.link}, planted: planted, plantedWant: want,
		after: func(t *testing.T, res moveResult) {
			if check != nil {
				check(t)
			}
		}}
}

// lateDirAtLinkCopy makes ln, just before it makes the managed link's copy
// inside the run's mktemp -d directory, create a real directory at that
// link's name, and list it in the returned file.
func lateDirAtLinkCopy(h *moveHome, link string) string {
	list := filepath.Join(h.Root, "planted")
	h.WrapTool("ln", `if [[ "${!#}" == `+moveQuote(link)+`.restore-*/`+moveQuote(filepath.Base(link))+` ]]; then
    mkdir -m 0755 -- "${!#}" || exit
    echo "${!#}" >>`+moveQuote(list)+`
fi`)
	return list
}

// assertNoCopiesIn fails unless dir, which restoreCopyNames does not reach,
// holds no restore copy.
func assertNoCopiesIn(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if restoreCopyPattern.MatchString(e.Name()) {
			t.Errorf("a copy is left in %s: %s", dir, e.Name())
		}
	}
}

// assertKeptUnlinked fails unless the move kept the old directory whole
// with its own agent-director, and the swap made no link: none at the old
// path, and this release's agent-director a regular file.
func assertKeptUnlinked(t *testing.T, h *moveHome) {
	t.Helper()
	if _, err := os.Lstat(h.Old); err == nil {
		t.Errorf("%s exists after the move", h.Old)
	}
	kept, _ := filepath.Glob(h.Old + ".pre-move-*")
	if len(kept) != 1 {
		t.Fatalf("kept directories = %v; want one", kept)
	}
	for _, p := range []string{filepath.Join(kept[0], "bin", "agent-director"), filepath.Join(h.New, "bin", "agent-director")} {
		if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("%s is not a regular file (%v): the swap linked it", p, err)
		}
	}
}

// TestRestoreCopies: each run makes the copies the rules say, sees them
// beside their items at the swap byte for byte, and leaves none of its own
// when it ends, whether it passes or fails, while another run's copies and
// an entry planted at a copy's name stay as they were: a link at the
// sentinel copy's name is not written through, a directory or a link to one
// at a name the managed link's copy could get is passed over, and a
// directory made at the link's name inside the run's own copy directory just
// before ln refuses, writing nothing outside that copy directory, which stays
// and is named as not removed.
func TestRestoreCopies(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and installs v0.17.3 and v0.11.1")
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) copiesCase
	}{
		{"in place, with an earlier run's copies", func(t *testing.T) copiesCase {
			c, p := newCopiesCrossing(t)
			kept := []string{p.bin + ".restore-20240101-000000.abcdef", p.link + ".restore-20240101-000000.ghijkl"}
			c.h.Write(kept[0], "an earlier run's copy\n", 0o700)
			c.h.Link(filepath.Join(c.h.Root, "earlier-target"), kept[1])
			return copiesCase{h: c.h, flags: c.valid(), items: []string{p.bin, p.admin, p.sentinel, p.link}, kept: kept}
		}},
		{"move due, store outside the old directory", func(t *testing.T) copiesCase {
			h, sentinel, link := newCopiesMoveHome(t, linksInHome)
			return copiesCase{h: h, items: []string{sentinel, link}, inside: []string{h.Old, h.New + "/"}}
		}},
		{"move due, link directory outside HOME through an alias", func(t *testing.T) copiesCase {
			var real, link string
			h, sentinel, _ := newCopiesMoveHome(t, func(h *moveHome) (string, string) {
				real = filepath.Join(h.Root, "links-out")
				if err := os.MkdirAll(real, 0o755); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(real, "agent-director")
				h.Link(filepath.Join(h.Old, "bin", "agent-director"), link)
				alias := filepath.Join(h.Home, "links-alias")
				h.Link(real, alias)
				return alias, link
			})
			return copiesCase{h: h, items: []string{sentinel, link}, inside: []string{h.Old, h.New + "/"},
				after: func(t *testing.T, res moveResult) {
					assertNoCopiesIn(t, real)
					if got, err := os.Readlink(link); err != nil || got != filepath.Join(h.New, "bin", "agent-director") {
						t.Errorf("the managed link %s names %q (%v); want this release's agent-director", link, got, err)
					}
				}}
		}},
		{"move due, link directory is the old bin", func(t *testing.T) copiesCase {
			h, sentinel, _ := newCopiesMoveHome(t, skippedLinkDir(func(h *moveHome) string { return filepath.Join(h.Old, "bin") }))
			return copiesCase{h: h, items: []string{sentinel}, inside: []string{h.Old, h.New + "/"},
				stdout: []string{"  symlink : skipped — " + filepath.Join(h.Old, "bin") + " is inside AD's old directory " + h.Old + ", which the move renames or replaces\n"},
				after:  func(t *testing.T, res moveResult) { assertKeptUnlinked(t, h) }}
		}},
		{"move due, link directory through an alias of the old root", func(t *testing.T) copiesCase {
			var alias string
			h, sentinel, _ := newCopiesMoveHome(t, skippedLinkDir(func(h *moveHome) string {
				alias = filepath.Join(h.Home, "old-alias")
				h.Link(h.Old, alias)
				return filepath.Join(alias, "bin")
			}))
			return copiesCase{h: h, items: []string{sentinel}, inside: []string{h.Old, h.New + "/"},
				stdout: []string{"  symlink : skipped — " + filepath.Join(alias, "bin") + " is inside AD's old directory " + h.Old + ", which the move renames or replaces\n"},
				after:  func(t *testing.T, res moveResult) { assertKeptUnlinked(t, h) }}
		}},
		{"no store", func(t *testing.T) copiesCase {
			return copiesCase{h: newMoveHome(t)}
		}},
		{"previous administrator has migrate-journal", func(t *testing.T) copiesCase {
			c := newCrossingCase(t)
			admin := filepath.Join(c.h.New, "admin", "agent-director-admin")
			c.h.Copy(c.h.S.Current.Admin, admin, 0o755)
			return copiesCase{h: c.h, flags: c.valid(),
				stdout: []string{"  previous: " + admin + " does not predate the native-input cutover (it has migrate-journal)\n"}}
		}},
		{"link planted at the sentinel copy's name", func(t *testing.T) copiesCase {
			c, p := newCopiesCrossing(t)
			h := c.h
			target := filepath.Join(h.Root, "planted-target")
			cc := copiesCase{h: h, flags: c.valid(), items: []string{p.bin, p.admin, p.sentinel, p.link},
				planted: filepath.Join(h.Root, "planted"), plantedWant: "link\t" + target,
				after: func(t *testing.T, res moveResult) {
					if b, err := os.ReadFile(target); err != nil || string(b) != "not the sentinel\n" {
						t.Errorf("the planted link's target was written through: %q, %v", b, err)
					}
				}}
			h.Write(target, "not the sentinel\n", 0o600)
			real := moveQuote(h.RealTool("mktemp"))
			h.WriteTool("mktemp", `if [[ "${!#}" == */migrate-authorized.restore-*.XXXXXX ]]; then
    name="$(`+real+` -u "${!#}")" || exit
    ln -s `+moveQuote(target)+` "$name" || exit
    echo "$name" >>`+moveQuote(cc.planted)+`
    if [[ "$1" == -u ]]; then echo "$name"; exit 0; fi
fi
exec `+real+` "$@"`)
			return cc
		}},
		{"directory link planted at the managed link copy's name", func(t *testing.T) copiesCase {
			c, p := newCopiesCrossing(t)
			target := filepath.Join(c.h.Root, "planted-dir")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			return linkCopyPlanted(c.h, c.valid(), p, plantAtLinkCopy(c.h, p.link, "ln -s "+moveQuote(target)), "link\t"+target,
				func(t *testing.T) {
					if got := restoreDescribe(t, target); got != "dir\t755\t0" {
						t.Errorf("the planted link's target directory is %q; want it empty, as it was", got)
					}
				})
		}},
		{"directory planted at the managed link copy's name", func(t *testing.T) copiesCase {
			c, p := newCopiesCrossing(t)
			return linkCopyPlanted(c.h, c.valid(), p, plantAtLinkCopy(c.h, p.link, "mkdir -m 0755"), "dir\t755\t0", nil)
		}},
		{"directory made at the managed link copy's name just before ln", func(t *testing.T) copiesCase {
			c, p := newCopiesCrossing(t)
			h := c.h
			list := lateDirAtLinkCopy(h, p.link)
			before := h.PhysicalSnapshot()
			return copiesCase{h: h, flags: c.valid(), items: []string{p.bin, p.admin, p.sentinel},
				fail: &moveWant{Code: 5, Err: "ErrJournalMigrationRefused"}, ownLeft: true,
				after: func(t *testing.T, res moveResult) {
					b, err := os.ReadFile(list)
					if err != nil || len(b) == 0 {
						t.Fatalf("ln's stand-in made no directory (%v)", err)
					}
					planted := strings.TrimSpace(string(b))
					raw, _ := os.Readlink(p.link)
					assertOwnRefusal(t, res, "restore_copies_unavailable", restoreUnavailableAction,
						"install.sh: cannot make the copies a restore needs; refusing to upgrade.",
						"  item : the managed PATH link, "+p.link,
						"  copy : "+planted,
						"  cause:",
						"    the copy "+planted+" is not a link to "+raw)
					// Accepted residual (develop-epic round 02 decision): a
					// directory the same user makes inside the run's private
					// 0700 copy directory during the run gets ln's link inside
					// it, so that copy directory stays, named as not removed.
					// Nothing outside it is written.
					staging := filepath.Dir(planted)
					if !strings.Contains(res.Stderr, "  not removed: "+staging+" ") {
						t.Errorf("stderr does not name %s as not removed:\n%s", staging, res.Stderr)
					}
					if fi, err := os.Lstat(planted); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
						t.Errorf("the planted directory %s is not the 0755 directory made (%v, %v)", planted, fi, err)
					}
					if entries, err := os.ReadDir(staging); err != nil || len(entries) != 1 {
						t.Errorf("the run's directory %s holds %d entries (%v); want only the planted directory", staging, len(entries), err)
					}
					rel, _ := filepath.Rel(h.Home, staging)
					if got := restoreCopyNames(t, h); strings.Join(got, "\n") != rel {
						t.Errorf("copies under HOME after the run = %v; want only %s, the pair's and the sentinel's removed", got, rel)
					}
					// The rest of HOME is physically as before, except the
					// size and mtime of the directories copies were made and
					// removed in.
					got := h.PhysicalSnapshot()
					for p := range got {
						if p == rel || strings.HasPrefix(p, rel+"/") {
							delete(got, p)
						}
					}
					want := moveTree{}
					for p, v := range before {
						want[p] = v
					}
					for _, dir := range []string{".config/agent-director", ".config/agent-director/bin", ".config/agent-director/admin", "links"} {
						for _, tr := range []moveTree{want, got} {
							if v, ok := tr[dir]; ok {
								tr[dir] = physicalTimes.ReplaceAllString(v, " ")
							}
						}
					}
					if d := moveDiff(want, got); d != "" {
						t.Errorf("the run changed HOME outside its own copy directory %s:\n%s", staging, d)
					}
				}}
		}},
		{"move failure", func(t *testing.T) copiesCase {
			h, sentinel, link := newCopiesMoveHome(t, linksInHome)
			h.WrapTool("cp", `if [[ "${!#}" == `+moveQuote(h.New)+`/* ]]; then echo 'cp: No space left on device' >&2; exit 1; fi`)
			return copiesCase{h: h, items: []string{sentinel, link}, fail: &moveWant{Code: 5, Err: "ErrMoveFailed"}}
		}},
		// S3, the sentinel's temp file failing after the swap, keeps its
		// behaviour under decision (a): Task t2.7tz.ab.p5 must keep this row
		// true.
		{"S3 sentinel temp file failure", func(t *testing.T) copiesCase {
			c, p := newCopiesCrossing(t)
			c.h.WrapTool("mktemp", `if [[ "${!#}" == */migrate-authorized.tmp.* ]]; then echo "mktemp: failed to create file via template '${!#}': Permission denied" >&2; exit 1; fi`)
			return copiesCase{h: c.h, flags: c.valid(), items: []string{p.bin, p.admin, p.sentinel, p.link},
				fail: &moveWant{Code: 5, Err: "ErrSchemaVerifyFailed", Stderr: []string{"install.sh: writing the migration sentinel FAILED"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.setup(t)
			h := c.h
			var rec string
			if c.fail == nil {
				rec = recordCopiesAtSwap(h)
			}
			was := map[string]string{}
			for _, p := range append(append([]string(nil), c.items...), c.kept...) {
				was[p] = restoreDescribe(t, p)
			}
			res := h.Install(c.flags...)
			if c.fail != nil {
				c.fail.Check(t, h, res)
			} else if res.Code != 0 {
				t.Fatalf("install: exit %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
			}
			for _, s := range c.stdout {
				if !strings.Contains(res.Stdout, s) {
					t.Errorf("stdout lacks %q:\n%s", s, res.Stdout)
				}
			}
			for _, item := range c.items {
				if s := "  restore : copied " + item + " to " + item + ".restore-"; !strings.Contains(res.Stdout, s) {
					t.Errorf("stdout lacks %q:\n%s", s, res.Stdout)
				}
			}
			if len(c.items) == 0 && strings.Contains(res.Stdout, "  restore : ") {
				t.Errorf("a run that makes no copy says it did:\n%s", res.Stdout)
			}

			var planted []string
			if c.planted != "" {
				b, err := os.ReadFile(c.planted)
				if err != nil || len(b) == 0 {
					t.Fatalf("nothing was planted at the copy's name (%v)", err)
				}
				planted = strings.Fields(string(b))
			}
			others := map[string]bool{}
			for _, p := range append(append([]string(nil), c.kept...), planted...) {
				others[p] = true
			}
			if rec != "" {
				at := readCopiesAtSwap(t, rec)
				ours, stamps := map[string]string{}, map[string]bool{}
				for p, desc := range at {
					for _, dir := range append([]string{h.Tmp + "/"}, c.inside...) {
						if strings.HasPrefix(p, dir) {
							t.Errorf("a copy lies under %s at the swap: %s", dir, p)
						}
					}
					if m := restoreCopyPattern.FindStringSubmatch(p); m != nil && !others[p] {
						ours[p] = desc
						stamps[m[1]] = true
					}
				}
				if len(stamps) > 1 {
					t.Errorf("this run's copies carry more than one timestamp: %v", ours)
				}
				for _, item := range c.items {
					var got []string
					for p := range ours {
						if strings.TrimSuffix(p, restoreCopyPattern.FindString(p)) == item {
							got = append(got, p)
						}
					}
					switch {
					case len(got) != 1:
						t.Errorf("copies of %s at the swap = %v; want one", item, got)
					case strings.HasPrefix(was[item], "link\t"):
						// A link's copy is a 0700 directory holding only a
						// link named as the item, with the same raw target.
						inner := filepath.Join(got[0], filepath.Base(item))
						if ours[got[0]] != "dir\t700\t1" || at[inner] != was[item] {
							t.Errorf("the copy %s is %q holding %s %q; want a 0700 directory holding only that link, %q, as %s was",
								got[0], ours[got[0]], inner, at[inner], was[item], item)
						}
					case ours[got[0]] != was[item]:
						t.Errorf("the copy %s is %q; want %q, as %s was", got[0], ours[got[0]], was[item], item)
					}
				}
				if len(ours) != len(c.items) {
					t.Errorf("this run's copies at the swap = %v; want one for each of %v", ours, c.items)
				}
			}

			var want []string
			for p := range others {
				rel, _ := filepath.Rel(h.Home, p)
				want = append(want, rel)
			}
			sort.Strings(want)
			if got := restoreCopyNames(t, h); !c.ownLeft && strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("copies under HOME after the run = %v; want only another run's and the planted %v", got, want)
			}
			for _, p := range c.kept {
				if got := restoreDescribe(t, p); got != was[p] {
					t.Errorf("an earlier run's copy %s is %q; was %q", p, got, was[p])
				}
			}
			for _, p := range planted {
				if got := restoreDescribe(t, p); got != c.plantedWant {
					t.Errorf("the planted entry %s is %q; want %q, as planted", p, got, c.plantedWant)
				}
			}
			if c.after != nil {
				c.after(t, res)
			}
		})
	}
}
