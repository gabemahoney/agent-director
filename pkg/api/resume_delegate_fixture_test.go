package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// delegateEnv adds only a scratch retained layout and transport observer to
// resumeEnv; rows, clock, store and no-input recorder remain its shared fixtures.
// Callers are serial because the effective HOME is part of the public contract.
type delegateEnv struct {
	*resumeEnv
	home, root string
	row        resumableRow
	calls      []resumedelegate.Request
	paths      []string
}

func newDelegateEnv(t *testing.T) *delegateEnv {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	e := &delegateEnv{resumeEnv: newResumeEnv(t), home: home, root: layout.Dir(home)}
	e.row = e.seedResumable(t, store.StateEnded, apitest.WithAgent("codex"))
	if err := apitest.SeedBusySendCoverage(e.dbPath, e.row.ID, store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	return e
}

// ready writes a retained-record fixture, not an installer. Publication and
// record-validation matrices belong to installversion; the selector consumes
// real files and their inventories here. No shared cross-writer builder.
func (e *delegateEnv) ready(t *testing.T, stamp string, sequence uint64, agents []string, digest string, ready bool, executable ...[]byte) string {
	t.Helper()
	core, currentDigest := store.BusySendCoreContractIdentity()
	if digest == "" {
		digest = currentDigest
	}
	dir := filepath.Join(e.root, layout.VersionsDir, layout.VersionID(stamp))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := installversion.VersionRecord{Facts: installversion.Facts{FormatVersion: 1, Version: stamp, Commit: "test-" + stamp,
		CoveredAgents: agents, CoreVersion: core, CoreDeclarationsSHA256: digest}, InstalledSequence: sequence}
	for _, name := range []string{layout.BinaryName, layout.AdminBinaryName} {
		data := []byte("#!/bin/sh\nexit 91\n") // an accidental real launch fails, never starts an agent
		if name == layout.BinaryName && len(executable) != 0 {
			data = executable[0]
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
		record.Files = append(record.Files, installversion.FileRecord{Path: name, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Size: int64(len(data)), Mode: 0700})
	}
	b, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, layout.VersionRecord), b, 0600); err != nil {
		t.Fatal(err)
	}
	if ready {
		if err := os.WriteFile(filepath.Join(dir, layout.VersionReady), []byte(fmt.Sprintf("%x\n", sha256.Sum256(b))), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, layout.BinaryName)
}

func (e *delegateEnv) covering(t *testing.T) string {
	t.Helper()
	return e.ready(t, "covering", 1, []string{"claude", "codex"}, "", true)
}

func (e *delegateEnv) run(t *testing.T, body func(resumedelegate.Request) ([]byte, bool, error)) {
	t.Helper()
	api.SetResumeDelegateRunnerForTest(e.c, func(ctx context.Context, path string, req resumedelegate.Request, wait time.Duration) ([]byte, bool, error) {
		e.calls = append(e.calls, req)
		e.paths = append(e.paths, path)
		if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(req.Deadline) || wait != e.cfg.Tmux.EffectivePipeCloseWait() {
			t.Errorf("deadline/wait changed: %v %t / %v, request %v", deadline, ok, wait, req.Deadline)
		}
		return body(req)
	})
}

func delegateResult(id string) []byte {
	b, _ := json.Marshal(resumedelegate.Response{ResponseFormat: 1, Result: json.RawMessage(fmt.Sprintf(`{"instance_id":%q,"pre_trust":"skipped","future":false,"nullable":null}`, id))})
	return b
}

// assertUntouched observes persisted row/default/custody facts, every tmux call
// and the trust files. It also forbids entry into resumeAfterJsonl's trail.
func (e *delegateEnv) untouched(t *testing.T) func() {
	t.Helper()
	row := e.columns(t, e.row.ID)
	facts := busyFacts(t, e.dbPath, e.row.ID)
	calls := e.rec.SocketCalls()
	trust := delegateFiles(t, e.home)
	mark := len(readAPITrailLines(t))
	return func() {
		t.Helper()
		rstAssertRow(t, e.resumeEnv, e.row.ID, row)
		if after := busyFacts(t, e.dbPath, e.row.ID); !reflect.DeepEqual(after, facts) {
			t.Errorf("delegator changed core: before %+v; after %+v", facts, after)
		}
		if !reflect.DeepEqual(calls, e.rec.SocketCalls()) {
			t.Errorf("delegator reached tmux: %+v", e.rec.SocketCalls())
		}
		if got := delegateFiles(t, e.home); !reflect.DeepEqual(got, trust) {
			t.Errorf("delegator changed files: before %v; after %v", trust, got)
		}
		assertResumeEvents(t, mark, e.row.ID)
	}
}

func delegateFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() == layout.TrailFile || entry.Name() == layout.ErrorLogFile {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			out[path], err = os.Readlink(path)
		} else {
			var b []byte
			b, err = os.ReadFile(path)
			out[path] = string(b)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
