package spawn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
)

// withStubClaudeJSON redirects claudeJSONPath to an absent file under
// t.TempDir(), returned, so no test touches the real ~/.claude.json.
func withStubClaudeJSON(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	saved := claudeJSONPath
	claudeJSONPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { claudeJSONPath = saved })
	return path
}

// TestPreTrustCwdWritesEntry pins AC #3, b.f75 and b.18k: pre-trust sets
// projects.<cwd>.hasTrustDialogAccepted in CLAUDE_CONFIG_DIR's .claude.json
// (home's when unset or empty), changes nothing else there or in the other
// file, and leaves no temp file (atomic rename).
func TestPreTrustCwdWritesEntry(t *testing.T) {
	const cwd, homeSeed = "/tmp/pretrust-cwd", `{"projects":{},"userID":"home-user"}`
	cases := []struct {
		name, seed string
		configDir  string // "": unset; "empty": set to ""; "dir": a temp dir
	}{
		{name: "new entry beside other keys",
			seed: `{"projects":{"/home/op":{"hasTrustDialogAccepted":true,"hasCompletedProjectOnboarding":true}},"userID":"op"}`},
		{name: "existing entry flipped, its keys kept",
			seed: `{"projects":{"` + cwd + `":{"hasTrustDialogAccepted":false,"exampleFiles":["a.go","b.go"]}}}`},
		{name: "empty file is an empty object"},
		{name: "CLAUDE_CONFIG_DIR's file, home untouched", seed: `{"projects":{},"userID":"u"}`, configDir: "dir"},
		{name: "empty CLAUDE_CONFIG_DIR falls back to home", seed: `{"projects":{}}`, configDir: "empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := withStubClaudeJSON(t)
			home, env := target, map[string]string(nil)
			switch tc.configDir {
			case "empty":
				env = map[string]string{"CLAUDE_CONFIG_DIR": ""}
			case "dir":
				dir := t.TempDir()
				env, target = map[string]string{"CLAUDE_CONFIG_DIR": dir}, filepath.Join(dir, ".claude.json")
				seedFile(t, home, homeSeed)
			}
			seedFile(t, target, tc.seed)
			want := map[string]any{}
			if tc.seed != "" {
				if err := json.Unmarshal([]byte(tc.seed), &want); err != nil {
					t.Fatalf("seed: %v", err)
				}
			}
			projects, _ := want["projects"].(map[string]any)
			if projects == nil {
				projects = map[string]any{}
				want["projects"] = projects
			}
			entry, _ := projects[cwd].(map[string]any)
			if entry == nil {
				entry = map[string]any{} // b.f75: no hasCompletedProjectOnboarding on a new entry
				projects[cwd] = entry
			}
			entry["hasTrustDialogAccepted"] = true

			if err := preTrustCwd(cwd, env, defaultLockWait); err != nil {
				t.Fatalf("preTrustCwd: %v", err)
			}
			if got := readClaudeJSON(t, target); !reflect.DeepEqual(got, want) {
				t.Errorf("claude.json = %v; want %v", got, want)
			}
			assertNoStray(t, filepath.Dir(target))
			if target != home {
				if got := mustReadFile(t, home); string(got) != homeSeed {
					t.Errorf("home claude.json = %q; want untouched", got)
				}
			}
		})
	}
}

// TestPreTrustConcurrentSpawnsDoNotCorrupt pins AC #4 and b.zjm: concurrent
// pre-trusts of one file take its lock in turn, so every cwd's entry lands.
func TestPreTrustConcurrentSpawnsDoNotCorrupt(t *testing.T) {
	env, path := seedConfigDir(t)

	const N = 20
	errs := make([]error, N)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = preTrustCwd(fmt.Sprintf("/tmp/concurrent/%d", i), env, defaultLockWait)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("preTrustCwd #%d: %v", i, err)
		}
	}
	got := readClaudeJSON(t, path)
	var lost []string
	for i := range errs {
		if cwd := fmt.Sprintf("/tmp/concurrent/%d", i); !trusts(got, cwd) {
			lost = append(lost, cwd)
		}
	}
	if len(lost) > 0 {
		t.Errorf("%d of %d concurrent pre-trusts lost their entry: %v", len(lost), N, lost)
	}
	if got["userID"] != "u" {
		t.Errorf("userID = %v; want the seeded \"u\" kept", got["userID"])
	}
	assertNoStray(t, filepath.Dir(path))
}

// TestPreTrustOutcome pins SR-22.6's shared step on a CLAUDE_CONFIG_DIR target:
// ok writes silently, off attempts nothing, and each failure reports failed with
// one warning line; no temp file or lock dir is left and the home file is never
// touched.
func TestPreTrustOutcome(t *testing.T) {
	const cwd = "/tmp/pretrust-outcome-cwd"
	const seed = `{"projects":{},"userID":"u"}`
	cases := []struct {
		name     string
		seed     string
		missing  bool // no .claude.json in the config dir
		readOnly bool // config dir the process cannot create files in
		off      bool
		want     PreTrustOutcome
		reason   string // extra text the failed line must carry
	}{
		{name: "ok", seed: seed, want: PreTrustOK},
		{name: "off leaves file lacking the entry", seed: seed, off: true, want: PreTrustSkipped},
		{name: "off creates no missing file", missing: true, off: true, want: PreTrustSkipped},
		{name: "missing file", missing: true, want: PreTrustFailed, reason: "file does not exist"},
		{name: "unparseable file", seed: "{not json", want: PreTrustFailed},
		{name: "unwritable config dir", seed: seed, readOnly: true, want: PreTrustFailed, reason: "take lock"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.readOnly && os.Geteuid() == 0 {
				t.Skip("root ignores directory mode bits")
			}
			home := withStubClaudeJSON(t)
			seedFile(t, home, `{"projects":{}}`)
			dir := t.TempDir()
			path := filepath.Join(dir, ".claude.json")
			if !tc.missing {
				seedFile(t, path, tc.seed)
			}
			if tc.readOnly {
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatalf("chmod: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			warn := capturePreTrustWarn(t)

			if got := PreTrust(cwd, map[string]string{"CLAUDE_CONFIG_DIR": dir}, tc.off, config.PreTrust{}); got != tc.want {
				t.Fatalf("PreTrust = %q; want %q", got, tc.want)
			}

			switch {
			case tc.want == PreTrustOK:
				got := readClaudeJSON(t, path)
				entry, _ := got["projects"].(map[string]any)[cwd].(map[string]any)
				if b, _ := entry["hasTrustDialogAccepted"].(bool); !b || got["userID"] != "u" {
					t.Errorf("claude.json = %v; want projects[%q].hasTrustDialogAccepted true and userID kept", got, cwd)
				}
			case tc.missing:
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("stat %s: %v; want the file still absent", path, err)
				}
			default:
				if got := mustReadFile(t, path); string(got) != tc.seed {
					t.Errorf("claude.json = %q; want byte-identical %q", got, tc.seed)
				}
			}
			assertNoStray(t, dir)

			if tc.want == PreTrustFailed {
				assertOneFailedLine(t, warn.String(), path, tc.reason)
			} else if warn.Len() != 0 {
				t.Errorf("warning = %q; want nothing printed", warn.String())
			}
			if got := mustReadFile(t, home); string(got) != `{"projects":{}}` {
				t.Errorf("home claude.json = %q; want untouched (CLAUDE_CONFIG_DIR wins)", got)
			}
		})
	}
}

// TestCodexPreTrust pins SR-5.4: a codex launch's pre-trust is a pure outcome of
// the opt-out and the extra env's CODEX_HOME. Off is skipped; unset, empty or
// absolute is ok; relative, "~"-prefixed or whitespace-only is failed.
func TestCodexPreTrust(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		off  bool
		want PreTrustOutcome
	}{
		{"nil env", nil, false, PreTrustOK},
		{"CODEX_HOME unset", map[string]string{"OTHER": "x"}, false, PreTrustOK},
		{"CODEX_HOME empty", map[string]string{"CODEX_HOME": ""}, false, PreTrustOK},
		{"CODEX_HOME absolute", map[string]string{"CODEX_HOME": "/abs/codex"}, false, PreTrustOK},
		{"CODEX_HOME absolute with a trailing space", map[string]string{"CODEX_HOME": "/abs/codex "}, false, PreTrustOK},
		{"CODEX_HOME relative", map[string]string{"CODEX_HOME": "rel/codex"}, false, PreTrustFailed},
		{"CODEX_HOME dot-relative", map[string]string{"CODEX_HOME": "./codex"}, false, PreTrustFailed},
		{"CODEX_HOME tilde", map[string]string{"CODEX_HOME": "~/codex"}, false, PreTrustFailed},
		{"CODEX_HOME whitespace only", map[string]string{"CODEX_HOME": "   "}, false, PreTrustFailed},
		{"off, nil env", nil, true, PreTrustSkipped},
		{"off outranks a relative CODEX_HOME", map[string]string{"CODEX_HOME": "rel"}, true, PreTrustSkipped},
		{"a CLAUDE_CONFIG_DIR is not read", map[string]string{"CLAUDE_CONFIG_DIR": "rel"}, false, PreTrustOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warn := capturePreTrustWarn(t)
			if got := CodexPreTrust(tc.env, tc.off); got != tc.want {
				t.Errorf("CodexPreTrust = %q; want %q", got, tc.want)
			}
			if warn.Len() != 0 {
				t.Errorf("warning = %q; want CodexPreTrust to print nothing", warn.String())
			}
		})
	}
}

// TestRunPreTrustCodex pins SR-5.4's step for a codex launch: it reports the
// outcome the composed launch carries, prints the one pinned warning only for
// failed, and takes no lock, waits for none and writes no .claude.json, even
// with that file's lock held and a long lock_wait_seconds.
func TestRunPreTrustCodex(t *testing.T) {
	const failedLine = "agent-director: pre-trust failed (CODEX_HOME %q is not an absolute path); the agent may stop at Codex's folder-trust prompt\n"
	cases := []struct {
		name    string
		carried PreTrustOutcome
		home    string
		want    string // warning
	}{
		{"ok prints nothing", PreTrustOK, "/abs/codex", ""},
		{"skipped prints nothing", PreTrustSkipped, "rel", ""},
		{"failed relative", PreTrustFailed, "rel/codex", fmt.Sprintf(failedLine, "rel/codex")},
		{"failed tilde", PreTrustFailed, "~/codex", fmt.Sprintf(failedLine, "~/codex")},
		{"failed whitespace only", PreTrustFailed, "   ", fmt.Sprintf(failedLine, "   ")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := withFakeLockClock(t, 0)
			env, path := seedConfigDir(t)
			held := holdLock(t, path, clock.now.Add(-time.Hour)) // stale: a Claude launch would break it
			env["CODEX_HOME"] = tc.home
			warn := capturePreTrustWarn(t)

			if got := RunPreTrust("codex", tc.carried, "/tmp/codex-cwd", env, false, config.PreTrust{LockWaitSeconds: 3600}); got != tc.carried {
				t.Errorf("RunPreTrust = %q; want the carried %q", got, tc.carried)
			}
			if warn.String() != tc.want {
				t.Errorf("warning = %q; want %q", warn.String(), tc.want)
			}
			if clock.slept != 0 {
				t.Errorf("waited %s; want no wait for the held lock", clock.slept)
			}
			assertSeedKept(t, path)
			assertLockKept(t, path, held)
		})
	}
}

// TestRunPreTrustClaude: RunPreTrust for a claude agent is PreTrust, whatever
// outcome is carried in.
func TestRunPreTrustClaude(t *testing.T) {
	const cwd = "/tmp/run-pretrust-claude"
	env, path := seedConfigDir(t)
	warn := capturePreTrustWarn(t)
	if got := RunPreTrust("claude", PreTrustSkipped, cwd, env, false, config.PreTrust{}); got != PreTrustOK {
		t.Fatalf("RunPreTrust = %q; want ok", got)
	}
	if !trusts(readClaudeJSON(t, path), cwd) || warn.Len() != 0 {
		t.Errorf("claude.json trusts %s = false or warning %q; want the entry written silently", cwd, warn.String())
	}
}

// TestPreTrustFailedWithoutConfigDir: with no CLAUDE_CONFIG_DIR a missing home
// file is named and not created; an unresolvable home still warns once.
func TestPreTrustFailedWithoutConfigDir(t *testing.T) {
	t.Run("home file missing", func(t *testing.T) {
		home := withStubClaudeJSON(t)
		warn := capturePreTrustWarn(t)
		if got := PreTrust("/tmp/x", nil, false, config.PreTrust{}); got != PreTrustFailed {
			t.Fatalf("PreTrust = %q; want failed", got)
		}
		assertOneFailedLine(t, warn.String(), home, "file does not exist")
		if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stat %s: %v; want the file still absent", home, err)
		}
	})
	t.Run("home unresolvable", func(t *testing.T) {
		saved := claudeJSONPath
		claudeJSONPath = func() (string, error) { return "", errors.New("no home dir") }
		t.Cleanup(func() { claudeJSONPath = saved })
		warn := capturePreTrustWarn(t)
		if got := PreTrust("/tmp/x", nil, false, config.PreTrust{}); got != PreTrustFailed {
			t.Fatalf("PreTrust = %q; want failed", got)
		}
		assertOneFailedLine(t, warn.String(), "no home dir", "")
	})
}

// TestPreTrustRefusesUnusableConfigDir pins b.nje: a set but non-absolute
// CLAUDE_CONFIG_DIR fails with one line quoting it and touches no file, not even
// the one the value names relative to the process cwd. Not parallel.
func TestPreTrustRefusesUnusableConfigDir(t *testing.T) {
	for _, v := range []string{"rel", "~/cfg", "rel\nx"} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			home := withStubClaudeJSON(t)
			seedFile(t, home, lockTestSeed)
			dir := filepath.Join(cwdfix.Temp(t), v)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			seedFile(t, filepath.Join(dir, ".claude.json"), lockTestSeed)
			warn := capturePreTrustWarn(t)

			if got := PreTrust("/tmp/bnje-cwd", map[string]string{"CLAUDE_CONFIG_DIR": v}, false, config.PreTrust{}); got != PreTrustFailed {
				t.Fatalf("PreTrust = %q; want failed", got)
			}
			for _, d := range []string{dir, filepath.Dir(home)} {
				if got := mustReadFile(t, filepath.Join(d, ".claude.json")); string(got) != lockTestSeed {
					t.Errorf("%s/.claude.json = %q; want byte-identical %q", d, got, lockTestSeed)
				}
				assertNoStray(t, d)
			}
			assertOneFailedLine(t, warn.String(), fmt.Sprintf("CLAUDE_CONFIG_DIR %q is not an absolute path", v))
		})
	}
}

// TestConfigDirUsable pins b.nje's one rule: only an absolute CLAUDE_CONFIG_DIR
// is usable; claudeJSONFor targets it, takes $HOME when empty, refuses the rest.
func TestConfigDirUsable(t *testing.T) {
	home := withStubClaudeJSON(t)
	cases := []struct {
		dir    string
		usable bool
	}{
		{"", false}, {"rel", false}, {"./rel", false}, {"~/x", false}, {"   ", false},
		{"/abs", true}, {"/abs ", true},
	}
	for _, tc := range cases {
		if got := ConfigDirUsable(tc.dir); got != tc.usable {
			t.Errorf("ConfigDirUsable(%q) = %v; want %v", tc.dir, got, tc.usable)
		}
		wantPath, wantErr := filepath.Join(tc.dir, ".claude.json"), error(nil)
		switch {
		case tc.dir == "":
			wantPath = home
		case !tc.usable:
			wantPath, wantErr = "", errConfigDirNotAbsolute
		}
		if path, err := claudeJSONFor(map[string]string{"CLAUDE_CONFIG_DIR": tc.dir}); path != wantPath || !errors.Is(err, wantErr) {
			t.Errorf("claudeJSONFor(%q) = %q, %v; want %q, %v", tc.dir, path, err, wantPath, wantErr)
		}
	}
}

// capturePreTrustWarn swaps preTrustWarn for a buffer for the test's life.
func capturePreTrustWarn(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := preTrustWarn
	preTrustWarn = &buf
	t.Cleanup(func() { preTrustWarn = saved })
	return &buf
}

// assertOneFailedLine checks warn is exactly one "pre-trust failed" line
// containing each non-empty want.
func assertOneFailedLine(t *testing.T, warn string, wants ...string) {
	t.Helper()
	ok := strings.Count(warn, "\n") == 1 && strings.HasSuffix(warn, "\n") &&
		strings.Contains(warn, "pre-trust failed") && !strings.Contains(warn, "skipped")
	for _, w := range wants {
		ok = ok && strings.Contains(warn, w)
	}
	if !ok {
		t.Errorf("warning = %q; want one \"pre-trust failed\" line containing %q", warn, wants)
	}
}

// seedConfigDir returns the extra env of a fresh CLAUDE_CONFIG_DIR and its
// .claude.json, seeded with lockTestSeed.
func seedConfigDir(t *testing.T) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	seedFile(t, path, lockTestSeed)
	return map[string]string{"CLAUDE_CONFIG_DIR": dir}, path
}

// defaultLockWait is pre-trust's wait for a held lock when lock_wait_seconds is
// missing (b.kr4).
var defaultLockWait = time.Duration(config.DefaultPreTrustLockWaitSeconds) * time.Second

// lockTestSeed is the .claude.json seedConfigDir writes.
const lockTestSeed = `{"projects":{},"userID":"u"}`

// trusts reports whether the parsed .claude.json cfg has
// projects[cwd].hasTrustDialogAccepted true.
func trusts(cfg map[string]any, cwd string) bool {
	entry, _ := cfg["projects"].(map[string]any)[cwd].(map[string]any)
	b, _ := entry["hasTrustDialogAccepted"].(bool)
	return b
}

// assertNoStray fails for each entry of dir other than .claude.json and the
// names in keep, such as a temp file or a lock dir left behind.
func assertNoStray(t *testing.T, dir string, keep ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != ".claude.json" && !slices.Contains(keep, e.Name()) {
			t.Errorf("stray entry in %s: %s", dir, e.Name())
		}
	}
}

// seedFile writes content to path, failing the test on error.
func seedFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
}

// mustReadFile returns path's bytes, failing the test on error.
func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

// readClaudeJSON parses the .claude.json at path as a generic JSON object.
func readClaudeJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read claude.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse claude.json: %v (raw=%q)", err, string(raw))
	}
	return out
}
