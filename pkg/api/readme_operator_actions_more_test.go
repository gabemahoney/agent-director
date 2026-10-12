package api_test

// readme_operator_actions_more_test.go checks docs/operator-actions.md's
// items for stopping a set of agents and an install outside the caller's
// switch-over (AC-DOC-19, SR-18.17), and "This store's id" with the places
// that point to it (SR-5.4), on readme_operator_actions_test.go's helpers.

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Item titles of "Operator actions" that the checks below find or name.
const (
	stoppingItemTitle   = "Stopping a set of agents before a binary change"
	switchOverItemTitle = "agent-director was installed outside the caller's switch-over"
	storeIDItemTitle    = "This store's id"
	pendingItemTitle    = "A `pending` row with no launch start or token"
)

// storeIDBusyTimeoutPlaceholder stands in storeIDCommand for the operator's
// [store] busy_timeout_ms, which the doc names instead of a literal
// (b.c7f); the run of the line substitutes it.
const storeIDBusyTimeoutPlaceholder = "<busy_timeout_ms>"

// storeIDCommand is the documented read-only lookup of this store's id.
const storeIDCommand = `sqlite3 -readonly -batch -init /dev/null -cmd ".timeout ` + storeIDBusyTimeoutPlaceholder +
	`" ~/.config/agent-director/state.db "SELECT value FROM store_meta WHERE key = 'store_id'"`

// storeIDItemCommands returns the trimmed lines starting "sqlite3" in the
// fenced blocks of the "This store's id" item. Both the command's pinned
// text and the run of it read the item through this one helper.
func storeIDItemCommands(t *testing.T, d mdDoc) []string {
	t.Helper()
	from, to := sectionLines(d, operatorActionsItem(t, d, storeIDItemTitle))
	var cmds []string
	inFence := false
	for _, line := range d.lines[from:to] {
		switch line = strings.TrimSpace(line); {
		case codeFenceRe.MatchString(line):
			inFence = !inFence
		case inFence && strings.HasPrefix(line, "sqlite3"):
			cmds = append(cmds, line)
		}
	}
	return cmds
}

// itemProcedure is an item's numbered list, named name: the first list after
// the item's first line that starts with lead ("" for the item's first list).
type itemProcedure struct {
	name  string
	lead  string
	steps []stepSpec
}

// acDocItem is an AC-DOC-19 item: statements anywhere in it, the items it
// names, and its procedures.
type acDocItem struct {
	title string
	prose []string
	refs  []string
	procs []itemProcedure
}

func quoted(title string) string { return `"` + title + `"` }

// acDoc19Items is SR-18.17's two items in order, with their content.
func acDoc19Items() []acDocItem {
	list := "agent-director list --label <key>=<value>"
	return []acDocItem{
		{
			title: stoppingItemTitle,
			prose: []string{"in this order", "Never delete a row or edit `state.db`"},
			procs: []itemProcedure{{name: "procedure", steps: []stepSpec{
				{"stop what starts agents", []string{"Stop whatever starts agents", "schedulers", "cron jobs"}, nil},
				{"list the set, choose a binary by the schema refusal", []string{list,
					"`ErrSchemaMismatch`", "use the binary that wrote it", "`ErrSchemaMigrationRequired`",
					"use the previous binary", "`ErrConfigMalformed`"}, nil},
				{"pause, else kill and the live-row sequence", []string{"agent-director pause --instance-id <id>",
					"`waiting` row", "`agent-director kill`", "steps 1 to 5 of the live-row sequence", "(caller-contract.md)"}, nil},
				{"hand the rest to the other items, then stop every serve", []string{"items above", "list the set again (step 2)",
					"(step 1 of " + quoted(inspectBlockTitle) + ")",
					"`serve`", "change the binary"}, nil},
			}}},
		},
		{
			title: switchOverItemTitle,
			prose: []string{"step 1 of " + quoted(stoppingItemTitle), "**Back** (the caller's version cannot be deployed)",
				"[Going back to the previous release](#going-back-to-the-previous-release)"},
			refs: []string{stoppingItemTitle, pendingItemTitle},
			procs: []itemProcedure{
				{name: "Forward", lead: "**Forward**", steps: []stepSpec{
					{"end the agent of each live pre-upgrade row", []string{"`get`", "no `tmux_socket`", "no `launch_started_at`",
						quoted(pendingItemTitle), "`agent-director find-missing`"}, nil},
					{"deploy the caller's version", []string{"Deploy the caller's version", "never delete them"}, nil},
				}},
			},
		},
	}
}

// TestReadmeOperatorActionsACDOC19Items checks both AC-DOC-19 items appear
// once each, in order after the pending-row item, with their content.
func TestReadmeOperatorActionsACDOC19Items(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdOperatorActions)
	prev := operatorActionsItem(t, d, pendingItemTitle)
	for _, it := range acDoc19Items() {
		h := operatorActionsItem(t, d, it.title)
		if h.line <= prev.line {
			t.Errorf("%s:%d: item %q comes before %q; SR-18.17 orders it after", d.path, h.line+1, it.title, prev.title)
		}
		prev = h
	}
	for _, it := range acDoc19Items() {
		t.Run(it.title, func(t *testing.T) {
			from, to := sectionLines(d, operatorActionsItem(t, d, it.title))
			lines := d.lines[from:to]
			text := normalised(strings.Join(lines, " "))
			for _, p := range it.prose {
				if !strings.Contains(text, p) {
					t.Errorf("%s %q lacks %q", d.path, it.title, p)
				}
			}
			for _, ref := range it.refs {
				if !strings.Contains(text, quoted(ref)) {
					t.Errorf("%s %q never names the item %q", d.path, it.title, ref)
				}
				operatorActionsItem(t, d, ref)
			}
			for _, p := range it.procs {
				t.Run(p.name, func(t *testing.T) {
					at := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, p.lead) })
					if at < 0 {
						t.Fatalf("%s %q has no line starting %q", d.path, it.title, p.lead)
					}
					steps := numberedSteps(lines[at:], from+at)
					checkSteps(t, d, it.title, steps, p.steps)
				})
			}
		})
	}
}

// TestReadmeOperatorActionsACDOC19TitlesNotAgentVisible checks no manifest
// text, Go source or package README names either AC-DOC-19 item.
func TestReadmeOperatorActionsACDOC19TitlesNotAgentVisible(t *testing.T) {
	t.Parallel()
	fold := func(s string) string { return strings.ToLower(normalised(s)) }
	knownSeen := false
	check := func(source, text string) {
		text = fold(text)
		knownSeen = knownSeen || strings.Contains(text, fold(apitest.OperatorActionsTitle))
		for _, title := range []string{stoppingItemTitle, switchOverItemTitle} {
			if strings.Contains(text, fold(title)) {
				t.Errorf("%s names the operator-actions item %q; only %s may", source, title, mdOperatorActions)
			}
		}
	}
	walkGoSources(t, check)
	eachManifestText(check)
	for _, rel := range []string{"pkg/api/README.md", "pkg/ts-bun-client/README.md"} {
		check(rel, strings.Join(readMD(t, filepath.Join(mdRepoRoot, rel)).lines, "\n"))
	}
	if !knownSeen { // the scanned texts point to the section itself, or the scan is vacuous
		t.Errorf("no scanned text names %q; the scan missed the agent-visible texts", apitest.OperatorActionsTitle)
	}
}

// TestReadmeOperatorActionsStoreIDCommand checks "This store's id" gives the
// read-only sqlite3 lookup, once, with its facts.
func TestReadmeOperatorActionsStoreIDCommand(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdOperatorActions)
	if cmds := storeIDItemCommands(t, d); len(cmds) != 1 || normalised(cmds[0]) != storeIDCommand {
		t.Errorf("%s %q gives sqlite3 commands %q; want exactly %q", d.path, storeIDItemTitle, cmds, storeIDCommand)
	}
	text := normalised(d.body(operatorActionsItem(t, d, storeIDItemTitle)))
	for _, want := range []string{"`store_id`", "16 hexadecimal characters", "`db_path`",
		"with `" + storeIDBusyTimeoutPlaceholder + "` set to your `[store] busy_timeout_ms`"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s %q lacks %q", d.path, storeIDItemTitle, want)
		}
	}
}

// TestReadmeOperatorActionsStoreIDPointers checks each place that compares a
// session label's store id with this store's links to "This store's id".
func TestReadmeOperatorActionsStoreIDPointers(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdOperatorActions)
	link := fmt.Sprintf("[%s](#%s)", storeIDItemTitle, mdAnchor(storeIDItemTitle))
	for _, c := range []struct {
		item string
		step int // 0: anywhere in the item
	}{
		{inspectBlockTitle, 0},
		{unusableNameItemTitle, 3},
		{"A session of another agent-director store", 0},
	} {
		t.Run(fmt.Sprintf("%s step %d", c.item, c.step), func(t *testing.T) {
			from, to := sectionLines(d, operatorActionsItem(t, d, c.item))
			text := normalised(strings.Join(d.lines[from:to], " "))
			if steps := numberedSteps(d.lines[from:to], from); c.step > len(steps) {
				t.Fatalf("%s %q has %d numbered steps; want step %d", d.path, c.item, len(steps), c.step)
			} else if c.step > 0 {
				text = steps[c.step-1].text()
			}
			if !strings.Contains(text, link) {
				t.Errorf("%s %q (step %d; 0 is the whole item) lacks %q", d.path, c.item, c.step, link)
			}
		})
	}
}

// linesOutsideCode returns lines without the fenced code blocks and their fences.
func linesOutsideCode(lines []string) []string {
	var out []string
	inFence := false
	for _, line := range lines {
		switch {
		case codeFenceRe.MatchString(line):
			inFence = !inFence
		case !inFence:
			out = append(out, line)
		}
	}
	return out
}
