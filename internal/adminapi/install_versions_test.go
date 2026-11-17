//go:build linux || darwin

package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
)

// versionAdapterPair substitutes only the external executable handshake.
// Real packaged-pair tests live in cmd/agent-director-admin, built there once.
func versionAdapterPair(t *testing.T, facts installversion.Facts) (string, string) {
	t.Helper()
	dir := t.TempDir()
	b, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	for name, arg := range map[string]string{"agent-director": "--install-version-facts", "agent-director-admin": "version-facts"} {
		data := "#!/bin/sh\n[ \"$#\" = 1 ] && [ \"$1\" = '" + arg + "' ] || exit 41\nprintf '%s\\n' '" + strings.ReplaceAll(string(b), "'", "'\"'\"'") + "'\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "agent-director"), filepath.Join(dir, "agent-director-admin")
}

func TestVersionAdminAdapterOutcomes(t *testing.T) {
	for _, tc := range []struct {
		point                        string
		published, durable, complete bool
	}{
		{"before:current.rename", false, false, false},
		{"after:current.rename", true, false, false},
		{"after:current.parent-sync", true, true, false},
		{"before:bootstrap.runtime-entry.rename", true, true, false},
	} {
		t.Run(tc.point, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			root := layout.Dir(home)
			facts := VersionFacts()
			runtime, admin := versionAdapterPair(t, facts)
			failure := errors.New("injected I/O")
			engine := installversion.Engine{Root: root, Hooks: installversion.Hooks{Observe: func(point, _ string) error {
				if point == tc.point {
					return failure
				}
				return nil
			}}}
			got, err := publishVersion(context.Background(), engine, runtime, admin)
			var refusal *VersionInstallRefusal
			if !errors.As(err, &refusal) || !errors.Is(err, failure) || !errors.Is(err, &VersionInstallRefusal{}) || refusal.Reason != "io_failure" || !refusal.Retryable || !reflect.DeepEqual(refusal.Result, got) {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			want := installversion.Result{Version: facts.Version, VersionID: layout.VersionID(facts.Version), PublicationState: "not_published", Durable: tc.durable, InitializationComplete: tc.complete}
			if tc.published {
				want.PublishedVersion = &facts.Version
				want.PublicationState = "published"
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("result=%+v want=%+v", got, want)
			}
			// Follow the same I/O recovery advice literally: remove only the fault,
			// rerun the same request, then inspect actual initialization evidence.
			if err.Error() != "version installation I/O failed; rerun the same operation to complete forward recovery" {
				t.Fatalf("advice=%q", err)
			}
			engine.Hooks = installversion.Hooks{}
			retry, err := publishVersion(context.Background(), engine, runtime, admin)
			if err != nil || !retry.Durable || !retry.InitializationComplete || !retry.Reused || retry.PublishedVersion == nil || *retry.PublishedVersion != facts.Version {
				t.Fatalf("retry=%+v err=%v", retry, err)
			}
			evidence, err := installversion.ReadInitialization(root)
			if err != nil || !evidence.Complete {
				t.Fatalf("evidence=%+v err=%v", evidence, err)
			}
		})
	}
}

func TestVersionAdminAdapterRecoversBeforeRefusal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := layout.Dir(home)
	facts := VersionFacts()
	old := facts
	old.Version = "previous-expanded"
	old.CoveredAgents = append(slices.Clone(old.CoveredAgents), "codex")
	runtime, admin := versionAdapterPair(t, old)
	engine := installversion.Engine{Root: root, Hooks: installversion.Hooks{Observe: func(point, _ string) error {
		if point == "before:current.rename" {
			return errors.New("interrupted selection")
		}
		return nil
	}}}
	if _, err := engine.Publish(context.Background(), runtime, admin, old); err == nil {
		t.Fatal("fault did not fire")
	}
	runtime, admin = versionAdapterPair(t, facts)
	got, err := publishVersion(context.Background(), installversion.Engine{Root: root}, runtime, admin)
	var refusal *VersionInstallRefusal
	if !errors.As(err, &refusal) || refusal.Reason != "coverage_regression" || refusal.Retryable {
		t.Fatalf("result=%+v err=%v", got, err)
	}
	if got.RecoveredVersion == nil || *got.RecoveredVersion != old.Version || got.PublishedVersion == nil || *got.PublishedVersion != old.Version || got.Version != facts.Version || got.PublicationState != "not_published" || got.Durable || !got.InitializationComplete {
		t.Fatalf("recovery misreported=%+v", got)
	}
	list, err := ListVersions(root)
	if err != nil || len(list.Versions) != 1 || *list.PublishedVersion != old.Version {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

func TestVersionAdminAdapterBootstrapEvidence(t *testing.T) {
	for _, sufficient := range []bool{true, false} {
		t.Run(map[bool]string{true: "evidenced-overwrite", false: "missing-evidence"}[sufficient], func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			root := layout.Dir(home)
			runtime, admin := versionAdapterPair(t, VersionFacts())
			engine := installversion.Engine{Root: root}
			if _, err := publishVersion(context.Background(), engine, runtime, admin); err != nil {
				t.Fatal(err)
			}
			anchor, err := os.ReadFile(filepath.Join(root, layout.InstallContract))
			if err != nil {
				t.Fatal(err)
			}
			for _, rel := range []string{"bin/agent-director", "admin/agent-director-admin"} {
				path := filepath.Join(root, rel)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("old script overwrite"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if !sufficient {
				if err := os.Remove(filepath.Join(root, layout.InstallInitialization)); err != nil {
					t.Fatal(err)
				}
			}
			got, err := publishVersion(context.Background(), engine, runtime, admin)
			if sufficient {
				if err != nil || !got.InitializationComplete || !got.Durable {
					t.Fatalf("recovery=%+v err=%v", got, err)
				}
				for rel, target := range map[string]string{"bin/agent-director": "../current/agent-director", "admin/agent-director-admin": "../current/agent-director-admin"} {
					link, err := os.Readlink(filepath.Join(root, rel))
					if err != nil || link != target {
						t.Fatalf("repaired %s=%q %v", rel, link, err)
					}
				}
			} else {
				var refusal *VersionInstallRefusal
				if !errors.As(err, &refusal) || refusal.Reason != "layout_invalid" || got.InitializationComplete {
					t.Fatalf("missing evidence=%+v err=%v", got, err)
				}
			}
			after, err := os.ReadFile(filepath.Join(root, layout.InstallContract))
			if err != nil || string(after) != string(anchor) {
				t.Fatalf("anchor changed: %v", err)
			}
		})
	}
}
