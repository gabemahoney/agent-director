package api_test

// readme_timing_values_test.go pins the README's [tmux] example block and
// timing table, and the listed README and architecture statements of the
// [tmux] timing defaults, their safe minimums and the kill and pause worst
// cases, to the internal/config constants; and the README's Claude Code
// minimum statements to each other (Epic 21, decision 6). A changed constant
// or one changed pinned value fails here. Other architecture worst-case
// statements are out of scope (Epic 21 lead decision). It reads the docs with
// readme_sections_test.go's mdDoc parser. Key names come from config.TmuxKey
// (SR-20.2), never spelt here. All tests share the TestReadmeTimingValues
// stem, so one -run selects them.

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// Doc headings this file reads, beside readme_sections_test.go's.
const (
	timingSettingsTitle   = "Timing settings (`[tmux]`)"
	stopSemanticsTitle    = "Stop semantics"
	packageInventoryTitle = "Package inventory"
	killFixtureTitle      = "pkg/api kill fixture and shared verb tables (reusable test fixtures)"
	prerequisitesTitle    = "Prerequisites"
	noExecFormItemTitle   = "A row stays `pending` and the trail shows `no_exec_form`"
)

// docNum is one stated number in a statement pattern: whole or decimal.
const docNum = `([0-9]+(?:\.[0-9]+)?)`

// leadingNumRe takes the leading number of a table cell, bold or not, so
// trailing text such as "(provisional)" or "or more" does not matter.
var leadingNumRe = regexp.MustCompile(`^\**([0-9]+)\b`)

// tmuxDefault returns key k's default as a duration.
func tmuxDefault(k config.TmuxKey) time.Duration { return config.Default().Tmux.Effective(k) }

// docSeconds formats d as the docs state durations: in seconds, shortest
// decimal ("5", "12.4").
func docSeconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

// itoa formats a whole value as the docs state it.
func itoa(v int64) string { return strconv.FormatInt(v, 10) }

// keyCode is key k's name as a quoted regexp of its code span.
func keyCode(k config.TmuxKey) string { return regexp.QuoteMeta("`" + k.Name() + "`") }

// killPaths returns SR-13.2's two kill paths at the defaults, recomputed from
// the constants: path (i) 2Q + 2A + E + 4W, path (ii) 3Q + 2A + 5W (which is
// also pause's tmux phase).
func killPaths() (p1, p2 time.Duration) {
	q := tmuxDefault(config.TmuxQueryTimeoutMs)
	a := tmuxDefault(config.TmuxActionTimeoutMs)
	e := tmuxDefault(config.TmuxKillExitWaitMs)
	w := tmuxDefault(config.TmuxPipeCloseWaitMs)
	return 2*q + 2*a + e + 4*w, 3*q + 2*a + 5*w
}

// normalised collapses each run of whitespace in text to one space, so a
// statement wrapped across lines still matches.
func normalised(text string) string { return strings.Join(strings.Fields(text), " ") }

// normalisedSection returns the normalised body of the one heading titled
// title in d, failing the test otherwise.
func normalisedSection(t *testing.T, d mdDoc, title string) string {
	t.Helper()
	hs := d.titled(title)
	if len(hs) != 1 {
		t.Fatalf("%s has %d headings titled %q; want 1", d.path, len(hs), title)
	}
	return normalised(d.body(hs[0]))
}

// docStatement is one stated value or set of values in a doc. Each group of
// pattern captures one value; want gives the value each group must state,
// from the constants (and, for a worked example, from the example's own
// input in got).
type docStatement struct {
	name    string
	path    string
	section string
	pattern string
	want    func(got []string) []string
}

// docTimingStatements lists the pinned prose statements of the timing
// defaults, minimums and kill and pause worst cases in the README and the
// architecture doc.
func docTimingStatements() []docStatement {
	start, window := config.TmuxStartingSessionSeconds, config.TmuxStoppingWindowSeconds
	grace, budget := config.TmuxPendingGraceSeconds, config.TmuxSweepBudgetSeconds
	create, pipe, exitWait := config.TmuxCreateTimeoutMs, config.TmuxPipeCloseWaitMs, config.TmuxKillExitWaitMs
	fixed := func(vals ...string) func([]string) []string { return func([]string) []string { return vals } }
	p1, p2 := killPaths()
	return []docStatement{
		{
			name: "architecture Stop semantics: kill exit wait default", path: mdArchitecture, section: stopSemanticsTitle,
			pattern: `\(` + keyCode(exitWait) + `, ` + docNum + ` s by default\)`,
			want:    fixed(docSeconds(tmuxDefault(exitWait))),
		},
		{
			name: "architecture Stop semantics: kill ceiling, path (i) the larger, and path (ii)", path: mdArchitecture, section: stopSemanticsTitle,
			pattern: `max\(2Q \+ 2A \+ E \+ 4W, 3Q \+ 2A \+ 5W\): \*\*` + docNum + ` s\*\* at the defaults on path \(i\) .*?, and ` + docNum + ` s on path \(ii\)`,
			want: func([]string) []string {
				if p1 < p2 {
					return []string{"(none: path (ii), " + docSeconds(p2) + " s, is now the larger)", docSeconds(p2)}
				}
				return []string{docSeconds(p1), docSeconds(p2)}
			},
		},
		{
			name: "architecture Stop semantics: kill's time on tmux alone", path: mdArchitecture, section: stopSemanticsTitle,
			pattern: `Its time waiting on tmux alone is at most 3Q \+ 2A \+ 5W \(` + docNum + ` s\)`,
			want:    fixed(docSeconds(p2)),
		},
		{
			name: "architecture Stop semantics: pause's tmux phase", path: mdArchitecture, section: stopSemanticsTitle,
			pattern: `3Q \+ 2A \+ 5W, \*\*` + docNum + ` s\*\* at the defaults`,
			want:    fixed(docSeconds(p2)),
		},
		{
			name: "architecture Stop semantics: pause wait default", path: mdArchitecture, section: stopSemanticsTitle,
			pattern: "`pause\\.timeout_seconds`, default " + docNum + ` s`,
			want:    fixed(strconv.Itoa(config.Default().Pause.TimeoutSeconds)),
		},
		{
			name: "architecture caller contract: stopping window and starting-session bound", path: mdArchitecture, section: callerContractClassesTitle,
			pattern: `The stopping window is ` + docNum + ` s by default \(` + keyCode(window) + `, safe minimum ` + docNum +
				` s\) and the starting-session bound ` + docNum + ` s by default \(` + keyCode(start) + `, safe minimum ` + docNum + ` s\)`,
			want: fixed(itoa(config.DefaultStoppingWindowSeconds), itoa(config.MinStoppingWindowSeconds),
				itoa(config.DefaultStartingSessionSeconds), itoa(config.MinStartingSessionSeconds)),
		},
		{
			name: "architecture caller contract: pending grace period default", path: mdArchitecture, section: callerContractClassesTitle,
			pattern: `The pending grace period is ` + docNum + ` s by default and configurable \(` + keyCode(grace) + `\)`,
			want:    fixed(itoa(config.DefaultPendingGraceSeconds)),
		},
		{
			name: "architecture caller contract: sweep budget default", path: mdArchitecture, section: callerContractClassesTitle,
			pattern: `per-run tmux time budget is ` + docNum + ` s by default and configurable \(` + keyCode(budget) + `\)`,
			want:    fixed(itoa(config.DefaultSweepBudgetSeconds)),
		},
		{
			name: "architecture package inventory: safe minimums", path: mdArchitecture, section: packageInventoryTitle,
			pattern: `Safe minimums: bound ` + docNum + ` s, stopping window ` + docNum + ` s, grace period ` + docNum +
				` s or ⌈\(create timeout \+ pipe-close wait\) / 1000⌉ \+ ` + docNum + ` s when larger`,
			want: fixed(itoa(config.MinStartingSessionSeconds), itoa(config.MinStoppingWindowSeconds),
				itoa(config.PendingGraceFloorSeconds), itoa(config.PendingGraceMarginSeconds)),
		},
		{
			name: "architecture kill fixture: starting-session bound and stopping window defaults", path: mdArchitecture, section: killFixtureTitle,
			pattern: "`defBound` / `defWindow`: the configured defaults \\(" + docNum + ` s, ` + docNum + ` s\)`,
			want:    fixed(itoa(config.DefaultStartingSessionSeconds), itoa(config.DefaultStoppingWindowSeconds)),
		},
		{
			name: "README caller contract: pending grace period default", path: mdTopREADME, section: callerContractTitle,
			pattern: `pending grace period \(` + docNum + ` s unless the operator configured another value\)`,
			want:    fixed(itoa(config.DefaultPendingGraceSeconds)),
		},
		{
			name: "README timing settings: grace period minimum rule", path: mdTopREADME, section: timingSettingsTitle,
			pattern: `its minimum: ` + docNum + ` s, or ` + keyCode(create) + ` \+ ` + keyCode(pipe) + ` \+ ` + docNum +
				` s \(rounded up to whole seconds\) when that is larger`,
			want: fixed(itoa(config.PendingGraceFloorSeconds), itoa(config.PendingGraceMarginSeconds)),
		},
		{
			name: "README timing settings: grace period worked example", path: mdTopREADME, section: timingSettingsTitle,
			pattern: "`" + regexp.QuoteMeta(create.Name()) + ` = ` + docNum + "` raises the minimum to " + docNum +
				`, so the default ` + docNum + ` is refused until ` + keyCode(grace) + ` is set to ` + docNum + ` or more`,
			want: func(got []string) []string {
				in, _ := strconv.ParseInt(got[0], 10, 64)
				m := config.PendingGraceMinimumSeconds(in, 0)
				def := itoa(config.DefaultPendingGraceSeconds)
				if m <= config.DefaultPendingGraceSeconds {
					def = "(none: the default " + def + " is not below the minimum " + itoa(m) + ")"
				}
				return []string{got[0], itoa(m), def, itoa(m)}
			},
		},
	}
}

// TestReadmeTimingValuesStatements: each listed prose statement of a timing
// default, minimum or worst case appears exactly once in its section and
// states the value recomputed from the internal/config constants.
func TestReadmeTimingValuesStatements(t *testing.T) {
	docs := map[string]mdDoc{}
	for _, s := range docTimingStatements() {
		t.Run(s.name, func(t *testing.T) {
			d, ok := docs[s.path]
			if !ok {
				d = readMD(t, s.path)
				docs[s.path] = d
			}
			text := normalisedSection(t, d, s.section)
			ms := regexp.MustCompile(s.pattern).FindAllStringSubmatch(text, -1)
			if len(ms) != 1 {
				t.Fatalf("%s section %q: %d statements match %q; want 1", s.path, s.section, len(ms), s.pattern)
			}
			got := ms[0][1:]
			want := s.want(got)
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("%s section %q, value %d of %q: the doc states %s; the constants give %s",
						s.path, s.section, i+1, ms[0][0], got[i], want[i])
				}
			}
		})
	}
}

// TestReadmeTimingValuesExampleBlock: the README's commented [tmux] example
// block lists exactly the nine keys, in table order, each at its default.
func TestReadmeTimingValuesExampleBlock(t *testing.T) {
	d := readMD(t, mdTopREADME)
	start := -1
	for i, line := range d.lines {
		if strings.HasPrefix(line, "[tmux]") {
			if start >= 0 {
				t.Fatalf("%s has a second [tmux] table at line %d (first at %d)", d.path, i+1, start+1)
			}
			start = i
		}
	}
	if start < 0 {
		t.Fatalf("%s has no [tmux] example table", d.path)
	}
	entryRe := regexp.MustCompile(`^# ([a-z_]+) = ([0-9]+)$`)
	var got [][]string
	for _, line := range d.lines[start+1:] {
		if mdFenceRe.MatchString(line) {
			break
		}
		m := entryRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s [tmux] example: line %q is not `# <key> = <default>` before the fence closes", d.path, line)
		}
		got = append(got, m[1:])
	}
	keys := config.TmuxKeys()
	if len(got) != len(keys) {
		t.Fatalf("%s [tmux] example lists %d keys; want %d: %v", d.path, len(got), len(keys), got)
	}
	for i, k := range keys {
		if got[i][0] != k.Name() {
			t.Errorf("[tmux] example entry %d is %q; want %q (table order)", i+1, got[i][0], k.Name())
		}
		if want := itoa(k.DefaultValue()); got[i][1] != want {
			t.Errorf("[tmux] example %s = %s; the default is %s", got[i][0], got[i][1], want)
		}
	}
}

// TestReadmeTimingValuesTable: the README's [tmux] table has one row per
// key, in table order, each with its unit, its default and its safe minimum
// ("none" for a key without one), comparing each cell's leading number.
func TestReadmeTimingValuesTable(t *testing.T) {
	d := readMD(t, mdTopREADME)
	hs := d.titled(timingSettingsTitle)
	if len(hs) != 1 {
		t.Fatalf("%s has %d headings titled %q; want 1", d.path, len(hs), timingSettingsTitle)
	}
	var rows [][]string
	for _, line := range strings.Split(d.body(hs[0]), "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, cells)
	}
	keys := config.TmuxKeys()
	if len(rows) != len(keys) {
		t.Fatalf("%s %q table has %d key rows; want %d", d.path, timingSettingsTitle, len(rows), len(keys))
	}
	defaults := config.Default().Tmux
	for i, k := range keys {
		row := rows[i]
		t.Run(k.Name(), func(t *testing.T) {
			if len(row) < 4 {
				t.Fatalf("row %d has %d cells; want key, unit, default, minimum: %v", i+1, len(row), row)
			}
			if want := "`" + k.Name() + "`"; row[0] != want {
				t.Fatalf("row %d names %s; want %s (table order)", i+1, row[0], want)
			}
			if row[1] != k.Unit().String() {
				t.Errorf("unit %q; want %q", row[1], k.Unit())
			}
			if got, want := leadingNum(row[2]), itoa(k.DefaultValue()); got != want {
				t.Errorf("default cell %q states %q; the constant is %s", row[2], got, want)
			}
			minCell := row[3]
			if minimum, ok := defaults.Minimum(k); ok {
				if got, want := leadingNum(minCell), itoa(minimum); got != want {
					t.Errorf("safe minimum cell %q states %q; the constants give %s", minCell, got, want)
				}
			} else if f := strings.Fields(minCell); len(f) == 0 || f[0] != "none" {
				t.Errorf("safe minimum cell %q; want none: the key has no safe minimum", minCell)
			}
		})
	}
}

// leadingNum returns cell's leading number, or "" when it has none.
func leadingNum(cell string) string {
	if m := leadingNumRe.FindStringSubmatch(cell); m != nil {
		return m[1]
	}
	return ""
}

// claudeMinimumStatements are the README's three statements of the oldest
// Claude Code agent-director supports, each with the heading that holds it;
// item marks an "Operator actions" item, found through operatorActionsItem.
var claudeMinimumStatements = []struct {
	section string
	item    bool
	pattern string
}{
	{prerequisitesTitle, false, "`claude` \\(Claude Code\\) " + claudeVersionRe + ` or later`},
	{noExecFormItemTitle, true, `runs a Claude Code older than ` + claudeVersionRe + `, so none of its hooks apply`},
	{noExecFormItemTitle, true, "Upgrade `claude` on PATH to " + claudeVersionRe + ` or later`},
}

// claudeVersionRe captures a three-part Claude Code version.
const claudeVersionRe = `([0-9]+\.[0-9]+\.[0-9]+)`

// TestReadmeTimingValuesClaudeCodeMinimum: each of the README's three Claude
// Code minimum statements appears exactly once in its section, and all three
// state the same version.
func TestReadmeTimingValuesClaudeCodeMinimum(t *testing.T) {
	d := readMD(t, mdTopREADME)
	var versions, stated []string
	for _, s := range claudeMinimumStatements {
		var text string
		if s.item {
			text = normalised(d.body(operatorActionsItem(t, d, s.section)))
		} else {
			text = normalisedSection(t, d, s.section)
		}
		ms := regexp.MustCompile(s.pattern).FindAllStringSubmatch(text, -1)
		if len(ms) != 1 {
			t.Fatalf("%s section %q: %d statements match %q; want 1", d.path, s.section, len(ms), s.pattern)
		}
		versions = append(versions, ms[0][1])
		stated = append(stated, fmt.Sprintf("%q", ms[0][0]))
	}
	for _, v := range versions[1:] {
		if v != versions[0] {
			t.Fatalf("the README's Claude Code minimum statements disagree: %s", strings.Join(stated, "; "))
		}
	}
}
