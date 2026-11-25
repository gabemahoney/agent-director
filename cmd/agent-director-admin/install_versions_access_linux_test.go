//go:build linux

package main_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
)

// traceVersionCommand observes the actual CLI and every descendant at the
// syscall boundary, including failed attempts. Setup/verification are outside
// the trace. strace is required, never silently skipped or replaced by hashes.
func traceVersionCommand(t *testing.T, bin, home string, args ...string) (string, string, int, string) {
	t.Helper()
	tracer, err := exec.LookPath("strace")
	if err != nil {
		t.Fatal("version access contract requires strace in the sandbox image (with own-child tracing allowed)")
	}
	log := filepath.Join(t.TempDir(), "syscalls.log")
	env := append(fakeTmuxEnv(t, home), "HOME="+home)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Suppress buffer contents: copying an executable reads embedded strings
	// such as "state.db", which are data, not path accesses. Opens, metadata
	// probes and annotated lock descriptors remain decoded, even on failure.
	argv := append([]string{"-f", "-qq", "-s", "4096", "-yy", "-o", log,
		"-e", "trace=%file,%desc,%process,%signal,%network",
		"-e", "raw=read,write,readv,writev,pread64,pwrite64,preadv,pwritev,getdents64", "--", bin}, args...)
	out, stderr, code := runBinInContext(t, ctx, tracer, "", env, argv...)
	data, err := os.ReadFile(log)
	versionMust(t, err)
	trace := string(data)
	if !strings.Contains(trace, "execve(") || strings.Contains(stderr, "strace:") || ctx.Err() != nil {
		t.Fatalf("access observer unavailable: %v %s\n%s", ctx.Err(), stderr, trace)
	}
	return out, stderr, code, trace
}

// strace may split a syscall while another thread runs. Rejoin by caller tid
// before matching; keep calls that never resume as attempted accesses.
func versionTraceLines(trace string) []string {
	var lines []string
	pending := map[string]int{}
	for _, line := range strings.Split(trace, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		tid := fields[0]
		if strings.Contains(line, "<unfinished ...>") {
			pending[tid] = len(lines)
		} else if strings.Contains(line, "<... ") {
			if j := strings.Index(line, " resumed>"); j >= 0 {
				if index, ok := pending[tid]; ok {
					prefix, _, _ := strings.Cut(lines[index], "<unfinished ...>")
					lines[index] = strings.TrimRight(prefix, " ") + line[j+len(" resumed>"):]
					delete(pending, tid)
					continue
				}
			}
		}
		lines = append(lines, line)
	}
	return lines
}

func versionForbiddenAccess(trace string) []string {
	var hits []string
	// Basenames cover disposable facts-child homes and relative openat paths.
	// -yy also annotates decoded descriptor operations such as flock; raw read
	// uses a numeric descriptor, so recognize both decimal and hexadecimal stdin.
	control := regexp.MustCompile(`\b(kill|tkill|pidfd_send_signal|connect|sendto|sendmsg)\(|\bread\((?:0|0x0)(?:<[^>]*>)?,`)
	// Go probes pidfd support with signal 0; that sends no signal. Actual
	// pidfd signals remain forbidden, as do kill/tkill and agent IPC attempts.
	pidfdProbe := regexp.MustCompile(`\bpidfd_send_signal\([^,]+, 0, NULL, 0(?:\)| <unfinished \.\.\.>$)`)
	// Match runtime path components in any HOME, including disposable facts
	// homes, relative paths and -yy descriptor annotations. Timestamp-only
	// wake changes (utimensat) are file accesses too.
	runtimePath := regexp.MustCompile(`(?:/|")(?:launch|input)(?:/|"|>)`)
	for _, line := range versionTraceLines(trace) {
		// An argv string naming a store is not an attempted filesystem access.
		if strings.Contains(line, "execve(") {
			continue
		}
		if strings.Contains(line, "state.db") || strings.Contains(line, "external-runtime.sqlite") || strings.Contains(line, "config.toml") || strings.Contains(line, "ad-trail.jsonl") || strings.Contains(line, store.BusySendWakeFile) || runtimePath.MatchString(line) || (control.MatchString(line) && !pidfdProbe.MatchString(line)) {
			hits = append(hits, line)
		}
	}
	return hits
}

func versionAccessCalibration(trace, input string) (failedOpen, wakeTouch, inputOpen bool) {
	for _, hit := range versionForbiddenAccess(trace) {
		if strings.Contains(hit, "openat(") && strings.Contains(hit, "state.db-wal") && strings.Contains(hit, "ENOENT") {
			failedOpen = true
		}
		if strings.Contains(hit, "utimensat(") && strings.Contains(hit, store.BusySendWakeFile) {
			wakeTouch = true
		}
		if strings.Contains(hit, "openat(") && strings.Contains(hit, filepath.Join(input, "missing")) && strings.Contains(hit, "ENOENT") {
			inputOpen = true
		}
	}
	return failedOpen, wakeTouch, inputOpen
}

// versionRuntimeSentinels uses BusySendWakePath's launch layout and Native's
// input/<store>/<token> directory and sibling lock, all under the test HOME.
func versionRuntimeSentinels(t *testing.T, home string) (wake, input string) {
	t.Helper()
	root := layout.Dir(home)
	wake = filepath.Join(root, "launch", "0123456789abcdef", "fedcba9876543210", store.BusySendWakeFile)
	input = filepath.Join(root, "input", "0123456789abcdef", "fedcba9876543210")
	for path, data := range map[string]string{wake: "", input + ".lock": "", filepath.Join(input, "attempt.json"): "runtime sentinel"} {
		versionMust(t, os.MkdirAll(filepath.Dir(path), 0700))
		versionMust(t, os.WriteFile(path, []byte(data), 0600))
	}
	// An old timestamp makes the calibration causal without a clock sleep.
	versionMust(t, os.Chtimes(wake, time.Unix(1, 0), time.Unix(1, 0)))
	return wake, input
}

func TestVersionAdminNoStoreAccess(t *testing.T) {
	// A swallowed failed attempt must still trip the observer. This calibrates
	// the measurement before claiming zero, including a timestamp-only wake and
	// native-input access in both the invoking and disposable facts-child homes.
	controlHome := t.TempDir()
	childHome := filepath.Join(layout.Dir(controlHome), "versions", ".staging-control", ".facts-home-control")
	for _, home := range []string{controlHome, childHome} {
		wake, input := versionRuntimeSentinels(t, home)
		before := versionTree(t, wake)["."]
		_, _, code, trace := traceVersionCommand(t, "/bin/sh", home, "-c", "cat \"$1\" >/dev/null 2>&1 || :; touch -c -m -- \"$2\" || exit 1; cat \"$3\" >/dev/null 2>&1 || :", "access-control", stateDB(home)+"-wal", wake, filepath.Join(input, "missing"))
		failedOpen, wakeTouch, inputOpen := versionAccessCalibration(trace, input)
		if code != 0 || !failedOpen || !wakeTouch || !inputOpen {
			t.Fatalf("observer missed runtime access in %s: exit=%d db=%v wake=%v input=%v\n%s", home, code, failedOpen, wakeTouch, inputOpen, trace)
		}
		after := versionTree(t, wake)["."]
		if after.Hash != before.Hash || after.Mode != before.Mode || after.ModTime.Equal(before.ModTime) {
			t.Fatalf("calibration was not a timestamp-only wake touch: before=%+v after=%+v", before, after)
		}
	}

	later := versionVariant(t, "later")
	mismatch := versionVariant(t, "core")
	for _, scenario := range []string{"fresh", "established", "anchor-mismatch", "pending-success", "pending-refusal", "partial-bootstrap", "overwritten-entries", "missing-evidence"} {
		t.Run(scenario, func(t *testing.T) {
			home := poisonedHome(t)
			root := layout.Dir(home)
			external := filepath.Join(home, "external-runtime.sqlite")
			versionMust(t, os.WriteFile(filepath.Join(root, "config.toml"), []byte("[store]\ndb_path = \""+external+"\"\n"), 0600))
			wake, input := versionRuntimeSentinels(t, home)
			trail := filepath.Join(root, "ad-trail.jsonl")
			versionMust(t, os.WriteFile(trail, []byte("runtime sentinel"), 0600))
			resources := []string{filepath.Join(root, "config.toml"), trail, wake, filepath.Dir(input)}
			for _, db := range []string{stateDB(home), external} {
				for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
					path := db + suffix
					versionMust(t, os.WriteFile(path, []byte("runtime sentinel"), 0600))
					resources = append(resources, path)
				}
			}
			facts := versionFactsOf(t, adminPath, home, false)
			if scenario == "fresh" {
				for _, rel := range []string{"bin/agent-director", "admin/agent-director-admin"} {
					path := filepath.Join(root, rel)
					versionMust(t, os.MkdirAll(filepath.Dir(path), 0700))
					versionMust(t, os.WriteFile(path, []byte("legacy regular executable"), 0700))
				}
			}
			if scenario != "fresh" {
				if scenario == "anchor-mismatch" {
					facts.Version, facts.CoreVersion = "previous", "other-core"
					_, err := seedVersion(t, root, facts, "")
					versionMust(t, err)
				} else if strings.HasPrefix(scenario, "pending") || scenario == "partial-bootstrap" {
					facts.Version = "previous"
					if scenario == "pending-refusal" {
						facts.CoveredAgents = append(facts.CoveredAgents, "codex")
					}
					point := "before:current.rename"
					if scenario == "partial-bootstrap" {
						point = "before:bootstrap.runtime-entry.rename"
					}
					if _, err := seedVersion(t, root, facts, point); err == nil {
						t.Fatal("setup fault did not fire")
					}
				} else {
					out, stderr, code := runAdmin(t, home, "publish-version", "--binary", mainPath)
					var r installversion.Result
					versionSuccess(t, out, stderr, code, &r)
				}
			}
			if scenario == "overwritten-entries" || scenario == "missing-evidence" {
				for _, rel := range []string{"bin/agent-director", "admin/agent-director-admin"} {
					path := filepath.Join(root, rel)
					versionMust(t, os.Remove(path))
					versionMust(t, os.WriteFile(path, []byte("old installer overwrite"), 0700))
				}
				if scenario == "missing-evidence" {
					versionMust(t, os.Remove(filepath.Join(root, layout.InstallInitialization)))
				}
			}
			before := map[string]map[string]versionFile{}
			for _, path := range resources {
				before[path] = versionTree(t, path)
			}
			measure := func(bin string, args []string, reason string, query bool) {
				t.Helper()
				out, stderr, code, trace := traceVersionCommand(t, bin, home, args...)
				if hits := versionForbiddenAccess(trace); len(hits) != 0 {
					t.Fatalf("runtime access attempted:\n%s", strings.Join(hits, "\n"))
				}
				for _, line := range strings.Split(trace, "\n") {
					if strings.Contains(line, "execve(") && !strings.Contains(line, "execve(\""+bin+"\"") {
						staged := strings.Contains(line, "execve(\""+filepath.Join(root, "versions", ".staging-"))
						factsQuery := strings.Contains(line, "\"--install-version-facts\"") || strings.Contains(line, "\"version-facts\"")
						if !staged || !factsQuery {
							t.Fatalf("unexpected child invocation: %s", line)
						}
					}
				}
				if reason != "" {
					versionRefusal(t, out, stderr, code, reason)
				} else if code != 0 || stderr != "" {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, out, stderr)
				}
				if query {
					seen := false
					for _, line := range strings.Split(trace, "\n") {
						if strings.Contains(line, "execve(") && strings.Contains(line, filepath.Join(root, "versions", ".staging-")) && strings.Contains(line, "--install-version-facts") {
							seen = true
						}
					}
					if !seen {
						t.Fatalf("no observed staged-runtime query:\n%s", trace)
					}
				}
				for path, image := range before {
					versionUnchanged(t, path, image)
				}
				assertInvocationKinds(t, home)
			}
			if scenario == "fresh" {
				measure(adminPath, []string{"version-facts"}, "", false)
				measure(adminPath, []string{"publish-version", "--help"}, "", false)
				measure(mainPath, []string{"--install-version-facts"}, "", false)
			}
			reason := ""
			if scenario == "anchor-mismatch" {
				reason = "core_mismatch"
			}
			if scenario == "pending-refusal" {
				reason = "coverage_regression"
			}
			if scenario == "missing-evidence" {
				reason = "layout_invalid"
			}
			measure(adminPath, []string{"publish-version", "--binary", mainPath}, reason, scenario == "fresh" || scenario == "anchor-mismatch" || strings.HasPrefix(scenario, "pending") || scenario == "partial-bootstrap")
			if reason != "" {
				measure(adminPath, []string{"publish-version", "--binary", mainPath}, reason, false)
				return
			}
			measure(adminPath, []string{"list-versions"}, "", false)
			if scenario == "established" {
				// Every copied pair is queried, including a differing runtime
				// submitted under a stamp which is already retained.
				measure(adminPath, []string{"publish-version", "--binary", mismatch.runtime}, "version_conflict", true)
				measure(later.admin, []string{"publish-version", "--binary", later.runtime}, "", true)
				measure(later.admin, []string{"rollback-version", "--version", testVersion}, "", false)
				unlock := holdVersionLock(t, root)
				measure(adminPath, []string{"version-facts"}, "", false)
				measure(adminPath, []string{"list-versions"}, "", false)
				measure(later.admin, []string{"rollback-version", "--version", testVersion}, "lock_busy", false)
				unlock()
				measure(later.admin, []string{"rollback-version", "--version", testVersion}, "", false)
				measure(adminPath, []string{"publish-version", "--binary", filepath.Join(home, "missing-candidate")}, "io_failure", false)
				measure(adminPath, []string{"publish-version", "--binary", mainPath}, "", false)
			}
		})
	}
}
