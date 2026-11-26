package main_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// b.51h: use the parity harness's registrations so new single-value flags
// acquire CLI regression coverage without maintaining another verb/flag list.
func TestB51hRepeatedVerbFlags(t *testing.T) {
	scan := scanCLISources(t)
	fakeDir := buildFakeTmux(t)
	// These are the collecting values (newStringSlice/newKVSlice), not scalars.
	repeatable := map[string]bool{"label": true, "extra-env": true, "allow": true, "deny": true, "ask": true, "agent-args": true}
	for _, verb := range sortedKeys(scan.flags) {
		if verb == "hook" { // hooks deliberately remain lenient
			continue
		}
		params := map[string]manifest.ParamDef{}
		for _, v := range manifest.Verbs {
			if v.Name == verb {
				for _, p := range v.Params {
					params[strings.ReplaceAll(p.Name, "_", "-")] = p
				}
			}
		}
		for _, f := range scan.flags[verb] {
			if repeatable[f.name] {
				continue
			}
			p, ok := params[f.name]
			if !ok {
				t.Fatalf("%s --%s has no manifest parameter", verb, f.name)
			}
			isBool := strings.TrimSuffix(p.Type, "?") == "bool"
			forms := []string{"separate", "equals", "mixed", "single-dash"}
			if isBool {
				// Go bool values use =value, never a separate following word.
				forms = []string{"equals", "bare", "mixed", "single-dash"}
			}
			for _, form := range forms {
				t.Run(verb+"/"+f.name+"/"+form, func(t *testing.T) {
					home := t.TempDir()
					value := repeatedFlagValue(p, f.name, home)
					args := []string{verb}
					if verb == "trail-emit" {
						args = append(args, "relay-attempt")
					}
					for _, other := range scan.flags[verb] {
						q := params[other.name]
						if other.name != f.name && (q.Required || other.name == "no-pre-trust") {
							args = append(args, "--"+other.name+"="+repeatedFlagValue(q, other.name, home))
						}
					}
					switch form {
					case "separate":
						args = append(args, "--"+f.name, value, "--"+f.name, value)
					case "equals":
						args = append(args, "--"+f.name+"="+value, "--"+f.name+"="+value)
					case "bare":
						args = append(args, "--"+f.name, "--"+f.name)
					case "mixed":
						args = append(args, "--"+f.name)
						if !isBool {
							args = append(args, value)
						}
						args = append(args, "--"+f.name+"="+value)
					case "single-dash":
						args = append(args, "-"+f.name)
						if !isBool {
							args = append(args, value)
						}
						args = append(args, "--"+f.name+"="+value)
					}
					if verb == "spawn" {
						args = append(args, "--", "native-prompt")
					}
					stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
					assertRepeatedFlagsNoFiles(t, home)
					env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
					if want := "flag --" + f.name + " may only be specified once"; env.ErrDescription != want {
						t.Errorf("description=%q; want %q", env.ErrDescription, want)
					}
				})
			}
		}
	}
}

func repeatedFlagValue(p manifest.ParamDef, name, home string) string {
	switch name {
	case "cwd", "endpoint":
		return home
	case "name", "template", "tmux-session-name":
		return "repeated-flag"
	case "instance-id", "parent", "request-token", "token", "caller-uuid", "force-id":
		return "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	case "expect-pane-sha256":
		return strings.Repeat("a", 64)
	case "older-than":
		return "7d"
	case "outcome":
		return "200"
	case "runtime-args":
		return "[]"
	case "runtime":
		return "/nonexistent/b51h-cursor"
	}
	if len(p.AllowedValues) > 0 {
		return p.AllowedValues[0]
	}
	switch strings.TrimSuffix(p.Type, "?") {
	case "bool":
		return "true"
	case "int", "int64":
		return "1"
	}
	return "value"
}

// Rejection must precede client setup, template writing and row lookup.
func assertRepeatedFlagsNoFiles(t *testing.T, home string, overrides ...string) {
	t.Helper()
	assertHomeTree(t, home, "tmux-tmpdir")
	for _, dir := range overrides {
		assertHomeTree(t, dir)
	}
	for _, path := range []string{stateDB(home), filepath.Join(directorDir(home), "templates")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("refused flags wrote %s (stat=%v)", path, err)
		}
	}
}

// b.51h: bad earlier values cannot be erased by a valid final copy, and
// row-dependent options must be refused before the unknown-id lookup.
func TestB51hRepeatedFlagsBeforeValidation(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	const id = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	for _, args := range [][]string{
		{"list", "--limit", "-1", "--limit", "0"},
		{"expire", "--older-than", "bogus", "--older-than", "7d"},
		{"list", "--state", "bogus", "--state", "working"},
		{"read-pane", "--instance-id", id, "--n-lines", "5", "--n-lines", "50"},
		{"send-keys", "--instance-id", id, "--text", "one", "--text", "two"},
		{"decide", "--instance-id", id, "--request-token", id, "--decision", "deny", "--decision", "allow"},
	} {
		t.Run(args[0]+"/"+args[len(args)-4], func(t *testing.T) {
			home := t.TempDir()
			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
			assertRepeatedFlagsNoFiles(t, home)
			env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
			if want := "flag " + args[len(args)-4] + " may only be specified once"; env.ErrDescription != want {
				t.Errorf("description=%q; want %q", env.ErrDescription, want)
			}
		})
	}
}

// b.51h decision A: global occurrences count across both parsing stages.
func TestB51hRepeatedGlobalFlags(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, name := range []string{"store-path", "home", "tmux-command", "create-if-missing"} {
		for _, position := range []string{"prefix", "cross-stage", "verb"} {
			t.Run(name+"/"+position, func(t *testing.T) {
				home, x, y := t.TempDir(), t.TempDir(), t.TempDir()
				a, b := x, y
				switch name {
				case "store-path":
					a, b = filepath.Join(x, "s.db"), filepath.Join(y, "s.db")
				case "tmux-command":
					a, b = filepath.Join(fakeDir, "tmux"), filepath.Join(fakeDir, "tmux")
				case "create-if-missing":
					a, b = "false", "true"
				}
				first, second := []string{"--" + name, a}, []string{"--" + name + "=" + b}
				var args []string
				switch position {
				case "prefix":
					args = append(append(first, "--"+name, b), "list")
				case "cross-stage":
					args = append(append(first, "list"), "--"+name, b)
				case "verb":
					args = append(append([]string{"list"}, first...), second...)
				}
				stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
				assertRepeatedFlagsNoFiles(t, home, x, y)
				for _, dir := range []string{x, y} {
					if _, err := os.Stat(filepath.Join(dir, "s.db")); !os.IsNotExist(err) {
						t.Errorf("refused global wrote store in %s: %v", dir, err)
					}
				}
				env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
				if want := "flag --" + name + " may only be specified once"; env.ErrDescription != want {
					t.Errorf("description=%q; want %q", env.ErrDescription, want)
				}
			})
		}
	}
}

func TestB51hRepeatableControls(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	t.Run("template-collectors", func(t *testing.T) {
		home := t.TempDir()
		args := []string{"make-template", "--name", "rep", "--agent", "claude",
			"--label", "a=1", "--label=b=2", "--extra-env", "X1=1", "--extra-env=X2=2",
			"--allow", "Read", "--allow=Write", "--deny", "D1", "--deny=D2",
			"--ask", "A1", "--ask=A2", "--agent-args", "--x", "--agent-args=--y"}
		stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
		if code != 0 || stderr != "" {
			t.Fatalf("make-template: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
		var got config.TemplateFile
		if _, err := toml.DecodeFile(filepath.Join(directorDir(home), "templates", "rep.toml"), &got); err != nil {
			t.Fatal(err)
		}
		want := config.TemplateFile{Agent: "claude", AgentArgs: []string{"--x", "--y"},
			ExtraEnv: map[string]string{"X1": "1", "X2": "2"}, AgentDirectorLabels: map[string]string{"a": "1", "b": "2"},
			Permissions: &config.TemplatePermissions{Allow: []string{"Read", "Write"}, Deny: []string{"D1", "D2"}, Ask: []string{"A1", "A2"}}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("template=%+v permissions=%+v; want %+v permissions=%+v", got, got.Permissions, want, want.Permissions)
		}
	})
	t.Run("list-labels-AND", func(t *testing.T) {
		home := t.TempDir()
		for _, row := range []struct{ id, labels string }{{"both", `{"a":"1","b":"2"}`}, {"a-only", `{"a":"1"}`}, {"b-only", `{"b":"2"}`}} {
			if _, err := apitest.SeedSpawn(stateDB(home), row.id, "ended", home, "off", "", true, apitest.WithRawLabels(row.labels)); err != nil {
				t.Fatal(err)
			}
		}
		stdout, stderr, code := runSpawnCLI(t, home, fakeDir, "list", "--label", "a=1", "--label=b=2")
		var got struct {
			Spawns []struct {
				ID string `json:"instance_id"`
			} `json:"spawns"`
		}
		if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &got) != nil || len(got.Spawns) != 1 || got.Spawns[0].ID != "both" {
			t.Fatalf("list must retain both filters: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
	t.Run("spawn-after-separator", func(t *testing.T) {
		home := t.TempDir()
		native := []string{"--cwd", "one", "--cwd=two", "--no-pre-trust", "--no-pre-trust"}
		args := append([]string{"--cwd", t.TempDir(), "--no-pre-trust", "--"}, native...)
		id := spawnOK(t, home, fakeDir, args...)
		if got := jsonOf(t, getRow(t, home, fakeDir, id)["agent_args"]); got != jsonOf(t, native) {
			t.Errorf("agent_args=%s; want %s", got, jsonOf(t, native))
		}
	})
	t.Run("hook-remains-lenient", func(t *testing.T) {
		stdout, stderr, code := runSpawnCLI(t, t.TempDir(), fakeDir, "hook", "--agent", "claude", "--agent=codex", "--timeout=1", "--timeout=2")
		// A hook without a launch may log a diagnostic; it must still fail open.
		if code != 0 || stdout != "" || strings.Contains(stderr, "ErrInvalidFlags") {
			t.Fatalf("hook: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}

func TestB51hSingleGlobalControls(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	for _, name := range []string{"store-path", "home", "tmux-command", "create-if-missing"} {
		for _, position := range []string{"prefix", "verb"} {
			t.Run(name+"/"+position, func(t *testing.T) {
				home, other := t.TempDir(), t.TempDir()
				value, wantStore := "true", stateDB(home)
				switch name {
				case "store-path":
					value = filepath.Join(other, "s.db")
					wantStore = value
				case "home":
					value, wantStore = other, stateDB(other)
				case "tmux-command":
					value = filepath.Join(fakeDir, "tmux")
				}
				args := []string{"--" + name, value, "list"}
				if position == "verb" {
					args = []string{"list", "--" + name + "=" + value}
				}
				stdout, stderr, code := runSpawnCLI(t, home, fakeDir, args...)
				if code != 0 || stderr != "" || strings.TrimSpace(stdout) != `{"spawns":[]}` {
					t.Fatalf("single global: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
				if _, err := os.Stat(wantStore); err != nil {
					t.Errorf("single global did not open %s: %v", wantStore, err)
				}
			})
		}
	}
}
