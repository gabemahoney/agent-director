package api_test

// readme_operator_actions_more_test.go checks the README's "Operator actions"
// items for stopping a set of agents and an install outside the caller's
// switch-over (AC-DOC-19, SR-18.17), and "This store's id" with the items that
// point to it, on readme_operator_actions_test.go's helpers.

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// Item titles of "Operator actions" that the checks below find or name.
const (
	stoppingItemTitle     = "Stopping a set of agents before a binary change"
	switchOverItemTitle   = "agent-director was installed outside the caller's switch-over"
	storeIDItemTitle      = "This store's id"
	leftoverItemTitle     = "A leftover, or a session with no valid label, or one that never reported in on a finished row"
	pendingItemTitle      = "A `pending` row with no launch start or token"
	agentProcessItemTitle = "An agent process that runs with no session or pane of its launch"
)

// storeIDCommand is the documented read-only lookup of this store's id.
const storeIDCommand = `sqlite3 -readonly -batch -init /dev/null -cmd ".timeout 10000" ~/.agent-director/state.db "SELECT value FROM store_meta WHERE key = 'store_id'"`

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

// liveAndTerminalStates splits every spawns state constant by
// store.IsLiveState (the store's liveStates set), keeping their order.
func liveAndTerminalStates() (live, terminal []string) {
	for _, s := range []string{store.StatePending, store.StateWaiting, store.StateWorking,
		store.StateAskUser, store.StateCheckPermission, store.StateEnded, store.StateMissing} {
		if store.IsLiveState(s) {
			live = append(live, s)
		} else {
			terminal = append(terminal, s)
		}
	}
	return live, terminal
}

func backticked(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = "`" + s + "`"
	}
	return out
}

// liveStateForms is the two accepted ways a step names the live states:
// every live state, or every state but the terminal ones.
func liveStateForms() [][]string {
	live, terminal := liveAndTerminalStates()
	return [][]string{backticked(live), {"any state but " + strings.Join(backticked(terminal), " and ")}}
}

// stepForms requires step (1 on) to hold every string of at least one form.
type stepForms struct {
	step  int
	name  string
	forms [][]string
}

// itemProcedure is an item's numbered list, named name: the first list after
// the item's first line that starts with lead ("" for the item's first
// list). Each of forms is checked beside the steps.
type itemProcedure struct {
	name  string
	lead  string
	steps []stepSpec
	forms []stepForms
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
	socket := "tmux -u -S '<socket>'"
	list := "agent-director list --label <key>=<value>"
	return []acDocItem{
		{
			title: stoppingItemTitle,
			prose: []string{"in this order", "Never delete, hand-edit or migrate `state.db`", "never delete a row"},
			refs:  []string{agentProcessItemTitle, leftoverItemTitle, pendingItemTitle},
			procs: []itemProcedure{{name: "procedure", forms: []stepForms{{3, "live states", liveStateForms()}}, steps: []stepSpec{
				{"stop what starts agents", []string{"Stop whatever starts agents first", "schedulers", "cron jobs"}, nil},
				{"choose a binary by the schema refusal", []string{"Use a binary that opens the store", list,
					"`ErrSchemaMismatch`", "the binary that wrote the store", "`ErrSchemaMigrationRequired`",
					"use the previous binary", "Keep a copy of the previous binary", "`ErrConfigMalformed`"}, nil},
				{"note the live rows", []string{"Note every row of the set"}, nil},
				{"a pre-release binary's prefix check", []string{"before this release", "prefix match", "(step 6)"}, []cmdShape{
					{sub: "list-sessions", want: []string{socket, "-F", "#{session_id}", "#{session_name}"}},
				}},
				{"pause, else kill and the live-row sequence", []string{"agent-director pause --claude-instance-id <id>",
					"`agent-director kill`", "live-row sequence", "(#caller-contract)",
					"agent-director status --claude-instance-id <id>", "Never delete a row"}, nil},
				{"hand the rest to the other items", []string{"items of this section",
					quoted(agentProcessItemTitle), quoted(leftoverItemTitle), quoted(pendingItemTitle)}, nil},
				{"confirm, then stop every serve", []string{"no live row of the set", list,
					"Then stop every other long-running agent-director process", "`agent-director serve`", "change the binary"}, []cmdShape{
					{sub: "list-sessions", want: []string{socket, "-F", "#{session_id}", "#{session_created}", "#{session_name}"}},
				}},
			}}},
		},
		{
			title: switchOverItemTitle,
			prose: []string{"step 1 of " + quoted(stoppingItemTitle), "Never delete those rows"},
			refs:  []string{stoppingItemTitle, pendingItemTitle},
			procs: []itemProcedure{
				{name: "Forward", lead: "**Forward**", steps: []stepSpec{
					{"find the rows from before the install", []string{"`tmux_socket`", "`launch_started_at`",
						list, "agent-director get --claude-instance-id <id>"}, nil},
					{"end each live one's agent by its session id", []string{"by its session id", quoted(pendingItemTitle),
						"`pause` never end such a row's session"}, nil},
					{"restart long-running processes", []string{"Restart every long-running agent-director process", "`agent-director serve`"}, nil},
					{"find-missing", []string{"`agent-director find-missing`"}, nil},
					{"deploy the caller's version", []string{"Deploy the caller's version", "`resume`", "`--reuse-finished`"}, nil},
				}},
				{name: "Back", lead: "**Back**", steps: []stepSpec{
					{"stop the set with this release's binary", []string{quoted(stoppingItemTitle), "this release's binary", "Forward step 2"}, nil},
					{"stop long-running processes", []string{"Stop every long-running agent-director process"}, nil},
					{"restore the store, then the previous binary", []string{"Restore the store, then the previous binary",
						"`state.db`", "`-wal`", "`-shm`", "(docs/migration-guide.md#v5--v4-reverses-migratev4tov5)"}, nil},
					{"start the caller's old version", []string{"Start the caller's old version"}, nil},
					{"later, the switch-over runbook", []string{"switch-over runbook from its start"}, nil},
				}},
			},
		},
	}
}

// TestReadmeOperatorActionsACDOC19Items checks both AC-DOC-19 items appear
// once each, in order after the pending-row item, with their content.
func TestReadmeOperatorActionsACDOC19Items(t *testing.T) {
	d := readMD(t, mdTopREADME)
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
			text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
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
					at := 0
					for at < len(lines) && !strings.HasPrefix(lines[at], p.lead) {
						at++
					}
					if at == len(lines) {
						t.Fatalf("%s %q has no line starting %q", d.path, it.title, p.lead)
					}
					steps := numberedSteps(lines[at:], from+at)
					checkSteps(t, d, it.title, steps, p.steps)
					for _, f := range p.forms {
						t.Run(fmt.Sprintf("step %d %s", f.step, f.name), func(t *testing.T) {
							if f.step > len(steps) {
								t.Fatalf("%s %q: step %d (%s) is missing", d.path, it.title, f.step, f.name)
							}
							text := steps[f.step-1].text()
							for _, form := range f.forms {
								if !slices.ContainsFunc(form, func(s string) bool { return !strings.Contains(text, s) }) {
									return
								}
							}
							t.Errorf("%s:%d: step %d (%s) holds none of these forms in full: %q; its text: %s",
								d.path, steps[f.step-1].first+1, f.step, f.name, f.forms, text)
						})
					}
				})
			}
		})
	}
}

// TestReadmeOperatorActionsACDOC19TitlesNotAgentVisible checks no manifest
// text, Go source or package README names either AC-DOC-19 item.
func TestReadmeOperatorActionsACDOC19TitlesNotAgentVisible(t *testing.T) {
	fold := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	knownSeen := false
	check := func(source, text string) {
		text = fold(text)
		knownSeen = knownSeen || strings.Contains(text, fold(apitest.OperatorActionsTitle))
		for _, title := range []string{stoppingItemTitle, switchOverItemTitle} {
			if strings.Contains(text, fold(title)) {
				t.Errorf("%s names the README item %q; only the README's %q may", source, title, apitest.OperatorActionsTitle)
			}
		}
	}
	walkGoSources(t, check)
	eachManifestText(check)
	for _, rel := range []string{"pkg/api/README.md", "pkg/ts-bun-client/README.md"} {
		check(rel, strings.Join(readMD(t, filepath.Join(mdRepoRoot, rel)).lines, "\n"))
	}

	// The scanned texts point to the section itself, or the scan is vacuous.
	if !knownSeen {
		t.Errorf("no scanned text names %q; the scan missed the agent-visible texts", apitest.OperatorActionsTitle)
	}
}

// TestReadmeOperatorActionsStoreIDCommand checks "This store's id" gives the
// read-only sqlite3 lookup, once, with its facts.
func TestReadmeOperatorActionsStoreIDCommand(t *testing.T) {
	d := readMD(t, mdTopREADME)
	h := operatorActionsItem(t, d, storeIDItemTitle)
	cmds := storeIDItemCommands(t, d)
	if len(cmds) != 1 || strings.Join(strings.Fields(cmds[0]), " ") != storeIDCommand {
		t.Errorf("%s %q gives sqlite3 commands %q; want exactly %q", d.path, storeIDItemTitle, cmds, storeIDCommand)
	}

	text := strings.Join(strings.Fields(d.body(h)), " ")
	for _, want := range []string{"agents' user", "16 lowercase hexadecimal characters", "changes nothing",
		"`db_path`", "prerequisite of `install.sh`", "`ad.launch.name_held`"} {
		if !strings.Contains(text, want) {
			t.Errorf("%s %q lacks %q", d.path, storeIDItemTitle, want)
		}
	}
}

// TestReadmeOperatorActionsStoreIDPointers checks each item that needs this
// store's id links to "This store's id" beside the trail record's store_id.
func TestReadmeOperatorActionsStoreIDPointers(t *testing.T) {
	d := readMD(t, mdTopREADME)
	link := fmt.Sprintf("[%s](#%s)", storeIDItemTitle, mdAnchor(storeIDItemTitle))
	for _, c := range []struct {
		item string
		step int // 0: anywhere in the item
	}{
		{leftoverItemTitle, 2},
		{unusableNameItemTitle, 3},
		{`A spawn refused as "left over from an earlier life"`, 0},
		{"A session of another agent-director store", 0},
	} {
		t.Run(fmt.Sprintf("%s step %d", c.item, c.step), func(t *testing.T) {
			from, to := sectionLines(d, operatorActionsItem(t, d, c.item))
			text := strings.Join(strings.Fields(strings.Join(d.lines[from:to], " ")), " ")
			if c.step > 0 {
				steps := numberedSteps(d.lines[from:to], from)
				if len(steps) < c.step {
					t.Fatalf("%s %q has %d numbered steps; want step %d", d.path, c.item, len(steps), c.step)
				}
				text = steps[c.step-1].text()
			}
			for _, want := range []string{link, "`store_id`"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s %q (step %d; 0 is the whole item) lacks %q", d.path, c.item, c.step, want)
				}
			}
		})
	}
}
