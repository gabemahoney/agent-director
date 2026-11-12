package installsh_test

// SR-9's installer cases on the Mac (darwin only; installer_mac_other_test.go
// is the rest's, with the same API). newMoveMacCase records the tools
// install.sh meets (SR-9.2) and, when the case fails, logs SR-9.4's report:
// the behaviour, the exit status and err_name wanted and seen, install.sh's
// output and the store's files before and after it ran. The helpers of
// TestMoveAfterCleanClose (b.zsj) read the old store by bytes only, because
// opening a cleanly closed WAL store with sqlite3 makes the -wal and -shm that
// case needs missing.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moveMacCase is one of SR-9's installer cases.
type moveMacCase struct {
	t         *testing.T
	h         *moveHome
	behaviour string
	// ran is set when Install starts install.sh and returned once it returns;
	// before and after are the store's files around that run.
	ran, returned bool
	res           moveResult
	before, after []string
}

// newMoveMacCase logs the tools the case's install.sh meets and registers the
// report a failed case logs. behaviour is the case's SR-9.4 name. Create it
// once h's PATH and environment are set, before the case's install.
func newMoveMacCase(t *testing.T, h *moveHome, behaviour string) *moveMacCase {
	t.Helper()
	c := &moveMacCase{t: t, h: h, behaviour: behaviour}
	c.recordTools()
	t.Cleanup(c.report)
	return c
}

// recordTools logs, comparing nothing, the bash that runs install.sh and the
// sqlite3 install.sh's own PATH finds. A record command that fails is logged:
// install.sh then fails on its own, with its own message.
func (c *moveMacCase) recordTools() {
	c.t.Helper()
	// run runs script with install.sh's bash in install.sh's environment, as
	// schedRunArgv reuses h.command with other arguments.
	run := func(script string) (string, error) {
		cmd := c.h.command()
		cmd.Args = []string{cmd.Args[0], "-c", script}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	resolve := func(p string) string {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return fmt.Sprintf("unresolved: %v", err)
		}
		return real
	}
	lines := []string{fmt.Sprintf("bash: %s -> %s", c.h.S.Bash, resolve(c.h.S.Bash))}
	if v, err := run(`printf '%s\n' "$BASH_VERSION"`); err != nil {
		lines = append(lines, fmt.Sprintf("bash BASH_VERSION: failed (%v): %s", err, v))
	} else {
		lines = append(lines, "bash BASH_VERSION: "+v)
	}
	if p, err := run(`command -v sqlite3`); err != nil {
		lines = append(lines, fmt.Sprintf("command -v sqlite3: failed (%v): %s", err, p))
	} else {
		lines = append(lines, fmt.Sprintf("command -v sqlite3: %s -> %s", p, resolve(p)))
		if v, err := run(`sqlite3 --version`); err != nil {
			lines = append(lines, fmt.Sprintf("sqlite3 --version: failed (%v): %s", err, v))
		} else {
			lines = append(lines, "sqlite3 --version: "+v)
		}
	}
	c.t.Logf("installer case %q: the tools install.sh meets\n  %s", c.behaviour, strings.Join(lines, "\n  "))
}

// Install runs install.sh as h.Install does, listing the store's files just
// before it and again as soon as it returns, before any later check reads a
// store.
func (c *moveMacCase) Install(args ...string) moveResult {
	c.t.Helper()
	c.before = moveMacStoreFiles(c.h.Home)
	c.ran = true
	res := c.h.Install(args...)
	c.after = moveMacStoreFiles(c.h.Home)
	c.res, c.returned = res, true
	return res
}

// WantSidecars fails the case unless db has a non-empty -wal and an -shm
// beside it, as b.up4's store does.
func (c *moveMacCase) WantSidecars(db string) {
	c.t.Helper()
	if fi, err := os.Lstat(db + "-wal"); err != nil {
		c.t.Fatalf("the old store has no %s-wal: %v", db, err)
	} else if fi.Size() == 0 {
		c.t.Fatalf("the old store's %s-wal is empty, want the pages its killed writer left", db)
	}
	if _, err := os.Lstat(db + "-shm"); err != nil {
		c.t.Fatalf("the old store has no %s-shm: %v", db, err)
	}
}

// report logs SR-9.4's block when the case has failed.
func (c *moveMacCase) report() {
	if !c.t.Failed() {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "installer case failed: %s\n", c.behaviour)
	switch {
	case !c.ran:
		b.WriteString("install.sh: never ran (the case failed before its install)\n")
		fmt.Fprintf(&b, "store files now:\n%s", moveMacLines(moveMacStoreFiles(c.h.Home)))
	case !c.returned:
		b.WriteString("install.sh: never returned (the case stopped while it ran)\n")
		fmt.Fprintf(&b, "store files before install.sh:\n%s", moveMacLines(c.before))
		fmt.Fprintf(&b, "store files now:\n%s", moveMacLines(moveMacStoreFiles(c.h.Home)))
	default:
		errName := "none"
		if name, ok := strings.CutPrefix(c.res.Last(), "install.sh: err_name="); ok {
			errName = name
		}
		fmt.Fprintf(&b, "exit status: want 0, seen %d\n", c.res.Code)
		fmt.Fprintf(&b, "err_name: want none, seen %s\n", errName)
		fmt.Fprintf(&b, "install.sh stdout:\n%s\n", c.res.Stdout)
		fmt.Fprintf(&b, "install.sh stderr:\n%s\n", c.res.Stderr)
		fmt.Fprintf(&b, "store files before install.sh:\n%s", moveMacLines(c.before))
		fmt.Fprintf(&b, "store files after install.sh:\n%s", moveMacLines(c.after))
	}
	c.t.Log(b.String())
}

// moveMacLines indents lines one to a line, or says there are none.
func moveMacLines(lines []string) string {
	if len(lines) == 0 {
		return "  (none)\n"
	}
	return "  " + strings.Join(lines, "\n  ") + "\n"
}

// moveMacStoreNames are the files a store listing shows.
var moveMacStoreNames = map[string]bool{"state.db": true, "state.db-wal": true, "state.db-shm": true, "state.db-journal": true}

// moveMacStoreFiles lists every store file under home, relative to it, with
// its mode and size, and a main store file's header versions. It only Lstats
// and reads 20 bytes, never opening a store with SQLite, so it changes
// nothing; a file it cannot read is listed with the error.
func moveMacStoreFiles(home string) []string {
	var out []string
	err := filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", p, err))
			return nil
		}
		if !moveMacStoreNames[d.Name()] {
			return nil
		}
		rel, _ := filepath.Rel(home, p)
		fi, err := d.Info()
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", rel, err))
			return nil
		}
		line := fmt.Sprintf("%s %v size=%d", rel, fi.Mode(), fi.Size())
		if d.Name() == "state.db" && fi.Mode().IsRegular() {
			if h, err := moveMacHeader(p); err != nil {
				line += fmt.Sprintf(" header: %v", err)
			} else {
				line += fmt.Sprintf(" header write=%d read=%d", h[18], h[19])
			}
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		out = append(out, fmt.Sprintf("%s: %v", home, err))
	}
	return out
}

// moveMacHeader is the first 20 bytes of the store file p, read as bytes only;
// it is an error unless they start with SQLite's magic.
func moveMacHeader(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, 20)
	if n, err := io.ReadFull(f, b); err != nil {
		return nil, fmt.Errorf("%d bytes, not a store header: %v", n, err)
	}
	if !bytes.HasPrefix(b, moveSQLiteMagic) {
		return nil, fmt.Errorf("no SQLite magic (%x)", b[:len(moveSQLiteMagic)])
	}
	return b, nil
}

// moveMacSnapshotCopy is h.Snapshot() without opening h's stores: it copies
// HOME byte for byte (each file's and directory's mode, each link's target
// text) into a fresh directory under h.Root, snapshots the copy and removes
// it. SQLite opens only the copy's stores, so nothing appears beside the
// original's.
func moveMacSnapshotCopy(t *testing.T, h *moveHome) moveTree {
	t.Helper()
	dir, err := os.MkdirTemp(h.Root, "snapshot-copy-")
	if err != nil {
		t.Fatal(err)
	}
	twin := *h
	twin.Home = filepath.Join(dir, "home")
	type dirMode struct {
		path string
		mode fs.FileMode
	}
	var dirs []dirMode
	err = filepath.WalkDir(h.Home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(h.Home, p)
		to := filepath.Join(twin.Home, rel)
		fi, err := d.Info()
		if err != nil {
			return err
		}
		switch mode := fi.Mode(); {
		case mode&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(target, to)
		case mode.IsDir():
			dirs = append(dirs, dirMode{to, mode})
			return os.Mkdir(to, 0o700)
		case mode.IsRegular():
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(to, b, 0o600); err != nil {
				return err
			}
			return os.Chmod(to, mode)
		default:
			return fmt.Errorf("%s is a %v, which the snapshot copy does not make", p, mode)
		}
	})
	if err != nil {
		t.Fatalf("copy %s for its snapshot: %v", h.Home, err)
	}
	// Directory modes go on last, deepest first, so a directory the copy
	// cannot write still got its entries.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i].path, dirs[i].mode); err != nil {
			t.Fatal(err)
		}
	}
	tree := twin.Snapshot()
	for _, d := range dirs {
		_ = os.Chmod(d.path, 0o700) // so RemoveAll can empty every directory
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove the snapshot copy %s: %v", dir, err)
	}
	return tree
}

// moveMacCheckClosedStore fails the case unless db is a WAL-mode store closed
// cleanly: SQLite's magic, header offsets 18 and 19 both 2, and no db-wal or
// db-shm beside it. It reads bytes and Lstats only. Call it immediately
// before the install, after every read of the old store.
func moveMacCheckClosedStore(t *testing.T, db string) {
	t.Helper()
	var found []string
	if h, err := moveMacHeader(db); err != nil {
		found = append(found, fmt.Sprintf("%s: %v", db, err))
	} else if h[18] != 2 || h[19] != 2 {
		found = append(found, fmt.Sprintf("%s has header write=%d read=%d, want 2 and 2 (WAL)", db, h[18], h[19]))
	}
	for _, side := range []string{db + "-wal", db + "-shm"} {
		if fi, err := os.Lstat(side); err == nil {
			found = append(found, fmt.Sprintf("%s exists (%v size=%d), want none", side, fi.Mode(), fi.Size()))
		} else if !errors.Is(err, fs.ErrNotExist) {
			found = append(found, fmt.Sprintf("%s: %v", side, err))
		}
	}
	if len(found) > 0 {
		t.Fatalf("the old store is not a cleanly closed WAL store:\n  %s", strings.Join(found, "\n  "))
	}
}
