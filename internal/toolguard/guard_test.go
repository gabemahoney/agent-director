package toolguard

import (
	"context"
	"encoding/json"
	"errors"
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
)

// Later path/Execute cases append rows here; setup may create files and replace
// args, cwd or the decision seam without acquiring another invocation path.
type guardCase struct {
	name  string
	tool  string
	input any
	want  Result
	setup func(*testing.T, *guardCall)
}

type guardCall struct {
	args     []string
	envelope map[string]any
	stdin    io.Reader
	ctx      context.Context
	check    func(context.Context, payload, []string) reason
	started  <-chan struct{}
	finished <-chan struct{}
	release  func()
	expires  bool
}

func TestGuardDecisions(t *testing.T) {
	// These literals are independent of the implementation's outcome formatter.
	failed := Result{2, "agent-director blocked this call: agent-director could not check it\n"}
	disabled := Result{2, "agent-director blocked this call: this call is disabled\n"}
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
			c.args[1],
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

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			cwd := filepath.Join(home, "work")
			if err := os.Mkdir(cwd, 0700); err != nil {
				t.Fatal(err)
			}
			// Serial capture observes accidental output, including after expiry.
			stdout, stderr := captureGuardOutput(t)
			synctest.Test(t, func(t *testing.T) {
				c := guardCall{
					args: []string{"--", filepath.Join(home, "decoy-secret")},
					envelope: map[string]any{
						"hook_event_name": "PreToolUse", "tool_name": tc.tool, "tool_input": tc.input, "cwd": cwd,
						"session_id": "decoy-session", "permission_mode": "auto-low",
						// This path cannot be read; envelope metadata stays opaque.
						"transcript_path": filepath.Join(home, "absent", "transcript.jsonl"),
						"future_metadata": map[string]any{"value": nil},
					},
					check: decide,
				}
				if tc.setup != nil {
					tc.setup(t, &c)
				}
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
				assertGuardOutcome(t, got, tc.want, stdout, stderr)
				if c.expires {
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
					assertGuardOutcome(t, got, tc.want, stdout, stderr)
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

// Hold a real input read or the injected rule until released. synctest advances
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
