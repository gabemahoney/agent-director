package spawn

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
)

// withStubClaudeJSON redirects claudeJSONPath to a file under t.TempDir() so
// pre-trust tests never touch the operator's real ~/.claude.json. The
// returned path is the location of the (initially absent) file; the
// test seeds it or leaves it absent as needed.
func withStubClaudeJSON(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude.json")
	saved := claudeJSONPath
	claudeJSONPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { claudeJSONPath = saved })
	return path
}

// TestPreTrustCreatesEntryInExistingFile pins AC #3: when ~/.claude.json
// exists with no entry for the cwd, pre-trust adds projects.<cwd> with
// hasTrustDialogAccepted=true and leaves every other key untouched.
func TestPreTrustCreatesEntryInExistingFile(t *testing.T) {
	path := withStubClaudeJSON(t)

	// Seed with an existing projects entry for some other dir plus a
	// top-level key Claude Code owns. Both must survive untouched.
	initial := `{
  "projects": {
    "/home/op": {"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}
  },
  "userID": "operator-uuid"
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cwd := "/tmp/cd-smoke-new"
	if err := preTrustCwd(cwd, nil, defaultLockWait); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	got := readClaudeJSON(t, path)

	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects not an object: %T", got["projects"])
	}
	cwdEntry, ok := projects[cwd].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] not an object: %T", cwd, projects[cwd])
	}
	if v, _ := cwdEntry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted = %v; want true", cwdEntry["hasTrustDialogAccepted"])
	}
	// We DO NOT set hasCompletedProjectOnboarding on the new entry —
	// that key has semantics beyond trust (bug b.f75 spec).
	if _, present := cwdEntry["hasCompletedProjectOnboarding"]; present {
		t.Errorf("preTrustCwd should not set hasCompletedProjectOnboarding on the new entry")
	}

	// Unrelated project entry survived.
	otherEntry, ok := projects["/home/op"].(map[string]any)
	if !ok {
		t.Fatalf("other project entry missing or wrong shape: %T", projects["/home/op"])
	}
	if v, _ := otherEntry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("unrelated project's hasTrustDialogAccepted should still be true")
	}
	if v, _ := otherEntry["hasCompletedProjectOnboarding"].(bool); !v {
		t.Errorf("unrelated project's hasCompletedProjectOnboarding should be preserved")
	}

	// Unknown top-level key survived.
	if got["userID"] != "operator-uuid" {
		t.Errorf("userID top-level key lost: %v", got["userID"])
	}
}

// TestPreTrustUpdatesExistingEntry pins AC #3 for the case where the
// cwd already has an entry but with hasTrustDialogAccepted=false. The
// flip-to-true must not disturb sibling keys.
func TestPreTrustUpdatesExistingEntry(t *testing.T) {
	path := withStubClaudeJSON(t)

	initial := `{
  "projects": {
    "/tmp/cd-existing": {"hasTrustDialogAccepted": false, "exampleFiles": ["a.go", "b.go"]}
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := preTrustCwd("/tmp/cd-existing", nil, defaultLockWait); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	got := readClaudeJSON(t, path)
	projects := got["projects"].(map[string]any)
	entry := projects["/tmp/cd-existing"].(map[string]any)

	if v, _ := entry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted = %v; want true", entry["hasTrustDialogAccepted"])
	}
	// Sibling keys preserved.
	files, ok := entry["exampleFiles"].([]any)
	if !ok || len(files) != 2 {
		t.Errorf("exampleFiles lost or wrong shape: %v", entry["exampleFiles"])
	}
}

// TestPreTrustAtomicRenameLeavesNoTempFile pins the temp+rename atomic
// write contract — after a successful pre-trust, the target file is
// the only ".claude.json*" inode in the directory.
func TestPreTrustAtomicRenameLeavesNoTempFile(t *testing.T) {
	path := withStubClaudeJSON(t)
	if err := os.WriteFile(path, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := preTrustCwd("/tmp/x", nil, defaultLockWait); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}
	assertNoStray(t, filepath.Dir(path))
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

// TestPreTrustEmptyFileTreatedAsEmptyObject pins the edge case where
// ~/.claude.json is zero bytes (Claude Code can leave it that way
// briefly during its own writes). preTrustCwd should treat that as
// an empty top-level object and seed the projects entry from scratch.
func TestPreTrustEmptyFileTreatedAsEmptyObject(t *testing.T) {
	path := withStubClaudeJSON(t)
	if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := preTrustCwd("/tmp/empty-case", nil, defaultLockWait); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	got := readClaudeJSON(t, path)
	projects := got["projects"].(map[string]any)
	entry := projects["/tmp/empty-case"].(map[string]any)
	if v, _ := entry["hasTrustDialogAccepted"].(bool); !v {
		t.Errorf("hasTrustDialogAccepted not true after seeding empty file: %v", entry)
	}
}

// TestPreTrustUsesClaudeConfigDirOverride pins the b.18k fix: when
// extraEnv["CLAUDE_CONFIG_DIR"] points to a dir that has a .claude.json,
// preTrustCwd must mutate that file and leave the stub (home) file unchanged.
func TestPreTrustUsesClaudeConfigDirOverride(t *testing.T) {
	// Set up the stub home file and seed it so we can verify it is untouched.
	homePath := withStubClaudeJSON(t)
	homeSeed := `{"projects":{},"userID":"home-user"}`
	if err := os.WriteFile(homePath, []byte(homeSeed), 0o600); err != nil {
		t.Fatalf("seed home stub: %v", err)
	}
	homeBytes, err := os.ReadFile(homePath)
	if err != nil {
		t.Fatalf("read home stub before call: %v", err)
	}

	// Set up an override dir with its own .claude.json.
	overrideDir := t.TempDir()
	overridePath := filepath.Join(overrideDir, ".claude.json")
	if err := os.WriteFile(overridePath, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed override: %v", err)
	}

	cwd := "/tmp/override-cwd"
	extraEnv := map[string]string{"CLAUDE_CONFIG_DIR": overrideDir}
	if err := preTrustCwd(cwd, extraEnv, defaultLockWait); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	// Override file must have gained the projects entry.
	got := readClaudeJSON(t, overridePath)
	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("override projects missing: %T", got["projects"])
	}
	entry, ok := projects[cwd].(map[string]any)
	if !ok {
		t.Fatalf("override projects[%q] missing", cwd)
	}
	if b, _ := entry["hasTrustDialogAccepted"].(bool); !b {
		t.Errorf("override hasTrustDialogAccepted = %v; want true", entry["hasTrustDialogAccepted"])
	}

	// Home stub file must be byte-equal to its seed — untouched.
	afterBytes, err := os.ReadFile(homePath)
	if err != nil {
		t.Fatalf("read home stub after call: %v", err)
	}
	if string(afterBytes) != string(homeBytes) {
		t.Errorf("home stub was mutated; want byte-equal to seed\nbefore: %s\nafter:  %s", homeBytes, afterBytes)
	}

	// Additionally confirm the home file's projects map has no entry for the override cwd.
	homeGot := readClaudeJSON(t, homePath)
	if homeProjects, ok := homeGot["projects"].(map[string]any); ok {
		if _, present := homeProjects[cwd]; present {
			t.Errorf("home stub projects[%q] should be absent but is present", cwd)
		}
	}
}

// TestPreTrustEmptyClaudeConfigDirFallsBack pins the b.18k fix-sketch
// point 1: an empty-string CLAUDE_CONFIG_DIR value is equivalent to "not
// set", so preTrustCwd must fall back to the home claudeJSONPath stub.
func TestPreTrustEmptyClaudeConfigDirFallsBack(t *testing.T) {
	homePath := withStubClaudeJSON(t)
	if err := os.WriteFile(homePath, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("seed home stub: %v", err)
	}

	cwd := "/tmp/fallback-cwd"
	extraEnv := map[string]string{"CLAUDE_CONFIG_DIR": ""} // empty → fall back
	if err := preTrustCwd(cwd, extraEnv, defaultLockWait); err != nil {
		t.Fatalf("preTrustCwd: %v", err)
	}

	// Home stub must have gained the entry.
	got := readClaudeJSON(t, homePath)
	projects, ok := got["projects"].(map[string]any)
	if !ok {
		t.Fatalf("projects missing: %T", got["projects"])
	}
	entry, ok := projects[cwd].(map[string]any)
	if !ok {
		t.Fatalf("projects[%q] missing in home stub; empty CLAUDE_CONFIG_DIR should fall back", cwd)
	}
	if b, _ := entry["hasTrustDialogAccepted"].(bool); !b {
		t.Errorf("hasTrustDialogAccepted = %v; want true", entry["hasTrustDialogAccepted"])
	}
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
// CLAUDE_CONFIG_DIR fails with one line quoting it, and no file is touched, not
// even the one the value names relative to the process cwd. Not parallel.
func TestPreTrustRefusesUnusableConfigDir(t *testing.T) {
	for _, v := range []string{"rel", "./rel", "~/cfg", "   ", "rel\nx"} {
		t.Run(fmt.Sprintf("%q", v), func(t *testing.T) {
			home := withStubClaudeJSON(t)
			seedFile(t, home, `{"projects":{}}`)
			dir := filepath.Join(cwdfix.Temp(t), v)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			path := filepath.Join(dir, ".claude.json")
			seedFile(t, path, lockTestSeed)
			warn := capturePreTrustWarn(t)

			if got := PreTrust("/tmp/bnje-cwd", map[string]string{"CLAUDE_CONFIG_DIR": v}, false, config.PreTrust{}); got != PreTrustFailed {
				t.Fatalf("PreTrust = %q; want failed", got)
			}
			if got := mustReadFile(t, path); string(got) != lockTestSeed {
				t.Errorf("%s = %q; want byte-identical %q", path, got, lockTestSeed)
			}
			assertNoStray(t, dir)
			if got := mustReadFile(t, home); string(got) != `{"projects":{}}` {
				t.Errorf("home claude.json = %q; want untouched", got)
			}
			assertNoStray(t, filepath.Dir(home))
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

// readClaudeJSON parses the stub claude.json file as a generic JSON
// object for assertion. Used by every test that inspects the result.
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
