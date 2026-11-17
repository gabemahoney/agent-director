package spawn

// droidrules_test.go covers the Droid launch's Rule Set step (SR-2, SR-3.2):
// agent-director's own checks and their sanitized refusals, the per-launch
// rule IDs, and Droid's own check, run against droidfix's fake program.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/droidfix"
	"golang.org/x/sys/unix"
)

// plantedValue marks Rule Set content a refusal must never echo.
const plantedValue = "planted-4f1c"

// rulesToken is the launch token the Rule Set tests give a launch.
const rulesToken = "0123456789abcdef"

// checkedTail is droidfix.RuleSet("tail") as checked under rulesToken.
const checkedTail = `{"version":1,"rules":[{"id":"agent-director/` + rulesToken + `/tail","decision":"allow","match":{"prefix":["tail"]},"tests":{"match":["tail -n 1 x"],"noMatch":["head x"]}}]}`

// ruleSetRefusal is ErrPermissionRulesRefused's description for kind, at
// rules[index] when index is not negative.
func ruleSetRefusal(index int, kind string) string {
	where := ""
	if index >= 0 {
		where = fmt.Sprintf("rules[%d] ", index)
	}
	return "ErrPermissionRulesRefused: Droid Rule Set refused: " + where + kind +
		"; an operator must fix the Rule Set File [launch] droid_rules_path names, or the droid program (see docs/experimental.md#droid-rules); no row, session, trust entry or launch file was written and nothing was launched"
}

// writeRuleSet writes content as a Rule Set File and returns its path.
func writeRuleSet(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ruleWith is an allow rule holding planted values, each named field replaced
// by (or added as) the raw JSON value that follows it, or removed for "".
func ruleWith(fields ...string) string {
	keys := []string{"id", "decision", "match", "tests", "reason"}
	values := map[string]string{
		"id":       strconv.Quote(plantedValue),
		"decision": `"allow"`,
		"match":    `{"prefix":["tail",["` + plantedValue + `"]]}`,
		"tests":    `{"match":["tail ` + plantedValue + `"],"noMatch":["head ` + plantedValue + `"]}`,
		"reason":   strconv.Quote(plantedValue),
	}
	for i := 0; i+1 < len(fields); i += 2 {
		if _, ok := values[fields[i]]; !ok {
			keys = append(keys, fields[i])
		}
		values[fields[i]] = fields[i+1]
	}
	var parts []string
	for _, key := range keys {
		if values[key] != "" {
			parts = append(parts, strconv.Quote(key)+":"+values[key])
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// rulesDoc is a Rule Set File holding rules.
func rulesDoc(rules ...string) string {
	return `{"version":1,"rules":[` + strings.Join(rules, ",") + `]}`
}

// rulesLaunch is a Droid launch's Resolved, as the Rule Set step receives it.
func rulesLaunch(extraEnv map[string]string) Resolved {
	return Resolved{Agent: config.AgentDroid, InstanceID: "row", RelayMode: "off", ExtraEnv: extraEnv, LaunchToken: rulesToken}
}

// TestDroidRuleSetRefusals (SR-2.2, SR-2.5): every kind agent-director's own
// checks refuse gives ErrPermissionRulesRefused naming only the kind and the
// rule's position, never a value from the file, and Droid's check never runs;
// the accepted controls reach it.
func TestDroidRuleSetRefusals(t *testing.T) {
	first := ruleWith("id", `"first"`)
	set := func(rule string) string { return rulesDoc(first, rule) }
	valid := rulesDoc(first)
	cases := []struct {
		name, content string
		file          func(t *testing.T, path string) // makes the path instead of content
		kind          string                          // "" accepts
	}{
		{name: "missing file", file: func(*testing.T, string) {}, kind: ruleSetUnreadable},
		{name: "directory", file: func(t *testing.T, path string) { mustDo(t, os.Mkdir(path, 0o700)) }, kind: ruleSetUnreadable},
		{name: "FIFO", file: func(t *testing.T, path string) { mustDo(t, syscall.Mkfifo(path, 0o600)) }, kind: ruleSetUnreadable},
		{name: "over 1 MiB", content: valid + strings.Repeat(" ", droidRuleSetMaxBytes-len(valid)+1), kind: ruleSetUnreadable},
		{name: "exactly 1 MiB", content: valid + strings.Repeat(" ", droidRuleSetMaxBytes-len(valid))},

		{name: "not JSON", content: plantedValue + " {", kind: ruleSetNotObject},
		{name: "invalid UTF-8", content: set(ruleWith("reason", "\"\xff"+plantedValue+"\"")), kind: ruleSetNotObject},
		{name: "array", content: "[" + first + "]", kind: ruleSetNotObject},
		{name: "string", content: strconv.Quote(plantedValue), kind: ruleSetNotObject},
		{name: "null", content: "null", kind: ruleSetNotObject},
		{name: "two objects", content: valid + valid, kind: ruleSetNotObject},
		{name: "version 2", content: `{"version":2,"rules":[` + first + `]}`, kind: ruleSetNotObject},
		{name: "version a string", content: `{"version":"1","rules":[` + first + `]}`, kind: ruleSetNotObject},
		{name: "version 1.5", content: `{"version":1.5,"rules":[` + first + `]}`, kind: ruleSetNotObject},
		{name: "version null", content: `{"version":null,"rules":[` + first + `]}`, kind: ruleSetNotObject},
		{name: "no version", content: `{"rules":[` + first + `]}`, kind: ruleSetNotObject},
		{name: "no rules", content: `{"version":1}`, kind: ruleSetNotObject},
		{name: "empty rules", content: `{"version":1,"rules":[]}`, kind: ruleSetNotObject},
		{name: "rules an object", content: `{"version":1,"rules":{"r":` + first + `}}`, kind: ruleSetNotObject},
		{name: "rules null", content: `{"version":1,"rules":null}`, kind: ruleSetNotObject},
		{name: "unknown top-level field", content: `{"version":1,"rules":[` + first + `],"` + plantedValue + `":1}`, kind: ruleSetTopField},
		{name: "top-level key in another case", content: `{"version":1,"Rules":[` + first + `]}`, kind: ruleSetTopField},

		{name: "rule not an object", content: set(strconv.Quote(plantedValue)), kind: ruleNotObject},
		{name: "id a number", content: set(ruleWith("id", "7")), kind: ruleNotObject},
		{name: "id null", content: set(ruleWith("id", "null")), kind: ruleNotObject},
		{name: "no decision", content: set(ruleWith("decision", "")), kind: ruleNotObject},
		{name: "no match", content: set(ruleWith("match", "")), kind: ruleNotObject},
		{name: "match not an object", content: set(ruleWith("match", `["tail"]`)), kind: ruleNotObject},
		{name: "no match.prefix", content: set(ruleWith("match", `{}`)), kind: ruleNotObject},
		{name: "empty match.prefix", content: set(ruleWith("match", `{"prefix":[]}`)), kind: ruleNotObject},
		{name: "match.prefix a string", content: set(ruleWith("match", `{"prefix":"tail"}`)), kind: ruleNotObject},
		{name: "prefix word a number", content: set(ruleWith("match", `{"prefix":["tail",7]}`)), kind: ruleNotObject},
		{name: "empty alternatives", content: set(ruleWith("match", `{"prefix":["tail",[]]}`)), kind: ruleNotObject},
		{name: "alternative a number", content: set(ruleWith("match", `{"prefix":["tail",["`+plantedValue+`",7]]}`)), kind: ruleNotObject},
		{name: "nested alternatives", content: set(ruleWith("match", `{"prefix":[["tail",["head"]]]}`)), kind: ruleNotObject},
		{name: "no tests", content: set(ruleWith("tests", "")), kind: ruleNotObject},
		{name: "no tests.match", content: set(ruleWith("tests", `{"noMatch":["`+plantedValue+`"]}`)), kind: ruleNotObject},
		{name: "tests.noMatch a string", content: set(ruleWith("tests", `{"match":["x"],"noMatch":"`+plantedValue+`"}`)), kind: ruleNotObject},
		{name: "tests.match entry a number", content: set(ruleWith("tests", `{"match":[7],"noMatch":["x"]}`)), kind: ruleNotObject},
		{name: "reason a number", content: set(ruleWith("reason", "7")), kind: ruleNotObject},
		{name: "enabled a string", content: set(ruleWith("enabled", `"true"`)), kind: ruleNotObject},
		{name: "enabled null", content: set(ruleWith("enabled", "null")), kind: ruleNotObject},
		{name: "unknown rule field", content: set(ruleWith(plantedValue, `"x"`)), kind: ruleField},
		{name: "rule key in another case", content: set(ruleWith("id", "", "ID", strconv.Quote(plantedValue))), kind: ruleField},
		{name: "unknown match field", content: set(ruleWith("match", `{"prefix":["tail"],"regex":"`+plantedValue+`"}`)), kind: ruleField},
		{name: "unknown tests field", content: set(ruleWith("tests", `{"match":["x"],"noMatch":["y"],"`+plantedValue+`":[]}`)), kind: ruleField},
		{name: "decision deny", content: set(ruleWith("decision", `"deny"`)), kind: ruleDecision},
		{name: "decision in another case", content: set(ruleWith("decision", `"Allow"`)), kind: ruleDecision},
		{name: "empty decision", content: set(ruleWith("decision", `""`)), kind: ruleDecision},
		{name: "empty id", content: set(ruleWith("id", `""`)), kind: ruleEmptyID},
		{name: "repeated id", content: set(ruleWith("id", `"first"`)), kind: ruleRepeatedID},
		{name: "reserved id", content: set(ruleWith("id", `"builtin/`+plantedValue+`"`)), kind: ruleReservedID},
		{name: "disabled rule", content: set(ruleWith("enabled", "false")), kind: ruleDisabled},
		{name: "enabled true", content: set(ruleWith("enabled", "true"))},
	}
	fake := droidfix.New(t, droidfix.Pass)
	accepted := 0
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rules.json")
			if tc.file != nil {
				tc.file(t, path)
			} else if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := config.Default()
			cfg.Launch.DroidRulesPath, cfg.Launch.DroidPath = path, fake.Path
			got, err := ResolveDroidRules(rulesLaunch(nil), cfg)
			if tc.kind == "" {
				accepted++
				if err != nil || got.DroidRules == nil {
					t.Fatalf("accepted Rule Set: %v", err)
				}
				return
			}
			index := 1
			if slices.Contains([]string{ruleSetUnreadable, ruleSetNotObject, ruleSetTopField}, tc.kind) {
				index = -1
			}
			if !errors.Is(err, ErrPermissionRulesRefused) || err.Error() != ruleSetRefusal(index, tc.kind) {
				t.Fatalf("refusal = %v\nwant %s", err, ruleSetRefusal(index, tc.kind))
			}
			if got.DroidRules != nil || got.DroidProgram != "" {
				t.Fatalf("refused launch carries rules %q, program %q", got.DroidRules, got.DroidProgram)
			}
		})
	}
	if calls := fake.Calls(t); len(calls) != accepted {
		t.Fatalf("Droid's check ran %d times; want only for the %d accepted files", len(calls), accepted)
	}
}

// TestDroidRuleSetProgram (SR-2.3): an allow rule's every program alternative
// at match.prefix position 0 must be tail, head or an absolute path whose
// final component, less one version suffix, is no General-Purpose Launcher;
// ask and block rules, and later positions, take any word.
func TestDroidRuleSetProgram(t *testing.T) {
	// SR-2.3's General-Purpose Launchers, as data apart from the code's list.
	launchers := strings.Fields(`sh bash dash zsh ksh mksh fish csh tcsh busybox
		python pypy perl ruby jruby node nodejs bun deno php lua luajit tclsh wish awk gawk mawk nawk
		env xargs find sudo su doas pkexec runuser sg nohup nice ionice timeout setsid stdbuf time chroot
		unshare nsenter flock script watch parallel ssh docker podman`)
	refused := []string{
		`["/usr/bin/python3.11"]`, `["/usr/bin/pypy3"]`, `["/usr/bin/lua5.4"]`, `["/usr/bin/node18"]`, `["/usr/bin/ruby-3.1"]`,
		`["./tail"]`, `["tests/run.sh"]`, `["~/bin/tool"]`, `["~"]`, `["ls"]`, `["bash"]`, `["tail2"]`, `["TAIL"]`, `[""]`,
		`[["tail","/usr/bin/sh"]]`, `[["/opt/tools/jobsched","head","~/bin/tool"]]`, `[["head","python3.11"]]`,
	}
	for _, name := range launchers {
		refused = append(refused, `["/usr/bin/`+name+`"]`, `["/usr/local/bin/`+name+`-1.2"]`, `["/opt/bin/`+name+`3"]`)
	}
	accepted := []string{
		`["tail"]`, `["head","-n","1"]`, `[["tail","head"]]`, `["/usr/bin/tail"]`, `["/opt/tools/jobsched",["wait","status"]]`,
		`["/usr/bin/python-config"]`, `["/usr/bin/shellcheck"]`, `["/usr/bin/3"]`, `["/opt/tool","bash"]`, `["/opt/tool",["sh","~/x"]]`,
	}
	check := func(decision, prefix string) error {
		_, err := checkDroidRuleSet([]byte(rulesDoc(ruleWith("id", `"first"`), ruleWith("decision", strconv.Quote(decision), "match", `{"prefix":`+prefix+`}`))), rulesToken)
		return err
	}
	for _, prefix := range refused {
		if err := check("allow", prefix); err == nil || err.Error() != ruleSetRefusal(1, ruleProgram) {
			t.Errorf("allow %s: %v; want the program refusal at rules[1]", prefix, err)
		}
	}
	for _, prefix := range accepted {
		if err := check("allow", prefix); err != nil {
			t.Errorf("allow %s: %v; want it accepted", prefix, err)
		}
	}
	for _, prefix := range []string{`["bash"]`, `["./bin/agent-director"]`, `[["~/x","/usr/bin/python3.11"]]`, `["sudo"]`} {
		for _, decision := range []string{"ask", "block"} {
			if err := check(decision, prefix); err != nil {
				t.Errorf("%s %s: %v; want it accepted", decision, prefix, err)
			}
		}
	}
}

// TestDroidRuleSetRewritesIDs (SR-3.2): each ID becomes
// agent-director/<launch token>/<operator id>, and the rest of each rule keeps
// its values, serialized once in a fixed field order with JSON's escapes.
func TestDroidRuleSetRewritesIDs(t *testing.T) {
	input := `{ "rules": [
		{"tests": {"noMatch": ["git status"], "match": ["git stash pop"]}, "match": {"prefix": ["git", ["stash", "clean"]]}, "decision": "ask", "id": "git \"stash\" é", "enabled": true},
		{"id": "filters", "decision": "allow", "match": {"prefix": [["tail", "head"]]}, "tests": {"match": ["tail -n 2 x"], "noMatch": ["cat x"]}, "reason": "read\\only\n"}
	], "version": 1 }`
	want := `{"version":1,"rules":[{"id":"agent-director/` + rulesToken + `/git \"stash\" é","decision":"ask","match":{"prefix":["git",["stash","clean"]]},"tests":{"match":["git stash pop"],"noMatch":["git status"]},"enabled":true},` +
		`{"id":"agent-director/` + rulesToken + `/filters","decision":"allow","match":{"prefix":[["tail","head"]]},"tests":{"match":["tail -n 2 x"],"noMatch":["cat x"]},"reason":"read\\only\n"}]}`
	for _, token := range []string{rulesToken, "fedcba9876543210"} {
		got, err := checkDroidRuleSet([]byte(input), token)
		if want := strings.ReplaceAll(want, rulesToken, token); err != nil || string(got) != want {
			t.Errorf("token %s: %s, %v\nwant %s", token, got, err, want)
		}
	}
}

// TestDroidRuleSetNativeCheck (SR-2.4): Droid's check runs as
// `<program> rules check --file <copy>` on a 0600 copy, in a 0700 directory,
// of exactly the checked bytes, whatever the umask; only exit 0 within the
// deadline passes; a failed removal of the copy refuses, naming any earlier
// problem too; otherwise nothing is left in the temporary directory.
func TestDroidRuleSetNativeCheck(t *testing.T) {
	// Serial: it sets TMPDIR, and one case the process umask.
	const short = 100 * time.Millisecond
	cases := []struct {
		name     string
		mode     droidfix.Mode
		program  string // "" the fake, else how the program is missing
		deadline time.Duration
		umask    int
		lock     bool   // the fake makes the copy's directory unremovable
		want     string // the problem kind; "" passes
	}{
		{name: "exit 0", mode: droidfix.Pass},
		{name: "exit 0 under umask 0477", mode: droidfix.Pass, umask: 0o477},
		{name: "exit 1", mode: droidfix.Fail, want: checkNonzeroExit},
		{name: "hang past a shortened deadline", mode: droidfix.Hang, deadline: short, want: "Droid's rules check did not finish within 100ms"},
		{name: "output over the limit", mode: droidfix.Flood, want: checkCouldNotRun},
		{name: "missing program", program: "missing", want: checkCouldNotRun},
		{name: "program not executable", program: "not executable", want: checkCouldNotRun},
		{name: "droid not on PATH", program: "not on PATH", want: checkCouldNotRun},
		{name: "copy not removed", mode: droidfix.Pass, lock: true, want: checkCleanupFailed},
		{name: "exit 1 and copy not removed", mode: droidfix.Fail, lock: true, want: checkNonzeroExit + ", and " + checkCleanupFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.lock && os.Geteuid() == 0 {
				t.Skip("root removes a 0500 directory's entries")
			}
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			fake := droidfix.New(t, tc.mode)
			if tc.lock {
				fake.LockCopyDir(t, true)
				t.Cleanup(func() { unlockEntries(t, tmp) })
			}
			cfg := config.Default()
			cfg.Launch.DroidRulesPath, cfg.Launch.DroidPath = writeRuleSet(t, droidfix.RuleSet("tail")), fake.Path
			var extraEnv map[string]string
			switch tc.program {
			case "missing":
				cfg.Launch.DroidPath = filepath.Join(t.TempDir(), "droid")
			case "not executable":
				cfg.Launch.DroidPath = filepath.Join(t.TempDir(), "droid")
				mustDo(t, os.WriteFile(cfg.Launch.DroidPath, []byte("#!/bin/sh\nexit 0\n"), 0o600))
			case "not on PATH":
				cfg.Launch.DroidPath, extraEnv = "", map[string]string{"PATH": t.TempDir()}
			}
			deadline := DroidRulesCheckDeadline
			if tc.deadline != 0 {
				deadline = tc.deadline
			}
			mask := -1
			if tc.umask != 0 {
				mask = unix.Umask(tc.umask)
			}
			start := time.Now()
			got, err := resolveDroidRules(rulesLaunch(extraEnv), cfg, deadline)
			elapsed := time.Since(start)
			if mask >= 0 {
				unix.Umask(mask)
			}

			if tc.want == "" {
				if err != nil || string(got.DroidRules) != checkedTail || got.DroidProgram != fake.Path {
					t.Fatalf("passed check: rules %s, program %q, %v; want %s from %s", got.DroidRules, got.DroidProgram, err, checkedTail, fake.Path)
				}
			} else if !errors.Is(err, ErrPermissionRulesRefused) || err.Error() != ruleSetRefusal(-1, tc.want) {
				t.Fatalf("refusal = %v\nwant %s", err, ruleSetRefusal(-1, tc.want))
			}
			if tc.mode == droidfix.Hang && elapsed >= DroidRulesCheckDeadline {
				t.Fatalf("hung check ended after %v; want its %v deadline to stop it", elapsed, short)
			}
			calls := fake.Calls(t)
			switch {
			case tc.program != "":
				if len(calls) != 0 {
					t.Fatalf("the fake ran %d times; want no run", len(calls))
				}
			case tc.mode != droidfix.Hang:
				if len(calls) != 1 {
					t.Fatalf("the fake ran %d times; want one check", len(calls))
				}
				c := calls[0]
				if !slices.Equal(c.Args, []string{"rules", "check", "--file", c.File}) || string(c.Input) != checkedTail || c.FileMode != 0o600 || c.DirMode != 0o700 {
					t.Fatalf("check call: args %q, input %s, modes %o/%o; want rules check --file <copy>, %s, 600/700", c.Args, c.Input, c.FileMode, c.DirMode, checkedTail)
				}
			}
			entries, readErr := os.ReadDir(tmp)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !tc.lock {
				if len(entries) != 0 {
					t.Fatalf("temporary directory holds %v after the check; want nothing", entries)
				}
				return
			}
			// The refusal names the directory left behind, holding the copy.
			if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "agent-director-droid-rules-") {
				t.Fatalf("temporary directory holds %v; want the one agent-director-droid-rules-* directory", entries)
			}
			if left, err := os.ReadFile(filepath.Join(tmp, entries[0].Name(), "rules.json")); err != nil || string(left) != checkedTail {
				t.Fatalf("left copy = %s, %v", left, err)
			}
		})
	}
}

// TestDroidRuleSetCheckEnvironment (SR-2.4): the check gets the version
// probe's environment without DroidForbiddenEnvKeys: no agent-director
// identity key, tmux key, host credential or Droid credential, ambient or
// supplied, while other keys and a supplied PATH stay; the program comes from
// droid_path or that PATH, and the launch runs the program that checked.
func TestDroidRuleSetCheckEnvironment(t *testing.T) {
	// Serial: it sets HOME and the ambient environment with t.Setenv.
	t.Setenv("HOME", t.TempDir())
	withheld := []string{"AGENT_DIRECTOR_INSTANCE_ID", "AGENT_DIRECTOR_AMBIENT", "TMUX", "TMUX_PANE",
		"OPENROUTER_API_KEY", "FACTORY_API_KEY", "AWS_BEARER_TOKEN_BEDROCK", "FACTORY_HOME_OVERRIDE"}
	for _, key := range withheld {
		t.Setenv(key, "ambient-"+plantedValue)
	}
	t.Setenv("AMBIENT_KEEP", "kept")
	for _, lookup := range []string{"droid_path", "PATH"} {
		t.Run(lookup, func(t *testing.T) {
			fake := droidfix.New(t, droidfix.Pass)
			cfg := config.Default()
			cfg.Experimental.Droid = true
			cfg.Launch.DroidRulesPath = writeRuleSet(t, droidfix.RuleSet("tail"))
			extra := map[string]string{"SUPPLIED_KEEP": "kept"}
			if lookup == "PATH" {
				extra["PATH"] = filepath.Dir(fake.Path)
			} else {
				cfg.Launch.DroidPath = fake.Path
			}
			r, err := Resolve(SpawnParams{Agent: "droid", CWD: t.TempDir(), AgentArgs: []string{"--model", "model"}, ExtraEnv: extra,
				AgentDirectorLabels: map[string]string{"team": "core"}}, cfg)
			mustDo(t, err)
			mustDo(t, Validate(&r))
			_, err = ApplyDefaults(&r, cfg, nil)
			mustDo(t, err)
			// Validation refuses these in extra_env; the check withholds them anyway.
			for _, key := range withheld {
				r.ExtraEnv[key] = "supplied-" + plantedValue
			}
			r.LaunchStoreID, r.LaunchToken = "store", rulesToken
			checked, err := ResolveDroidRules(r, cfg)
			if err != nil || checked.DroidProgram != fake.Path {
				t.Fatalf("check: program %q, %v; want %s", checked.DroidProgram, err, fake.Path)
			}
			calls := fake.Calls(t)
			if len(calls) != 1 {
				t.Fatalf("the fake ran %d times; want one check", len(calls))
			}
			env := calls[0].Env
			for key, value := range env {
				if strings.HasPrefix(key, "AGENT_DIRECTOR_") || slices.Contains(withheld, key) || strings.Contains(value, plantedValue) {
					t.Errorf("check environment holds %s", key)
				}
			}
			if env["AMBIENT_KEEP"] != "kept" || env["SUPPLIED_KEEP"] != "kept" || lookup == "PATH" && env["PATH"] != extra["PATH"] {
				t.Errorf("check environment lost ordinary keys: AMBIENT_KEEP=%q SUPPLIED_KEEP=%q PATH=%q", env["AMBIENT_KEEP"], env["SUPPLIED_KEEP"], env["PATH"])
			}
			c, err := ComposeLaunch(checked, cfg)
			if err != nil || len(c.Create.Command) != 3 || c.Create.Command[0] != fake.Path {
				t.Fatalf("launch command %q, %v; want the checked program %s", c.Create.Command, err, fake.Path)
			}
		})
	}
}

// unlockEntries gives each directory in dir back its owner's write
// permission, so a test's cleanup can remove what a locked check left.
func unlockEntries(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		_ = os.Chmod(filepath.Join(dir, e.Name()), 0o700)
	}
}

// mustDo fails the test on err.
func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
