//go:build linux || darwin

package installversion

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPublicationHistoryRecovery(t *testing.T) {
	for _, point := range []string{"after:history.intent.parent-sync", "before:current.rename", "after:current.rename", "after:current.parent-sync", "after:history.completion.rename"} {
		for _, operation := range []string{"publish", "rollback"} {
			t.Run(point+"/"+operation, func(t *testing.T) {
				f := newPublicationFixture(t)
				publishOK(t, f.root, f.a)
				expanded := makeCandidate(t, factsFor("expanded", "claude", "codex"))
				_, err := publishCandidate(f.root, expanded, stopAt(point))
				expectRefusal(t, err, "io_failure")
				visible := "expanded"
				if point == "after:history.intent.parent-sync" || point == "before:current.rename" {
					visible = "old"
				}
				assertUsablePublication(t, f.root, visible)
				before := snapshot(t, filepath.Join(f.root, "versions"))
				history := snapshot(t, filepath.Join(f.root, "publications"))
				for attempt := 0; attempt < 2; attempt++ {
					var r Result
					if operation == "publish" {
						r, err = publishCandidate(f.root, f.b, Hooks{})
					} else {
						r, err = (Engine{Root: f.root}).Rollback("old")
					}
					ref := expectRefusal(t, err, "coverage_regression")
					if ref.PublicationState != "not_published" || ref.Durable {
						t.Fatalf("refused target claims publication: %#v", ref)
					}
					if attempt == 0 && point != "after:history.completion.rename" && (r.RecoveredVersion == nil || *r.RecoveredVersion != "expanded") {
						t.Fatalf("lost recovered selection: %+v", r)
					}
					assertEstablished(t, f.root, "expanded")
					assertPreserved(t, filepath.Join(f.root, "versions"), before)
					assertPreserved(t, filepath.Join(f.root, "publications"), history)
				}
			})
		}
	}
}

func TestUnpublishedVersionDoesNotExpandFloor(t *testing.T) {
	for _, point := range []string{"after:stage.final-rename", "after:ready.parent-sync"} {
		t.Run(point, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			wide := makeCandidate(t, factsFor("unpublished", "claude", "codex"))
			_, err := publishCandidate(f.root, wide, stopAt(point))
			expectRefusal(t, err, "io_failure")
			assertUsablePublication(t, f.root, "old")
			retained := snapshot(t, filepath.Join(f.root, "versions"))
			publishOK(t, f.root, f.b)
			versions := installed(t, f.root)
			assertSame(t, len(versions), 3)
			assertSame(t, versions[1].Version, "unpublished")
			assertSame(t, versions[1].InstalledSequence, uint64(2))
			assertSame(t, versions[2].Version, "new")
			assertSame(t, versions[2].InstalledSequence, uint64(3))
			assertPreserved(t, filepath.Join(f.root, "versions"), retained)
			_, err = (Engine{Root: f.root}).Rollback("old")
			must(t, err)
			assertUsablePublication(t, f.root, "old")
		})
	}
}

func TestPublicationEvidenceRefusesCorruption(t *testing.T) {
	for _, kind := range []string{"missing-initialization", "invalid-initialization", "wrong-target", "wrong-contract", "missing-anchor", "history-gap", "completion-digest", "intent-target", "current-disagrees"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			publishOK(t, f.root, f.b)
			p := filepath.Join(f.root, "install-initialization.json")
			switch kind {
			case "missing-initialization":
				must(t, os.Remove(p))
			case "invalid-initialization":
				must(t, os.WriteFile(p, []byte("{"), 0600))
			case "wrong-target", "wrong-contract":
				i, err := ReadInitialization(f.root)
				must(t, err)
				if kind == "wrong-target" {
					i.RuntimeTarget = "../elsewhere/agent-director"
				} else {
					i.CoreVersion = "changed"
				}
				writeJSON(t, p, i)
			case "missing-anchor":
				must(t, os.Remove(filepath.Join(f.root, "install-contract.json")))
			case "history-gap":
				must(t, os.Remove(filepath.Join(f.root, "publications", "00000000000000000001.intent.json")))
			case "completion-digest":
				writeJSON(t, filepath.Join(f.root, "publications", "00000000000000000002.complete.json"), completion{1, 2, "bad"})
			case "intent-target":
				p = filepath.Join(f.root, "publications", "00000000000000000002.intent.json")
				var intent publication
				must(t, readJSON(p, &intent))
				intent.VersionID = "0000000000000000000000000000000000000000000000000000000000000000"
				writeJSON(t, p, intent)
			case "current-disagrees":
				must(t, os.Remove(filepath.Join(f.root, "current")))
				must(t, os.Symlink("versions/"+installed(t, f.root)[0].VersionID, filepath.Join(f.root, "current")))
			}
			before := snapshot(t, f.root)
			_, err := publishCandidate(f.root, f.b, Hooks{})
			if kind == "intent-target" {
				// Missing referenced resources must refuse without mutating any
				// evidence, regardless of whether the I/O classifier can prove why.
				if err == nil {
					t.Fatal("unresolvable history accepted")
				}
			} else {
				expectRefusal(t, err, "layout_invalid")
			}
			assertSame(t, snapshot(t, f.root), before)
		})
	}
}

func TestBootstrapEvidencedRepair(t *testing.T) {
	for _, kind := range []string{"missing-runtime", "missing-admin", "overwrite-both", "unsafe-directory", "unsafe-entry"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			anchor := bytesAt(t, filepath.Join(f.root, "install-contract.json"))
			evidence := bytesAt(t, filepath.Join(f.root, "install-initialization.json"))
			retained := snapshot(t, filepath.Join(f.root, "versions"))
			switch kind {
			case "missing-runtime":
				must(t, os.Remove(filepath.Join(f.root, "bin", "agent-director")))
			case "missing-admin":
				must(t, os.Remove(filepath.Join(f.root, "admin", "agent-director-admin")))
			case "overwrite-both":
				for _, p := range []string{"bin/agent-director", "admin/agent-director-admin"} {
					path := filepath.Join(f.root, p)
					must(t, os.Remove(path))
					must(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0700))
				}
				must(t, os.Chmod(filepath.Join(f.root, "admin"), 0755))
			case "unsafe-directory":
				must(t, os.Rename(filepath.Join(f.root, "admin"), filepath.Join(f.root, "saved-admin")))
				must(t, os.Symlink("saved-admin", filepath.Join(f.root, "admin")))
			case "unsafe-entry":
				p := filepath.Join(f.root, "bin", "agent-director")
				must(t, os.Remove(p))
				must(t, os.Mkdir(p, 0700))
			}
			before := snapshot(t, f.root)
			_, err := publishCandidate(f.root, f.b, Hooks{})
			if kind == "unsafe-directory" || kind == "unsafe-entry" {
				expectRefusal(t, err, "layout_invalid")
				assertSame(t, snapshot(t, f.root), before)
			} else {
				must(t, err)
				assertEstablished(t, f.root, "new")
			}
			assertPreserved(t, filepath.Join(f.root, "versions"), retained)
			assertSame(t, bytesAt(t, filepath.Join(f.root, "install-contract.json")), anchor)
			assertSame(t, bytesAt(t, filepath.Join(f.root, "install-initialization.json")), evidence)
		})
	}
}

func TestBootstrapCompletionIsSeparateFromSelection(t *testing.T) {
	for _, point := range []string{"after:current.parent-sync", "after:bootstrap.runtime-entry.rename", "after:bootstrap.admin-entry.rename", "before:bootstrap.completion.rename"} {
		t.Run(point, func(t *testing.T) {
			f, r := crashSetup(t, "legacy")
			result, err := runRequest(r, stopAt(point))
			expectRefusal(t, err, "io_failure")
			if result.InitializationComplete || !result.Durable || result.PublicationState != "published" {
				t.Fatalf("bootstrap result %+v", result)
			}
			i, err := ReadInitialization(f.root)
			must(t, err)
			if i.Complete {
				t.Fatal("incomplete bootstrap marked complete")
			}
			assertCrashVisibility(t, f, "legacy")
			publishOK(t, f.root, f.a)
			assertEstablished(t, f.root, "old")
		})
	}
}

func TestPublicationRecoveryPreservesObservedSelection(t *testing.T) {
	for _, point := range []string{"before:current.rename", "after:current.rename"} {
		t.Run(point, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			_, err := publishCandidate(f.root, f.b, stopAt(point))
			expectRefusal(t, err, "io_failure")
			want := "old"
			if point == "after:current.rename" {
				want = "new"
			}
			assertUsablePublication(t, f.root, want)
			_, err = publishCandidate(f.root, f.b, stopAt("before:history.intent.recovery-sync"))
			ref := expectRefusal(t, err, "io_failure")
			if ref.PublishedVersion == nil || *ref.PublishedVersion != want || ref.RecoveredVersion != nil || ref.PublicationState != "not_published" || ref.Durable {
				t.Fatalf("validated selection lost or recovery overstated: %#v", ref)
			}
			assertUsablePublication(t, f.root, want)
		})
	}
}
