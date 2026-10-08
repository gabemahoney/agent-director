package spawn

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/cwdfix"
)

// seedHomes seeds lockTestSeed into three dirs' .claude.json, returned by name: "A"
// is agent-director's own home (claudeJSONPath, stubbed), "B" and "C" are others.
func seedHomes(t *testing.T) map[string]string {
	t.Helper()
	dirs := map[string]string{"A": filepath.Dir(withStubClaudeJSON(t)), "B": t.TempDir(), "C": t.TempDir()}
	for _, d := range dirs {
		seedFile(t, filepath.Join(d, ".claude.json"), lockTestSeed)
	}
	return dirs
}

// assertHomesKept fails for each dir in dirs, other than the one named except,
// whose .claude.json is not lockTestSeed byte for byte or that holds anything else.
func assertHomesKept(t *testing.T, dirs map[string]string, except string) {
	t.Helper()
	for name, d := range dirs {
		if name == except {
			continue
		}
		if got := mustReadFile(t, filepath.Join(d, ".claude.json")); string(got) != lockTestSeed {
			t.Errorf("%s/.claude.json = %q; want byte-identical %q", name, got, lockTestSeed)
		}
		assertNoStray(t, d)
	}
}

// TestPreTrustTargetsExtraEnvHome pins b.wb4: with no CLAUDE_CONFIG_DIR the extra
// env's HOME names the file and its lock; the own home (A) only when HOME is empty.
func TestPreTrustTargetsExtraEnvHome(t *testing.T) {
	const cwd = "/tmp/wb4-cwd"
	cases := []struct {
		name string
		env  map[string]string // a value "B" or "C" stands for that dir
		want string            // the dir whose .claude.json gains the entry
	}{
		{"HOME's file, not the own home's", map[string]string{"HOME": "B"}, "B"},
		{"empty CLAUDE_CONFIG_DIR moves on to HOME", map[string]string{"CLAUDE_CONFIG_DIR": "", "HOME": "B"}, "B"},
		{"CLAUDE_CONFIG_DIR wins over HOME", map[string]string{"CLAUDE_CONFIG_DIR": "C", "HOME": "B"}, "C"},
		{"CLAUDE_CONFIG_DIR wins over a relative HOME", map[string]string{"CLAUDE_CONFIG_DIR": "C", "HOME": "rel"}, "C"},
		{"empty HOME falls back to the own home", map[string]string{"HOME": ""}, "A"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs := seedHomes(t)
			env := map[string]string{}
			for k, v := range tc.env {
				if d, ok := dirs[v]; ok {
					v = d
				}
				env[k] = v
			}
			underLock := map[string][]string{}
			onBeforeCommit(t, func() {
				for name, d := range dirs {
					underLock[name] = treeEntries(t, d)
				}
			})
			warn := capturePreTrustWarn(t)

			if got := PreTrust(cwd, env, false, config.PreTrust{}); got != PreTrustOK {
				t.Fatalf("PreTrust = %q (warning %q); want ok", got, warn)
			}
			if got := readClaudeJSON(t, filepath.Join(dirs[tc.want], ".claude.json")); !trusts(got, cwd) || got["userID"] != "u" {
				t.Errorf("%s/.claude.json = %v; want projects[%q] trusted and userID kept", tc.want, got, cwd)
			}
			assertNoStray(t, dirs[tc.want])
			assertHomesKept(t, dirs, tc.want)
			for name := range dirs {
				want := []string{".claude.json"}
				if name == tc.want {
					want = append(want, ".claude.json.lock")
				}
				if !reflect.DeepEqual(underLock[name], want) {
					t.Errorf("%s under the lock = %q; want %q (only the target's own lock)", name, underLock[name], want)
				}
			}
		})
	}
}

// TestPreTrustRefusesUnusableConfigLocation pins b.nje and b.wb4: a non-absolute
// CLAUDE_CONFIG_DIR (even over an absolute HOME), or HOME alone, fails with one
// line %q-quoting it and touches no file, not even the one the value names
// relative to the process cwd. TestConfigDirUsable covers every value; this
// keeps two per variable end to end. Not parallel.
func TestPreTrustRefusesUnusableConfigLocation(t *testing.T) {
	cases := []struct {
		key, value string // the refused variable
		withHome   bool   // HOME is also set, to B
	}{
		{key: "HOME", value: "rel"},
		{key: "HOME", value: "~/home"},
		{key: "CLAUDE_CONFIG_DIR", value: "rel"},
		{key: "CLAUDE_CONFIG_DIR", value: "~/cfg"},
		{key: "CLAUDE_CONFIG_DIR", value: "rel\nx"},
		{key: "CLAUDE_CONFIG_DIR", value: "rel", withHome: true},
	}
	for _, tc := range cases {
		name := fmt.Sprintf("%s=%q", tc.key, tc.value)
		if tc.withHome {
			name += " over absolute HOME"
		}
		t.Run(name, func(t *testing.T) {
			dirs := seedHomes(t)
			rel := filepath.Join(cwdfix.Temp(t), tc.value)
			if err := os.MkdirAll(rel, 0o700); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			seedFile(t, filepath.Join(rel, ".claude.json"), lockTestSeed)
			dirs["cwd-relative"] = rel
			env := map[string]string{tc.key: tc.value}
			if tc.withHome {
				env["HOME"] = dirs["B"]
			}
			warn := capturePreTrustWarn(t)

			if got := PreTrust("/tmp/wb4-cwd", env, false, config.PreTrust{}); got != PreTrustFailed {
				t.Fatalf("PreTrust = %q; want failed", got)
			}
			assertHomesKept(t, dirs, "")
			assertOneFailedLine(t, warn.String(), fmt.Sprintf("%s %q is not an absolute path", tc.key, tc.value))
		})
	}
}
