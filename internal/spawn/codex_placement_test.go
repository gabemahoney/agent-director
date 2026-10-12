package spawn

import (
	"fmt"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
)

// Where AD's settings stand among the caller's Codex arguments, which of them
// it does not repeat, and the order of a resume (SR-5.1, SR-5.2, SR-7.4, RN-13,
// RN-20). What the settings are is in codex_compose_test.go.

// codexArgShapes are the caller's arguments to a launch: before is what stands
// before the first `--`, after is the `--` and what follows (the prompt).
var codexArgShapes = []struct {
	name          string
	before, after []string
}{
	{"none", nil, nil},
	{"options only", []string{"--model", "o3", "-c", "model_reasoning_effort=\"high\"", "--search"}, nil},
	{"options and prompt", []string{"--model", "o3"}, []string{"--", "fix the build"}},
	{"prompt only", nil, []string{"--", "fix the build"}},
	{"image option", []string{"-i", "/img/a.png", "--model", "o3"}, nil},
	{"images kept together", []string{"-i", "/img/a.png", "/img/b.png", "--model", "o3"}, []string{"--", "look"}},
	{"prompt holds our flags", []string{"--model", "o3"}, []string{"--", "--no-daemon", "--enable", "write_stdin_approval", "--dangerously-bypass-hook-trust"}},
	{"lone dashes", nil, []string{"--"}},
}

// TestCodexSettingsStandAfterPassThroughArguments: for a spawn and a resume,
// the caller's options come first and unchanged, every AD setting stands after
// the last of them and before the first `--`, and the prompt after `--` is
// untouched. A copy of an AD flag in the prompt is the prompt, so AD still
// adds its own.
func TestCodexSettingsStandAfterPassThroughArguments(t *testing.T) {
	for _, c := range codexComposers {
		for _, sh := range codexArgShapes {
			t.Run(c.name+"/"+sh.name, func(t *testing.T) {
				withStubExe(t, codexTestExe)
				args := append(slices.Clone(sh.before), sh.after...)
				out := c.compose(t, codexIn{args: args, cwd: "/work/proj"}, codexConfig(true, true, 0))
				l := codexfix.Split(t, out.argv)

				if !slices.Equal(out.argv[out.start:out.start+len(sh.before)], sh.before) {
					t.Errorf("argv after the program = %q; want the caller's options %q first", out.argv[out.start:], sh.before)
				}
				if !slices.Equal(tailFrom(out.argv, sh.after), sh.after) {
					t.Errorf("argv ends %q; want the prompt part %q unchanged at the end", out.argv, sh.after)
				}
				if len(sh.after) > 0 && !slices.Equal(l.AfterDashDashArgs(), sh.after[1:]) {
					t.Errorf("after `--` = %q; want %q", l.AfterDashDashArgs(), sh.after[1:])
				}
				first := out.start + len(sh.before)
				for _, it := range l.Items {
					if it.Kind == codexfix.KindDashDash {
						if it.Index != len(out.argv)-len(sh.after) {
							t.Errorf("`--` at %d; want the caller's, at %d", it.Index, len(out.argv)-len(sh.after))
						}
						continue
					}
					if it.Index < first || it.AfterDashDash {
						continue
					}
					if it.Kind == codexfix.KindResume || it.Kind == codexfix.KindPassThrough {
						t.Errorf("part %v at %d stands among AD's settings; want only settings after the options", it.Args, it.Index)
					}
				}
				if got := l.PassThrough(); !slices.Equal(got, sh.before) {
					t.Errorf("pass-through before `--` = %q; want %q", got, sh.before)
				}
				for _, it := range l.Before(codexfix.KindHooks) {
					if it.Index <= l.LastPassThroughIndex() {
						t.Errorf("hook part at %d stands before the last pass-through argument at %d", it.Index, l.LastPassThroughIndex())
					}
				}
				// A flag in the prompt is data: AD's own copy is still there before `--`.
				if n := len(l.Of(codexfix.KindNoDaemon)); n != 1+countOf(sh.after, "--no-daemon") {
					t.Errorf("--no-daemon appears %d times; want AD's once plus the prompt's", n)
				}
				if n := len(l.Before(codexfix.KindNoDaemon)); n != 1 {
					t.Errorf("--no-daemon before `--` appears %d times; want once", n)
				}
			})
		}
	}
}

// tailFrom returns the last len(want) elements of argv.
func tailFrom(argv, want []string) []string { return argv[len(argv)-len(want):] }

// countOf is how many elements of s equal v.
func countOf(s []string, v string) int {
	n := 0
	for _, e := range s {
		if e == v {
			n++
		}
	}
	return n
}

// TestCodexSettingsSkipWhatTheCallerPassed: an option AD would add that the
// caller's arguments already carry before `--` is left out, so it appears once;
// the same option after `--` is the prompt and does not count. Pre-trust is ok
// so the bypass is in play.
func TestCodexSettingsSkipWhatTheCallerPassed(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		count func(codexfix.Launch) int
	}{
		{"--no-daemon", []string{"--no-daemon"}, func(l codexfix.Launch) int { return len(l.Of(codexfix.KindNoDaemon)) }},
		{"bypass flag", []string{"--dangerously-bypass-hook-trust"}, func(l codexfix.Launch) int { return len(l.Of(codexfix.KindBypass)) }},
		{"--enable separate", []string{"--enable", "write_stdin_approval"}, func(l codexfix.Launch) int { return len(l.Enabled(codexfix.FeatureWriteStdinApproval)) }},
		{"--enable=", []string{"--enable=write_stdin_approval"}, func(l codexfix.Launch) int { return len(l.Enabled(codexfix.FeatureWriteStdinApproval)) }},
	}
	for _, c := range codexComposers {
		for _, tc := range cases {
			for _, where := range []string{"before", "after"} {
				t.Run(fmt.Sprintf("%s/%s/%s", c.name, tc.name, where), func(t *testing.T) {
					withStubExe(t, codexTestExe)
					args := tc.args
					want := 1
					if where == "after" {
						args = append([]string{"--"}, tc.args...)
						want = 2 // the prompt's copy and AD's own
					}
					out := c.compose(t, codexIn{args: args, cwd: "/work/proj"}, codexConfig(false, false, 0))
					l := codexfix.Split(t, out.argv)
					if got := tc.count(l); got != want {
						t.Errorf("%s appears %d times in %q; want %d", tc.name, got, out.argv, want)
					}
				})
			}
		}
	}
}

// TestCodexQuestionOffWinsOverCallerArguments: with disable_ask_user on, a
// caller's (or a stored) setting that turns the question tool on stands before
// AD's off setting, which is last of everything before `--`, so Codex applies it.
func TestCodexQuestionOffWinsOverCallerArguments(t *testing.T) {
	on := []string{"-c", "tools.experimental_request_user_input.enabled=true"}
	for _, c := range codexComposers {
		for _, sh := range codexArgShapes {
			t.Run(c.name+"/"+sh.name, func(t *testing.T) {
				withStubExe(t, codexTestExe)
				args := append(slices.Concat(sh.before, on), sh.after...)
				out := c.compose(t, codexIn{args: args, cwd: "/work/proj"}, codexConfig(false, true, 0))
				q := codexfix.Split(t, out.argv).Of(codexfix.KindQuestionTool)
				if len(q) != 2 || !q[0].Bool || q[1].Bool {
					t.Fatalf("question-tool parts = %+v; want the caller's on, then AD's off", q)
				}
				last, ok := codexfix.Split(t, out.argv).Last()
				if !ok || last.Index != q[1].Index {
					t.Errorf("last part before `--` is at %d; want AD's off setting at %d", last.Index, q[1].Index)
				}
			})
		}
	}
}

// TestCodexResumeOrdering: a resume is codex, `resume`, the session id, then the
// replayed arguments (`-i` among them, after the id) and AD's settings, with the
// question tool off last; nothing of AD's stands before `resume`.
func TestCodexResumeOrdering(t *testing.T) {
	withStubExe(t, codexTestExe)
	args := []string{"--model", "o3", "-i", "/img/a.png", "-c", "model_reasoning_effort=\"high\"", "--", "go on"}
	var resume codexComposerFn
	for _, c := range codexComposers {
		if c.name == "resume" {
			resume = c.compose
		}
	}
	out := resume(t, codexIn{args: args, cwd: "/work/proj"}, codexConfig(true, true, 0))
	l := codexfix.Split(t, out.argv)
	if out.argv[0] != "codex" || out.argv[1] != "resume" || out.argv[2] != codexTestSession {
		t.Fatalf("argv starts %q; want codex resume %s", out.argv[:3], codexTestSession)
	}
	r, ok := l.Resume()
	if !ok || r.Index != 1 || r.SessionID != codexTestSession {
		t.Errorf("resume part = %+v (found %v); want at index 1 with id %s", r, ok, codexTestSession)
	}
	for _, it := range l.Items {
		if it.Index < 1 && it.Kind != codexfix.KindResume {
			t.Errorf("part %+v stands before resume", it)
		}
	}
	if got := l.PassThrough(); !slices.Equal(got, args[:6]) {
		t.Errorf("replayed arguments = %q; want %q, after the id and in order", got, args[:6])
	}
	if i := slices.Index(out.argv, "-i"); i < 3 {
		t.Errorf("-i at %d; want after the session id", i)
	}
	if got := l.AfterDashDashArgs(); !slices.Equal(got, []string{"go on"}) {
		t.Errorf("after `--` = %q; want the stored prompt", got)
	}
	if last, _ := l.Last(); last.Kind != codexfix.KindQuestionTool || last.Bool {
		t.Errorf("last part before `--` = %+v; want the question tool off", last)
	}
}

// codexComposerFn is a composer from codexComposers.
type codexComposerFn = func(t *testing.T, in codexIn, cfg config.Config) codexOut

// TestCodexPassThroughProjectsStandsBeforeOurs: a caller's `-c projects=...`
// stays and AD's folder trust for the cwd stands after it, so on ok both are
// in force and AD's is the later.
func TestCodexPassThroughProjectsStandsBeforeOurs(t *testing.T) {
	theirs := []string{"-c", `projects={"/other"={trust_level="untrusted"}}`}
	for _, c := range codexComposers {
		t.Run(c.name, func(t *testing.T) {
			withStubExe(t, codexTestExe)
			out := c.compose(t, codexIn{args: theirs, cwd: "/work/proj"}, codexConfig(false, false, 0))
			p := codexfix.Split(t, out.argv).Of(codexfix.KindProjects)
			if len(p) != 2 || p[0].Projects["/other"] != "untrusted" || p[1].Projects["/work/proj"] != "trusted" || p[0].Index >= p[1].Index {
				t.Errorf("projects parts = %+v; want the caller's, then AD's trusting /work/proj", p)
			}
		})
	}
}

// TestCodexBypassPassesUnchangedWithoutFolderTrust: with pre-trust skipped or
// failed AD adds no bypass, and one the caller passed stays exactly once.
func TestCodexBypassPassesUnchangedWithoutFolderTrust(t *testing.T) {
	for _, c := range codexComposers {
		for _, pt := range codexPreTrusts[1:] {
			t.Run(c.name+"/"+pt.name, func(t *testing.T) {
				withStubExe(t, codexTestExe)
				out := c.compose(t, codexIn{args: []string{"--dangerously-bypass-hook-trust"}, cwd: "/work/proj", extraEnv: pt.extraEnv, noPreTrust: pt.noPreTrust},
					codexConfig(false, false, 0))
				l := codexfix.Split(t, out.argv)
				if b := l.Of(codexfix.KindBypass); len(b) != 1 || b[0].Index != out.start {
					t.Errorf("bypass parts = %+v; want the caller's alone, first", b)
				}
				if len(l.Of(codexfix.KindProjects)) != 0 {
					t.Errorf("projects parts = %+v; want none on %s", l.Of(codexfix.KindProjects), pt.name)
				}
				if len(l.Hooks()) == 0 {
					t.Errorf("no hooks carried; AD's hook trust is still carried on %s", pt.name)
				}
			})
		}
	}
}

// TestCodexComposedRow: the spawn composition's row records the request as
// given (the caller's arguments without AD's settings, the extra env and the
// pre-trust choice), the agent, and the create names the session and cwd; the
// caller's argument slice is not changed.
func TestCodexComposedRow(t *testing.T) {
	withStubExe(t, codexTestExe)
	t.Setenv(envInstanceID, "parent-1")
	args := []string{"--model", "o3", "--", "go"}
	r := codexResolved(codexIn{args: slices.Clone(args), cwd: "/work/proj", extraEnv: map[string]string{"FOO": "bar"}, noPreTrust: true})
	got, err := ComposeLaunch(r, codexConfig(true, true, 0))
	if err != nil {
		t.Fatal(err)
	}
	row := got.Row
	if row.Agent != "codex" || row.InstanceID != "id-codex-1" || row.ParentID != "parent-1" || row.CWD != "/work/proj" || row.TmuxSessionName != "cd-codex-1" ||
		row.RelayMode != "off" || !row.NoPreTrust || !slices.Equal(row.AgentArgs, args) || row.ExtraEnv["FOO"] != "bar" || row.Labels["role"] != "worker" {
		t.Errorf("row = %+v; want the request as given, agent codex and parent parent-1", row)
	}
	if c := got.Create; c.Name != "cd-codex-1" || c.CWD != "/work/proj" || c.InstanceID != "id-codex-1" {
		t.Errorf("create = %+v; want the session name, cwd and instance id", c)
	}
	if got.PreTrust != PreTrustSkipped {
		t.Errorf("pre-trust = %q; want skipped", got.PreTrust)
	}
	if !slices.Equal(r.AgentArgs, args) {
		t.Errorf("caller's arguments = %q after composing; want %q unchanged", r.AgentArgs, args)
	}
}
