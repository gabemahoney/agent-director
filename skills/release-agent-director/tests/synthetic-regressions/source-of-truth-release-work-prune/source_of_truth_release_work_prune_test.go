// Package sourceoftruthreleaseworkprune_test is a synthetic-regression test
// for bug b.vvv: the SR-16 source-of-truth gate must NOT fire on the
// release skill's per-version bump worktree under .release-work/, while
// still firing on real in-tree authoritative version sites.
//
// BACKGROUND (b.vvv incident)
// ============================
// The retried v0.7.5 release aborted in Phase 5 at the coverage.go-root gate
// because the source-of-truth gate scanned .release-work/release-v0.7.5/
// (the worktree the release skill's branch-and-bump phase creates to host
// the bumped package.json) and reported the bumped
// .release-work/release-v0.7.5/pkg/ts-bun-client/package.json as a
// secondary authoritative version site. The fix extends the prune list in
// check-source-of-truth.ts so any path component named `.release-work` is
// skipped during traversal, the same way `node_modules`, `.git`, `dist`,
// `testdata`, `fixtures`, and `reference` are already skipped.
//
// DESIGN
// ======
// Sub-case A (.release-work/ pruned, no false positive):
//   1. Create .release-work/release-v9.9.9/pkg/ts-bun-client/package.json
//      with a "version" field (this is exactly what the release skill's
//      bump commit produces inside the worktree — would have triggered P1
//      pre-fix).
//   2. Run the gate from repo root; assert exit 0 and silent stderr.
//
// Sub-case B (sibling violation OUTSIDE .release-work/ still fires):
//   1. Create tools/test-bvvv-{pid}/package.json with a "version" field
//      (this is OUTSIDE .release-work/, so the gate MUST still fire).
//   2. Run the gate; assert non-zero exit, stderr names the offending
//      relative path, stderr contains "invariant.source-of-truth", and the
//      first stderr line is valid JSON with gate == that key.
//
// CLEANUP-ON-FAILURE PATTERN
// ==========================
// t.Cleanup is registered before any mutation so it fires unconditionally
// even when t.Fatalf (which calls runtime.Goexit) is hit mid-test. Sub-case
// A only removes the .release-work/ root if this test was the one that
// created it (check existence FIRST, then mkdir, then register cleanup).
//
// DEPENDENCY
// ==========
// Requires `bun` and `git` in PATH; skips gracefully if either is absent.
package sourceoftruthreleaseworkprune_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// seedsMutationLockPath returns the path to the cross-process advisory lock
// file (pkg/api/apitest/.seeds-mutation.lock) that serializes repo-tree
// mutation against the repo-root package walkers. See acquireSeedsLock.
func seedsMutationLockPath(root string) string {
	return filepath.Join(root, "pkg", "api", "apitest", ".seeds-mutation.lock")
}

// acquireSeedsLock grabs the seeds-mutation flock (LOCK_EX) before this test
// creates/removes a directory under tools/ (a walk-reachable location: not
// dot/underscore/testdata-prefixed). Sub-case B builds
// tools/test-bvvv-<pid>/ and RemoveAll's it; that is the walk-reachable
// mutation. (Sub-case A's .release-work/ subtree is dot-prefixed and skipped
// by the walker, but the flock is held across the whole test for simplicity.)
//
// b.2y5: helper-tag-replay's TestHelperTagReplay runs `go build ./...` from
// the repo root while holding this same seeds-mutation flock. That walker
// enumerates every package directory, including tools/. If our
// tools/test-bvvv-<pid>/ directory is created and then RemoveAll'd mid-walk,
// the walker races: it can observe the directory during enumeration and then
// fail to open it, surfacing as
// `pattern ./...: open /work/<dir>: no such file or directory`. The b.mgw
// go-root fixture scope-down removed go-root's ~62s seeds-flock hold that had
// incidentally serialized these, exposing the latent race. Holding the seeds
// flock across our entire tree mutation window closes it.
//
// LOCK ORDERING (b.2y5): this test also takes acquireSourceOfTruthLock (the
// ts-bun-client scripts lock). To avoid deadlock, every package that holds
// BOTH locks MUST acquire the seeds-mutation lock FIRST, then the
// source-of-truth lock. This test observes that invariant. (The
// seeds-lock-only holders — helper-tag-replay, coverage-go-root-fires — never
// take the source-of-truth lock, so there is no reverse-order acquirer to
// deadlock against.)
//
// The lock is released after our subtests' RemoveAll cleanups run: t.Cleanup
// is LIFO, and subtest cleanups run when each subtest returns (before this
// parent function's cleanups), so restoring the tree happens before we unlock.
// The lock file is gitignored.
func acquireSeedsLock(t *testing.T, root string) {
	t.Helper()
	lockPath := seedsMutationLockPath(root)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("acquireSeedsLock: open %s: %v", lockPath, err)
	}
	// LOCK_EX blocks until no other process holds the lock.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatalf("acquireSeedsLock: flock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}

// acquireSourceOfTruthLock serializes tests that mutate the repo tree and
// then run the source-of-truth gate. Multiple tests under
// synthetic-regressions/ create fixture files (under tools/, skills/,
// reference/, .release-work/) and invoke the gate — without serialization
// they observe each other's fixtures and produce false positives/negatives.
// The lock file is gitignored.
func acquireSourceOfTruthLock(t *testing.T, root string) {
	t.Helper()
	lockPath := filepath.Join(root, "pkg", "ts-bun-client", "scripts", ".source-of-truth-mutation.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("acquireSourceOfTruthLock: open %s: %v", lockPath, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		t.Fatalf("acquireSourceOfTruthLock: flock: %v", err)
	}
	t.Cleanup(func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	})
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("repoRoot: could not find go.mod walking up from %s", dir)
		}
		dir = parent
	}
}

func runGate(t *testing.T, root string) (int, string) {
	t.Helper()
	cmd := exec.Command("bun", "run", "pkg/ts-bun-client/scripts/check-source-of-truth.ts")
	cmd.Dir = root
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	_ = cmd.Run() // non-zero is expected in sub-case B
	return cmd.ProcessState.ExitCode(), stderrBuf.String()
}

func TestSourceOfTruthReleaseWorkPrune(t *testing.T) {
	// b.2y5: skip when running inside coverage-go-root-fires' inner
	// `go test ./... -race` run. That outer test holds the seeds-mutation flock
	// (now that this test also takes it) and sets COVERAGE_GO_ROOT_NESTED=1 in
	// the gate subprocess env. Without this guard, the inner instance of this
	// test would block forever on acquireSeedsLock — a flock deadlock, the same
	// hazard helper-tag-replay and coverage-go-root-fires already guard against.
	if os.Getenv("COVERAGE_GO_ROOT_NESTED") == "1" {
		t.Skip("skipping repo-tree mutation: running inside coverage.go-root gate inner test suite")
	}

	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not in PATH — skipping b.vvv regression test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH — skipping b.vvv regression test")
	}

	root := repoRoot(t)

	// b.2y5: acquire the seeds-mutation flock FIRST (before the source-of-truth
	// lock) so its LIFO cleanup releases LAST — after both this parent's and the
	// subtests' tree-restoration cleanups. This serializes our tools/ mutation
	// (sub-case B) against helper-tag-replay's repo-root `go build ./...`
	// walker. See acquireSeedsLock for the race and the
	// seeds-then-source-of-truth ordering.
	acquireSeedsLock(t, root)

	// Serialize against the other source-of-truth gate tests, which also
	// mutate the repo tree.
	acquireSourceOfTruthLock(t, root)

	// ── Sub-case A: .release-work/ subtree must be pruned ────────────────────
	t.Run("A_release_work_subtree_pruned", func(t *testing.T) {
		releaseWorkRoot := filepath.Join(root, ".release-work")

		// Decide whether this test is the creator of .release-work/ BEFORE
		// any mutation, so cleanup can safely remove the root iff we own it.
		var preExisted bool
		if _, err := os.Stat(releaseWorkRoot); err == nil {
			preExisted = true
		} else if !os.IsNotExist(err) {
			t.Fatalf("Stat %s: %v", releaseWorkRoot, err)
		}

		bumpDir := filepath.Join(releaseWorkRoot, "release-v9.9.9", "pkg", "ts-bun-client")

		// Register cleanup BEFORE any mutation so it fires even if
		// MkdirAll/WriteFile/t.Fatalf trips mid-way.
		t.Cleanup(func() {
			if preExisted {
				// Only remove the bump subtree we created; the rest of
				// .release-work/ belongs to whoever was here first.
				ourSubtree := filepath.Join(releaseWorkRoot, "release-v9.9.9")
				if err := os.RemoveAll(ourSubtree); err != nil {
					t.Errorf("t.Cleanup: RemoveAll %s: %v", ourSubtree, err)
				}
				return
			}
			// We created .release-work/ from nothing → remove the whole root.
			if err := os.RemoveAll(releaseWorkRoot); err != nil {
				t.Errorf("t.Cleanup: RemoveAll %s: %v", releaseWorkRoot, err)
			}
		})

		if err := os.MkdirAll(bumpDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", bumpDir, err)
		}

		pkgPath := filepath.Join(bumpDir, "package.json")
		pkgContent := `{"name":"agent-director-bumped","version":"9.9.9"}` + "\n"
		if err := os.WriteFile(pkgPath, []byte(pkgContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", pkgPath, err)
		}

		exitCode, stderr := runGate(t, root)

		if exitCode != 0 {
			t.Fatalf("gate exited %d for files under .release-work/ — expected 0 (pruned);\nstderr:\n%s", exitCode, stderr)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("gate produced unexpected stderr for files under .release-work/;\nstderr:\n%s", stderr)
		}

		t.Logf("b.vvv sub-case A verified: .release-work/ subtree was pruned (no false positive).")
	})

	// ── Sub-case B: sibling violation OUTSIDE .release-work/ still fires ────
	t.Run("B_sibling_violation_outside_release_work_still_fires", func(t *testing.T) {
		suffix := fmt.Sprintf("%d", os.Getpid())
		violationDir := filepath.Join(root, "tools", "test-bvvv-"+suffix)

		// Register cleanup BEFORE mutation.
		t.Cleanup(func() {
			if err := os.RemoveAll(violationDir); err != nil {
				t.Errorf("t.Cleanup: RemoveAll %s: %v", violationDir, err)
			}
		})

		if err := os.MkdirAll(violationDir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", violationDir, err)
		}

		pkgPath := filepath.Join(violationDir, "package.json")
		pkgContent := `{"name":"sibling","version":"9.9.9"}` + "\n"
		if err := os.WriteFile(pkgPath, []byte(pkgContent), 0o644); err != nil {
			t.Fatalf("WriteFile %s: %v", pkgPath, err)
		}

		exitCode, stderr := runGate(t, root)

		if exitCode == 0 {
			t.Fatalf("gate exited 0 — expected non-zero for sibling package.json with version at %s", pkgPath)
		}

		relPath := filepath.Join("tools", "test-bvvv-"+suffix, "package.json")
		if !strings.Contains(stderr, relPath) {
			t.Fatalf("gate stderr does not name the offending file %q;\nstderr:\n%s", relPath, stderr)
		}

		const gateKey = "invariant.source-of-truth"
		if !strings.Contains(stderr, gateKey) {
			t.Fatalf("gate stderr does not contain %q;\nstderr:\n%s", gateKey, stderr)
		}

		firstLine := strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0]
		var v map[string]string
		if err := json.Unmarshal([]byte(firstLine), &v); err != nil {
			t.Fatalf("first stderr line is not valid JSON: %v\nline: %s", err, firstLine)
		}
		if v["gate"] != gateKey {
			t.Fatalf("JSON violation gate=%q, want %q", v["gate"], gateKey)
		}

		t.Logf("b.vvv sub-case B verified: sibling package.json outside .release-work/ still fires.\nOffending file: %s\nSample stderr line: %s", relPath, firstLine)
	})
}
