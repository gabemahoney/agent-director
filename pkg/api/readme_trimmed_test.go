package api_test

// readme_trimmed_test.go pins b.3tg: the six user docs moved out of the README
// stay within their size targets, wrapped at 80 columns, and free of the
// history docs/user-docs-guide.md forbids.

import (
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// trimmedDocWidth is the wrap width of the user docs.
const trimmedDocWidth = 80

// trimmedDocs are the docs b.3tg trimmed and their `wc -l` targets.
var trimmedDocs = []struct {
	path     string
	maxLines int
}{
	{mdOperatorActions, 300},
	{mdConfiguration, 100},
	{mdInstall, 100},
	{"../../docs/maintenance.md", 60},
	{"../../docs/usage.md", 100},
	{mdCallerContract, 70},
}

var (
	// trimmedHistoryRe matches the history, release and date references the
	// user-docs guide forbids. A bare "2.1.280 or later" minimum, a release
	// candidate or a development build named as a version rule are current
	// state and allowed. It runs over folded prose, so a marker split by the
	// 80-column wrap ("before this", then "release") still matches.
	trimmedHistoryRe = regexp.MustCompile(`(?i)before this release|releases? before|from v?\d+\.\d+\.\d+|` +
		`\bsince (?:release|version|v?\d)|\b\d{4}-\d{2}-\d{2}\b|\bused to (?:be|have)\b|` +
		`\bb\.[a-z0-9]{3}\b|\bSR-\d`)
	// trimmedTokenRe is one word of a prose line; a code span or a link
	// counts as one word even when it holds spaces.
	trimmedTokenRe  = regexp.MustCompile("(?:`[^`]*`|\\[[^\\]]*\\]\\([^)]*\\)|\\S)+")
	trimmedListMark = regexp.MustCompile(`^\s*(?:[-*]|\d+\.)\s+`)
)

// trimmedDocProblems returns what is wrong with a doc's text: more lines than
// maxLines (as `wc -l` counts), a prose line wider than 80 columns, or a
// history marker. Code blocks, tables, headings and lines of one unbreakable
// code span or link are not prose. History markers are looked for outside code
// blocks, in the text with its line breaks folded to spaces; a blank line, a
// heading or a table row ends the fold.
func trimmedDocProblems(text string, maxLines int) []string {
	var problems []string
	if n := strings.Count(text, "\n"); n > maxLines {
		problems = append(problems, "has "+itoa(int64(n))+" lines; target "+itoa(int64(maxLines))+
			" (from b.3tg and docs/user-docs-guide.md \"Keep it short\": trim the file first; raise the limit only deliberately)")
	}
	var folded strings.Builder
	var starts, nums []int // the offset in folded of each line's text, and its 1-based line
	inFence := false
	for i, line := range strings.Split(text, "\n") {
		at := "line " + itoa(int64(i+1)) + ": "
		switch {
		case codeFenceRe.MatchString(line):
			inFence = !inFence
			continue
		case inFence:
			continue
		case strings.HasPrefix(line, "|"), strings.HasPrefix(line, "#"):
		case utf8.RuneCountInString(line) > trimmedDocWidth &&
			len(trimmedTokenRe.FindAllString(trimmedListMark.ReplaceAllString(line, ""), -1)) > 1:
			problems = append(problems, at+"prose wider than 80 columns")
		}
		starts, nums = append(starts, folded.Len()), append(nums, i+1)
		folded.WriteString(strings.TrimSpace(line))
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "|") || strings.HasPrefix(line, "#") {
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

// TestTrimmedUserDocs checks each of the six trimmed user docs meets its size
// target, its 80-column wrap and the no-history rule (b.3tg).
func TestTrimmedUserDocs(t *testing.T) {
	t.Parallel()
	for _, d := range trimmedDocs {
		t.Run(d.path, func(t *testing.T) {
			data, err := os.ReadFile(d.path)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range trimmedDocProblems(string(data), d.maxLines) {
				t.Errorf("%s %s", d.path, p)
			}
		})
	}
}

// TestTrimmedDocProblemsFindsWrappedMarkers checks the history scan sees a
// marker the 80-column wrap splits, reports the line it starts on, skips code
// blocks and leaves current-state wording alone.
func TestTrimmedDocProblemsFindsWrappedMarkers(t *testing.T) {
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
	} {
		t.Run(c.name, func(t *testing.T) {
			got := trimmedDocProblems(c.text, 100)
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
