//go:build linux || darwin

package installversion

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRetainedVersionsAndRollback(t *testing.T) {
	f := newPublicationFixture(t)
	publishOK(t, f.root, f.a)
	old := installed(t, f.root)[0]
	anchor := bytesAt(t, filepath.Join(f.root, "install-contract.json"))
	unowned := filepath.Join(f.root, "versions", ".staging-other-invocation")
	must(t, os.Mkdir(unowned, 0700))
	must(t, os.WriteFile(filepath.Join(unowned, "keep"), []byte("not this operation"), 0600))
	links := map[string]os.FileInfo{}
	for _, p := range []string{"bin/agent-director", "admin/agent-director-admin"} {
		i, err := os.Lstat(filepath.Join(f.root, p))
		must(t, err)
		links[p] = i
	}
	publishOK(t, f.root, f.b)
	retained := snapshot(t, filepath.Join(f.root, "versions"))
	history := snapshot(t, filepath.Join(f.root, "publications"))
	r, err := (Engine{Root: f.root}).Rollback("old")
	must(t, err)
	if !r.Reused || !r.Durable {
		t.Fatalf("rollback result %+v", r)
	}
	assertEstablished(t, f.root, "old")
	assertPreserved(t, filepath.Join(f.root, "versions"), retained)
	assertPreserved(t, filepath.Join(f.root, "publications"), history)
	assertSame(t, bytesAt(t, filepath.Join(f.root, "install-contract.json")), anchor)
	r = publishOK(t, f.root, f.a)
	if !r.Reused {
		t.Fatal("identical restage not reused")
	}
	assertSame(t, installed(t, f.root)[0].InstalledSequence, old.InstalledSequence)
	for p, before := range links {
		after, err := os.Lstat(filepath.Join(f.root, p))
		must(t, err)
		if !os.SameFile(before, after) {
			t.Fatalf("canonical entry replaced: %s", p)
		}
	}
	expanded := makeCandidate(t, factsFor("expanded", "claude", "codex"))
	publishOK(t, f.root, expanded)
	_, err = (Engine{Root: f.root}).Rollback("old")
	expectRefusal(t, err, "coverage_regression")
	_, err = publishCandidate(f.root, f.a, Hooks{})
	expectRefusal(t, err, "coverage_regression")
	assertPreserved(t, filepath.Join(f.root, "versions"), retained)
	assertSame(t, len(installed(t, f.root)), 3)
	assertSame(t, string(bytesAt(t, filepath.Join(unowned, "keep"))), "not this operation")
	assertUsablePublication(t, f.root, "expanded")
	// A delayed direct start still executes old bytes while current selects
	// the expanded version, without consulting any process inventory.
	for _, p := range []string{old.RuntimeExecutable, filepath.Join(old.Directory, "agent-director-admin")} {
		out, err := exec.Command(p).CombinedOutput()
		must(t, err)
		assertSame(t, string(out), "old\n")
	}
}

func TestVersionRestageConflict(t *testing.T) {
	for _, kind := range []string{"bytes", "commit", "mode", "coverage"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			before := snapshot(t, f.root)
			c := makeCandidate(t, f.a.Facts)
			switch kind {
			case "bytes":
				must(t, os.WriteFile(c.Runtime, append(bytesAt(t, c.Runtime), []byte("# changed\n")...), 0700))
			case "commit":
				c.Facts.Commit = "other"
				payload(t, c.Runtime, RuntimeFactsArgument, c.Facts)
				payload(t, c.Admin, "version-facts", c.Facts)
			case "mode":
				must(t, os.Chmod(c.Runtime, 0500))
			case "coverage":
				c.Facts.CoveredAgents = []string{"claude", "codex"}
				payload(t, c.Runtime, RuntimeFactsArgument, c.Facts)
				payload(t, c.Admin, "version-facts", c.Facts)
			}
			_, err := publishCandidate(f.root, c, Hooks{})
			expectRefusal(t, err, "version_conflict")
			assertSame(t, snapshot(t, f.root), before)
		})
	}
}

func TestInstalledVersionDiscovery(t *testing.T) {
	f := newPublicationFixture(t)
	assertSame(t, len(installed(t, f.root)), 0)
	if _, err := os.Stat(f.root); !os.IsNotExist(err) {
		t.Fatalf("reader created root: %v", err)
	}
	publishOK(t, f.root, f.a)
	publishOK(t, f.root, f.b)
	_, err := (Engine{Root: f.root}).Rollback("old")
	must(t, err)
	want := installed(t, f.root)
	must(t, os.Mkdir(filepath.Join(f.root, "versions", ".staging-unowned"), 0700))
	must(t, os.WriteFile(filepath.Join(f.root, "versions", ".staging-unowned", "keep"), []byte("unowned"), 0600))
	// Discovery neither follows current nor acquires/creates the lock.
	must(t, os.Remove(filepath.Join(f.root, "install.lock")))
	for _, target := range []string{"", "versions/does-not-exist", "versions/" + want[1].VersionID} {
		must(t, os.Remove(filepath.Join(f.root, "current")))
		if target != "" {
			must(t, os.Symlink(target, filepath.Join(f.root, "current")))
		}
		before := snapshot(t, f.root)
		assertSame(t, installed(t, f.root), want)
		assertSame(t, snapshot(t, f.root), before)
		if target == "" {
			must(t, os.Symlink("placeholder", filepath.Join(f.root, "current")))
		}
	}
}
