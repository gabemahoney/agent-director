//go:build linux || darwin

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestResumeDelegationRejectsOldCapabilityContract(t *testing.T) {
	// Serial: scratch HOME; reuse the selector's no-launch/custody observers.
	// b.kii PM1: the old v21 tag alone cannot authorize a covering runtime.
	e := newDelegateEnv(t)
	e.ready(t, "pre-capability", 1, []string{"claude", "codex"}, "45bbc95a6429e2f483f3ab4bc1cc0b8d12d40f607e06b6bf4416d87fff1cb7f7", true)
	e.ready(t, "intermediate-capability", 2, []string{"claude", "codex"}, "1d3840eb6589d5cd258eecec4f9f7514a225881e96b95ba0896c6abceaae061c", true)
	e.run(t, func(resumedelegate.Request) ([]byte, bool, error) {
		t.Fatal("delegated to an incompatible capability contract")
		return nil, false, nil
	})
	check := e.untouched(t)
	_, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID})
	assertOneName(t, err, "ErrSpawnNotResumable")
	want := api.NotResumableDetails{Cause: "covering_version_unavailable", Provider: "codex"}
	if !reflect.DeepEqual(api.ErrDetails(err), want) || len(e.calls) != 0 {
		t.Fatalf("resume = %v / %+v, calls=%d; want %+v without a launch", err, api.ErrDetails(err), len(e.calls), want)
	}
	check()
}

func TestResumeDelegationRetainedBeforeFloorGrowth(t *testing.T) {
	// Serial: scratch HOME; one selector fixture for retained-ready versus
	// newly refused candidates, not a second publication-validation matrix.
	e := newDelegateEnv(t)
	core, digest := store.BusySendCoreContractIdentity()
	publish := func(stamp string, agents []string, hooks installversion.Hooks) error {
		facts := installversion.Facts{FormatVersion: 1, Version: stamp, Commit: stamp, CoveredAgents: agents, CoreVersion: core, CoreDeclarationsSHA256: digest}
		b, _ := json.Marshal(facts)
		dir := t.TempDir()
		for _, name := range []string{layout.BinaryName, layout.AdminBinaryName} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' '"+string(b)+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
		}
		_, err := (installversion.Engine{Root: e.root, Hooks: hooks}).Publish(context.Background(), filepath.Join(dir, layout.BinaryName), filepath.Join(dir, layout.AdminBinaryName), facts)
		return err
	}
	base := []string{"claude", "codex"}
	if err := publish("base", base, installversion.Hooks{}); err != nil {
		t.Fatal(err)
	}
	// Richer seq2 is ready but has no publication intent yet. Seq3 is then
	// legitimately validated under the old floor, before seq2 is published.
	stopped := false
	err := publish("richer", []string{"claude", "codex", "droid"}, installversion.Hooks{Observe: func(point, path string) error {
		if point == "after:ready.parent-sync" {
			stopped = true
			return errors.New("stop before publication")
		}
		return nil
	}})
	if err == nil || !stopped {
		t.Fatalf("ready barrier not reached: %v", err)
	}
	if err := publish("retained", base, installversion.Hooks{}); err != nil {
		t.Fatal(err)
	}
	if err := publish("richer", []string{"claude", "codex", "droid"}, installversion.Hooks{}); err != nil {
		t.Fatal(err)
	}
	err = publish("new-refused", base, installversion.Hooks{})
	var refused *installversion.Refusal
	if !errors.As(err, &refused) || refused.Reason != "coverage_regression" {
		t.Fatalf("new candidate: %v", err)
	}
	installed, err := installversion.ListInstalled(e.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range installed {
		if v.Version == "new-refused" {
			t.Fatal("refused candidate became ready")
		}
	}
	want := filepath.Join(layout.VersionDir(e.root, "retained"), layout.BinaryName)
	e.run(t, func(resumedelegate.Request) ([]byte, bool, error) { return delegateResult(e.row.ID), true, nil })
	check := e.untouched(t)
	if _, err := e.c.Resume(api.ResumeParams{InstanceID: e.row.ID}); err != nil {
		t.Fatal(err)
	}
	if len(e.paths) != 1 || e.paths[0] != want {
		t.Fatalf("selector reapplied the floor or followed current: %v; want %s", e.paths, want)
	}
	check()
}

func TestResumeRuntimeIndependentOfInstallLock(t *testing.T) {
	// Serial: effective HOME; external advisory-lock holder.
	e := newDelegateEnv(t)
	e.covering(t)
	stop := holdBusyResource(t, "install", filepath.Join(e.root, layout.InstallLock))
	defer stop()
	// Neither a usable current link nor initialization metadata is needed.
	if err := os.WriteFile(filepath.Join(e.root, layout.CurrentVersion), []byte("unusable current"), 0600); err != nil {
		t.Fatal(err)
	}
	before := delegateFiles(t, e.home)
	finished := make(chan error, 1)
	go func() {
		c, err := api.New(api.Options{StorePath: e.dbPath, ConfigPath: filepath.Join(filepath.Dir(e.dbPath), "config.toml"), TmuxClient: e.rec})
		if err != nil {
			finished <- err
			return
		}
		defer c.Close()
		_, err = c.GetDelivery(api.GetDeliveryParams{CallerUUID: busyCaller, DeliveryRequestID: "unknown"})
		if err != nil {
			finished <- err
			return
		}
		api.SetResumeDelegateRunnerForTest(c, func(_ context.Context, path string, req resumedelegate.Request, _ time.Duration) ([]byte, bool, error) {
			if path != filepath.Join(layout.VersionDir(e.root, "covering"), layout.BinaryName) {
				return nil, false, errors.New("wrong selected executable")
			}
			return delegateResult(e.row.ID), true, nil
		})
		_, err = c.Resume(api.ResumeParams{InstanceID: e.row.ID})
		finished <- err
	}()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		stop()
		<-finished
		t.Fatal("runtime waited for installation lock")
	}
	if !reflect.DeepEqual(before, delegateFiles(t, e.home)) {
		t.Fatal("runtime initialized/repaired an installation")
	}
	for path := range delegateFiles(t, e.home) {
		if strings.Contains(filepath.Base(path), "migration") {
			t.Fatalf("runtime created migration marker %s", path)
		}
	}
}
