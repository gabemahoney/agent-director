//go:build linux || darwin

package installversion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// publicationFixture is the component-local shared scratch layout and facts
// payload. It never builds per row or consults a live HOME, store or PID list.
type publicationFixture struct {
	home, root string
	a, b       candidate
}

type candidate struct {
	Runtime, Admin string
	Facts          Facts
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func factsFor(stamp string, covered ...string) Facts {
	if covered == nil {
		covered = []string{"claude"}
	}
	return Facts{FormatVersion: 1, Version: stamp, Commit: "commit-" + stamp,
		CoveredAgents: covered, CoreVersion: "core-1", CoreDeclarationsSHA256: strings.Repeat("a", 64)}
}

func payload(t testing.TB, path, argument string, facts Facts) {
	t.Helper()
	b, err := json.Marshal(facts)
	must(t, err)
	// POSIX sh's builtin printf is the only payload dependency. The exact
	// private argument is checked; ordinary invocation identifies old bytes.
	script := "#!/bin/sh\nif [ \"$#\" = 0 ]; then printf '%s\\n' '" + facts.Version + "'; exit; fi\n" +
		"[ \"$#\" = 1 ] && [ \"$1\" = '" + argument + "' ] || exit 41\n" +
		"printf '%s\\n' '" + strings.ReplaceAll(string(b), "'", "'\"'\"'") + "'\n"
	must(t, os.WriteFile(path, []byte(script), 0700))
}

func makeCandidate(t testing.TB, facts Facts) candidate {
	t.Helper()
	dir := t.TempDir()
	c := candidate{filepath.Join(dir, "agent-director"), filepath.Join(dir, "agent-director-admin"), facts}
	payload(t, c.Runtime, RuntimeFactsArgument, facts)
	payload(t, c.Admin, "version-facts", facts)
	return c
}

func newPublicationFixture(t testing.TB) publicationFixture {
	t.Helper()
	home := t.TempDir()
	return publicationFixture{home, filepath.Join(home, ".config", "agent-director"),
		makeCandidate(t, factsFor("old")), makeCandidate(t, factsFor("new"))}
}

func publishCandidate(root string, c candidate, hooks Hooks) (Result, error) {
	return (Engine{Root: root, Hooks: hooks}).Publish(context.Background(), c.Runtime, c.Admin, c.Facts)
}

func publishOK(t testing.TB, root string, c candidate) Result {
	t.Helper()
	r, err := publishCandidate(root, c, Hooks{})
	must(t, err)
	if r.PublicationState != "published" || !r.Durable || !r.InitializationComplete || r.PublishedVersion == nil || *r.PublishedVersion != c.Facts.Version {
		t.Fatalf("incomplete success: %+v", r)
	}
	return r
}

func expectRefusal(t testing.TB, err error, reason string) *Refusal {
	t.Helper()
	var r *Refusal
	if !errors.As(err, &r) || r.Reason != reason || r.Retryable != (reason == "io_failure" || reason == "lock_busy") {
		t.Fatalf("want %s refusal, got %#v (%v)", reason, r, err)
	}
	return r
}

func bytesAt(t testing.TB, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	must(t, err)
	return b
}

type treeEntry struct {
	Mode fs.FileMode
	Data string
}
type treeImage map[string]treeEntry

func snapshot(t testing.TB, root string) treeImage {
	t.Helper()
	image := treeImage{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		v := treeEntry{Mode: info.Mode()}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			v.Data, err = os.Readlink(path)
		case info.Mode().IsRegular():
			var b []byte
			b, err = os.ReadFile(path)
			v.Data = string(b)
		case !info.IsDir():
			return fmt.Errorf("unsupported snapshot resource %s", path)
		}
		if err != nil {
			return err
		}
		image[rel] = v
		return nil
	})
	if !errors.Is(err, os.ErrNotExist) {
		must(t, err)
	}
	return image
}

func assertPreserved(t testing.TB, root string, before treeImage) {
	t.Helper()
	after := snapshot(t, root)
	for name, want := range before {
		if got, ok := after[name]; !ok || got != want {
			t.Fatalf("retained %s changed: got %+v, want %+v", name, got, want)
		}
	}
}

func assertSelected(t testing.TB, root, stamp string) InstalledVersion {
	t.Helper()
	v, err := ReadPublished(root)
	must(t, err)
	if v == nil || v.Version != stamp {
		t.Fatalf("published %+v, want %q", v, stamp)
	}
	for _, name := range []string{"agent-director", "agent-director-admin"} {
		out, err := exec.Command(filepath.Join(v.Directory, name)).CombinedOutput()
		must(t, err)
		if string(out) != stamp+"\n" {
			t.Fatalf("%s executes %q, want %q", name, out, stamp)
		}
	}
	return *v
}

func assertEstablished(t testing.TB, root, stamp string) {
	t.Helper()
	assertUsablePublication(t, root, stamp)
	i, err := ReadInitialization(root)
	must(t, err)
	if !i.Complete {
		t.Fatal("bootstrap not complete")
	}
	c, err := ReadContract(root)
	must(t, err)
	if c != i.Contract {
		t.Fatal("anchor differs from original evidence")
	}
	for path, target := range map[string]string{"bin/agent-director": "../current/agent-director", "admin/agent-director-admin": "../current/agent-director-admin"} {
		got, err := os.Readlink(filepath.Join(root, path))
		must(t, err)
		if got != target {
			t.Fatalf("%s -> %q, want %q", path, got, target)
		}
	}
	info, err := os.Stat(filepath.Join(root, "admin"))
	must(t, err)
	if info.Mode().Perm() != 0700 {
		t.Fatalf("admin mode %o", info.Mode().Perm())
	}
}

func assertUsablePublication(t testing.TB, root, stamp string) InstalledVersion {
	t.Helper()
	v := assertSelected(t, root, stamp)
	for _, leaf := range []string{"bin/agent-director", "admin/agent-director-admin"} {
		out, err := exec.Command(filepath.Join(root, leaf)).CombinedOutput()
		must(t, err)
		if string(out) != stamp+"\n" {
			t.Fatalf("permanent entry %s executes %q, want %q", leaf, out, stamp)
		}
	}
	return v
}

func installed(t testing.TB, root string) []InstalledVersion {
	t.Helper()
	v, err := ListInstalled(root)
	must(t, err)
	return v
}

func assertSame(t testing.TB, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func writeJSON(t testing.TB, path string, value any) {
	t.Helper()
	b, err := json.Marshal(value)
	must(t, err)
	must(t, os.WriteFile(path, b, 0600))
}

func rehashRecord(t testing.TB, directory string, edit func(*VersionRecord)) {
	t.Helper()
	var r VersionRecord
	must(t, json.Unmarshal(bytesAt(t, filepath.Join(directory, "version.json")), &r))
	edit(&r)
	writeJSON(t, filepath.Join(directory, "version.json"), r)
	hash := sha256.Sum256(bytesAt(t, filepath.Join(directory, "version.json")))
	must(t, os.WriteFile(filepath.Join(directory, "ready"), []byte(fmt.Sprintf("%x\n", hash)), 0600))
}

type childRequest struct {
	Root       string
	Candidate  candidate
	Rollback   string
	CrashIndex int
	HoldPoint  string
}

func runRequest(r childRequest, hooks Hooks) (Result, error) {
	if r.Rollback != "" {
		return (Engine{Root: r.Root, Hooks: hooks}).Rollback(r.Rollback)
	}
	return publishCandidate(r.Root, r.Candidate, hooks)
}

// TestPublicationChild uses the existing admin-test convention: re-exec the
// test executable with a private request, and use pipes as causal barriers.
func TestPublicationChild(t *testing.T) {
	path := os.Getenv("AD_INSTALLVERSION_TEST_REQUEST")
	if path == "" {
		return
	}
	var r childRequest
	must(t, json.Unmarshal(bytesAt(t, path), &r))
	index := 0
	_, err := runRequest(r, Hooks{Observe: func(point, path string) error {
		index++
		if index == r.CrashIndex {
			fmt.Fprintln(os.Stdout, point)
			os.Exit(77) // No deferred file cleanup or lock release.
		}
		if point == r.HoldPoint {
			fmt.Fprintln(os.Stdout, "held")
			var b [1]byte
			_, err := os.Stdin.Read(b[:])
			return err
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func childCommand(t testing.TB, request childRequest) *exec.Cmd {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "request.json")
	writeJSON(t, path, request)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPublicationChild$")
	cmd.Env = append(os.Environ(), "HOME="+dir, "AD_INSTALLVERSION_TEST_REQUEST="+path)
	return cmd
}

var injectedIO = errors.New("injected filesystem failure")

func stopAt(point string) Hooks {
	return Hooks{Observe: func(got, _ string) error {
		if got == point {
			return injectedIO
		}
		return nil
	}}
}
