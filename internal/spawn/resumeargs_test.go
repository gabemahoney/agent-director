package spawn

import (
	"reflect"
	"slices"
	"testing"
)

type claudeParsedRole struct {
	canonical string
	values    []string
	known     bool
}

func claudeParsedRoles(options []ArgOption) []claudeParsedRole {
	var roles []claudeParsedRole
	for _, o := range options {
		roles = append(roles, claudeParsedRole{o.Canonical, o.Values, o.Known})
	}
	return roles
}

func assertClaudeMissingValue(t *testing.T, args []string, canonical string) {
	t.Helper()
	options := ReadClaudeArgs(args).Options
	for i := len(options) - 1; i >= 0; i-- {
		if options[i].Canonical == canonical {
			if len(options[i].Values) != 0 {
				t.Fatalf("%s gained a value in %q: %+v", canonical, args, options[i])
			}
			return
		}
	}
	t.Fatalf("missing required option %s disappeared from %q", canonical, args)
}

func TestExtractClaudeIncompleteRequiredValues(t *testing.T) {
	option := func(index int, name string, values ...string) ArgOption {
		o := ArgOption{Index: index, Name: name, Canonical: name, Spelling: name, Known: true, Values: values}
		for i := range values {
			o.ValueIndices = append(o.ValueIndices, index+1+i)
		}
		return o
	}
	for _, tc := range []struct {
		name, missing       string
		print               bool
		args, want, dropped []string
		removed             []ArgOption
	}{
		{"scalar prompt exposure", "--model", false,
			[]string{"--debug", "--continue", "prompt", "--model"}, []string{"--debug", "prompt", "--model"}, []string{"--continue"}, []ArgOption{option(1, "--continue")}},
		{"missing environment cleans orphan controls", "--environment", false,
			[]string{"--debug", "--cloud", "OLD", "prompt", "--ref", "main", "--environment"}, []string{"--debug", "prompt", "--environment"}, []string{"--cloud", "OLD", "--ref", "main"}, []ArgOption{option(1, "--cloud", "OLD"), option(4, "--ref", "main")}},
		{"missing pool cleans orphan controls", "--pool", false,
			[]string{"--debug", "--remote", "OLD", "prompt", "--on-branch", "work", "--pool"}, []string{"--debug", "prompt", "--pool"}, []string{"--remote", "OLD", "--on-branch", "work"}, []ArgOption{option(1, "--remote", "OLD"), option(4, "--on-branch", "work")}},
		{"required variadic print exposure", "--tools", true,
			[]string{"--debug", "--print", "prompt", "--tools"}, []string{"--debug", "prompt", "--tools"}, []string{"--print"}, []ArgOption{option(1, "--print")}},
		{"earlier scalar value does not mask later missing value", "--model", false,
			[]string{"--model=sonnet", "--debug", "--continue", "prompt", "--model"}, []string{"--model=sonnet", "--debug", "prompt", "--model"}, []string{"--continue"}, []ArgOption{option(2, "--continue")}},
		{"cluster remains literal", "--model", false,
			[]string{"-cd2e", "prompt", "--model"}, []string{"-d2e", "prompt", "--model"}, []string{"-cd2e"}, []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-cd2e", ClusterStart: 1, ClusterEnd: 2, Known: true}}},
		{"removed missing selector still excludes repair", "", false,
			[]string{"--debug", "--continue", "prompt", "--attach-serve"}, []string{"--debug", "prompt"}, []string{"--continue", "--attach-serve"}, []ArgOption{option(1, "--continue"), option(3, "--attach-serve")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.args)
			if tc.missing != "" {
				assertClaudeMissingValue(t, tc.args, tc.missing)
			}
			got := ExtractResumeArgs("claude", tc.args)
			if tc.print {
				got = extractClaudeArgs(tc.args, true)
			}
			if !slices.Equal(got.Args, tc.want) || !slices.Equal(got.Dropped, tc.dropped) || !reflect.DeepEqual(got.Removed, tc.removed) {
				t.Fatalf("extracted = %+v; want literal args %q, dropped %q, removed %+v", got, tc.want, tc.dropped, tc.removed)
			}
			if tc.missing != "" {
				assertClaudeMissingValue(t, got.Args, tc.missing)
			}
			if !slices.Equal(tc.args, before) {
				t.Fatal("extraction changed incomplete input")
			}
		})
	}
}

// Literal roles follow the endpoint parser evidence and the approved repair
// decision; the production option/selector tables do not supply expectations.
func TestExtractClaudePreservesParsedRoles(t *testing.T) {
	flag := func(index int, spelling, canonical string) ArgOption {
		return ArgOption{Index: index, Name: spelling, Canonical: canonical, Spelling: spelling, Known: true}
	}
	for _, tc := range []struct {
		name                string
		print               bool
		args, want, dropped []string
		removed             []ArgOption
		roles               []claudeParsedRole
		operands            []string
	}{
		{
			name: "optional boundary with later option",
			args: []string{"--debug", "--continue", "fix this", "--model", "sonnet"},
			want: []string{"--debug", "--model", "sonnet", "--", "fix this"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(1, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--debug", nil, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"fix this"},
		},
		{
			name: "print optional boundary with later option", print: true,
			args: []string{"--debug", "--print", "fix this", "--model", "sonnet"},
			want: []string{"--debug", "--model", "sonnet", "--", "fix this"}, dropped: []string{"--print"},
			removed: []ArgOption{flag(1, "--print", "--print")},
			roles:   []claudeParsedRole{{"--debug", nil, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"fix this"},
		},
		{
			name: "list boundary",
			args: []string{"--tools", "Read", "--continue", "prompt"},
			want: []string{"--tools", "Read", "--", "prompt"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(2, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--tools", []string{"Read"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "print list boundary and multiple operands", print: true,
			args: []string{"--tools", "Read", "--print", "one", "--model=sonnet", "two"},
			want: []string{"--tools", "Read", "--model=sonnet", "--", "one", "two"}, dropped: []string{"--print"},
			removed: []ArgOption{flag(2, "--print", "--print")},
			roles:   []claudeParsedRole{{"--tools", []string{"Read"}, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"one", "two"},
		},
		{
			name: "required list keeps owned denied value",
			args: []string{"--tools", "--resume", "Read", "--continue", "prompt"},
			want: []string{"--tools", "--resume", "Read", "--", "prompt"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(3, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--tools", []string{"--resume", "Read"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "continue cluster exact short collision",
			args: []string{"--verbose", "-cd2e", "prompt"},
			want: []string{"--verbose", "--debug=2e", "prompt"}, dropped: []string{"-cd2e"},
			removed: []ArgOption{{Index: 1, Name: "-c", Canonical: "--continue", Spelling: "-cd2e", ClusterStart: 1, ClusterEnd: 2, Known: true}},
			roles:   []claudeParsedRole{{"--verbose", nil, true}, {"--debug", []string{"2e"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "print cluster exact short collision", print: true,
			args: []string{"--verbose", "-pd2e", "prompt"},
			want: []string{"--verbose", "--debug=2e", "prompt"}, dropped: []string{"-pd2e"},
			removed: []ArgOption{{Index: 1, Name: "-p", Canonical: "--print", Spelling: "-pd2e", ClusterStart: 1, ClusterEnd: 2, Known: true}},
			roles:   []claudeParsedRole{{"--verbose", nil, true}, {"--debug", []string{"2e"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "safe attached equals cluster keeps spelling",
			args: []string{"-cd=2e", "prompt"}, want: []string{"-d=2e", "prompt"}, dropped: []string{"-cd=2e"},
			removed: []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-cd=2e", ClusterStart: 1, ClusterEnd: 2, Known: true}},
			roles:   []claudeParsedRole{{"--debug", []string{"=2e"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "safe cluster preserves remaining short order", print: true,
			args: []string{"-vpcd", "--model=sonnet", "prompt"}, want: []string{"-vd", "--model=sonnet", "prompt"}, dropped: []string{"-vpcd"},
			removed: []ArgOption{{Name: "-p", Canonical: "--print", Spelling: "-vpcd", ClusterStart: 2, ClusterEnd: 3, Known: true}, {Name: "-c", Canonical: "--continue", Spelling: "-vpcd", ClusterStart: 3, ClusterEnd: 4, Known: true}},
			roles:   []claudeParsedRole{{"--version", nil, true}, {"--debug", nil, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "short optional boundary after cluster deletion",
			args: []string{"-cd", "--continue", "prompt", "--model=sonnet"},
			want: []string{"-d", "--model=sonnet", "--", "prompt"}, dropped: []string{"-cd", "--continue"},
			removed: []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-cd", ClusterStart: 1, ClusterEnd: 2, Known: true}, flag(1, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--debug", nil, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "safe equals list does not move prompt",
			args: []string{"--tools=Read", "--continue", "prompt", "--model=sonnet"},
			want: []string{"--tools=Read", "prompt", "--model=sonnet"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(1, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--tools", []string{"Read"}, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"prompt"},
		},
		{
			name: "safe prompt preceding later options stays in place",
			args: []string{"first", "--continue", "--debug=api", "second", "--model", "sonnet"},
			want: []string{"first", "--debug=api", "second", "--model", "sonnet"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(1, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--debug", []string{"api"}, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"first", "second"},
		},
		{
			name: "safe original delimiter stays literal",
			args: []string{"--debug", "--continue", "--", "--model", "sonnet"},
			want: []string{"--debug", "--", "--model", "sonnet"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(1, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--debug", nil, true}}, operands: []string{"--model", "sonnet"},
		},
		{
			name: "repair preserves owned delimiter and protected later operands",
			args: []string{"--model", "--", "--debug", "--continue", "early", "--verbose", "--", "--continue", "late"},
			want: []string{"--model", "--", "--debug", "--verbose", "--", "early", "--continue", "late"}, dropped: []string{"--continue"},
			removed: []ArgOption{flag(3, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--model", []string{"--"}, true}, {"--debug", nil, true}, {"--verbose", nil, true}}, operands: []string{"early", "--continue", "late"},
		},
		{
			name: "selector owned value and repair retain original indices",
			args: []string{"--debug", "--resume", "OLD", "fix this", "--model=sonnet", "--continue"},
			want: []string{"--debug", "--model=sonnet", "--", "fix this"}, dropped: []string{"--resume", "OLD", "--continue"},
			removed: []ArgOption{{Index: 1, Name: "--resume", Canonical: "--resume", Spelling: "--resume", Values: []string{"OLD"}, ValueIndices: []int{2}, Known: true}, flag(5, "--continue", "--continue")},
			roles:   []claudeParsedRole{{"--debug", nil, true}, {"--model", []string{"sonnet"}, true}}, operands: []string{"fix this"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.args)
			original := ReadClaudeArgs(tc.args)
			var survivors []ArgOption
			for _, o := range original.Options {
				dropped := false
				for _, removed := range tc.removed {
					if o.Index == removed.Index && o.ClusterStart == removed.ClusterStart {
						if !reflect.DeepEqual(o, removed) {
							t.Fatalf("original removed role = %+v; want %+v", o, removed)
						}
						dropped = true
					}
				}
				if !dropped {
					survivors = append(survivors, o)
				}
			}
			got := ExtractResumeArgs("claude", tc.args)
			if tc.print {
				got = extractClaudeArgs(tc.args, true)
			}
			if !slices.Equal(got.Args, tc.want) || !slices.Equal(got.Dropped, tc.dropped) || !reflect.DeepEqual(got.Removed, tc.removed) {
				t.Fatalf("extracted = %+v; want args %q, dropped %q, removed %+v", got, tc.want, tc.dropped, tc.removed)
			}
			filtered := ReadClaudeArgs(got.Args)
			if roles := claudeParsedRoles(survivors); !reflect.DeepEqual(roles, tc.roles) {
				t.Fatalf("original surviving roles = %+v; want literal roles %+v", roles, tc.roles)
			}
			if roles := claudeParsedRoles(filtered.Options); !reflect.DeepEqual(roles, tc.roles) || !reflect.DeepEqual(roles, claudeParsedRoles(survivors)) {
				t.Fatalf("filtered roles = %+v; want original and literal roles %+v", roles, tc.roles)
			}
			for _, reading := range []struct {
				args []string
				read ClaudeArgs
			}{{tc.args, original}, {got.Args, filtered}} {
				var operands []string
				for _, index := range reading.read.Positionals {
					operands = append(operands, reading.args[index])
				}
				if !slices.Equal(operands, tc.operands) {
					t.Fatalf("operands in %q = %q; want original prompt %q", reading.args, operands, tc.operands)
				}
			}
			if !slices.Equal(tc.args, before) {
				t.Fatal("extraction changed input")
			}
		})
	}
}

// Literal selectors and arities come from b.ssz's endpoint parser evidence,
// independently of the production ResumeArgs sets.
func TestExtractClaudeVerifiedSelectors(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selector  []string
		canonical string
	}{
		{"continue short", []string{"-c"}, "--continue"},
		{"continue long", []string{"--continue"}, "--continue"},
		{"fork", []string{"--fork-session"}, "--fork-session"},
		{"reply", []string{"--reply-on-resume"}, "--reply-on-resume"},
		{"resume short bare", []string{"-r"}, "--resume"},
		{"resume short detached", []string{"-r", "S1"}, "--resume"},
		{"resume short attached", []string{"-rS1"}, "--resume"},
		{"resume short equals", []string{"-r=S1"}, "--resume"},
		{"resume bare", []string{"--resume"}, "--resume"},
		{"resume detached", []string{"--resume", "S1"}, "--resume"},
		{"resume equals", []string{"--resume=S1"}, "--resume"},
		{"from pr bare", []string{"--from-pr"}, "--from-pr"},
		{"from pr detached", []string{"--from-pr", "123"}, "--from-pr"},
		{"from pr equals", []string{"--from-pr=123"}, "--from-pr"},
		{"teleport bare", []string{"--teleport"}, "--teleport"},
		{"teleport detached", []string{"--teleport", "S1"}, "--teleport"},
		{"teleport equals", []string{"--teleport=S1"}, "--teleport"},
		{"resume at detached", []string{"--resume-session-at", "turn"}, "--resume-session-at"},
		{"resume at equals", []string{"--resume-session-at=turn"}, "--resume-session-at"},
		{"drops turn detached", []string{"--resume-drops-turn", "turn"}, "--resume-drops-turn"},
		{"drops turn equals", []string{"--resume-drops-turn=turn"}, "--resume-drops-turn"},
		{"rewind detached", []string{"--rewind-files", "turn"}, "--rewind-files"},
		{"rewind equals", []string{"--rewind-files=turn"}, "--rewind-files"},
		{"cloud bare", []string{"--cloud"}, "--cloud"},
		{"cloud description", []string{"--cloud", "new task description"}, "--cloud"},
		{"cloud existing session", []string{"--cloud=S1"}, "--cloud"},
		{"remote bare", []string{"--remote"}, "--remote"},
		{"remote description", []string{"--remote", "new task description"}, "--remote"},
		{"remote existing url", []string{"--remote=https://session/S1"}, "--remote"},
		{"attach serve detached", []string{"--attach-serve", "S1"}, "--attach-serve"},
		{"attach serve equals", []string{"--attach-serve=S1"}, "--attach-serve"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"--model", "opus"}, tc.selector...)
			args = append(args, "--verbose", "prompt")
			got := ExtractResumeArgs("claude", args)
			if !slices.Equal(got.Args, []string{"--model", "opus", "--verbose", "prompt"}) || !slices.Equal(got.Dropped, tc.selector) {
				t.Fatalf("ExtractResumeArgs(%q) = %+v", args, got)
			}
			if len(got.Removed) != 1 || got.Removed[0].Canonical != tc.canonical || got.Removed[0].Index != 2 || got.Removed[0].Spelling != tc.selector[0] {
				t.Fatalf("removed selector metadata = %+v", got.Removed)
			}
		})
	}
}

func TestExtractResumeArgsOrderAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, agent         string
		args, want, dropped []string
	}{
		{"owned exact long survives", "claude", []string{"--append-system-prompt", "--resume", "foo"}, []string{"--append-system-prompt", "--resume", "foo"}, nil},
		{"owned short survives", "claude", []string{"--model", "-c", "--append-system-prompt", "-concise answers", "--resume", "OLD"}, []string{"--model", "-c", "--append-system-prompt", "-concise answers"}, []string{"--resume", "OLD"}},
		{"required selector owns delimiter", "claude", []string{"--resume-session-at", "--", "-c", "prompt"}, []string{"prompt"}, []string{"--resume-session-at", "--", "-c"}},
		{"unowned delimiter protects prompt", "claude", []string{"-c", "--", "--resume", "OLD", "-p"}, []string{"--", "--resume", "OLD", "-p"}, []string{"-c"}},
		{"debug value c is benign", "claude", []string{"-dc", "-nrocket", "-d2e"}, []string{"-dc", "-nrocket", "-d2e"}, nil},
		{"partial cluster original drop", "claude", []string{"-cd", "--model=opus"}, []string{"-d", "--model=opus"}, []string{"-cd"}},
		{"switch extraction preserves print", "claude", []string{"-pc"}, []string{"-p"}, []string{"-pc"}},
		{"repeated selectors original order", "claude", []string{"-r=S1", "--verbose", "--resume", "S2", "--continue", "prompt"}, []string{"--verbose", "prompt"}, []string{"-r=S1", "--resume", "S2", "--continue"}},
		{"session id creates fresh session", "claude", []string{"--session-id", "NEW", "--fork-session", "--resume=OLD"}, []string{"--session-id", "NEW"}, []string{"--fork-session", "--resume=OLD"}},
		{"session id equals and name kept verbatim", "claude", []string{"--session-id=NEW", "-n=rocket", "--resume="}, []string{"--session-id=NEW", "-n=rocket"}, []string{"--resume="}},
		{"unknown is valueless", "claude", []string{"--future", "--resume", "S1", "prompt"}, []string{"--future", "prompt"}, []string{"--resume", "S1"}},
		{"codex full resume selection", "codex", []string{"--model", "gpt", "resume", "OLD", "--all", "--include-non-interactive", "prompt"}, []string{"--model", "gpt", "--", "prompt"}, []string{"resume", "OLD", "--all", "--include-non-interactive"}},
		{"codex last means first positional prompt", "codex", []string{"resume", "--last", "fork", "--all"}, []string{"--", "fork"}, []string{"resume", "--last", "--all"}},
		{"codex bare operand is id", "codex", []string{"resume", "bare prompt"}, nil, []string{"resume", "bare prompt"}},
		{"codex prompt after delimiter", "codex", []string{"resume", "OLD", "--", "--last"}, []string{"--", "--last"}, []string{"resume", "OLD"}},
		{"codex delimiter first operand is id", "codex", []string{"resume", "--", "OLD", "resume"}, []string{"--", "resume"}, []string{"resume", "OLD"}},
		{"codex last inside prompt is no selector", "codex", []string{"resume", "--", "--last"}, []string{"--"}, []string{"resume", "--last"}},
		{"codex fork last and all", "codex", []string{"fork", "--last", "--all", "prompt"}, []string{"--", "prompt"}, []string{"fork", "--last", "--all"}},
		{"codex fork excludes resume only flag", "codex", []string{"fork", "OLD", "--include-non-interactive", "prompt"}, []string{"--include-non-interactive", "--", "prompt"}, []string{"fork", "OLD"}},
		{"codex owned command name", "codex", []string{"--model", "resume", "prompt", "fork"}, []string{"--model", "resume", "prompt", "fork"}, nil},
		{"codex command name in prompt", "codex", []string{"hello", "resume", "--last"}, []string{"hello", "resume", "--last"}, nil},
		{"codex command after delimiter", "codex", []string{"--", "resume", "--all"}, []string{"--", "resume", "--all"}, nil},
		{"codex image owns selector name", "codex", []string{"-i", "resume", "fork", "--model=x"}, []string{"-i", "resume", "fork", "--model=x"}, nil},
		{"codex settings order and original prompts", "codex", []string{"-c", "a=1", "resume", "OLD", "--model=x", "two words", "-c=b=2", "--", "--resume"}, []string{"-c", "a=1", "--model=x", "-c=b=2", "--", "two words", "--resume"}, []string{"resume", "OLD"}},
		{"unknown agent copied unchanged", "future", []string{"--resume", "OLD"}, []string{"--resume", "OLD"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := slices.Clone(tc.args)
			got := ExtractResumeArgs(tc.agent, tc.args)
			if !slices.Equal(got.Args, tc.want) || !slices.Equal(got.Dropped, tc.dropped) {
				t.Fatalf("ExtractResumeArgs(%q, %q) = %+v; want %q, dropped %q", tc.agent, tc.args, got, tc.want, tc.dropped)
			}
			if !slices.Equal(tc.args, original) {
				t.Fatal("extraction mutated input")
			}
			if len(got.Args) > 0 {
				got.Args[0] = "changed"
				if !slices.Equal(tc.args, original) {
					t.Fatal("survivors alias input")
				}
			}
		})
	}
}

func TestExtractClaudeRemovedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		removed []ArgOption
	}{
		{"partial continue", []string{"-cd"}, []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-cd", ClusterStart: 1, ClusterEnd: 2, Known: true}}},
		{"continue after print", []string{"-pc"}, []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-pc", ClusterStart: 2, ClusterEnd: 3, Known: true}}},
		{"attached resume equals retained", []string{"--verbose", "-r=S1"}, []ArgOption{{Index: 1, Name: "-r", Canonical: "--resume", Spelling: "-r=S1", Values: []string{"=S1"}, ClusterStart: 1, ClusterEnd: 5, Known: true}}},
		{"owned required value", []string{"--rewind-files", "--resume"}, []ArgOption{{Name: "--rewind-files", Canonical: "--rewind-files", Spelling: "--rewind-files", Values: []string{"--resume"}, ValueIndices: []int{1}, Known: true}}},
		{"orphan control owned value", []string{"--cloud=OLD", "--ref", "--environment"}, []ArgOption{{Name: "--cloud", Canonical: "--cloud", Spelling: "--cloud=OLD", Values: []string{"OLD"}, Known: true}, {Index: 1, Name: "--ref", Canonical: "--ref", Spelling: "--ref", Values: []string{"--environment"}, ValueIndices: []int{2}, Known: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractResumeArgs("claude", tc.args).Removed; !reflect.DeepEqual(got, tc.removed) {
				t.Fatalf("Removed = %+v; want %+v", got, tc.removed)
			}
		})
	}
}

// These cases pin the operator-approved conditional cleanup, including context
// and control spellings that are owned values rather than independent options.
func TestExtractClaudeCloudControls(t *testing.T) {
	for _, tc := range []struct {
		name                string
		args, want, dropped []string
	}{
		{"orphan controls and values ordered", []string{"--ref", "main", "--model", "opus", "--cloud", "description", "--on-branch=work", "--forward-home-settings", "false", "prompt"}, []string{"--model", "opus", "prompt"}, []string{"--ref", "main", "--cloud", "description", "--on-branch=work", "--forward-home-settings", "false"}},
		{"environment preserves controls", []string{"--cloud=S1", "--forward-home-settings", "false", "--environment", "ENV", "--ref", "main", "--on-branch", "work"}, []string{"--forward-home-settings", "false", "--environment", "ENV", "--ref", "main", "--on-branch", "work"}, []string{"--cloud=S1"}},
		{"pool alias preserves controls", []string{"--remote", "description", "--ref=main", "--pool=POOL", "--forward-home-settings=0", "--on-branch=work"}, []string{"--ref=main", "--pool=POOL", "--forward-home-settings=0", "--on-branch=work"}, []string{"--remote", "description"}},
		{"explicit environment alongside pool", []string{"--pool", "POOL", "--remote=OLD", "--environment=ENV", "--ref", "main"}, []string{"--pool", "POOL", "--environment=ENV", "--ref", "main"}, []string{"--remote=OLD"}},
		{"both cloud aliases removed", []string{"--remote", "OLD", "--cloud", "description", "--ref", "main"}, nil, []string{"--remote", "OLD", "--cloud", "description", "--ref", "main"}},
		{"no selector no cleanup", []string{"--ref", "main", "--on-branch", "work", "--forward-home-settings", "true"}, []string{"--ref", "main", "--on-branch", "work", "--forward-home-settings", "true"}, nil},
		{"attach serve does not trigger cleanup", []string{"--attach-serve", "OLD", "--ref", "main"}, []string{"--ref", "main"}, []string{"--attach-serve", "OLD"}},
		{"continue does not trigger cleanup", []string{"-c", "--forward-home-settings", "true"}, []string{"--forward-home-settings", "true"}, []string{"-c"}},
		{"owned cloud no cleanup", []string{"--append-system-prompt", "--cloud", "--ref", "main"}, []string{"--append-system-prompt", "--cloud", "--ref", "main"}, nil},
		{"cloud operand no cleanup", []string{"--ref", "main", "--", "--cloud", "OLD"}, []string{"--ref", "main", "--", "--cloud", "OLD"}, nil},
		{"owned environment not context", []string{"--cloud=OLD", "--append-system-prompt", "--environment", "ENV", "--ref", "main"}, []string{"--append-system-prompt", "--environment", "ENV"}, []string{"--cloud=OLD", "--ref", "main"}},
		{"owned pool not context", []string{"--remote=OLD", "--model", "--pool", "--on-branch", "work"}, []string{"--model", "--pool"}, []string{"--remote=OLD", "--on-branch", "work"}},
		{"prompt environment not context", []string{"--cloud=OLD", "--forward-home-settings", "false", "--", "--environment", "ENV"}, []string{"--", "--environment", "ENV"}, []string{"--cloud=OLD", "--forward-home-settings", "false"}},
		{"missing environment value not context", []string{"--cloud=OLD", "--ref", "main", "--environment"}, []string{"--environment"}, []string{"--cloud=OLD", "--ref", "main"}},
		{"required environment owns selector", []string{"--environment", "--cloud", "--ref", "main"}, []string{"--environment", "--cloud", "--ref", "main"}, nil},
		{"context required value can be option shaped", []string{"--cloud=OLD", "--pool", "--resume", "--ref", "main"}, []string{"--pool", "--resume", "--ref", "main"}, []string{"--cloud=OLD"}},
		{"owned control survives", []string{"--remote=OLD", "--append-system-prompt", "--ref", "main", "--forward-home-settings", "--environment", "ENV"}, []string{"--append-system-prompt", "--ref", "main", "ENV"}, []string{"--remote=OLD", "--forward-home-settings", "--environment"}},
		{"control owns cloud so no actual strip", []string{"--ref", "--cloud", "--forward-home-settings", "false"}, []string{"--ref", "--cloud", "--forward-home-settings", "false"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractResumeArgs("claude", tc.args)
			if !slices.Equal(got.Args, tc.want) || !slices.Equal(got.Dropped, tc.dropped) {
				t.Fatalf("ExtractResumeArgs(%q) = %+v; want %q, dropped %q", tc.args, got, tc.want, tc.dropped)
			}
		})
	}
}
