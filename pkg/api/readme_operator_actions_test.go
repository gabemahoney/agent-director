package api_test

// readme_operator_actions_test.go checks the README's "Operator actions"
// removal procedure for a row whose recorded name cannot be used gives its
// five steps in order, and that every tmux command in the section targets a
// session or pane by id (SR-18.17, SR-20.6), on readme_sections_test.go's
// shared mdDoc parser.

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// unusableNameItemTitle is the procedure's item heading in "Operator actions".
const unusableNameItemTitle = "A row whose recorded name cannot be used"

// byIDTargets are the only -t targets the section may give (SR-18.17's form).
var byIDTargets = []string{"'<session id>'", "'<pane id>'"}

var (
	// codeFenceRe also matches fences indented under a nested list item.
	codeFenceRe = regexp.MustCompile("^[ \t]*(`{3,}|~{3,})")
	codeSpanRe  = regexp.MustCompile("`([^`]+)`")
	shellWordRe = regexp.MustCompile(`'[^']*'|\S+`)
	stepRe      = regexp.MustCompile(`^(\d+)\.[ \t]`)
)

// tmuxCmd is one tmux command of the README: its 1-based line, its words
// joined by single spaces, its subcommand and the words after it.
type tmuxCmd struct {
	line int
	text string
	sub  string
	args []string
}

// parseTmux parses s as a tmux command line, skipping the global -S, -L and
// -f arguments; ok is false when s is not a tmux command.
func parseTmux(s string, line int) (c tmuxCmd, ok bool) {
	words := shellWordRe.FindAllString(s, -1)
	if len(words) == 0 || words[0] != "tmux" {
		return c, false
	}
	c = tmuxCmd{line: line, text: strings.Join(words, " ")}
	for i := 1; i < len(words); i++ {
		switch w := words[i]; {
		case c.sub != "":
			c.args = append(c.args, w)
		case w == "-S" || w == "-L" || w == "-f":
			i++
		case !strings.HasPrefix(w, "-"):
			c.sub = w
		}
	}
	return c, c.sub != ""
}

// flagLetters returns the letters of a single-dash flag word up to and
// including any 't', and the rest of the word after that 't'.
func flagLetters(w string) (letters, rest string, ok bool) {
	if len(w) < 2 || w[0] != '-' || w[1] == '-' {
		return "", "", false
	}
	if i := strings.IndexByte(w[1:], 't'); i >= 0 {
		return w[1 : i+2], w[i+2:], true
	}
	return w[1:], "", true
}

// targets returns c's -t targets, in getopt's attached or separate form.
func (c tmuxCmd) targets() []string {
	var out []string
	for i, w := range c.args {
		letters, rest, ok := flagLetters(w)
		switch {
		case !ok || !strings.HasSuffix(letters, "t"):
		case rest != "":
			out = append(out, rest)
		case i+1 < len(c.args):
			out = append(out, c.args[i+1])
		default:
			out = append(out, "")
		}
	}
	return out
}

// hasFlag reports whether c sets flag letter f.
func (c tmuxCmd) hasFlag(f byte) bool {
	for _, w := range c.args {
		if letters, _, ok := flagLetters(w); ok && strings.IndexByte(letters, f) >= 0 {
			return true
		}
	}
	return false
}

// tmuxCommands collects the tmux commands of lines (README lines from index
// first on): each fenced-block line and each inline code span. Later README
// command checks use it, never their own collector.
func tmuxCommands(lines []string, first int) []tmuxCmd {
	var cmds []tmuxCmd
	var prose strings.Builder
	var starts, nums []int
	inFence := false
	for i, line := range lines {
		switch {
		case codeFenceRe.MatchString(line):
			inFence = !inFence
		case inFence:
			if c, ok := parseTmux(line, first+i+1); ok {
				cmds = append(cmds, c)
			}
		default:
			starts, nums = append(starts, prose.Len()), append(nums, first+i+1)
			prose.WriteString(line + " ")
		}
	}
	for _, m := range codeSpanRe.FindAllStringSubmatchIndex(prose.String(), -1) {
		at := sort.SearchInts(starts, m[0]+1) - 1
		if c, ok := parseTmux(prose.String()[m[2]:m[3]], nums[at]); ok {
			cmds = append(cmds, c)
		}
	}
	sort.SliceStable(cmds, func(i, j int) bool { return cmds[i].line < cmds[j].line })
	return cmds
}

// procStep is one top-level numbered step of a Markdown list.
type procStep struct {
	num   string
	first int
	lines []string
}

// text is the step's text with whitespace normalised.
func (s procStep) text() string { return strings.Join(strings.Fields(strings.Join(s.lines, " ")), " ") }

// numberedSteps splits lines (README lines from index first on) into their
// top-level numbered steps; the list ends at an unindented non-step line.
// Later README procedure checks use it, never their own list splitter.
func numberedSteps(lines []string, first int) []procStep {
	var steps []procStep
	inFence := false
	for i, line := range lines {
		if codeFenceRe.MatchString(line) {
			inFence = !inFence
		}
		if m := stepRe.FindStringSubmatch(line); m != nil && !inFence {
			steps = append(steps, procStep{num: m[1], first: first + i})
		} else if len(steps) == 0 {
			continue
		} else if !inFence && line != "" && line[0] != ' ' && line[0] != '\t' {
			break
		}
		steps[len(steps)-1].lines = append(steps[len(steps)-1].lines, line)
	}
	return steps
}

// cmdShape is a tmux command a step must give: its subcommand, substrings of
// its words, and a flag it must not set.
type cmdShape struct {
	sub    string
	want   []string
	noFlag byte
}

func (s cmdShape) matches(c tmuxCmd) bool {
	if c.sub != s.sub || s.noFlag != 0 && c.hasFlag(s.noFlag) {
		return false
	}
	for _, w := range s.want {
		if !strings.Contains(c.text, w) {
			return false
		}
	}
	return true
}

func (s cmdShape) String() string {
	out := fmt.Sprintf("tmux %s with %q", s.sub, s.want)
	if s.noFlag != 0 {
		out += fmt.Sprintf(" without -%c", s.noFlag)
	}
	return out
}

// stepSpec is one procedure step: content it must name and the tmux commands
// it must give, in order.
type stepSpec struct {
	name  string
	prose []string
	cmds  []cmdShape
}

// unusableNameSteps is SR-18.17's five steps in order.
func unusableNameSteps() []stepSpec {
	identify := []string{"kept_ids", "ad.expire.kept", "ErrInternal", "agent-director read-pane", "quotes", "`list`"}
	for _, tok := range unusableNameTokens() {
		identify = append(identify, "`"+tok.note+"`", "`"+tok.kept+"`")
	}
	socket, byID := "-S '<socket>'", "-t '<session id>'"
	listing := cmdShape{sub: "list-sessions", want: []string{socket, "-F", "#{session_id}", "#{session_created}", "#{session_name}"}}
	return []stepSpec{
		{"identify the row and its name", identify, nil},
		{"find candidate sessions by id", []string{"stored form"}, []cmdShape{listing}},
		{"confirm ownership", []string{"hint"}, []cmdShape{
			{sub: "show-options", want: []string{socket, byID, "-v @ad_owner"}, noFlag: 'q'},
			{sub: "show-environment", want: []string{socket, byID, "AGENT_DIRECTOR_INSTANCE_ID"}},
		}},
		{"end the session by id", []string{"by name", "`=`"}, []cmdShape{
			{sub: "kill-session", want: []string{socket, byID}},
			listing,
		}},
		{"remove the row", []string{"agent-director-admin delete"}, nil},
	}
}

// operatorActionsItem returns the item heading titled title, failing the test
// unless it is exactly one heading inside "Operator actions". Later README
// item checks use it, never their own heading search.
func operatorActionsItem(t *testing.T, d mdDoc, title string) mdHeading {
	t.Helper()
	oa := operatorActions(t, d)
	from, to := sectionLines(d, oa)
	hs := d.titled(title)
	if len(hs) != 1 || hs[0].line < from || hs[0].line >= to || hs[0].level <= oa.level {
		t.Fatalf("%s: want exactly one item %q inside %q; found %v", d.path, title, apitest.OperatorActionsTitle, hs)
	}
	return hs[0]
}

// TestReadmeOperatorActionsUnusableNameSteps checks the unusable-name
// procedure gives SR-18.17's five steps in order, each with its content.
func TestReadmeOperatorActionsUnusableNameSteps(t *testing.T) {
	d := readMD(t, mdTopREADME)
	from, to := sectionLines(d, operatorActionsItem(t, d, unusableNameItemTitle))
	checkSteps(t, d, unusableNameItemTitle, numberedSteps(d.lines[from:to], from), unusableNameSteps())
}

// checkSteps checks steps are want's steps, numbered 1 on, each naming its
// prose and giving its tmux commands in order (one subtest per step).
func checkSteps(t *testing.T, d mdDoc, item string, steps []procStep, want []stepSpec) {
	t.Helper()
	if len(steps) != len(want) {
		t.Errorf("%s %q has %d numbered steps; want %d", d.path, item, len(steps), len(want))
	}
	for i, w := range want {
		t.Run(fmt.Sprintf("step %d %s", i+1, w.name), func(t *testing.T) {
			if i >= len(steps) {
				t.Fatalf("%s %q: step %d (%s) is missing", d.path, item, i+1, w.name)
			}
			s := steps[i]
			if s.num != fmt.Sprint(i+1) {
				t.Errorf("%s:%d: step %d (%s) is numbered %s", d.path, s.first+1, i+1, w.name, s.num)
			}
			text := s.text()
			for _, p := range w.prose {
				if !strings.Contains(text, p) {
					t.Errorf("%s:%d: step %d (%s) lacks %q", d.path, s.first+1, i+1, w.name, p)
				}
			}
			all := tmuxCommands(s.lines, s.first)
			rest := all
			for _, shape := range w.cmds {
				at := slices.IndexFunc(rest, shape.matches)
				if at < 0 {
					t.Errorf("%s:%d: step %d (%s) lacks, in order, %v; its tmux commands: %q", d.path, s.first+1, i+1, w.name, shape, cmdTexts(all))
					break
				}
				rest = rest[at+1:]
			}
		})
	}
}

// TestReadmeOperatorActionsTargetsByID checks every tmux -t target in
// "Operator actions" is a session-id or pane-id placeholder, never a name.
func TestReadmeOperatorActionsTargetsByID(t *testing.T) {
	d := readMD(t, mdTopREADME)
	from, to := sectionLines(d, operatorActions(t, d))
	cmds := tmuxCommands(d.lines[from:to], from)
	targeted := 0
	for _, c := range cmds {
		for _, target := range c.targets() {
			targeted++
			if !slices.Contains(byIDTargets, target) {
				t.Errorf("%s:%d: tmux %s targets %q, not one of %q: %s", d.path, c.line, c.sub, target, byIDTargets, c.text)
			}
		}
	}

	// The collector must find the procedure's commands, or the check is vacuous.
	for _, sub := range []string{"list-sessions", "show-options", "kill-session"} {
		if !slices.ContainsFunc(cmds, func(c tmuxCmd) bool { return c.sub == sub }) {
			t.Errorf("%s %q: no tmux %s command collected; the collector missed it", d.path, apitest.OperatorActionsTitle, sub)
		}
	}
	if targeted == 0 {
		t.Errorf("%s %q: no tmux -t target collected from %d commands", d.path, apitest.OperatorActionsTitle, len(cmds))
	}
}

// cmdTexts is cmds' texts, for failure messages.
func cmdTexts(cmds []tmuxCmd) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = c.text
	}
	return out
}
