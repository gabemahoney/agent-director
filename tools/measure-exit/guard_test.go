package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// guardRun is one guard.sh invocation's result.
type guardRun struct {
	code           int
	stdout, stderr string
}

// guardFixture is a fixture home holding a stand-in store, a state file
// path outside it, and recording shims that guard.sh must never call.
type guardFixture struct {
	home, store, state, shimLog, shimDir string
}

// newGuardFixture writes state.db and ad-trail.jsonl into a fixture home.
func newGuardFixture(t *testing.T) *guardFixture {
	t.Helper()
	g := &guardFixture{home: t.TempDir(), state: filepath.Join(t.TempDir(), "guard.state"), shimDir: t.TempDir()}
	g.store = filepath.Join(g.home, ".agent-director")
	g.shimLog = filepath.Join(t.TempDir(), "shim.log")
	writeFile(t, filepath.Join(g.store, "state.db"), "stand-in database bytes\n")
	writeFile(t, filepath.Join(g.store, "ad-trail.jsonl"), `{"event":"host.own","claude_instance_id":"host-row-1"}`+"\n")
	for _, name := range []string{"sqlite3", "agent-director", "tmux"} {
		shim := "#!/bin/sh\necho \"" + name + " $*\" >> '" + g.shimLog + "'\nexit 0\n"
		if err := os.WriteFile(filepath.Join(g.shimDir, name), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

// path is a store file's path.
func (g *guardFixture) path(name string) string { return filepath.Join(g.store, name) }

// run runs guard.sh with the recording shims first on PATH and HOME elsewhere.
func (g *guardFixture) run(t *testing.T, args ...string) guardRun {
	t.Helper()
	cmd := exec.Command("./guard.sh", args...)
	cmd.Env = []string{"PATH=" + g.shimDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "LC_ALL=C"}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	r := guardRun{stdout: out.String(), stderr: errOut.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		r.code = exitErr.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return r
}

// snapshot runs a quiet-host (busy false) or busy-host snapshot of the fixture.
func (g *guardFixture) snapshot(t *testing.T, busy bool) guardRun {
	t.Helper()
	args := []string{"snapshot", "--state", g.state, "--home", g.home}
	if busy {
		args = append(args, "--busy-host")
	} else {
		args = append(args, "--settle", "0")
	}
	r := g.run(t, args...)
	if r.code != 0 {
		t.Fatalf("snapshot exit %d: %s%s", r.code, r.stdout, r.stderr)
	}
	return r
}

// verify runs verify with extra args.
func (g *guardFixture) verify(t *testing.T, extra ...string) guardRun {
	t.Helper()
	return g.run(t, append([]string{"verify", "--state", g.state, "--home", g.home}, extra...)...)
}

// assertShimsUnused fails when guard.sh ran sqlite3, agent-director or tmux.
func (g *guardFixture) assertShimsUnused(t *testing.T) {
	t.Helper()
	if b, err := os.ReadFile(g.shimLog); err == nil {
		t.Errorf("guard.sh ran a forbidden program: %s", b)
	}
}

// sum is a file's SHA-256 as guard.sh prints it.
func sum(t *testing.T, path string) string {
	t.Helper()
	h := sha256.Sum256([]byte(readFile(t, path)))
	return hex.EncodeToString(h[:])
}

// appendTrail appends text to the fixture trail.
func (g *guardFixture) appendTrail(t *testing.T, text string) {
	t.Helper()
	f, err := os.OpenFile(g.path("ad-trail.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestGuardQuietUnchangedPasses(t *testing.T) {
	g := newGuardFixture(t)
	g.snapshot(t, false)
	r := g.verify(t)
	if r.code != 0 || !strings.Contains(r.stdout, "guard passed: both files identical") ||
		!strings.Contains(r.stdout, "home checked: "+g.home+" (override") {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, name := range []string{"state.db", "ad-trail.jsonl"} {
		s := sum(t, g.path(name))
		for _, when := range []string{"before", "after "} {
			if !strings.Contains(r.stdout, when+" "+s) || !strings.Contains(r.stdout, name) {
				t.Errorf("verify does not print %s %s %s:\n%s", name, when, s, r.stdout)
			}
		}
	}
	g.assertShimsUnused(t)
}

func TestGuardQuietDetectsChanges(t *testing.T) {
	for _, name := range []string{"state.db", "ad-trail.jsonl"} {
		other := map[string]string{"state.db": "ad-trail.jsonl", "ad-trail.jsonl": "state.db"}[name]
		for _, change := range []string{"modify", "delete", "create"} {
			t.Run(name+" "+change, func(t *testing.T) {
				g := newGuardFixture(t)
				if change == "create" {
					if err := os.Remove(g.path(name)); err != nil {
						t.Fatal(err)
					}
				}
				g.snapshot(t, false)
				switch change {
				case "modify":
					b := []byte(readFile(t, g.path(name)))
					b[0] ^= 1
					if err := os.WriteFile(g.path(name), b, 0o600); err != nil {
						t.Fatal(err)
					}
				case "delete":
					if err := os.Remove(g.path(name)); err != nil {
						t.Fatal(err)
					}
				case "create":
					writeFile(t, g.path(name), "new\n")
				}
				r := g.verify(t)
				if r.code != 1 || !strings.Contains(r.stderr, "GUARD FAILED: "+g.path(name)+" changed") || strings.Contains(r.stderr, other) {
					t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
				}
			})
		}
	}
}

func TestGuardQuietBothAbsentAndWAL(t *testing.T) {
	g := newGuardFixture(t)
	for _, name := range []string{"state.db", "ad-trail.jsonl"} {
		if err := os.Remove(g.path(name)); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, g.path("state.db-wal"), "wal")
	g.snapshot(t, false)
	r := g.verify(t)
	if r.code != 0 || strings.Count(r.stdout, "absent") < 4 || !strings.Contains(r.stdout, "info: "+g.path("state.db-wal")+" present") {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
}

func TestGuardQuietRefusesABusyHost(t *testing.T) {
	g := newGuardFixture(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for end := time.Now().Add(1500 * time.Millisecond); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
			if f, err := os.OpenFile(g.path("ad-trail.jsonl"), os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				_, _ = f.WriteString(`{"event":"host.busy"}` + "\n")
				_ = f.Close()
			}
		}
	}()
	r := g.run(t, "snapshot", "--state", g.state, "--home", g.home, "--settle", "1")
	<-done
	if r.code != 3 || !strings.Contains(r.stderr, "the host is not quiet") || !strings.Contains(r.stderr, "busy-host") {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(g.state); err == nil {
		t.Error("a refused snapshot wrote its state file")
	}
}

func TestGuardRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(t *testing.T, g *guardFixture) []string
		text string
	}{
		{"verify without a snapshot", func(t *testing.T, g *guardFixture) []string {
			return []string{"verify", "--state", g.state, "--home", g.home}
		}, "no snapshot"},
		{"a state file under the store", func(t *testing.T, g *guardFixture) []string {
			return []string{"snapshot", "--state", g.path("guard.state"), "--home", g.home, "--settle", "0"}
		}, "must not lie under"},
		{"an existing state file", func(t *testing.T, g *guardFixture) []string {
			writeFile(t, g.state, "mode=quiet\n")
			return []string{"snapshot", "--state", g.state, "--home", g.home, "--settle", "0"}
		}, "already exists"},
		{"a relative home", func(t *testing.T, g *guardFixture) []string {
			return []string{"snapshot", "--state", g.state, "--home", "rel"}
		}, "absolute"},
		{"ids at snapshot", func(t *testing.T, g *guardFixture) []string {
			return []string{"snapshot", "--state", g.state, "--home", g.home, "--id", "mx-run-identifier-1"}
		}, "belong to verify"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuardFixture(t)
			r := g.run(t, tc.args(t, g)...)
			if r.code != 2 || !strings.Contains(r.stderr, tc.text) {
				t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
			}
			if _, err := os.Stat(g.path("guard.state")); err == nil {
				t.Error("a state file was written under the store")
			}
		})
	}
}

// snapshotTree records every path under root with its size, mode and mtime.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out[p] = fi.Mode().String() + " " + strconv.FormatInt(fi.Size(), 10) + " " + fi.ModTime().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGuardIsReadOnly(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "quiet", true: "busy"}[busy], func(t *testing.T) {
			g := newGuardFixture(t)
			before := snapshotTree(t, g.home)
			contents := readFile(t, g.path("state.db")) + readFile(t, g.path("ad-trail.jsonl"))
			g.snapshot(t, busy)
			if r := g.verify(t, map[bool][]string{false: nil, true: {"--id", "mx-run-identifier-1"}}[busy]...); r.code != 0 {
				t.Fatalf("verify exit %d:\n%s%s", r.code, r.stdout, r.stderr)
			}
			after := snapshotTree(t, g.home)
			if len(after) != len(before) {
				t.Errorf("the fixture home changed: %v -> %v", before, after)
			}
			for p, v := range before {
				if after[p] != v {
					t.Errorf("%s changed: %s -> %s", p, v, after[p])
				}
			}
			if readFile(t, g.path("state.db"))+readFile(t, g.path("ad-trail.jsonl")) != contents {
				t.Error("a store file's content changed")
			}
			g.assertShimsUnused(t)
		})
	}
}

func TestGuardDefaultHomeIsThePasswdEntry(t *testing.T) {
	u, err := user.Current()
	if err != nil || u.HomeDir == "" {
		t.Skipf("no passwd entry: %v", err)
	}
	if _, err := os.Stat(filepath.Join(u.HomeDir, ".agent-director")); err == nil {
		t.Skip("the sandbox user's home has a store; this case must not read one")
	}
	before, _ := os.ReadDir(u.HomeDir)
	g := newGuardFixture(t)
	r := g.run(t, "snapshot", "--state", g.state, "--settle", "0")
	if r.code != 0 || !strings.Contains(r.stdout, "home checked: "+u.HomeDir+" (passwd entry of uid "+u.Uid+")") {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	if v := g.run(t, "verify", "--state", g.state); v.code != 0 {
		t.Fatalf("verify exit %d:\n%s%s", v.code, v.stdout, v.stderr)
	}
	after, _ := os.ReadDir(u.HomeDir)
	if len(after) != len(before) {
		t.Errorf("the passwd home gained entries: %d -> %d", len(before), len(after))
	}
}

func TestGuardBusyHost(t *testing.T) {
	const runID = "mx-run-identifier-1"
	idsFile := "run_id " + runID + "\ninstance_id 6f1c2a9e-instance\nsocket /tmp/mx-tmux-abc/tmux-1000/default\n"
	for _, tc := range []struct {
		name   string
		before func(t *testing.T, g *guardFixture)
		during func(t *testing.T, g *guardFixture)
		code   int
		text   string
	}{
		{name: "nothing appended", code: 0, text: "scanning 0 appended bytes"},
		{name: "unrelated lines appended", during: func(t *testing.T, g *guardFixture) {
			g.appendTrail(t, `{"event":"host.own","claude_instance_id":"host-row-2"}`+"\n")
		}, code: 0, text: "none of the run's identifiers"},
		{name: "an identifier only before the snapshot", before: func(t *testing.T, g *guardFixture) {
			g.appendTrail(t, `{"run":"`+runID+`"}`+"\n")
		}, code: 0, text: "none of the run's identifiers"},
		{name: "an identifier appended", during: func(t *testing.T, g *guardFixture) {
			g.appendTrail(t, `{"event":"ad.hook.fired","claude_instance_id":"6f1c2a9e-instance","prompt":"TRAIL-CONTENT-SENTINEL"}`+"\n")
		}, code: 1, text: "1 6f1c2a9e-instance"},
		{name: "a truncated trail", during: func(t *testing.T, g *guardFixture) {
			if err := os.Truncate(g.path("ad-trail.jsonl"), 3); err != nil {
				t.Fatal(err)
			}
		}, code: 1, text: "truncated or rotated"},
		{name: "a rotated trail", during: func(t *testing.T, g *guardFixture) {
			if err := os.Rename(g.path("ad-trail.jsonl"), g.path("ad-trail.jsonl.1")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, g.path("ad-trail.jsonl"), strings.Repeat("x", 200)+"\n")
		}, code: 1, text: "truncated or rotated"},
		{name: "a deleted trail", during: func(t *testing.T, g *guardFixture) {
			if err := os.Remove(g.path("ad-trail.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, code: 1, text: "deleted or rotated"},
		{name: "absent both times", before: func(t *testing.T, g *guardFixture) {
			if err := os.Remove(g.path("ad-trail.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, code: 0, text: "absent before and after"},
		{name: "created clean during the run", before: func(t *testing.T, g *guardFixture) {
			if err := os.Remove(g.path("ad-trail.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, during: func(t *testing.T, g *guardFixture) { g.appendTrail(t, "{}\n") }, code: 0, text: "created during the run"},
		{name: "created with an identifier", before: func(t *testing.T, g *guardFixture) {
			if err := os.Remove(g.path("ad-trail.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, during: func(t *testing.T, g *guardFixture) { g.appendTrail(t, runID+"\n") }, code: 1, text: "1 " + runID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuardFixture(t)
			ids := filepath.Join(t.TempDir(), "harness-ids.txt")
			writeFile(t, ids, idsFile)
			if tc.before != nil {
				tc.before(t, g)
			}
			if s := g.snapshot(t, true); !strings.Contains(s.stdout, "state.db not checked on a busy host") {
				t.Errorf("snapshot:\n%s", s.stdout)
			}
			if tc.during != nil {
				tc.during(t, g)
			}
			r := g.verify(t, "--ids-file", ids, "--id", runID)
			out := r.stdout + r.stderr
			if r.code != tc.code || !strings.Contains(out, tc.text) || !strings.Contains(r.stdout, "state.db not checked on a busy host") {
				t.Fatalf("exit %d, want %d with %q:\n%s", r.code, tc.code, tc.text, out)
			}
			assertAbsent(t, "guard output", out, "TRAIL-CONTENT-SENTINEL", "host-row-")
			g.assertShimsUnused(t)
		})
	}
}

func TestGuardBusyHostNeverReadsTheDatabase(t *testing.T) {
	g := newGuardFixture(t)
	// A database that cannot be read as a file: any read attempt fails.
	if err := os.Remove(g.path("state.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(g.path("state.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	g.snapshot(t, true)
	if r := g.verify(t, "--id", "mx-run-identifier-1"); r.code != 0 {
		t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
	}
	g.assertShimsUnused(t)
}

func TestGuardBusyHostIdentifierRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		text string
	}{
		{"no identifiers", nil, "needs identifiers"},
		{"a short identifier", []string{"--id", "short"}, "shorter than 8"},
		{"an unreadable ids file", []string{"--ids-file", "/nonexistent/harness-ids.txt"}, "not readable"},
		{"busy mode chosen at verify", []string{"--busy-host", "--id", "mx-run-identifier-1"}, "chosen at snapshot time"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuardFixture(t)
			g.snapshot(t, true)
			r := g.verify(t, tc.args...)
			if r.code != 2 || !strings.Contains(r.stderr, tc.text) {
				t.Fatalf("exit %d:\n%s%s", r.code, r.stdout, r.stderr)
			}
		})
	}
}
