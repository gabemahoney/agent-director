//go:build linux || darwin

package main_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
)

func TestVersionAdminFacts(t *testing.T) {
	home := t.TempDir()
	core, digest := store.BusySendCoreContractIdentity()
	// b.kii PM1 deliberately replaces the unreleased v21 declarations.
	// Pin independently so runtime/admin agreeing on stale facts cannot pass.
	if core != "busy-send-core-v1:" || digest != "48949be96b1ee5db31161ee71b9d2aa594003801298819194c64b05446612601" {
		t.Fatalf("compiled core = %s/%s; want final capability contract", core, digest)
	}
	covered := store.BusySendCoveredAgents()
	slices.Sort(covered)
	want := installversion.Facts{FormatVersion: 1, Version: testVersion, Commit: testCommit,
		CoreVersion: core, CoreDeclarationsSHA256: digest, CoveredAgents: covered}
	for _, runtime := range []bool{false, true} {
		bin := adminPath
		if runtime {
			bin = mainPath
		}
		got := versionFactsOf(t, bin, home, runtime)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("facts = %+v, want %+v", got, want)
		}
	}
	if _, err := os.Lstat(layout.Dir(home)); !os.IsNotExist(err) {
		t.Fatalf("facts created root: %v", err)
	}
	// The private handshake is absent from the public help/manifest; version
	// aliases and their exact JSON are already covered by TestVersionMatchesMainBinary.
	for _, args := range [][]string{{"help"}, {"--help"}} {
		out, stderr, code := runMain(t, home, args...)
		if code != 0 || stderr != "" || strings.Contains(out, "install-version-facts") {
			t.Fatalf("public help: %d %s %s", code, out, stderr)
		}
	}
	out, stderr, code := runMain(t, home, "--install-version-facts", "extra")
	if code != 1 || out != "" || stderr != "" {
		t.Fatalf("private malformed argv: %d %q %q", code, out, stderr)
	}
}

func TestVersionAdminOutcomes(t *testing.T) {
	t.Run("establish-reuse-list", func(t *testing.T) {
		home := poisonedHome(t)
		root := layout.Dir(home)
		// Today's regular executable entries, replaced by publish-version itself.
		for rel, bin := range map[string]string{"bin/agent-director": mainPath, "admin/agent-director-admin": adminPath} {
			path := filepath.Join(root, rel)
			versionMust(t, os.MkdirAll(filepath.Dir(path), 0700))
			data, err := os.ReadFile(bin)
			versionMust(t, err)
			versionMust(t, os.WriteFile(path, data, 0700))
		}
		for _, reused := range []bool{false, true} {
			out, stderr, code := runAdmin(t, home, "publish-version", "--binary", mainPath)
			var got installversion.Result
			versionSuccess(t, out, stderr, code, &got)
			versionResultFields(t, []byte(out), 8)
			want := installversion.Result{VersionID: layout.VersionID(testVersion), Version: testVersion,
				PublishedVersion: &got.Version, PublicationState: "published", Durable: true, Reused: reused, InitializationComplete: true}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("result=%+v want %+v", got, want)
			}
		}
		for rel, target := range map[string]string{"bin/agent-director": "../current/agent-director", "admin/agent-director-admin": "../current/agent-director-admin"} {
			got, err := os.Readlink(filepath.Join(root, rel))
			versionMust(t, err)
			if got != target {
				t.Fatalf("%s -> %s", rel, got)
			}
		}
		info, err := os.Stat(filepath.Join(root, "admin"))
		versionMust(t, err)
		if info.Mode().Perm() != 0700 {
			t.Fatalf("admin mode %o", info.Mode().Perm())
		}
		init, err := installversion.ReadInitialization(root)
		versionMust(t, err)
		if !init.Complete || init.InitialVersionID != layout.VersionID(testVersion) {
			t.Fatalf("initial evidence %+v", init)
		}
		holdVersionLock(t, root) // List, facts and help must remain usable under it.
		out, stderr, code := runAdmin(t, home, "list-versions")
		var list adminapi.InstalledVersions
		versionSuccess(t, out, stderr, code, &list)
		if len(list.Versions) != 1 || list.PublishedVersion == nil || *list.PublishedVersion != testVersion {
			t.Fatalf("list=%+v", list)
		}
		v := list.Versions[0]
		if v.VersionID != layout.VersionID(testVersion) || v.Directory != layout.VersionDir(root, testVersion) || v.RuntimeExecutable != filepath.Join(v.Directory, "agent-director") || !filepath.IsAbs(v.Directory) {
			t.Fatalf("retained paths %+v", v)
		}
		if !reflect.DeepEqual(v.Facts, versionFactsOf(t, adminPath, home, false)) {
			t.Fatalf("retained facts %+v", v)
		}
		for _, args := range [][]string{{"version-facts"}, {"publish-version", "--help"}} {
			_, stderr, code := runAdmin(t, home, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("read under lock: %d %s", code, stderr)
			}
		}
		assertInvocationKinds(t, home)
	})
	for _, reason := range []string{"core_mismatch", "coverage_regression", "layout_invalid", "io_failure"} {
		t.Run(reason, func(t *testing.T) {
			home := poisonedHome(t)
			root := layout.Dir(home)
			f := versionFactsOf(t, adminPath, home, false)
			f.Version = "previous"
			switch reason {
			case "core_mismatch":
				f.CoreVersion = "different-permanent-core"
			case "coverage_regression":
				f.CoveredAgents = append(f.CoveredAgents, "codex")
			}
			_, err := seedVersion(t, root, f, "")
			versionMust(t, err)
			args := []string{"publish-version", "--binary", mainPath}
			if reason == "layout_invalid" {
				args = []string{"rollback-version", "--version", "not-retained"}
			}
			if reason == "io_failure" {
				args[2] = filepath.Join(home, "absent")
			}
			before := versionTree(t, root)
			out, stderr, code := runAdmin(t, home, args...)
			r := versionRefusal(t, out, stderr, code, reason)
			if r.PublishedVersion == nil || *r.PublishedVersion != f.Version || r.PublicationState != "not_published" || r.Durable || r.RecoveredVersion != nil || !r.InitializationComplete {
				t.Fatalf("refusal facts=%+v", r)
			}
			versionUnchanged(t, root, before)
		})
	}
}

func TestVersionAdminRuntimeFactsMismatch(t *testing.T) {
	for _, variant := range []string{"core", "coverage"} {
		t.Run(variant, func(t *testing.T) {
			p := versionVariant(t, variant)
			home := poisonedHome(t)
			root := layout.Dir(home)
			admin := versionFactsOf(t, adminPath, home, false)
			runtime := versionFactsOf(t, p.runtime, home, true)
			if runtime.Version != admin.Version || runtime.Commit != admin.Commit || runtime.Equal(admin) {
				t.Fatalf("not a same-stamp differing pair: %+v %+v", runtime, admin)
			}
			old := admin
			old.Version = "previous"
			_, err := seedVersion(t, root, old, "")
			versionMust(t, err)
			before := versionTree(t, root)
			out, stderr, code := runAdmin(t, home, "publish-version", "--binary", p.runtime)
			r := versionRefusal(t, out, stderr, code, "version_conflict")
			if r.Version != testVersion || r.VersionID != layout.VersionID(testVersion) || r.PublishedVersion == nil || *r.PublishedVersion != "previous" || r.PublicationState != "not_published" || r.Durable {
				t.Fatalf("mismatch facts=%+v", r)
			}
			versionUnchanged(t, root, before) // Includes ready inventory, anchor and floor history.
		})
	}
}

func TestVersionAdminRetainedExecution(t *testing.T) {
	home := poisonedHome(t)
	root := layout.Dir(home)
	later := versionVariant(t, "later")
	var result installversion.Result
	out, stderr, code := runAdmin(t, home, "publish-version", "--binary", mainPath)
	versionSuccess(t, out, stderr, code, &result)
	retained := layout.VersionDir(root, testVersion)
	before := versionTree(t, retained)
	paths := []string{}
	for _, entry := range []string{"bin/agent-director", "admin/agent-director-admin"} {
		path, err := filepath.EvalSymlinks(filepath.Join(root, entry))
		versionMust(t, err)
		if filepath.Dir(path) != retained {
			t.Fatalf("capture=%s", path)
		}
		paths = append(paths, path)
	}
	out, stderr, code = runBin(t, later.admin, home, "publish-version", "--binary", later.runtime)
	versionSuccess(t, out, stderr, code, &result)
	if result.Version != testVersion+"-later" {
		t.Fatalf("later result=%+v", result)
	}
	for _, rollback := range []bool{false, true} {
		if rollback {
			out, stderr, code = runBin(t, later.admin, home, "rollback-version", "--version", testVersion)
			versionSuccess(t, out, stderr, code, &result)
			if result.PublishedVersion == nil || *result.PublishedVersion != testVersion || !result.Reused || !result.Durable {
				t.Fatalf("rollback=%+v", result)
			}
		}
		// These direct starts have no registration or previous process observation.
		for _, path := range paths {
			out, stderr, code = runBin(t, path, home, "version")
			var got map[string]any
			versionSuccess(t, out, stderr, code, &got)
			if !reflect.DeepEqual(got, map[string]any{"version": testVersion, "commit": testCommit, "config_dir": "~/.config/agent-director"}) {
				t.Fatalf("retained execution=%s", out)
			}
		}
		versionUnchanged(t, retained, before)
	}
	versions, err := installversion.ListInstalled(root)
	versionMust(t, err)
	if len(versions) != 2 {
		t.Fatalf("retained=%+v", versions)
	}
	for _, v := range versions {
		if len(v.Files) != 2 || v.Files[0].Path != "agent-director" || v.Files[1].Path != "agent-director-admin" {
			t.Fatalf("inventory=%+v", v.Files)
		}
		for _, f := range v.Files {
			info, err := os.Stat(filepath.Join(v.Directory, f.Path))
			versionMust(t, err)
			if info.Mode().Perm()&0100 == 0 || uint32(info.Mode().Perm()) != f.Mode || info.Size() != f.Size {
				t.Fatalf("resource=%+v mode=%o", f, info.Mode())
			}
		}
	}
	assertInvocationKinds(t, home)
}

// H9: follow the exact lock advice by releasing only the competing lock,
// then repeat the identical argv; no store or layout repair is prescribed.
func TestAdviceFollow_H9_VersionLockRetry(t *testing.T) {
	home := poisonedHome(t)
	root := layout.Dir(home)
	unlock := holdVersionLock(t, root)
	args := []string{"publish-version", "--binary", mainPath}
	env := append(fakeTmuxEnv(t, home), "HOME="+home)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, stderr, code := runBinInContext(t, ctx, adminPath, "", env, args...)
	versionRefusal(t, out, stderr, code, "lock_busy")
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("nonwaiting lock refusal took %v", elapsed)
	}
	var envelope errorEnvelope
	versionJSON(t, stderr, &envelope)
	if envelope.ErrDescription != "version installation refused: another administrator holds the installation lock; retry later" {
		t.Fatalf("advice=%q", envelope.ErrDescription)
	}
	unlock()
	out, stderr, code = runAdmin(t, home, args...)
	var got installversion.Result
	versionSuccess(t, out, stderr, code, &got)
	if !got.Durable || !got.InitializationComplete || got.PublishedVersion == nil || *got.PublishedVersion != testVersion {
		t.Fatalf("retry=%+v", got)
	}
}

// H10: restore only the absent candidate, then rerun the same operation.
func TestAdviceFollow_H10_VersionIORetry(t *testing.T) {
	home := poisonedHome(t)
	path := filepath.Join(t.TempDir(), "candidate")
	args := []string{"publish-version", "--binary", path}
	out, stderr, code := runAdmin(t, home, args...)
	versionRefusal(t, out, stderr, code, "io_failure")
	var env errorEnvelope
	versionJSON(t, stderr, &env)
	if env.ErrDescription != "version installation I/O failed; rerun the same operation to complete forward recovery" {
		t.Fatalf("advice=%q", env.ErrDescription)
	}
	data, err := os.ReadFile(mainPath)
	versionMust(t, err)
	versionMust(t, os.WriteFile(path, data, 0700))
	out, stderr, code = runAdmin(t, home, args...)
	var got installversion.Result
	versionSuccess(t, out, stderr, code, &got)
	if !got.Durable || !got.InitializationComplete {
		t.Fatalf("retry=%+v", got)
	}
}

// Keep nulls on the wire: omitting them would invent knowledge for callers.
func TestVersionAdminEmptyList(t *testing.T) {
	home := t.TempDir()
	out, stderr, code := runAdmin(t, home, "list-versions")
	var got map[string]json.RawMessage
	versionSuccess(t, out, stderr, code, &got)
	if len(got) != 2 || string(got["versions"]) != "[]" || string(got["published_version"]) != "null" {
		t.Fatalf("empty list=%s", out)
	}
	if _, err := os.Stat(layout.Dir(home)); !os.IsNotExist(err) {
		t.Fatalf("list created root: %v", err)
	}
}

func TestVersionAdminArgumentRefusals(t *testing.T) {
	verbs := [][]string{{"version-facts"}, {"list-versions"}, {"publish-version", "--binary", mainPath}, {"rollback-version", "--version", testVersion}}
	flags := [][]string{
		{"--store-path", ""}, {"--store-path="}, {"--store-path=state.db"},
		{"--tmux-command", "tmux"}, {"--tmux-command="}, {"--tmux-command=tmux"},
		{"--create-if-missing", "true"}, {"--create-if-missing", "false"},
		{"--create-if-missing=true"}, {"--create-if-missing=false"},
	}
	for _, verb := range verbs {
		for _, flag := range flags {
			for _, after := range []bool{false, true} {
				t.Run(strings.Join(verb[:1], "")+"/"+strings.Join(flag, " ")+"/"+map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
					home := poisonedHome(t)
					before := versionTree(t, layout.Dir(home))
					out, stderr, code := runAdmin(t, home, withFlags(flag, after, verb...)...)
					assertOnlyEnvelope(t, out, stderr, code, "ErrInvalidFlags")
					versionUnchanged(t, layout.Dir(home), before)
					assertInvocationKinds(t, home)
				})
			}
		}
	}
	for _, args := range [][]string{
		{"publish-version", "--binary"}, {"publish-version", "--binary="},
		{"rollback-version", "--version"}, {"rollback-version", "--version="},
		{"version-facts", "extra"}, {"list-versions", "--unknown"},
		{"publish-version", "--binary", mainPath, "extra"}, {"rollback-version", "--version", testVersion, "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := poisonedHome(t)
			before := versionTree(t, layout.Dir(home))
			out, stderr, code := runAdmin(t, home, args...)
			assertOnlyEnvelope(t, out, stderr, code, "ErrInvalidFlags")
			versionUnchanged(t, layout.Dir(home), before)
		})
	}
}
