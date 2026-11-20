package main_test

import (
	"os"
	"path/filepath"
	"testing"
)

// The standalone hook must bypass ordinary startup on either decision, even
// when config/store readers would fail or the HOME still needs migration.
func TestToolGuardCLIWithoutState(t *testing.T) {
	for _, layout := range []string{"absent-store", "broken-config", "unusable-store", "old-layout"} {
		for _, decision := range []struct {
			tool   string
			code   int
			stderr string
		}{
			{tool: "Task"},
			{tool: "AskUser", code: 2, stderr: "agent-director blocked this call: this call is disabled\n"},
		} {
			t.Run(layout+"/"+decision.tool, func(t *testing.T) {
				home, cwd := t.TempDir(), t.TempDir()
				var old oldLayout
				files := map[string]string{}
				switch layout {
				case "broken-config":
					files[filepath.Join(directorDir(home), "config.toml")] = "this is [not toml\n"
					files[stateDB(home)] = "not a database: decoy store\n"
				case "unusable-store":
					// A directory cannot be opened as a SQLite database, independently
					// of permissions or the user running the container.
					files[filepath.Join(stateDB(home), "sentinel")] = "keep the store directory\n"
				case "old-layout":
					old = newOldLayout(t)
					home = old.home
				}
				if layout == "broken-config" || layout == "unusable-store" {
					files[filepath.Join(directorDir(home), "ad-trail.jsonl")] = "decoy trail: do not append\n"
					files[filepath.Join(directorDir(home), "errors.log")] = "decoy log: do not append\n"
					files[filepath.Join(directorDir(home), "launches", "sentinel")] = "decoy launch: keep\n"
				}
				files[filepath.Join(cwd, "sentinel")] = "private working directory: keep\n"
				for path, body := range files {
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				homeBefore, cwdBefore := homeTree(t, home), homeTree(t, cwd)
				stdout, stderr, code := mustRun(t, cliOpts{
					dir: cwd, env: homeEnv(home, nil), deadline: surfaceDeadline,
					stdin: `{"hook_event_name":"PreToolUse","tool_name":"` + decision.tool + `","tool_input":{}}`,
				}, "tool-guard", "--", filepath.Join(home, "absent-decoy"))
				if code != decision.code || stdout != "" || stderr != decision.stderr {
					t.Errorf("exit=%d stdout=%q stderr=%q; want exit=%d, empty stdout, stderr=%q",
						code, stdout, stderr, decision.code, decision.stderr)
				}
				assertHomeTree(t, home, homeBefore...)
				assertHomeTree(t, cwd, cwdBefore...)
				if layout == "old-layout" {
					old.assertUntouched(t)
				}
				for path, want := range files {
					if got, err := os.ReadFile(path); err != nil || string(got) != want {
						t.Errorf("%s = %q (err %v); want unchanged %q", path, got, err, want)
					}
				}
			})
		}
	}
}

func TestToolGuardCLIArguments(t *testing.T) {
	old, cwd := newOldLayout(t), t.TempDir()
	entry := filepath.Join(old.home, "absent-decoy")
	redirectedHome := filepath.Join(cwd, "redirected-home")
	for _, tc := range []struct {
		name  string
		args  []string
		valid bool
	}{
		{name: "missing-separator-and-list", args: []string{"tool-guard"}},
		{name: "missing-separator", args: []string{"tool-guard", entry}},
		{name: "empty-list", args: []string{"tool-guard", "--"}},
		{name: "relative-entry", args: []string{"tool-guard", "--", entry, "relative"}},
		{name: "help-flag", args: []string{"tool-guard", "--help"}},
		{name: "help-word", args: []string{"tool-guard", "help"}},
		{name: "flag-after-separator", args: []string{"tool-guard", "--", "--help"}},
		{name: "home-flag-before-separator", args: []string{"tool-guard", "--home", redirectedHome, "--", entry}},
		{name: "global-home-prefix", args: []string{"--home", redirectedHome, "tool-guard", "--", entry}},
		{name: "global-home-equals-prefix", args: []string{"--home=" + redirectedHome, "tool-guard", "--", entry}},
		{name: "global-invalid-bool-prefix", args: []string{"--create-if-missing", "not-a-boolean", "tool-guard", "--", entry}},
		{name: "multiple-global-prefixes", args: []string{"--home", redirectedHome, "--store-path=" + filepath.Join(cwd, "state.db"), "tool-guard", "--", entry}},
		{name: "literal-absolute-entries", valid: true, args: []string{
			"tool-guard", "--", entry, filepath.Join(old.home, "decoy with spaces"),
			filepath.Join(old.home, `decoy'"quotes`),
			filepath.Join(old.home, "decoy;$(touch INJECTED);`touch BACKTICK`;*?[x]"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwdBefore := homeTree(t, cwd)
			stdout, stderr, code := mustRun(t, cliOpts{
				dir: cwd, env: homeEnv(old.home, nil), deadline: surfaceDeadline,
				stdin: `{"hook_event_name":"PreToolUse","tool_name":"Task","tool_input":{}}`,
			}, tc.args...)
			wantCode, wantStderr := 2, "agent-director blocked this call: agent-director could not check it\n"
			if tc.valid {
				wantCode, wantStderr = 0, ""
			}
			if code != wantCode || stdout != "" || stderr != wantStderr {
				t.Errorf("exit=%d stdout=%q stderr=%q; want exit=%d, empty stdout, stderr=%q",
					code, stdout, stderr, wantCode, wantStderr)
			}
			old.assertUntouched(t)
			assertHomeTree(t, cwd, cwdBefore...)
		})
	}
}
