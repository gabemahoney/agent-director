//go:build linux || darwin

package installversion

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
	"golang.org/x/sys/unix"
)

func TestVersionRecordValidation(t *testing.T) {
	for _, stamp := range []string{"release/v1", " v1\n", "版本-é"} {
		hash := sha256.Sum256([]byte(stamp))
		assertSame(t, layout.VersionID(stamp), fmt.Sprintf("%x", hash))
	}
	for _, tc := range []struct {
		name string
		edit func(*VersionRecord)
	}{
		{"format", func(r *VersionRecord) { r.FormatVersion = 2 }},
		{"stamp", func(r *VersionRecord) { r.Version = "other" }},
		{"zero-sequence", func(r *VersionRecord) { r.InstalledSequence = 0 }},
		{"nil-covered", func(r *VersionRecord) { r.CoveredAgents = nil }},
		{"duplicate-covered", func(r *VersionRecord) { r.CoveredAgents = []string{"claude", "claude"} }},
		{"unsorted-covered", func(r *VersionRecord) { r.CoveredAgents = []string{"codex", "claude"} }},
		{"core-digest", func(r *VersionRecord) { r.CoreDeclarationsSHA256 = strings.Repeat("A", 64) }},
		{"escape", func(r *VersionRecord) { r.Files[0].Path = "../agent-director" }},
		{"missing-admin", func(r *VersionRecord) { r.Files = r.Files[:1] }},
		{"digest", func(r *VersionRecord) { r.Files[0].SHA256 = strings.Repeat("b", 64) }},
		{"size", func(r *VersionRecord) { r.Files[1].Size++ }},
		{"mode", func(r *VersionRecord) { r.Files[0].Mode = 0600 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			v := installed(t, f.root)[0]
			rehashRecord(t, v.Directory, tc.edit)
			if _, err := ListInstalled(f.root); err == nil {
				t.Fatal("corrupt ready record was discovered")
			}
			if _, err := ReadPublished(f.root); err == nil {
				t.Fatal("corrupt ready record was selected")
			}
			_, err := (Engine{Root: f.root}).Rollback("old")
			expectRefusal(t, err, "layout_invalid")
		})
	}
}

func TestInstalledVersionCorruption(t *testing.T) {
	for _, kind := range []string{"bytes", "mode", "resource-symlink", "resource-fifo", "record-symlink", "ready-digest", "record-trailing", "directory-symlink", "duplicate-sequence", "current-escape"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			v := installed(t, f.root)[0]
			resource := filepath.Join(v.Directory, "agent-director")
			switch kind {
			case "bytes":
				must(t, os.WriteFile(resource, []byte("changed"), 0700))
			case "mode":
				must(t, os.Chmod(resource, 0500))
			case "resource-symlink":
				must(t, os.Remove(resource))
				must(t, os.Symlink(f.a.Runtime, resource))
			case "resource-fifo":
				must(t, os.Remove(resource))
				must(t, unix.Mkfifo(resource, 0600))
			case "record-symlink":
				p := filepath.Join(v.Directory, "version.json")
				outside := filepath.Join(t.TempDir(), "record")
				must(t, os.WriteFile(outside, bytesAt(t, p), 0600))
				must(t, os.Remove(p))
				must(t, os.Symlink(outside, p))
			case "ready-digest":
				must(t, os.WriteFile(filepath.Join(v.Directory, "ready"), []byte(strings.Repeat("0", 64)+"\n"), 0600))
			case "record-trailing":
				p := filepath.Join(v.Directory, "version.json")
				must(t, os.WriteFile(p, append(bytesAt(t, p), []byte("{}")...), 0600))
			case "directory-symlink":
				outside := filepath.Join(t.TempDir(), "version")
				must(t, os.Rename(v.Directory, outside))
				must(t, os.Symlink(outside, v.Directory))
			case "duplicate-sequence":
				publishOK(t, f.root, f.b)
				rehashRecord(t, installed(t, f.root)[1].Directory, func(r *VersionRecord) { r.InstalledSequence = 1 })
			case "current-escape":
				must(t, os.Remove(filepath.Join(f.root, "current")))
				must(t, os.Symlink(v.Directory, filepath.Join(f.root, "current")))
			}
			if kind == "current-escape" {
				if len(installed(t, f.root)) != 1 {
					t.Fatal("discovery followed current")
				}
				if _, err := ReadPublished(f.root); err == nil {
					t.Fatal("escaping current accepted")
				}
			} else if _, err := ListInstalled(f.root); err == nil {
				t.Fatal("invalid retained layout accepted")
			}
		})
	}
}

func TestPublicationValidation(t *testing.T) {
	core, digest := store.BusySendCoreContractIdentity()
	for _, kind := range []string{"version", "commit", "core-version", "core-digest", "covered", "admin-copy", "missing-facts", "malformed-facts", "core-mismatch", "pre-capability-digest", "intermediate-capability-digest", "coverage-regression"} {
		t.Run(kind, func(t *testing.T) {
			f := newPublicationFixture(t, Contract{FormatVersion, core, digest})
			publishOK(t, f.root, f.a)
			facts := factsFor("candidate")
			facts.CoreVersion, facts.CoreDeclarationsSHA256 = core, digest
			c := makeCandidate(t, facts)
			reported := c.Facts
			reason := "version_conflict"
			switch kind {
			case "version":
				reported.Version = "different"
			case "commit":
				reported.Commit = "different"
			case "core-version":
				reported.CoreVersion = "different"
			case "core-digest":
				reported.CoreDeclarationsSHA256 = strings.Repeat("b", 64)
			case "covered":
				reported.CoveredAgents = []string{"claude", "codex"}
			case "admin-copy":
				reported.Commit = "different"
			case "core-mismatch":
				c.Facts.CoreVersion = "different"
				reported = c.Facts
				reason = "core_mismatch"
			case "pre-capability-digest", "intermediate-capability-digest":
				// b.kii PM1: v21/core-v1 stays fixed, but both executables
				// must match the final immutable declaration anchor. These
				// are self-consistent pairs, not runtime/admin disagreements.
				c.Facts.CoreDeclarationsSHA256 = "45bbc95a6429e2f483f3ab4bc1cc0b8d12d40f607e06b6bf4416d87fff1cb7f7"
				if kind == "intermediate-capability-digest" {
					c.Facts.CoreDeclarationsSHA256 = "1d3840eb6589d5cd258eecec4f9f7514a225881e96b95ba0896c6abceaae061c"
				}
				reported = c.Facts
				reason = "core_mismatch"
			case "coverage-regression":
				c.Facts.CoveredAgents = []string{}
				reported = c.Facts
				reason = "coverage_regression"
			}
			payload(t, c.Runtime, RuntimeFactsArgument, reported)
			payload(t, c.Admin, "version-facts", c.Facts)
			if kind == "admin-copy" {
				payload(t, c.Runtime, RuntimeFactsArgument, c.Facts)
				payload(t, c.Admin, "version-facts", reported)
			}
			if kind == "missing-facts" {
				must(t, os.WriteFile(c.Runtime, []byte("#!/bin/sh\nexit 42\n"), 0700))
			}
			if kind == "malformed-facts" {
				must(t, os.WriteFile(c.Runtime, []byte("#!/bin/sh\nprintf '{}{}'\n"), 0700))
			}
			before := snapshot(t, f.root)
			_, err := publishCandidate(f.root, c, Hooks{})
			ref := expectRefusal(t, err, reason)
			if ref.PublicationState != "not_published" || ref.Durable {
				t.Fatalf("refused result %#v", ref)
			}
			assertSame(t, snapshot(t, f.root), before)
			assertUsablePublication(t, f.root, "old")
			// A rejected expansion must not raise the floor.
			publishOK(t, f.root, f.b)
			assertUsablePublication(t, f.root, "new")
			result, err := (Engine{Root: f.root}).Rollback("old")
			must(t, err)
			if result.PublicationState != "published" || !result.Durable {
				t.Fatalf("rollback result %+v", result)
			}
			assertUsablePublication(t, f.root, "old")
			anchor, err := ReadContract(f.root)
			must(t, err)
			assertSame(t, anchor, Contract{FormatVersion, core, digest})
		})
	}
}

func TestPublicationUsesExactStagedBytes(t *testing.T) {
	for _, change := range []string{"source", "staged"} {
		t.Run(change, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			original := bytesAt(t, f.b.Runtime)
			hit := false
			h := Hooks{Observe: func(point, path string) error {
				if point == "before:stage.facts" && filepath.Base(path) == "agent-director" {
					hit = true
					if change == "source" {
						payload(t, f.b.Runtime, RuntimeFactsArgument, factsFor("unexpected"))
					}
				}
				if point == "after:stage.facts" && filepath.Base(path) == "agent-director" && change == "staged" {
					must(t, os.WriteFile(path, append(bytesAt(t, path), []byte("# changed after handshake\n")...), 0700))
				}
				return nil
			}}
			_, err := publishCandidate(f.root, f.b, h)
			if !hit {
				t.Fatal("source-change barrier missed")
			}
			if change == "staged" {
				expectRefusal(t, err, "version_conflict")
				assertSame(t, len(installed(t, f.root)), 1)
				return
			}
			must(t, err)
			v := assertUsablePublication(t, f.root, "new")
			assertSame(t, bytesAt(t, v.RuntimeExecutable), original)
			assertSame(t, v.Facts, f.b.Facts)
			hash := sha256.Sum256(original)
			assertSame(t, v.Files[0].SHA256, fmt.Sprintf("%x", hash))
		})
	}
}

func TestPublicationFactsInfrastructureFailure(t *testing.T) {
	for _, binary := range []string{"agent-director", "agent-director-admin"} {
		t.Run(binary, func(t *testing.T) {
			f := newPublicationFixture(t)
			publishOK(t, f.root, f.a)
			var blocked string
			h := Hooks{Observe: func(point, path string) error {
				if point == "before:stage.facts" && filepath.Base(path) == binary {
					blocked = filepath.Dir(path)
					must(t, os.Chmod(blocked, 0500))
				}
				return nil
			}}
			_, err := publishCandidate(f.root, f.b, h)
			if blocked == "" {
				t.Fatal("facts-query barrier missed")
			}
			// Restore test-owned permissions before assertions/cleanup even when
			// the engine's deferred removal was refused by the same real EACCES.
			must(t, os.Chmod(blocked, 0700))
			expectRefusal(t, err, "io_failure")
			assertUsablePublication(t, f.root, "old")
			assertSame(t, len(installed(t, f.root)), 1)
		})
	}
}
