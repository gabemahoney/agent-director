//go:build linux || darwin

package main_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"golang.org/x/sys/unix"
)

func versionMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func versionJSON(t testing.TB, text string, value any) {
	t.Helper()
	versionMust(t, json.Unmarshal([]byte(text), value))
}

func versionSuccess(t testing.TB, stdout, stderr string, code int, value any) {
	t.Helper()
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	versionJSON(t, stdout, value)
}

func versionRefusal(t *testing.T, stdout, stderr string, code int, reason string) installversion.Refusal {
	t.Helper()
	assertOnlyEnvelope(t, stdout, stderr, code, "ErrVersionInstallRefused")
	var envelope struct {
		Details installversion.Refusal `json:"details"`
	}
	versionJSON(t, stderr, &envelope)
	r := envelope.Details
	if r.Reason != reason || r.Retryable != (reason == "io_failure" || reason == "lock_busy") {
		t.Fatalf("details=%+v, want reason %s", r, reason)
	}
	var wire map[string]json.RawMessage
	versionJSON(t, stderr, &wire)
	if len(wire) != 3 {
		t.Fatalf("envelope keys: %s", stderr)
	}
	versionResultFields(t, wire["details"], 10)
	return r
}

func versionResultFields(t testing.TB, raw []byte, size int) {
	t.Helper()
	var fields map[string]json.RawMessage
	versionJSON(t, string(raw), &fields)
	if len(fields) != size {
		t.Fatalf("result field count: %s", raw)
	}
	for _, key := range []string{"version_id", "version", "published_version", "publication_state", "durable", "reused", "recovered_version", "initialization_complete"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing %s: %s", key, raw)
		}
	}
}

func versionFactsOf(t *testing.T, bin, home string, runtime bool) installversion.Facts {
	t.Helper()
	arg := "version-facts"
	if runtime {
		arg = "--install-version-facts"
	}
	out, stderr, code := runBin(t, bin, home, arg)
	var f installversion.Facts
	versionSuccess(t, out, stderr, code, &f)
	var fields map[string]json.RawMessage
	versionJSON(t, out, &fields)
	if len(fields) != 6 {
		t.Fatalf("facts fields=%s", out)
	}
	return f
}

type versionPair struct{ runtime, admin string }

// versionVariant reuses TestMain's stamp, build cache and cleanup directory.
// Go's source overlay changes only temporary files; the shared tree is never written.
var versionVariants = map[string]versionPair{}

func versionVariant(t *testing.T, name string) versionPair {
	t.Helper()
	if p, ok := versionVariants[name]; ok {
		return p
	}
	dir := filepath.Join(filepath.Dir(adminPath), "version-"+name)
	versionMust(t, os.Mkdir(dir, 0700))
	root, err := filepath.Abs("../..")
	versionMust(t, err)
	stamp := testVersion
	args := []string{"build"}
	if name == "later" {
		stamp += "-later"
	} else {
		rel, old, replacement := "internal/store/busy_send_schema.go", "busy-send-core-v1:", "busy-send-core-test:"
		if name == "coverage" {
			rel, old, replacement = "internal/store/busy_send_coverage.go", "[...]string{AgentClaude}", "[...]string{AgentClaude, AgentCodex}"
		}
		original := filepath.Join(root, rel)
		data, err := os.ReadFile(original)
		versionMust(t, err)
		if strings.Count(string(data), old) != 1 {
			t.Fatalf("variant %s replacement no longer unique", name)
		}
		changed := filepath.Join(dir, "changed.go")
		versionMust(t, os.WriteFile(changed, []byte(strings.Replace(string(data), old, replacement, 1)), 0600))
		mapping, err := json.Marshal(map[string]any{"Replace": map[string]string{original: changed}})
		versionMust(t, err)
		overlay := filepath.Join(dir, "overlay.json")
		versionMust(t, os.WriteFile(overlay, mapping, 0600))
		args = append(args, "-overlay="+overlay)
	}
	args = append(args, "-ldflags=-X github.com/gabemahoney/agent-director/internal/version.Version="+stamp+" -X github.com/gabemahoney/agent-director/internal/version.Commit="+testCommit)
	pair := versionPair{filepath.Join(dir, "agent-director"), adminPath}
	build := func(out, pkg string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", append(append([]string{}, args...), "-o", out, pkg)...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("variant %s: %v\n%s", name, err, out)
		}
	}
	build(pair.runtime, "./cmd/agent-director")
	if name == "later" {
		pair.admin = filepath.Join(dir, "agent-director-admin")
		build(pair.admin, "./cmd/agent-director-admin")
	}
	versionVariants[name] = pair
	return pair
}

// seedVersion uses the real engine to construct valid old-version history.
// Only the external executable facts response is synthetic, as in qa's fixture.
func seedVersion(t *testing.T, root string, facts installversion.Facts, point string) (installversion.Result, error) {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(facts)
	versionMust(t, err)
	for name, arg := range map[string]string{"agent-director": "--install-version-facts", "agent-director-admin": "version-facts"} {
		text := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = '" + arg + "' ] || exit 41\nprintf '%s\\n' '" + strings.ReplaceAll(string(b), "'", "'\"'\"'") + "'\n"
		versionMust(t, os.WriteFile(filepath.Join(dir, name), []byte(text), 0700))
	}
	hooks := installversion.Hooks{Observe: func(got, _ string) error {
		if point != "" && got == point {
			return fmt.Errorf("injected %s", point)
		}
		return nil
	}}
	return (installversion.Engine{Root: root, Hooks: hooks}).Publish(context.Background(), filepath.Join(dir, "agent-director"), filepath.Join(dir, "agent-director-admin"), facts)
}

type versionFile struct {
	Mode    fs.FileMode
	ModTime time.Time
	Hash    [32]byte
	Link    string
}

func versionTree(t testing.TB, root string) map[string]versionFile {
	t.Helper()
	result := map[string]versionFile{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
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
		f := versionFile{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			f.ModTime = info.ModTime()
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			f.Hash = sha256.Sum256(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			f.Link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		result[rel] = f
		return nil
	})
	versionMust(t, err)
	return result
}

func versionUnchanged(t testing.TB, root string, before map[string]versionFile) {
	t.Helper()
	if after := versionTree(t, root); !reflect.DeepEqual(after, before) {
		t.Fatalf("tree changed under %s", root)
	}
}

func holdVersionLock(t *testing.T, root string) func() {
	t.Helper()
	versionMust(t, os.MkdirAll(root, 0700))
	f, err := os.OpenFile(filepath.Join(root, layout.InstallLock), os.O_CREATE|os.O_RDWR, 0600)
	versionMust(t, err)
	versionMust(t, unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB))
	t.Cleanup(func() { _ = f.Close() })
	return func() { versionMust(t, unix.Flock(int(f.Fd()), unix.LOCK_UN)) }
}
