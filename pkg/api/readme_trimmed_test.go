package api_test

// readme_trimmed_test.go pins b.3tg: the six user docs moved out of the README
// stay free of the history docs/user-docs-guide.md forbids (lines 8-9). It checks
// only that no-history rule. It does not check a doc's length or line width
// (b.zd9).

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// trimmedDocs are the docs b.3tg trimmed.
var trimmedDocs = []string{
	mdOperatorActions,
	mdConfiguration,
	mdInstall,
	"../../docs/maintenance.md",
	"../../docs/usage.md",
	mdCallerContract,
}

// trimmedHistoryRe matches the history, release and date references the
// user-docs guide forbids. A bare "2.1.280 or later" minimum, a release
// candidate or a development build named as a version rule are current
// state and allowed. It runs over folded prose, so a marker split across
// lines ("before this", then "release") still matches.
var trimmedHistoryRe = regexp.MustCompile(`(?i)before this release|releases? before|from v?\d+\.\d+\.\d+|` +
	`\bsince (?:release|version|v?\d)|\b\d{4}-\d{2}-\d{2}\b|\bused to (?:be|have)\b|` +
	`\bb\.[a-z0-9]{3}\b|\bSR-\d`)

// trimmedDocProblems returns the history markers in a doc's text. It checks
// nothing else: a doc's length and line width are never a problem. Markers are
// looked for outside code blocks, in the text with its line breaks folded to
// spaces; a blank line, a heading or a table row ends the fold.
func trimmedDocProblems(text string) []string {
	var problems []string
	var folded strings.Builder
	var starts, nums []int // the offset in folded of each line's text, and its 1-based line
	inFence := false
	for i, line := range strings.Split(text, "\n") {
		if codeFenceRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		starts, nums = append(starts, folded.Len()), append(nums, i+1)
		line = strings.TrimSpace(line)
		folded.WriteString(line)
		if line == "" || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "#") {
			folded.WriteString("\n")
		} else {
			folded.WriteString(" ")
		}
	}
	for _, m := range trimmedHistoryRe.FindAllStringIndex(folded.String(), -1) {
		line := nums[sort.SearchInts(starts, m[0]+1)-1]
		problems = append(problems, "line "+itoa(int64(line))+": history marker "+strconv.Quote(folded.String()[m[0]:m[1]]))
	}
	return problems
}

// TestTrimmedUserDocs checks each of the six trimmed user docs keeps the
// no-history rule (b.3tg). It does not check length or width.
func TestTrimmedUserDocs(t *testing.T) {
	t.Parallel()
	for _, path := range trimmedDocs {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range trimmedDocProblems(string(data)) {
				t.Errorf("%s %s", path, p)
			}
		})
	}
}

// TestTrimmedDocProblems checks the history scan sees a marker split across
// lines, reports the line it starts on, skips code blocks, leaves current-state
// wording alone and sets no limit on a doc's length or a line's width (b.zd9).
func TestTrimmedDocProblems(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, text string
		want       []string // substrings of the problems, one per problem
	}{
		{"split release", "Steps only for a binary from before this\n  release.\n", []string{"line 1: history marker \"before this release\""}},
		{"split version", "Present\nfrom\n0.11.0 on.\n", []string{"line 2: history marker \"from 0.11.0\""}},
		{"code block", "```sh\n# from 0.11.0, before this release 2026-05-27\n```\n", nil},
		{"across a blank line", "Dropped from\n\n0.11.0 notes.\n", nil},
		{"current state", "Needs 2.1.280 or later.\nA release candidate is fine.\nThey are used to end it.\n", nil},
		{"used to be", "It used to be per user.\n", []string{"used to be"}},
		{"since a version", "The count rose since\n3 runs.\n", []string{"since 3"}},
		{"since inside a word", "The whatsince 3 runs.\n", nil},
		{"long doc", strings.Repeat("A short line of prose.\n", 1000), nil},
		{"wide prose line", "This marker-free line of plain prose runs well past eighty columns, so a width rule would flag it.\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := trimmedDocProblems(c.text)
			if len(got) != len(c.want) {
				t.Fatalf("problems %q; want %d matching %q", got, len(c.want), c.want)
			}
			for i, w := range c.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("problem %d is %q; want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}
