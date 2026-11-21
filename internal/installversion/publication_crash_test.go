//go:build linux || darwin

package installversion

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
)

type crashEvent struct{ point, path string }

func crashSetup(t testing.TB, scenario string) (publicationFixture, childRequest) {
	t.Helper()
	f := newPublicationFixture(t)
	r := childRequest{Root: f.root, Candidate: f.a}
	switch scenario {
	case "fresh":
	case "legacy":
		for _, leaf := range []string{"bin/agent-director", "admin/agent-director-admin"} {
			path := filepath.Join(f.root, leaf)
			must(t, os.MkdirAll(filepath.Dir(path), 0700))
			must(t, os.WriteFile(path, []byte("#!/bin/sh\nprintf 'legacy\\n'\n"), 0700))
		}
	case "publish", "rollback", "recover-intent", "recover-final":
		publishOK(t, f.root, f.a)
		r.Candidate = f.b
		switch scenario {
		case "rollback":
			publishOK(t, f.root, f.b)
			r.Rollback = f.a.Facts.Version
		case "recover-intent", "recover-final":
			point := "before:current.rename"
			if scenario == "recover-final" {
				point = "after:stage.final-rename"
			}
			_, err := publishCandidate(f.root, f.b, stopAt(point))
			expectRefusal(t, err, "io_failure")
			assertUsablePublication(t, f.root, "old")
		}
	default:
		t.Fatalf("unknown crash scenario %s", scenario)
	}
	return f, r
}

func eventPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		if strings.HasPrefix(part, ".staging-") {
			parts[i] = "<stage>"
		}
		if strings.HasPrefix(part, ".tmp-") {
			parts[i] = "<temp>"
		}
		if len(part) == 64 {
			parts[i] = "<version>"
		}
	}
	return strings.Join(parts, "/")
}

func assertCrashVisibility(t testing.TB, f publicationFixture, scenario string) {
	t.Helper()
	v, err := ReadPublished(f.root)
	must(t, err)
	established := scenario == "publish" || scenario == "rollback" || scenario == "recover-intent" || scenario == "recover-final"
	if established && v == nil {
		t.Fatal("established installation lost its published entry before recovery")
	}
	if v != nil {
		if v.Version != "old" && v.Version != "new" {
			t.Fatalf("unexpected selection %+v", v)
		}
		if established {
			assertUsablePublication(t, f.root, v.Version)
		} else {
			assertSelected(t, f.root, v.Version)
		}
		anchor, err := ReadContract(f.root)
		must(t, err)
		if anchor != contractFor(v.Facts) {
			t.Fatal("selected incompatible executable")
		}
	}
	if scenario == "legacy" {
		for _, leaf := range []string{"bin/agent-director", "admin/agent-director-admin"} {
			path := filepath.Join(f.root, leaf)
			out, err := exec.Command(path).CombinedOutput()
			must(t, err)
			if string(out) != "legacy\n" && string(out) != "old\n" {
				t.Fatalf("entry %s unusable: %q", leaf, out)
			}
			if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 && v == nil {
				t.Fatal("permanent link precedes current")
			}
		}
	}
}

// TestPublicationCrashRecovery covers every observed before/after boundary,
// including repeated resources, through actual fsync/rename, abrupt exit, and
// conservative loss of unsynced writes. Every row logs its operation and path.
func TestPublicationCrashRecovery(t *testing.T) {
	for _, scenario := range []string{"fresh", "legacy", "publish", "rollback", "recover-intent", "recover-final"} {
		t.Run(scenario, func(t *testing.T) {
			f, request := crashSetup(t, scenario)
			var events []crashEvent
			_, err := runRequest(request, Hooks{Observe: func(point, path string) error {
				events = append(events, crashEvent{point, eventPath(f.root, path)})
				return nil
			}})
			must(t, err)
			if scenario == "fresh" {
				requireDurabilityBoundaries(t, events)
			}
			for index, event := range events {
				name := fmt.Sprintf("%03d_%s_%s", index+1, event.point, strings.ReplaceAll(event.path, "/", "_"))
				t.Run(name, func(t *testing.T) {
					for _, mode := range []string{"io-error", "process-death", "storage-loss"} {
						t.Run(mode, func(t *testing.T) {
							f, request := crashSetup(t, scenario)
							retained := snapshot(t, filepath.Join(f.root, "versions"))
							history := snapshot(t, filepath.Join(f.root, "publications"))
							// Preparation can deliberately leave an unfinished final;
							// it may gain ready, but no existing byte may change.
							if mode == "process-death" {
								request.CrashIndex = index + 1
								out, err := childCommand(t, request).CombinedOutput()
								var exit *exec.ExitError
								if !errors.As(err, &exit) || exit.ExitCode() != 77 || string(out) != event.point+"\n" {
									t.Fatalf("crash boundary %s: %v, %s", event.point, err, out)
								}
							} else {
								durable := newDurableImage(t, f.home)
								seen := 0
								hooks := Hooks{Sync: durable.sync, Observe: func(point, path string) error {
									seen++
									if seen != index+1 {
										return nil
									}
									assertSame(t, crashEvent{point, eventPath(f.root, path)}, event)
									return injectedIO
								}}
								_, err := runRequest(request, hooks)
								expectRefusal(t, err, "io_failure")
								if seen != index+1 {
									t.Fatalf("missed boundary: %d", seen)
								}
								if mode == "storage-loss" {
									restored := t.TempDir()
									durable.restore(restored)
									f.home, f.root = restored, filepath.Join(restored, ".config", "agent-director")
									request.Root = f.root
								}
							}
							assertPreserved(t, filepath.Join(f.root, "versions"), retained)
							assertPreserved(t, filepath.Join(f.root, "publications"), history)
							assertCrashVisibility(t, f, scenario)
							visible, err := ReadPublished(f.root)
							must(t, err)
							request.CrashIndex = 0
							_, err = runRequest(request, Hooks{})
							must(t, err)
							want := request.Candidate.Facts.Version
							if request.Rollback != "" {
								want = request.Rollback
							}
							if visible != nil && visible.Version == want {
								assertSelected(t, f.root, visible.Version)
							}
							assertEstablished(t, f.root, want)
							assertPreserved(t, filepath.Join(f.root, "versions"), retained)
							assertPreserved(t, filepath.Join(f.root, "publications"), history)
							stable := snapshot(t, f.root)
							_, err = runRequest(request, Hooks{})
							must(t, err)
							assertSame(t, snapshot(t, f.root), stable)
						})
					}
				})
			}
		})
	}
}

func requireDurabilityBoundaries(t testing.TB, events []crashEvent) {
	t.Helper()
	seen := map[string]bool{}
	for _, event := range events {
		seen[event.point] = true
	}
	ops := []string{"root.directory-sync", "root.parent-sync", "root.ancestor-sync", "lock.sync", "lock.parent-sync", "directory.sync", "directory.parent-sync",
		"stage.agent-director.file-sync", "stage.agent-director-admin.file-sync", "stage.facts", "stage.directory-sync", "stage.final-rename", "stage.final-parent-sync",
		"bootstrap.version-sync", "bootstrap.version-parent-sync", "ready.version-sync", "ready.final-parent-sync",
		"history.intent.recovery-sync", "history.intent.recovery-parent-sync", "current.rename", "current.parent-sync",
		"bootstrap.runtime-entry.rename", "bootstrap.runtime-entry.parent-sync", "bootstrap.admin-entry.rename", "bootstrap.admin-entry.parent-sync"}
	for _, record := range []string{"stage.record", "bootstrap.intent", "bootstrap.anchor", "ready", "history.intent", "history.completion", "bootstrap.completion"} {
		for _, suffix := range []string{"file-sync", "rename", "parent-sync"} {
			ops = append(ops, record+"."+suffix)
		}
	}
	for _, op := range ops {
		for _, prefix := range []string{"before:", "after:"} {
			if !seen[prefix+op] {
				t.Errorf("missing real durability boundary %s%s", prefix, op)
			}
		}
	}
}

func TestPublicationRenameDurability(t *testing.T) {
	for _, operation := range []string{"publish", "rollback"} {
		for _, survives := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/new-entry-survives=%t", operation, survives), func(t *testing.T) {
				f, r := crashSetup(t, operation)
				prior, want := "old", "new"
				if operation == "rollback" {
					prior, want = "new", "old"
				}
				d := newDurableImage(t, f.home)
				_, err := runRequest(r, Hooks{Sync: d.sync, Observe: stopAt("after:current.rename").Observe})
				ref := expectRefusal(t, err, "io_failure")
				if ref.PublicationState != "published" || ref.Durable {
					t.Fatalf("rename result %#v", ref)
				}
				assertUsablePublication(t, f.root, want)
				visible, err := os.Readlink(filepath.Join(f.root, "current"))
				must(t, err)
				restored := t.TempDir()
				d.restore(restored)
				r.Root = filepath.Join(restored, ".config", "agent-director")
				surviving := prior
				if survives {
					path := filepath.Join(r.Root, "current")
					must(t, os.Remove(path))
					must(t, os.Symlink(visible, path))
					surviving = want
				}
				assertUsablePublication(t, r.Root, surviving)
				_, err = runRequest(r, Hooks{})
				must(t, err)
				assertEstablished(t, r.Root, want)
			})
		}
	}
	for _, primitive := range []string{"rename", "sync"} {
		t.Run("actual-"+primitive+"-error", func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			armed := false
			h := Hooks{Observe: func(point, _ string) error {
				if point == "before:current."+map[string]string{"rename": "rename", "sync": "parent-sync"}[primitive] {
					armed = true
				}
				return nil
			}}
			h.Rename = func(a, b string) error {
				if primitive == "rename" && armed {
					return injectedIO
				}
				return os.Rename(a, b)
			}
			h.Sync = func(file *os.File) error {
				if primitive == "sync" && armed {
					return injectedIO
				}
				return file.Sync()
			}
			_, err := publishCandidate(f.root, f.b, h)
			ref := expectRefusal(t, err, "io_failure")
			want := "unknown"
			if primitive == "sync" {
				want = "published"
			}
			if ref.PublicationState != want || ref.Durable {
				t.Fatalf("primitive result %#v", ref)
			}
			selected := "old"
			if primitive == "sync" {
				selected = "new"
			}
			assertUsablePublication(t, f.root, selected)
			publishOK(t, f.root, f.b)
		})
	}
}

func TestInstalledVersionReadiness(t *testing.T) {
	f := newPublicationFixture(t)
	publishOK(t, f.root, f.a)
	seen := map[string]bool{}
	h := Hooks{Observe: func(point, path string) error {
		if point == "after:stage.final-rename" || point == "after:stage.final-parent-sync" || point == "before:ready.rename" || point == "after:ready.parent-sync" {
			seen[point] = true
			want := 1
			if point == "after:ready.parent-sync" {
				want = 2
			}
			if got := len(installed(t, f.root)); got != want {
				t.Fatalf("%s %s: discovered %d, want %d", point, path, got, want)
			}
			assertUsablePublication(t, f.root, "old")
		}
		return nil
	}}
	_, err := publishCandidate(f.root, f.b, h)
	must(t, err)
	if len(seen) != 4 {
		t.Fatalf("readiness barriers: %v", seen)
	}
	versions := installed(t, f.root)
	for _, v := range versions {
		assertSame(t, v.Directory, filepath.Join(f.root, "versions", layout.VersionID(v.Version)))
		assertSame(t, v.RuntimeExecutable, filepath.Join(v.Directory, "agent-director"))
	}
}
