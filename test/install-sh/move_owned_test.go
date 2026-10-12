package installsh_test

// What install.sh does, after it moves ~/.agent-director to
// ~/.config/agent-director (SR-11.7), to the entries it owns outside that
// directory: the help hooks in ~/.claude/settings.json, AD's MCP registration
// (read with `claude mcp get`, through a stand-in claude that records every
// call and replays captures from testdata/claude-mcp/) and the PATH links.
// Assertions read the stand-in's recorded calls and the files the install
// leaves, and the lines it prints.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const ownedClaudeDir = "testdata/claude-mcp"

// ownedCase is one `mcp get agent-director` capture, named as in the index,
// and what the installer must make of it.
type ownedCase struct {
	name string
	// kind: none (not found), oldPlain (the old binary, Args exactly
	// `serve --stdio`, no Environment: entry (a bare Environment: line is
	// none), a scope), oldOther (the old directory
	// but another form, args, environment or no scope) or other (another command).
	kind string
	// scope is the `-s` value the capture's remove line holds ("" when it has none).
	scope string
}

// ownedCases is every case each capture set must hold.
var ownedCases = []ownedCase{
	{"none-found-many-others", "none", ""},
	{"old-plain-local", "oldPlain", "local"},
	{"old-plain-project", "oldPlain", "project"},
	{"old-plain-user", "oldPlain", "user"},
	{"old-plain-extras", "oldPlain", "local"},
	// A bare `Environment:` line (nothing under it) is how Claude Code prints an
	// empty env: it is still a plain registration and is re-pointed.
	{"old-plain-empty-env", "oldPlain", "user"},
	{"old-env", "oldOther", "user"},
	{"old-args", "oldOther", "project"},
	{"old-other-form", "oldOther", ""},
	{"old-no-scope", "oldOther", ""},
	{"other-command", "other", "local"},
	{"other-command-near-old-dir", "other", "user"},
	{"other-command-extras", "other", "user"},
}

// ownedSets lists the capture sets (a directory per Claude Code version).
func ownedSets(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(ownedClaudeDir)
	if err != nil {
		t.Fatal(err)
	}
	var sets []string
	for _, e := range entries {
		if e.IsDir() {
			sets = append(sets, e.Name())
		}
	}
	if len(sets) == 0 {
		t.Fatalf("%s holds no capture set", ownedClaudeDir)
	}
	return sets
}

// ownedIndex reads INDEX.tsv: set/case to kind, and to the exit status it names.
func ownedIndex(t *testing.T) (kind, exit map[string]string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(ownedClaudeDir, "INDEX.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	kind, exit = map[string]string{}, map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "file\t") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 5 {
			t.Fatalf("INDEX.tsv row has %d columns, want 5: %q", len(f), line)
		}
		kind[f[0]], exit[f[0]] = f[3], f[2]
	}
	return kind, exit
}

// TestOwnedCaptureSets checks the capture sets and their index: every set
// holds every case with its exit status, every file is indexed as synthetic or
// captured, and the index names no file that is not there. A real capture
// replacing a placeholder keeps the same names, so it passes unchanged.
func TestOwnedCaptureSets(t *testing.T) {
	kind, exit := ownedIndex(t)
	seen := map[string]bool{}
	for _, set := range ownedSets(t) {
		for _, c := range ownedCases {
			key := set + "/" + c.name
			seen[key] = true
			base := filepath.Join(ownedClaudeDir, key)
			for _, ext := range []string{".stdout", ".exit"} {
				if _, err := os.Stat(base + ext); err != nil {
					t.Errorf("%s: %v", key, err)
				}
			}
			if k := kind[key]; k != "synthetic" && k != "captured" {
				t.Errorf("%s: index kind = %q, want synthetic or captured", key, k)
			}
			code, err := os.ReadFile(base + ".exit")
			if err != nil {
				continue
			}
			if got := strings.TrimSpace(string(code)); got != exit[key] {
				t.Errorf("%s: .exit holds %q, index says %q", key, got, exit[key])
			}
			if n, err := strconv.Atoi(strings.TrimSpace(string(code))); err != nil || (n != 0) != (c.kind == "none") {
				t.Errorf("%s: exit status %q does not fit a %s case", key, code, c.kind)
			}
		}
		files, _ := filepath.Glob(filepath.Join(ownedClaudeDir, set, "*"))
		for _, f := range files {
			base := strings.TrimSuffix(f, filepath.Ext(f))
			rel, _ := filepath.Rel(ownedClaudeDir, base)
			if !seen[rel] {
				t.Errorf("%s is not a known case (add it to ownedCases or remove it)", f)
			}
		}
	}
	for key := range kind {
		if !seen[key] {
			t.Errorf("index names %s, which has no case in a set", key)
		}
	}
}

// ownedCall is one recorded call of the stand-in claude.
type ownedCall struct {
	Pwd          string
	NewBin, Kept bool
	Args         []string
}

func (c ownedCall) line() string { return strings.Join(c.Args, " ") }

// ownedClaude puts the stand-in claude first on PATH, replaying the capture
// set/name for `mcp get agent-director` ("" set: none-found-many-others of the
// first set).
func ownedClaude(t *testing.T, h *moveHome, set, name string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join(ownedClaudeDir, "claude.sh"))
	if err != nil {
		t.Fatal(err)
	}
	h.Copy(script, filepath.Join(h.Tools, "claude"), 0o755)
	h.Env["CLAUDE_STANDIN_LOG"] = filepath.Join(h.Root, "claude-calls.log")
	if set == "" {
		set = ownedSets(t)[0]
	}
	capture, err := filepath.Abs(filepath.Join(ownedClaudeDir, set, name))
	if err != nil {
		t.Fatal(err)
	}
	h.Env["CLAUDE_STANDIN_CAPTURE"] = capture
}

// ownedCalls reads the calls the stand-in recorded.
func ownedCalls(t *testing.T, h *moveHome) []ownedCall {
	t.Helper()
	b, err := os.ReadFile(h.Env["CLAUDE_STANDIN_LOG"])
	if os.IsNotExist(err) {
		return nil
	} else if err != nil {
		t.Fatal(err)
	}
	var calls []ownedCall
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		f := strings.SplitN(line, "\t", 4)
		switch f[0] {
		case "CALL":
			calls = append(calls, ownedCall{Pwd: f[1], NewBin: f[2] == "1", Kept: f[3] == "1"})
		case "ARG":
			calls[len(calls)-1].Args = append(calls[len(calls)-1].Args, f[1])
		default:
			t.Fatalf("claude call log: unexpected line %q", line)
		}
	}
	return calls
}

// ownedMCPCalls is the lines of the calls that are not `--version`.
func ownedMCPCalls(calls []ownedCall) []ownedCall {
	var out []ownedCall
	for _, c := range calls {
		if len(c.Args) > 0 && c.Args[0] == "mcp" {
			out = append(out, c)
		}
	}
	return out
}

func ownedLines(calls []ownedCall) []string {
	out := []string{}
	for _, c := range calls {
		out = append(out, c.line())
	}
	return out
}

// ownedWorkDir gives the case a working directory of its own, which the
// install runs in and `mcp get` must run in.
func ownedWorkDir(t *testing.T, h *moveHome) {
	t.Helper()
	h.Cwd = filepath.Join(h.Home, "work", "project")
	if err := os.MkdirAll(h.Cwd, 0o755); err != nil {
		t.Fatal(err)
	}
}

func ownedCanonical(h *moveHome) string { return filepath.Join(h.New, "bin", "agent-director") }

// ownedExpect is what an install shows for a capture of the given kind, with
// and without --register-mcp: the calls after `mcp get agent-director`, and
// what stdout holds and lacks.
func ownedExpect(h *moveHome, c ownedCase, register bool) (calls []string, want moveWant) {
	canon := ownedCanonical(h)
	scope := c.scope
	if c.kind == "oldOther" && scope == "" {
		scope = "<scope>"
	}
	remove := "mcp remove agent-director -s " + scope
	add := fmt.Sprintf("mcp add -s %s agent-director %s serve --stdio", scope, canon)
	plain := fmt.Sprintf("mcp add agent-director %s serve --stdio", canon)
	switch c.kind {
	case "none":
		want.NoStdout = []string{"mcp remove", "could not read"}
	case "oldPlain":
		if register {
			calls = []string{remove, add}
			want.Stdout = []string{"re-registered with claude mcp (scope " + scope + ")"}
			want.NoStdout = []string{"could not read", "registered with claude mcp\n"}
		} else {
			want.Stdout = []string{remove, add}
			want.NoStdout = []string{"could not read"}
		}
	case "oldOther":
		want.Stdout = []string{"could not read the registration", remove, add}
	case "other":
		want.NoStdout = []string{"mcp remove", "could not read", "found from this directory", "re-point", "Re-point"}
		if !register {
			want.NoStdout = append(want.NoStdout, "  mcp     :")
		}
	}
	if register && c.kind != "oldPlain" {
		calls = []string{plain}
		want.Stdout = append(want.Stdout, "mcp     : registered with claude mcp")
	}
	return calls, want
}

// ownedInstall builds an old install and runs install.sh against the stand-in
// claude with the capture set/name. It returns the home and the result.
func ownedInstall(t *testing.T, set, name string, register bool, env map[string]string) (*moveHome, moveResult) {
	t.Helper()
	h := newMoveHome(t)
	ownedClaude(t, h, set, name)
	ownedWorkDir(t, h)
	for k, v := range env {
		h.Env[k] = v
	}
	h.BuildOld(withMoveNoStore())
	var args []string
	if register {
		args = append(args, "--register-mcp")
	}
	return h, h.Install(args...)
}

// ownedCheckGets checks what every run of an upgrade must show of its claude
// calls: exactly one `mcp get agent-director`, first of the mcp calls, made in
// the install's working directory and after the move, and none naming another
// server; then the calls after it are want.
func ownedCheckGets(t *testing.T, h *moveHome, want []string) {
	t.Helper()
	mcp := ownedMCPCalls(ownedCalls(t, h))
	if len(mcp) == 0 || mcp[0].line() != "mcp get agent-director" {
		t.Fatalf("claude mcp calls = %v, want `mcp get agent-director` first", ownedLines(mcp))
	}
	for _, c := range mcp {
		if !c.NewBin || !c.Kept {
			t.Errorf("`claude %s` ran before the move was done (new bin %v, kept dir %v)", c.line(), c.NewBin, c.Kept)
		}
		if strings.Contains(c.line(), "agent_director") {
			t.Errorf("`claude %s` names the near-name server", c.line())
		}
		if strings.HasPrefix(c.line(), "mcp list") {
			t.Errorf("`claude %s`: the installer reads one server, not the list", c.line())
		}
	}
	if mcp[0].Pwd != h.Cwd {
		t.Errorf("mcp get ran in %q, want the install's working directory %q", mcp[0].Pwd, h.Cwd)
	}
	if got := ownedLines(mcp[1:]); !reflect.DeepEqual(got, append([]string{}, want...)) && !(len(got) == 0 && len(want) == 0) {
		t.Errorf("claude mcp calls after `mcp get` = %q, want %q", got, want)
	}
}

// TestOwnedMCPOutcomes is SR-11.7's table of MCP registration outcomes: each
// capture, with and without --register-mcp, in every capture set.
func TestOwnedMCPOutcomes(t *testing.T) {
	for _, set := range ownedSets(t) {
		for _, c := range ownedCases {
			for _, register := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/register=%v", set, c.name, register)
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					h, res := ownedInstall(t, set, c.name, register, nil)
					calls, want := ownedExpect(h, c, register)
					want.Code = 0
					want.Stdout = append(want.Stdout, "kept    :")
					want.Check(t, h, res)
					ownedCheckGets(t, h, calls)
					if n := strings.Count(res.Stdout, "found from this directory"); n > 1 {
						t.Errorf("stdout says no registration was found %d times, want at most one:\n%s", n, res.Stdout)
					}
				})
			}
		}
	}
}

// TestOwnedMCPFailures covers a remove or add that fails on the way: the
// install goes on (exit 0) and names the commands that finish the job.
func TestOwnedMCPFailures(t *testing.T) {
	for _, set := range ownedSets(t) {
		rows := []struct {
			name, capture string
			env           map[string]string
			calls         func(h *moveHome) []string
			want          moveWant
		}{
			{
				name: "add-fails-after-the-remove", capture: "old-plain-user", env: map[string]string{"CLAUDE_STANDIN_ADD_FAIL": "1"},
				calls: func(h *moveHome) []string {
					return []string{"mcp remove agent-director -s user", fmt.Sprintf("mcp add -s user agent-director %s serve --stdio", ownedCanonical(h))}
				},
				want: moveWant{Stderr: []string{"was removed, but adding it again failed", "mcp add -s user agent-director {HOME}/.config/agent-director/bin/agent-director serve --stdio"},
					NoStdout: []string{"re-registered"}},
			},
			{
				name: "remove-fails", capture: "old-plain-project", env: map[string]string{"CLAUDE_STANDIN_REMOVE_FAIL": "1"},
				calls: func(h *moveHome) []string { return []string{"mcp remove agent-director -s project"} },
				want: moveWant{Stderr: []string{"removing it failed", "mcp remove agent-director -s project",
					"mcp add -s project agent-director {HOME}/.config/agent-director/bin/agent-director serve --stdio"},
					NoStdout: []string{"re-registered"}},
			},
			{
				name: "the-plain-add-fails", capture: "none-found-many-others", env: map[string]string{"CLAUDE_STANDIN_ADD_FAIL": "1"},
				calls: func(h *moveHome) []string {
					return []string{fmt.Sprintf("mcp add agent-director %s serve --stdio", ownedCanonical(h))}
				},
				want: moveWant{Stderr: []string{"registration failed"}, NoStdout: []string{"registered with claude mcp"}},
			},
		}
		for _, row := range rows {
			t.Run(set+"/"+row.name, func(t *testing.T) {
				t.Parallel()
				h, res := ownedInstall(t, set, row.capture, true, row.env)
				row.want.Code = 0
				row.want.Check(t, h, res)
				ownedCheckGets(t, h, row.calls(h))
			})
		}
	}
}

// TestOwnedMCPNotInPreflight: a refusal before the move records no claude
// mcp call (and the exit-5 ones no call at all), while an install that does
// not move reads the registration at the same step, in the working directory.
func TestOwnedMCPNotInPreflight(t *testing.T) {
	rows := []struct {
		name  string
		setup func(t *testing.T, h *moveHome)
		want  moveWant
		// anyCall is true when `claude --version` may have run (after pre-flight's refusals).
		anyCall bool
	}{
		{
			name: "both-directories-present",
			setup: func(t *testing.T, h *moveHome) {
				h.BuildOld()
				h.Write(filepath.Join(h.New, "config.toml"), "[defaults]\n", 0o600)
			},
			want: moveWant{Code: 5, Err: "ErrMoveConflict"},
		},
		{
			name:  "an-agent-is-live",
			setup: func(t *testing.T, h *moveHome) { h.BuildOld(withMoveLive("working")) },
			want:  moveWant{Code: 5, Err: "ErrAgentsLive"},
		},
		{
			name: "an-earlier-releases-pair",
			setup: func(t *testing.T, h *moveHome) {
				h.Pair = h.S.NoConfigDir
				h.BuildOld(withMoveNoStore())
			},
			want:    moveWant{Code: 3},
			anyCall: true,
		},
		{
			name: "the-copy-fails-on-a-full-disk",
			setup: func(t *testing.T, h *moveHome) {
				h.WriteTool("cp", "echo 'cp: No space left on device' >&2\nexit 1")
				h.BuildOld(withMoveNoStore())
			},
			want:    moveWant{Code: 5, Err: "ErrMoveFailed"},
			anyCall: true,
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			ownedClaude(t, h, "", "old-plain-local")
			ownedWorkDir(t, h)
			row.setup(t, h)
			res := h.Install("--register-mcp")
			h.Release()
			row.want.Check(t, h, res)
			calls := ownedCalls(t, h)
			if m := ownedMCPCalls(calls); len(m) > 0 {
				t.Errorf("a refused install made mcp calls: %v", ownedLines(m))
			}
			if !row.anyCall && len(calls) > 0 {
				t.Errorf("a pre-flight refusal made claude calls: %v", ownedLines(calls))
			}
		})
	}

	t.Run("a-fresh-install-reads-it-at-the-register-step", func(t *testing.T) {
		h := newMoveHome(t)
		ownedClaude(t, h, "", "none-found-many-others")
		ownedWorkDir(t, h)
		res := h.Install("--register-mcp")
		moveWant{Code: 0}.Check(t, h, res)
		mcp := ownedMCPCalls(ownedCalls(t, h))
		if len(mcp) != 2 || mcp[0].line() != "mcp get agent-director" || !strings.HasPrefix(mcp[1].line(), "mcp add agent-director ") {
			t.Fatalf("mcp calls = %v, want the get, then the plain add", ownedLines(mcp))
		}
		if mcp[0].Pwd != h.Cwd || !mcp[0].NewBin {
			t.Errorf("mcp get ran in %q with the new binary in place %v, want %q and true", mcp[0].Pwd, mcp[0].NewBin, h.Cwd)
		}
	})
}

// ownedSettings is a settings.json an old install left: help hooks naming the
// old directory's binary beside entries that are not AD's.
func ownedSettings(h *moveHome) string {
	old := filepath.Join(h.Old, "bin", "agent-director")
	return fmt.Sprintf(`{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "%[1]s help"}]},
      {"matcher": "startup", "hooks": [{"type": "command", "command": "/usr/local/bin/my-start"}]},
      {"hooks": [{"type": "command", "command": "/opt/wrapper %[1]s help"}]}
    ],
    "SessionEnd": [
      {"matcher": "compact", "hooks": [{"type": "command", "command": "%[1]s help"}]},
      {"hooks": [{"type": "command", "command": "/usr/local/bin/my-end"}]}
    ]
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}
`, old)
}

func ownedParse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, s)
	}
	return v
}

func ownedFileBytes(t *testing.T, path string) (string, os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), fi.Mode().Perm()
}

func ownedDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestOwnedSettingsHooks: after the move, hooks on drop the entries naming the
// old directory's binary and add the new ones, keeping every other entry and
// the file's mode, with a backup; --no-hooks leaves the file byte for byte and
// prints the fix.
func TestOwnedSettingsHooks(t *testing.T) {
	setup := func(t *testing.T, flags []string) (*moveHome, string) {
		t.Helper()
		h := newMoveHome(t)
		ownedClaude(t, h, "", "none-found-many-others")
		ownedWorkDir(t, h)
		h.Flags = flags
		h.BuildOld(withMoveNoStore())
		return h, filepath.Join(h.Home, ".claude", "settings.json")
	}

	t.Run("hooks-on-replace-the-old-entries", func(t *testing.T) {
		h, path := setup(t, []string{"--no-symlink"})
		orig := ownedSettings(h)
		h.Write(path, orig, 0o600)
		res := h.Install()
		moveWant{Code: 0, Stdout: []string{"hooks   : injected into " + path, "removed 2 hook entries that named the old directory's binary"}}.Check(t, h, res)
		help := ownedCanonical(h) + " help"
		old := filepath.Join(h.Old, "bin", "agent-director")
		want := ownedParse(t, fmt.Sprintf(`{
  "model": "opus",
  "hooks": {
    "SessionStart": [
      {"matcher": "startup", "hooks": [{"type": "command", "command": "/usr/local/bin/my-start"}]},
      {"hooks": [{"type": "command", "command": "/opt/wrapper %s help"}]},
      {"hooks": [{"type": "command", "command": %q}]}
    ],
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": "/usr/local/bin/my-end"}]},
      {"matcher": "compact", "hooks": [{"type": "command", "command": %q}]}
    ]
  },
  "permissions": {"allow": ["Bash(ls:*)"]}
}`, old, help, help))
		got, mode := ownedFileBytes(t, path)
		if !reflect.DeepEqual(ownedParse(t, got), want) {
			t.Errorf("settings.json after the install:\n%s\nwant (as JSON):\n%v", got, want)
		}
		if mode != 0o600 {
			t.Errorf("settings.json mode = %v, want 0600", mode)
		}
		// No command that starts with the old binary's path is left (the entry
		// whose command only holds it after another program is not AD's).
		var left []string
		for _, m := range regexp.MustCompile(`"command":\s*"([^"]*)"`).FindAllStringSubmatch(got, -1) {
			if strings.HasPrefix(m[1], old) {
				left = append(left, m[1])
			}
		}
		if len(left) > 0 {
			t.Errorf("settings.json still has commands starting with %s: %v", old, left)
		}
		backups, _ := filepath.Glob(path + ".bak.*")
		if len(backups) != 1 {
			t.Fatalf("backups = %v, want one", backups)
		}
		if b, bmode := ownedFileBytes(t, backups[0]); b != orig || bmode != 0o600 {
			t.Errorf("backup %s is not the original (mode %v):\n%s", backups[0], bmode, b)
		}
	})

	t.Run("hooks-on-with-no-old-entries-print-no-removal", func(t *testing.T) {
		h, path := setup(t, []string{"--no-symlink"})
		h.Write(path, `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/usr/local/bin/my-start"}]}]}}`+"\n", 0o600)
		res := h.Install()
		moveWant{Code: 0, Stdout: []string{"hooks   : injected into " + path}, NoStdout: []string{"removed", "old directory's binary"}}.Check(t, h, res)
		got, _ := ownedFileBytes(t, path)
		if !strings.Contains(got, "/usr/local/bin/my-start") || !strings.Contains(got, ownedCanonical(h)+" help") {
			t.Errorf("settings.json lacks the kept entry or the new ones:\n%s", got)
		}
	})

	t.Run("no-hooks-leaves-the-file-and-prints-the-fix", func(t *testing.T) {
		h, path := setup(t, []string{"--no-hooks", "--no-symlink"})
		orig := ownedSettings(h)
		h.Write(path, orig, 0o640)
		res := h.Install()
		old := filepath.Join(h.Old, "bin", "agent-director")
		moveWant{Code: 0,
			Stdout: []string{
				"hooks   : skipped (--no-hooks)",
				"holds hook entries that name the old directory's binary",
				"    SessionStart: " + old + " help",
				"    SessionEnd: " + old + " help",
				"Fix: re-run this install without --no-hooks",
				ownedCanonical(h),
			},
			NoStdout: []string{"/opt/wrapper", "my-start", "my-end"},
		}.Check(t, h, res)
		got, mode := ownedFileBytes(t, path)
		if got != orig || mode != 0o640 {
			t.Errorf("settings.json changed (mode %v):\n%s", mode, got)
		}
		if names := ownedDirNames(t, filepath.Dir(path)); !reflect.DeepEqual(names, []string{"settings.json"}) {
			t.Errorf("~/.claude holds %v, want settings.json alone (no backup, no temp file)", names)
		}
	})

	t.Run("no-hooks-with-no-old-entries-prints-nothing-about-hooks", func(t *testing.T) {
		h, path := setup(t, []string{"--no-hooks", "--no-symlink"})
		orig := `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/usr/local/bin/my-start"}]}]}}` + "\n"
		h.Write(path, orig, 0o600)
		res := h.Install()
		moveWant{Code: 0, Stdout: []string{"hooks   : skipped (--no-hooks)"}, NoStdout: []string{"old directory's binary", "Fix:"}}.Check(t, h, res)
		if got, _ := ownedFileBytes(t, path); got != orig {
			t.Errorf("settings.json changed:\n%s", got)
		}
	})

	t.Run("no-hooks-with-no-settings-file-creates-none", func(t *testing.T) {
		h, path := setup(t, []string{"--no-hooks", "--no-symlink"})
		res := h.Install()
		moveWant{Code: 0, NoStdout: []string{"old directory's binary"}}.Check(t, h, res)
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Errorf("~/.claude exists after an install with --no-hooks and no settings file (%v)", err)
		}
	})
}

// TestOwnedPathLinks: the link the install manages (in ~/.local/bin or
// --symlink-dir) leads to the new binary afterwards; another link into the old
// bin directory and an old $PATH entry are printed with their fix and left.
func TestOwnedPathLinks(t *testing.T) {
	oldBin := func(h *moveHome) string { return filepath.Join(h.Old, "bin", "agent-director") }
	leads := func(t *testing.T, link, want string) {
		t.Helper()
		got, err := os.Readlink(link)
		if err != nil {
			t.Errorf("%s: %v", link, err)
		} else if got != want {
			t.Errorf("%s -> %s, want %s", link, got, want)
		}
	}
	// printed checks that stdout names link as one still leading into the old
	// bin with the fix, leaves the link as it was, then runs the printed fix.
	printed := func(t *testing.T, h *moveHome, res moveResult, link, was string) {
		t.Helper()
		fix := fmt.Sprintf("    ln -sfn %s %s", ownedCanonical(h), link)
		moveWant{Code: 0, Stdout: []string{"old link: " + link + " still leads into the old directory's bin", fix}}.Check(t, h, res)
		leads(t, link, was)
		if r := h.Run(h.S.Bash, "-c", strings.TrimSpace(fix)); r.Code != 0 {
			t.Fatalf("the printed fix failed: %s %s", r.Stdout, r.Stderr)
		}
		leads(t, link, ownedCanonical(h))
	}
	notPrinted := moveWant{Code: 0, NoStdout: []string{"old link:", "old PATH:"}}
	rows := []struct {
		name  string
		flags []string
		setup func(t *testing.T, h *moveHome)
		check func(t *testing.T, h *moveHome, res moveResult)
	}{
		{
			name:  "the-managed-link-in-symlink-dir-is-re-pointed",
			flags: []string{"--no-hooks", "--symlink-dir", "{HOME}/bin"},
			setup: func(t *testing.T, h *moveHome) { h.Link(oldBin(h), filepath.Join(h.Home, "bin", "agent-director")) },
			check: func(t *testing.T, h *moveHome, res moveResult) {
				notPrinted.Check(t, h, res)
				leads(t, filepath.Join(h.Home, "bin", "agent-director"), ownedCanonical(h))
			},
		},
		{
			name:  "the-managed-link-with-a-relative-target-is-re-pointed",
			flags: []string{"--no-hooks", "--symlink-dir", "{HOME}/bin"},
			setup: func(t *testing.T, h *moveHome) {
				h.Link("../.agent-director/bin/agent-director", filepath.Join(h.Home, "bin", "agent-director"))
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				notPrinted.Check(t, h, res)
				leads(t, filepath.Join(h.Home, "bin", "agent-director"), ownedCanonical(h))
			},
		},
		{
			name:  "the-managed-link-in-local-bin-is-re-pointed",
			flags: []string{"--no-hooks"},
			setup: func(t *testing.T, h *moveHome) {
				h.Link(oldBin(h), filepath.Join(h.Home, ".local", "bin", "agent-director"))
				// ~/.local/bin is the default link directory only while it is on PATH.
				h.Env["PATH"] = filepath.Join(h.Home, ".local", "bin") + ":" + h.Tools + ":" + h.S.Toolbox
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				notPrinted.Check(t, h, res)
				leads(t, filepath.Join(h.Home, ".local", "bin", "agent-director"), ownedCanonical(h))
			},
		},
		{
			name:  "a-local-bin-link-is-printed-under-no-symlink",
			flags: []string{"--no-hooks", "--no-symlink"},
			setup: func(t *testing.T, h *moveHome) {
				h.Link(oldBin(h), filepath.Join(h.Home, ".local", "bin", "agent-director"))
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				printed(t, h, res, filepath.Join(h.Home, ".local", "bin", "agent-director"), oldBin(h))
			},
		},
		{
			name:  "a-local-bin-link-is-printed-when-symlink-dir-is-elsewhere",
			flags: []string{"--no-hooks", "--symlink-dir", "{HOME}/bin"},
			setup: func(t *testing.T, h *moveHome) {
				if err := os.MkdirAll(filepath.Join(h.Home, "bin"), 0o755); err != nil {
					t.Fatal(err)
				}
				h.Link(oldBin(h), filepath.Join(h.Home, ".local", "bin", "agent-director"))
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				printed(t, h, res, filepath.Join(h.Home, ".local", "bin", "agent-director"), oldBin(h))
				// The link the install manages was created and leads to the new binary.
				leads(t, filepath.Join(h.Home, "bin", "agent-director"), ownedCanonical(h))
			},
		},
		{
			name:  "a-link-found-on-PATH-is-printed",
			flags: []string{"--no-hooks", "--no-symlink"},
			setup: func(t *testing.T, h *moveHome) {
				h.Link("../.agent-director/bin/agent-director", filepath.Join(h.Home, "other", "agent-director"))
				h.Env["PATH"] = filepath.Join(h.Home, "other") + ":" + h.Tools + ":" + h.S.Toolbox
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				printed(t, h, res, filepath.Join(h.Home, "other", "agent-director"), "../.agent-director/bin/agent-director")
			},
		},
		{
			name:  "a-link-to-another-binary-is-not-printed",
			flags: []string{"--no-hooks", "--no-symlink"},
			setup: func(t *testing.T, h *moveHome) {
				h.Link(h.S.Current.Main, filepath.Join(h.Home, ".local", "bin", "agent-director"))
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				notPrinted.Check(t, h, res)
				leads(t, filepath.Join(h.Home, ".local", "bin", "agent-director"), h.S.Current.Main)
			},
		},
		{
			name:  "an-old-PATH-entry-is-printed",
			flags: []string{"--no-hooks", "--no-symlink"},
			setup: func(t *testing.T, h *moveHome) {
				h.Env["PATH"] = filepath.Join(h.Old, "bin") + ":" + h.Tools + ":" + h.S.Toolbox
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				moveWant{Code: 0,
					Stdout:   []string{"old PATH: $PATH names " + filepath.Join(h.Old, "bin") + ", the old directory's bin", "replace it with " + filepath.Join(h.New, "bin") + ", then open a new shell"},
					NoStdout: []string{"old link:"}}.Check(t, h, res)
			},
		},
		{
			name:  "an-old-PATH-entry-with-a-trailing-slash-is-printed",
			flags: []string{"--no-hooks", "--no-symlink"},
			setup: func(t *testing.T, h *moveHome) {
				h.Env["PATH"] = filepath.Join(h.Old, "bin") + "/:" + h.Tools + ":" + h.S.Toolbox
			},
			check: func(t *testing.T, h *moveHome, res moveResult) {
				moveWant{Code: 0, Stdout: []string{"old PATH: $PATH names " + filepath.Join(h.Old, "bin") + "/"}}.Check(t, h, res)
			},
		},
		{
			name:  "other-PATH-entries-are-not-printed",
			flags: []string{"--no-hooks", "--no-symlink"},
			setup: func(t *testing.T, h *moveHome) {
				for _, d := range []string{"elsewhere", filepath.Join(".agent-director-tools", "bin")} {
					if err := os.MkdirAll(filepath.Join(h.Home, d), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				h.Env["PATH"] = strings.Join([]string{filepath.Join(h.Home, "elsewhere"), filepath.Join(h.Home, ".agent-director-tools", "bin"),
					filepath.Join(h.New, "bin"), h.Tools, h.S.Toolbox}, ":")
			},
			check: func(t *testing.T, h *moveHome, res moveResult) { notPrinted.Check(t, h, res) },
		},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			h := newMoveHome(t)
			ownedClaude(t, h, "", "none-found-many-others")
			h.BuildOld(withMoveNoStore())
			h.Flags = nil
			for _, f := range row.flags {
				h.Flags = append(h.Flags, h.expand(f))
			}
			row.setup(t, h)
			row.check(t, h, h.Install())
		})
	}
}
