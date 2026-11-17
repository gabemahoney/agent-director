package toolguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/toolguardfix"
)

// Later path/Execute cases append rows here; setup may create files and replace
// args, cwd or the decision seam without acquiring another invocation path.
type guardCase struct {
	name  string
	tool  string
	input any
	want  Result
	setup func(*testing.T, *guardCall)
	// next mutates the same fixture between two calls, retaining argv.
	next func(*testing.T, *guardCall) Result
}

type guardCall struct {
	files    toolguardfix.Layout
	args     []string
	envelope map[string]any
	stdin    io.Reader
	ctx      context.Context
	check    func(context.Context, payload, []string) reason
	started  <-chan struct{}
	finished <-chan struct{}
	release  func()
	expires  bool
	deadline time.Time
}

func TestGuardDecisions(t *testing.T) {
	// These literals are independent of the implementation's outcome formatter.
	failed := Result{2, "agent-director blocked this call: agent-director could not check it\n"}
	disabled := Result{2, "agent-director blocked this call: this call is disabled\n"}
	covered := Result{2, "agent-director blocked this call: this call accesses a protected path\n"}
	const marker = "DECOY-CARRIED-TEXT"
	const patch = "*** Begin Patch\n*** Add File: note.txt\n+ordinary text\n*** End Patch\n"
	cases := []guardCase{
		{name: "AskUser arbitrary fields", tool: "AskUser", input: map[string]any{"questions": 17, "file_path": nil, "future": true}, want: disabled},
		{name: "AskUser scalar input", tool: "AskUser", input: marker, want: disabled},
		{name: "AskUser no input", tool: "AskUser", want: disabled, setup: func(t *testing.T, c *guardCall) { delete(c.envelope, "tool_input") }},
	}
	for _, tool := range []string{"Task", "TodoWrite", "Script", "FetchUrl", "WebSearch", "mcp__decoy__Read", "Unknown-" + marker} {
		cases = append(cases, guardCase{name: "unchecked/" + tool, tool: tool, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.args[1], "command": "cat " + c.args[1], "future": []any{nil, true}}
		}})
	}
	for _, input := range []any{nil, 42, "opaque", []any{nil, true}} {
		cases = append(cases, guardCase{name: "unchecked input shape", tool: "Task", input: input})
	}
	cases = append(cases, guardCase{name: "unchecked absent input and unusable cwd", tool: "Script", setup: func(t *testing.T, c *guardCall) {
		delete(c.envelope, "tool_input")
		c.envelope["cwd"] = []any{false}
	}})

	// SR-6.1's allowed/required policy and hx's pinned Droid 1.5.0 schemas.
	// G1 payload rows 5-6 and 38 confirm Glob's array and scalar spellings.
	// Odd enum/number values deliberately protect type-only validation.
	checked := []struct {
		tool     string
		required string
		fields   map[string]any
	}{
		{"Read", "file_path", map[string]any{"file_path": "note.txt", "offset": json.Number("-1.5"), "limit": json.Number("1e1000"), "image_quality": "future-quality"}},
		{"Create", "file_path", map[string]any{"file_path": "note.txt", "content": marker}},
		{"Edit", "file_path", map[string]any{"file_path": "note.txt", "old_str": marker, "new_str": "replacement", "change_all": false}},
		{"ApplyPatch", "input", map[string]any{"input": patch}},
		{"LS", "", map[string]any{"directory_path": ".", "ignorePatterns": []string{"*.tmp"}}},
		{"Grep", "", map[string]any{"path": ".", "pattern": marker, "glob_pattern": "*.txt", "output_mode": "future-mode", "case_insensitive": true, "line_numbers": false, "context": 1, "context_before": 2, "context_after": 3, "head_limit": 4}},
		{"Glob", "", map[string]any{"folder": ".", "patterns": []string{"*.txt", "src/**/*.go"}, "excludePatterns": []string{"*.tmp"}}},
		{"Execute", "command", map[string]any{"command": "printf safe", "summary": marker, "timeout": json.Number("-0.5"), "riskLevel": "future-risk", "riskLevelReason": marker, "fireAndForget": true}},
	}
	for _, tool := range checked {
		minimal := map[string]any{}
		if tool.required != "" {
			minimal[tool.required] = tool.fields[tool.required]
		}
		cases = append(cases,
			guardCase{name: tool.tool + "/minimal", tool: tool.tool, input: minimal},
			guardCase{name: tool.tool + "/all fields", tool: tool.tool, input: tool.fields},
		)
		unknown := maps.Clone(tool.fields)
		unknown["unsupported_"+marker] = true
		cases = append(cases, guardCase{name: tool.tool + "/unknown key", tool: tool.tool, input: unknown, want: failed})
		if tool.required != "" {
			missing := maps.Clone(tool.fields)
			delete(missing, tool.required)
			cases = append(cases, guardCase{name: tool.tool + "/missing required", tool: tool.tool, input: missing, want: failed})
		}
	}
	cases = append(cases,
		guardCase{name: "Glob/scalar patterns", tool: "Glob", input: map[string]any{"patterns": "*.txt", "excludePatterns": "*.tmp"}},
		guardCase{name: "Glob/empty arrays", tool: "Glob", input: map[string]any{"patterns": []string{}, "excludePatterns": []string{}}},
		guardCase{name: "LS/empty array", tool: "LS", input: map[string]any{"ignorePatterns": []string{}}},
	)
	for _, bad := range []struct {
		name, tool string
		input      any
	}{
		{"null required string", "Read", map[string]any{"file_path": nil}},
		{"number instead of string", "Create", map[string]any{"file_path": "note.txt", "content": 3}},
		{"string instead of number", "Read", map[string]any{"file_path": "note.txt", "offset": "1"}},
		{"null number", "Grep", map[string]any{"head_limit": nil}},
		{"number instead of bool", "Edit", map[string]any{"file_path": "note.txt", "change_all": 1}},
		{"null bool", "Execute", map[string]any{"command": "printf safe", "fireAndForget": nil}},
		{"scalar instead of string array", "LS", map[string]any{"ignorePatterns": "*.tmp"}},
		{"null array", "LS", map[string]any{"ignorePatterns": nil}},
		{"nonstring array member", "LS", map[string]any{"ignorePatterns": []any{"*.tmp", false}}},
		{"null Glob union", "Glob", map[string]any{"patterns": nil}},
		{"object Glob union", "Glob", map[string]any{"patterns": map[string]any{}}},
		{"null Glob array member", "Glob", map[string]any{"patterns": []any{"*.txt", nil}}},
		{"nonstring exclusion member", "Glob", map[string]any{"excludePatterns": []any{2}}},
		{"null input object", "Read", nil},
		{"array input object", "LS", []any{}},
		{"scalar input object", "Grep", marker},
	} {
		cases = append(cases, guardCase{name: "invalid field/" + bad.name, tool: bad.tool, input: bad.input, want: failed})
	}
	cases = append(cases, guardCase{name: "checked input missing", tool: "Glob", want: failed, setup: func(t *testing.T, c *guardCall) { delete(c.envelope, "tool_input") }})
	for _, raw := range []struct{ name, text string }{
		{"empty", ""}, {"whitespace", " \n\t"}, {"malformed", "{bad"}, {"truncated", `{"hook_event_name":`},
		{"null", "null"}, {"array", "[]"}, {"scalar", "42"},
		{"second object", `{"hook_event_name":"PreToolUse","tool_name":"Task"} {}`},
		{"trailing garbage", `{"hook_event_name":"PreToolUse","tool_name":"Task"} !`},
	} {
		cases = append(cases, guardCase{name: "JSON/" + raw.name, want: failed, setup: func(t *testing.T, c *guardCall) { c.stdin = strings.NewReader(raw.text) }})
	}
	for _, bad := range []struct {
		name, key string
		value     any
		missing   bool
	}{
		{"missing event", "hook_event_name", nil, true},
		{"wrong event", "hook_event_name", "PostToolUse", false},
		{"mistyped event", "hook_event_name", 1, false},
		{"null event", "hook_event_name", nil, false},
		{"missing name", "tool_name", nil, true},
		{"mistyped name", "tool_name", []any{}, false},
		{"null name", "tool_name", nil, false},
	} {
		cases = append(cases, guardCase{name: "envelope/" + bad.name, tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) {
			if bad.missing {
				delete(c.envelope, bad.key)
			} else {
				c.envelope[bad.key] = bad.value
			}
		}})
	}
	for _, args := range [][]string{nil, {"--"}, {"/absolute"}, {"--help", "--", "/absolute"}, {"--", "relative"}, {"--", ""}, {"--", "/absolute", "relative"}} {
		cases = append(cases, guardCase{name: "invalid argv", tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) { c.args = args }})
	}
	cases = append(cases, guardCase{name: "absolute absent entries stay literal", tool: "Read", input: map[string]any{"file_path": "note.txt"}, setup: func(t *testing.T, c *guardCall) {
		wantEntries := []string{
			c.files.MissingSecret,
			filepath.Join(filepath.Dir(c.args[1]), "missing 'quoted' $dollar; space"),
			filepath.Join(filepath.Dir(c.args[1]), "another \"quoted\" entry"),
		}
		// Keep the expected slice separate from argv so rewriting argv in place
		// cannot rewrite the oracle. None of these paths exists under this HOME.
		c.args = append([]string{"--"}, wantEntries...)
		c.ctx = context.Background()
		called := false
		t.Cleanup(func() {
			if !called {
				t.Error("decision rule did not receive the entries")
			}
		})
		c.check = func(ctx context.Context, p payload, entries []string) reason {
			called = true
			if !slices.Equal(entries, wantEntries) {
				t.Errorf("decision entries = %q; want %q in this order", entries, wantEntries)
			}
			return decide(ctx, p, entries)
		}
	}})

	for _, tool := range []string{"Create", "Edit", "ApplyPatch"} {
		cases = append(cases, guardCase{name: "large carried text/" + tool, tool: tool, setup: func(t *testing.T, c *guardCall) {
			// guard.go's allowance is two 5 MiB Edit strings, or one 10 MiB
			// Create/patch string, plus JSON expansion. No provider size cap.
			text := strings.Repeat("ordinary text ", (5<<20)/14) + c.args[1]
			switch tool {
			case "Create":
				c.envelope["tool_input"] = map[string]any{"file_path": "note.txt", "content": text + text}
			case "Edit":
				c.envelope["tool_input"] = map[string]any{"file_path": "note.txt", "old_str": text, "new_str": text}
			case "ApplyPatch":
				c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Add File: note.txt\n+" + text + text + "\n*** End Patch\n"}
			}
		}})
	}
	cases = append(cases,
		guardCase{name: "over documented stdin cap", tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) {
			// Valid object plus legal whitespace: only the byte budget refuses it.
			const object = `{"hook_event_name":"PreToolUse","tool_name":"Task"}`
			c.stdin = io.MultiReader(strings.NewReader(object), io.LimitReader(readFunc(func(p []byte) (int, error) {
				for i := range p {
					p[i] = ' '
				}
				return len(p), nil
			}), (64<<20)+1-int64(len(object))))
		}},
		guardCase{name: "read error after valid object", tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) {
			c.stdin = io.MultiReader(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Task"}`), readFunc(func([]byte) (int, error) { return 0, errors.New(marker) }))
		}},
		guardCase{name: "reader panic", tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) {
			c.stdin = readFunc(func([]byte) (int, error) { panic(marker) })
		}},
		guardCase{name: "decision panic", tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) {
			c.ctx = context.Background()
			c.check = func(context.Context, payload, []string) reason { panic(marker) }
		}},
		guardCase{name: "unknown internal outcome", tool: "Task", want: failed, setup: func(t *testing.T, c *guardCall) {
			c.ctx = context.Background()
			c.check = func(context.Context, payload, []string) reason { return reason(255) }
		}},
	)
	for _, stage := range []string{"read", "decision"} {
		for _, expires := range []bool{false, true} {
			label, want := "healthy", Result{}
			if expires {
				label, want = "deadline", failed
			}
			cases = append(cases, guardCase{name: stage + "/" + label, tool: "Task", want: want, setup: func(t *testing.T, c *guardCall) {
				gateGuardOperation(t, c, stage, expires)
			}})
		}
	}

	// SR-6.2 / SR-8.1: real decoys, with independent witnesses for both
	// resolution orders and both interpretations of a tilde spelling.
	cases = append(cases,
		guardCase{name: "paths/directory equality", tool: "Read", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.SecretDir}
		}},
		guardCase{name: "paths/missing descendant", tool: "Create", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.MissingSecret + "/new/file", "content": "ordinary"}
		}},
		guardCase{name: "paths/existing file equality", tool: "Edit", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.SecretFile}
		}},
		guardCase{name: "paths/relative link", tool: "Read", want: covered, input: map[string]any{"file_path": "secret-link/key"}},
		guardCase{name: "paths/absolute parent link", tool: "Edit", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.ParentLink + "/.ssh/key"}
		}},
		guardCase{name: "paths/relative parent components", tool: "Create", want: covered, input: map[string]any{"file_path": "../home/.ssh/new/file"}},
		guardCase{name: "paths/resolved listed alias", tool: "Read", want: covered, setup: func(t *testing.T, c *guardCall) {
			// cwd, listed entry and absolute target are in three different folders.
			c.args = []string{"--", c.files.ListedLink}
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.ResolvedSecret + "/decoy"}
		}},
		guardCase{name: "paths/listed alias", tool: "Read", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.ListedLink + "/decoy"}
		}},
		guardCase{name: "paths/kernel order only", tool: "Read", want: covered, input: map[string]any{"file_path": "detour/../key"}},
		guardCase{name: "paths/lexical order only", tool: "Read", want: covered, input: map[string]any{"file_path": "detour/../protected"}, setup: func(t *testing.T, c *guardCall) {
			// The kernel form is HOME/.ssh/protected, outside this sole entry.
			c.args = []string{"--", filepath.Join(c.files.Work, "protected")}
		}},
		guardCase{name: "paths/missing safe suffix", tool: "Create", input: map[string]any{"file_path": "not-yet/new/file"}},
		guardCase{name: "paths/ordinary existing file", tool: "Read", setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.OrdinaryFile}
		}},
		guardCase{name: "paths/tilde HOME descendant", tool: "Read", input: map[string]any{"file_path": "~/.ssh/key"}, want: covered},
		guardCase{name: "paths/tilde HOME equality", tool: "Read", input: map[string]any{"file_path": "~"}, want: covered, setup: func(t *testing.T, c *guardCall) {
			c.args = []string{"--", c.files.Home}
		}},
		guardCase{name: "paths/tilde literal descendant", tool: "Read", input: map[string]any{"file_path": "~/key"}, want: covered, setup: func(t *testing.T, c *guardCall) {
			if err := os.Symlink(c.files.SecretDir, filepath.Join(c.files.Work, "~")); err != nil {
				t.Fatal(err)
			}
		}},
		guardCase{name: "paths/tilde literal equality", tool: "Read", input: map[string]any{"file_path": "~"}, want: covered, setup: func(t *testing.T, c *guardCall) {
			c.args = []string{"--", filepath.Join(c.files.Work, "~")}
		}},
		guardCase{name: "paths/root covers decoy", tool: "Read", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.args = []string{"--", "/"}
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.SafeFile}
		}},
		guardCase{name: "paths/fresh listed link", tool: "Read", setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.LateTarget + "/decoy"}
			fixed := slices.Clone(c.args)
			t.Cleanup(func() {
				if !slices.Equal(c.args, fixed) {
					t.Error("argv changed between decisions")
				}
			})
		}, next: func(t *testing.T, c *guardCall) Result {
			if err := os.Symlink(c.files.LateTarget, c.files.LateLink); err != nil {
				t.Fatal(err)
			}
			return covered
		}},
	)
	for _, sibling := range []string{"~/.sshx/key", "~/.ssh.bak/key"} {
		cases = append(cases, guardCase{name: "paths/component sibling " + sibling, tool: "Read", input: map[string]any{"file_path": sibling}})
	}
	for _, fold := range []bool{false, true} {
		want, label := Result{}, "Linux"
		if fold {
			want, label = covered, "Darwin simulated"
		}
		cases = append(cases, guardCase{name: "paths/case " + label, tool: "Read", want: want, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": filepath.Join(c.files.Home, ".SSH", "key")}
			c.ctx = context.Background()
			c.check = func(ctx context.Context, p payload, entries []string) reason {
				r := newPathResolver(ctx)
				r.foldCase = fold
				return decideFiles(p, entries, r)
			}
		}})
	}

	// SR-6.3: every header is a target; patch bodies are carried text.
	for _, header := range []string{"Add File", "Update File", "Delete File", "Move to"} {
		cases = append(cases, guardCase{name: "patch/late " + header, tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Update File: ordinary.txt\n@@\n-old\n+new\n*** " + header + ": " + c.files.SecretDirFile + "\n*** End Patch\n"}
		}})
	}
	cases = append(cases,
		guardCase{name: "patch/unsafe move source", tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Update File: " + c.files.SecretFile + "\n*** Move to: ordinary.txt\n*** End Patch\n"}
		}},
		guardCase{name: "patch/safe files and carried headers CRLF", tool: "ApplyPatch", setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\r\n*** Add File: new.txt\r\n+*** Delete File: " + c.files.SecretFile + "\r\n*** Update File: ordinary.txt\r\n@@ " + c.files.SecretDir + "\r\n-" + c.files.SecretFile + "\r\n+replacement\r\n*** Move to: moved.txt\r\n*** Delete File: old.txt\r\n*** End Patch\r\n"}
		}},
		guardCase{name: "patch/significant filename whitespace", tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.args = []string{"--", filepath.Join(c.files.Work, " protected ")}
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Add File:  protected \n+ordinary\n*** End Patch\n"}
		}},
		// R1: Droid trims header targets. Raw and provider interpretations
		// must both be checked, including JavaScript's non-ASCII whitespace.
		guardCase{name: "patch/R1 leading Add", tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Add File:   " + c.files.SecretDir + "/new\n+ordinary\n*** End Patch\n"}
		}},
		guardCase{name: "patch/R1 leading Delete", tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Delete File: \t" + c.files.SecretFile + "\n*** End Patch\n"}
		}},
		guardCase{name: "patch/R1 trailing Update", tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Update File: " + c.files.SecretFile + "\u00a0\n@@\n-old\n+new\n*** End Patch\n"}
		}},
		guardCase{name: "patch/R1 FEFF Move", tool: "ApplyPatch", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Update File: ordinary.txt\n*** Move to: \ufeff" + c.files.SecretDir + "/moved\n@@\n-old\n+new\n*** End Patch\n"}
		}},
		guardCase{name: "patch/R1 whitespace-only target", tool: "ApplyPatch", want: failed, input: map[string]any{"input": "*** Begin Patch\n*** Add File:  \t\u00a0\ufeff\n+ordinary\n*** End Patch\n"}},
		guardCase{name: "patch/R1 safe whitespace and NEL control", tool: "ApplyPatch", setup: func(t *testing.T, c *guardCall) {
			// U+0085 is a filename character, unlike U+FEFF and U+00A0.
			c.envelope["tool_input"] = map[string]any{"input": "*** Begin Patch\n*** Add File: \ufeff " + c.files.SafeDir + "/new \u00a0\n+ordinary\n*** Delete File: \u0085" + c.files.SecretFile + "\u0085\n*** End Patch\n"}
		}},
	)
	for _, bad := range []struct{ name, text string }{
		{"missing begin", "*** Add File: note.txt\n*** End Patch\n"},
		{"missing end", "*** Begin Patch\n*** Add File: note.txt\n+body\n"},
		{"trailing data", patch + "extra"},
		{"empty target", "*** Begin Patch\n*** Delete File: \n*** End Patch\n"},
		{"malformed header", "*** Begin Patch\n*** Add File:note.txt\n*** End Patch\n"},
		{"unknown control", "*** Begin Patch\n*** Add File: note.txt\n*** Unknown: " + marker + "\n*** End Patch\n"},
	} {
		cases = append(cases, guardCase{name: "patch/" + bad.name, tool: "ApplyPatch", input: map[string]any{"input": bad.text}, want: failed})
	}

	// Search-root checking is independent of patterns and carried exclusions.
	for _, tool := range []string{"LS", "Grep", "Glob"} {
		field := map[string]string{"LS": "directory_path", "Grep": "path", "Glob": "folder"}[tool]
		for _, root := range []string{"covered", "ancestor", "omitted ancestor", "resolved ancestor", "safe with links"} {
			want := covered
			if root == "safe with links" || (tool == "LS" && root != "covered") {
				want = Result{}
			}
			cases = append(cases, guardCase{name: "search/" + tool + "/" + root, tool: tool, want: want, setup: func(t *testing.T, c *guardCall) {
				path := c.files.Work
				switch root {
				case "covered":
					path = c.files.WorkLink
				case "ancestor", "omitted ancestor":
					path = c.files.Home
				case "resolved ancestor":
					path = c.files.ResolvedSecret
					c.args = []string{"--", c.files.ListedLink + "/decoy"}
				}
				fields := map[string]any{field: path}
				if root == "omitted ancestor" {
					delete(fields, field)
					c.envelope["cwd"] = path
				}
				switch tool {
				case "LS":
					fields["ignorePatterns"] = []string{c.files.SecretDir}
				case "Grep":
					fields["pattern"], fields["glob_pattern"] = c.files.SecretFile, c.files.SecretDir+"/*"
				case "Glob":
					fields["patterns"], fields["excludePatterns"] = []string{"narrow/*.txt"}, []string{c.files.SecretDir + "/*", "*/../ignored"}
				}
				c.envelope["tool_input"] = fields
			}})
		}
		// Explicit absolute roots need neither cwd nor HOME.
		cases = append(cases, guardCase{name: "search/" + tool + "/absolute without bases", tool: tool, setup: func(t *testing.T, c *guardCall) {
			delete(c.envelope, "cwd")
			if err := os.Unsetenv("HOME"); err != nil {
				t.Fatal(err)
			}
			c.envelope["tool_input"] = map[string]any{field: c.files.SafeDir}
		}})
	}
	cases = append(cases,
		guardCase{name: "search/root ancestor", tool: "Grep", input: map[string]any{"path": "/"}, want: covered},
		guardCase{name: "paths/absolute file without bases", tool: "Read", setup: func(t *testing.T, c *guardCall) {
			c.envelope["cwd"] = "relative"
			if err := os.Unsetenv("HOME"); err != nil {
				t.Fatal(err)
			}
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.SafeFile}
		}},
		guardCase{name: "Glob/absolute later pattern", tool: "Glob", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"folder": c.files.SafeDir, "patterns": []string{"*.txt", c.files.ResolvedSecret + "/*"}}
		}},
		guardCase{name: "Glob/literal prefix link", tool: "Glob", input: map[string]any{"patterns": "secret-link/*.txt"}, want: covered},
		guardCase{name: "Glob/raw root before parent", tool: "Glob", want: covered, setup: func(t *testing.T, c *guardCall) {
			// Resolving folder first would lose Work/protected's lexical form.
			c.args = []string{"--", filepath.Join(c.files.Work, "protected")}
			c.envelope["tool_input"] = map[string]any{"folder": c.files.DotDotLink, "patterns": "../protected/*"}
		}},
		guardCase{name: "Glob/folder sibling", tool: "Glob", want: covered, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"folder": c.files.GlobRoot, "patterns": "../secret/*"}
		}},
		guardCase{name: "Glob/in folder control", tool: "Glob", setup: func(t *testing.T, c *guardCall) {
			// Rebasing this onto cwd would see Work/secret-link and block.
			c.envelope["tool_input"] = map[string]any{"folder": c.files.GlobRoot, "patterns": "secret-link/*"}
		}},
		guardCase{name: "Glob/absolute safe without bases", tool: "Glob", setup: func(t *testing.T, c *guardCall) {
			delete(c.envelope, "cwd")
			if err := os.Unsetenv("HOME"); err != nil {
				t.Fatal(err)
			}
			c.envelope["tool_input"] = map[string]any{"folder": c.files.SafeDir, "patterns": c.files.SafeDir + "/*.txt"}
		}},
		guardCase{name: "Glob/bounded wildcard controls", tool: "Glob", input: map[string]any{"patterns": []string{"*.txt", "file?.go", "[ab]/*.go", "{src,lib}/**/*.go", `quoted\*/file`, `[{]/*.go`, `\{literal/*.go`, "missing/../*.txt"}}},
	)
	for _, interpretation := range []string{"literal link", "HOME"} {
		cases = append(cases, guardCase{name: "Glob/tilde root " + interpretation, tool: "Glob", want: covered, input: map[string]any{"folder": "~", "patterns": "../secret/*"}, setup: func(t *testing.T, c *guardCall) {
			c.args = []string{"--", c.files.GlobSecret}
			if interpretation == "HOME" {
				t.Setenv("HOME", c.files.GlobRoot)
			} else if err := os.Symlink(c.files.GlobRoot, filepath.Join(c.files.Work, "~")); err != nil {
				t.Fatal(err)
			}
		}})
	}
	for _, pattern := range []string{"*/../escape", "?/../escape", "[ab]/../escape", "{a,b}/../escape", `quoted\*/../escape`, `*/\.\./escape`, "{safe,../secret}/*", "{left/right,safe}/*", "{safe,..}/*", "{safe,~user}/*"} {
		cases = append(cases, guardCase{name: "Glob/unbounded " + pattern, tool: "Glob", input: map[string]any{"patterns": pattern}, want: failed})
	}

	// SR-5.4: required bases and metadata failures must fail closed, without
	// replacing real traversal with a decision stub.
	for _, cwd := range []string{"", "relative"} {
		for _, tool := range []string{"Read", "LS", "Grep", "Glob"} {
			cases = append(cases, guardCase{name: "bases/" + tool + "/cwd " + cwd, tool: tool, want: failed, setup: func(t *testing.T, c *guardCall) {
				c.envelope["cwd"] = cwd
				if cwd == "" {
					delete(c.envelope, "cwd")
				}
				fields := map[string]any{}
				if tool == "Read" {
					fields["file_path"] = "ordinary.txt"
				}
				c.envelope["tool_input"] = fields
			}})
		}
	}
	for _, home := range []string{"", "relative"} {
		cases = append(cases, guardCase{name: "bases/tilde HOME " + home, tool: "Read", input: map[string]any{"file_path": "~/ordinary"}, want: failed, setup: func(t *testing.T, c *guardCall) {
			if home == "" {
				if err := os.Unsetenv("HOME"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("HOME", home)
			}
		}})
	}
	for _, fault := range []string{"target permission", "entry permission", "link read"} {
		cases = append(cases, guardCase{name: "lookup/" + fault, tool: "Read", want: failed, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.SafeFile}
			c.ctx = context.Background()
			c.check = func(ctx context.Context, p payload, entries []string) reason {
				r := newPathResolver(ctx)
				if fault == "link read" {
					r.readlink = func(path string) (string, error) {
						if path == c.files.ListedLink {
							return "", errors.New(marker)
						}
						return os.Readlink(path)
					}
				} else {
					target := c.files.SafeFile
					if fault == "entry permission" {
						target = c.files.SecretDir
					}
					r.lstat = func(path string) (os.FileInfo, error) {
						if path == target {
							return nil, &os.PathError{Op: "lstat", Path: path, Err: os.ErrPermission}
						}
						return os.Lstat(path)
					}
				}
				return decideFiles(p, entries, r)
			}
		}})
	}
	for _, suffix := range []string{"/child", "/../safe", "/"} {
		cases = append(cases, guardCase{name: "lookup/nonfolder " + suffix, tool: "Read", input: map[string]any{"file_path": "ordinary.txt" + suffix}, want: failed})
	}
	for _, chain := range []bool{false, true} {
		label := "loop"
		if chain {
			label = "excessive chain"
		}
		cases = append(cases, guardCase{name: "lookup/" + label, tool: "Read", input: map[string]any{"file_path": "link-0"}, want: failed, setup: func(t *testing.T, c *guardCall) {
			count := 1
			if chain {
				count = 41
			}
			for i := 0; i < count; i++ {
				target := "link-0"
				if chain {
					target = fmt.Sprintf("link-%d", i+1)
					if i == count-1 {
						target = c.files.SafeFile
					}
				}
				if err := os.Symlink(target, filepath.Join(c.files.Work, fmt.Sprintf("link-%d", i))); err != nil {
					t.Fatal(err)
				}
			}
		}})
	}
	cases = append(cases, guardCase{name: "unchecked/no eager path work", tool: "Task", setup: func(t *testing.T, c *guardCall) {
		if err := os.Symlink("loop", filepath.Join(c.files.Work, "loop")); err != nil {
			t.Fatal(err)
		}
		c.args = []string{"--", filepath.Join(c.files.Work, "loop")}
		delete(c.envelope, "cwd")
		if err := os.Unsetenv("HOME"); err != nil {
			t.Fatal(err)
		}
		c.envelope["tool_input"] = map[string]any{"file_path": "~/loop"}
	}})
	for _, expires := range []bool{false, true} {
		label, want := "healthy", Result{}
		if expires {
			label, want = "original deadline", failed
		}
		cases = append(cases, guardCase{name: "lookup/" + label, tool: "Read", want: want, setup: func(t *testing.T, c *guardCall) {
			c.envelope["tool_input"] = map[string]any{"file_path": c.files.SafeFile}
			gateGuardOperation(t, c, "lookup", expires)
		}})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files, err := toolguardfix.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", files.Home)
			// Serial capture observes accidental output, including after expiry.
			stdout, stderr := captureGuardOutput(t)
			synctest.Test(t, func(t *testing.T) {
				c := guardCall{
					files: files,
					args:  append([]string{"--"}, files.Entries...),
					envelope: map[string]any{
						"hook_event_name": "PreToolUse", "tool_name": tc.tool, "tool_input": tc.input, "cwd": files.Work,
						"session_id": "decoy-session", "permission_mode": "auto-low",
						// This path cannot be read; envelope metadata stays opaque.
						"transcript_path": filepath.Join(files.Home, "absent", "transcript.jsonl"),
						"future_metadata": map[string]any{"value": nil},
					},
					check: decide,
				}
				if tc.setup != nil {
					tc.setup(t, &c)
				}
				want := tc.want
				for invocation := 0; ; invocation++ {
					if c.stdin == nil {
						c.stdin = guardInput(t, c.envelope)
					}
					result := make(chan Result, 1)
					go func() {
						if c.ctx == nil {
							result <- Run(c.args, c.stdin)
						} else {
							result <- run(c.ctx, c.args, c.stdin, c.check)
						}
					}()
					if c.started != nil {
						<-c.started
						if !c.expires {
							c.release()
						}
					}
					got := <-result
					synctest.Wait()
					assertGuardOutcome(t, got, want, stdout, stderr)
					if c.expires {
						if !c.deadline.IsZero() && !time.Now().Equal(c.deadline) {
							t.Errorf("outcome at %v; want original deadline %v", time.Now(), c.deadline)
						}
						select {
						case <-c.finished:
							t.Error("operation finished before its deadline outcome")
						default:
						}
						if c.ctx.Err() != context.DeadlineExceeded {
							t.Errorf("context error = %v; want deadline exceeded", c.ctx.Err())
						}
						c.release()
					}
					if c.finished != nil {
						<-c.finished
						// Join the released worker, including its private publication.
						// synctest also fails if any goroutine survives this test bubble.
						synctest.Wait()
						assertGuardOutcome(t, got, want, stdout, stderr)
					}
					if tc.next == nil || invocation != 0 {
						break
					}
					want = tc.next(t, &c)
					c.stdin = nil
				}
			})
		})
	}
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

func guardInput(t *testing.T, envelope map[string]any) io.Reader {
	t.Helper()
	b, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(b) + "\n \t")
}

// Hold an input read, metadata lookup or rule until released. synctest advances
// the deadline only when the caller and operation are both causally blocked.
func gateGuardOperation(t *testing.T, c *guardCall, stage string, expires bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	c.ctx, c.expires = ctx, expires
	started, finished, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c.started, c.finished = started, finished
	c.release = sync.OnceFunc(func() { close(release) })
	t.Cleanup(c.release)
	if stage == "read" {
		input := guardInput(t, c.envelope)
		first := true
		c.stdin = readFunc(func(p []byte) (int, error) {
			if first {
				first = false
				close(started)
				<-release
				defer close(finished)
			}
			return input.Read(p)
		})
	} else if stage == "lookup" {
		c.deadline, _ = ctx.Deadline()
		// In the fake clock, input consumes three quarters of the original
		// budget before metadata stalls. A renewed timeout would return late.
		input := guardInput(t, c.envelope)
		first := true
		c.stdin = readFunc(func(p []byte) (int, error) {
			if first {
				first = false
				<-time.After(3 * time.Second / 4)
			}
			return input.Read(p)
		})
		c.check = func(ctx context.Context, p payload, entries []string) reason {
			defer close(finished)
			r := newPathResolver(ctx)
			held := false
			r.lstat = func(path string) (os.FileInfo, error) {
				if path == c.files.SafeFile && !held {
					held = true
					close(started)
					<-release
				}
				return os.Lstat(path)
			}
			return decideFiles(p, entries, r)
		}
	} else {
		c.check = func(ctx context.Context, p payload, entries []string) reason {
			close(started)
			<-release
			defer close(finished)
			// Even a late success cannot replace the deadline block.
			return noDecision
		}
	}
}

func captureGuardOutput(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	files := make([]*os.File, 2)
	for i, name := range []string{"stdout", "stderr"} {
		file, err := os.Create(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
		t.Cleanup(func() { file.Close() })
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[0], files[1]
	t.Cleanup(func() { os.Stdout, os.Stderr = oldOut, oldErr })
	return files[0], files[1]
}

func assertGuardOutcome(t *testing.T, got, want Result, stdout, stderr *os.File) {
	t.Helper()
	if got != want {
		t.Errorf("outcome = %#v; want %#v", got, want)
	}
	for _, file := range []*os.File{stdout, stderr} {
		b, err := os.ReadFile(file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if len(b) != 0 {
			t.Errorf("unexpected package output in %s: %q", filepath.Base(file.Name()), b)
		}
	}
}
