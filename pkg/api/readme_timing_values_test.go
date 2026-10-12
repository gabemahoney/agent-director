package api_test

// readme_timing_values_test.go pins docs/configuration.md's [tmux] example block and
// timing table, and the listed user-doc and architecture statements of the
// [tmux] timing defaults, safe minimums and kill and pause worst cases, to
// the internal/config constants, and the user docs' per-agent (Claude Code,
// Codex) minimum statements to each other (Epic 21, decision 6; SR-15.2). Key names come from
// config.TmuxKey (SR-20.2), never spelt here. -run TestReadmeTimingValues
// selects them all.

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
	prerequisitesTitle    = "Prerequisites"
	requirementsTitle     = "Requirements"
	noExecFormItemTitle   = "A row stays `pending` and the trail shows `no_exec_form`"

	codexOtherCauseItemTitle = "A Codex row stays `pending` or goes `missing` for another reason"
)

var (
	// docNumRe is one stated number: whole or decimal, not part of a word ("2Q").
	docNumRe = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?\b`)
	// leadingNumRe is a table cell's leading number, bold or not, whatever follows ("(provisional)", "or more").
	leadingNumRe = regexp.MustCompile(`^\**([0-9]+)\b`)
)

func tmuxDefault(k config.TmuxKey) time.Duration { return config.Default().Tmux.Effective(k) }

// docSeconds formats d as the docs state durations: seconds, shortest decimal ("5", "12.4").
func docSeconds(d time.Duration) string { return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func keyCode(k config.TmuxKey) string { return "`" + k.Name() + "`" }

// worstCases recomputes, at the defaults, SR-13.2's two kill paths, (i)
// 2Q + 2A + E + 4W and (ii) 3Q + 2A + 5W; kill-finished on this id's own
// abandoned launch, its follow-up then its wait, 3Q + 2A + E + 5W for one
// session and 2A + 2W for each more (b.myx); and pause's tmux phase
// 3Q + 3A + 6W (its line clear, C-u, b.9o4, one action call more than kill's
// path (ii)).
func worstCases() (kill1, kill2, abandoned, perSession, pause time.Duration) {
	q, a := tmuxDefault(config.TmuxQueryTimeoutMs), tmuxDefault(config.TmuxActionTimeoutMs)
	e, w := tmuxDefault(config.TmuxKillExitWaitMs), tmuxDefault(config.TmuxPipeCloseWaitMs)
	return 2*q + 2*a + e + 4*w, 3*q + 2*a + 5*w, 3*q + 2*a + e + 5*w, 2*a + 2*w, 3*q + 3*a + 6*w
}

// normalised collapses each run of whitespace in text to one space, so a
// statement wrapped across lines still matches.
func normalised(text string) string { return strings.Join(strings.Fields(text), " ") }

// sectionBody returns the body of the one heading titled title in d, failing the test otherwise.
func sectionBody(t *testing.T, d mdDoc, title string) string {
	t.Helper()
	hs := d.titled(title)
	if len(hs) != 1 {
		t.Fatalf("%s has %d headings titled %q; want 1", d.path, len(hs), title)
	}
	return d.body(hs[0])
}

// docStatement is one stated value or set of values in a doc. Value i is the
// first number after anchors[i] (a key's code span, a formula or a short
// term), searched from the end of value i-1; an empty anchor takes the next
// number. want gives the value each must state, from the constants (and, for
// a worked example, from the example's own input in got).
type docStatement struct {
	name, path, section string
	anchors             []string
	want                func(got []string) []string
}

// statedNumbers returns, for each anchor in turn, the first number after it
// in text, searching from the end of the previous number.
func statedNumbers(text string, anchors []string) ([]string, error) {
	var got []string
	pos := 0
	for _, a := range anchors {
		i := strings.Index(text[pos:], a)
		if i < 0 {
			return got, fmt.Errorf("no %q after the values %q", a, got)
		}
		pos += i + len(a)
		loc := docNumRe.FindStringIndex(text[pos:])
		if loc == nil {
			return got, fmt.Errorf("no number after %q", a)
		}
		got = append(got, text[pos+loc[0]:pos+loc[1]])
		pos += loc[1]
	}
	return got, nil
}

// docTimingStatements lists the pinned prose statements of the timing
// defaults, minimums and kill and pause worst cases in the user docs and the
// architecture doc.
func docTimingStatements() []docStatement {
	start, window := config.TmuxStartingSessionSeconds, config.TmuxStoppingWindowSeconds
	pipe, exitWait := config.TmuxPipeCloseWaitMs, config.TmuxKillExitWaitMs
	fixed := func(vals ...string) func([]string) []string { return func([]string) []string { return vals } }
	p1, p2, abandoned, perSession, pause := worstCases()
	arch, conf := mdArchitecture, mdConfiguration
	return []docStatement{
		{"architecture Stop semantics: kill exit wait default", arch, stopSemanticsTitle,
			[]string{keyCode(exitWait)}, fixed(docSeconds(tmuxDefault(exitWait)))},
		{"architecture Stop semantics: kill ceiling, path (i) the larger, and path (ii)", arch, stopSemanticsTitle,
			[]string{"max(2Q + 2A + E + 4W, 3Q + 2A + 5W)", ""}, func([]string) []string {
				if p1 < p2 {
					return []string{"(none: path (ii), " + docSeconds(p2) + " s, is now the larger)", docSeconds(p2)}
				}
				return []string{docSeconds(p1), docSeconds(p2)}
			}},
		{"architecture Stop semantics: kill's time on tmux alone", arch, stopSemanticsTitle,
			[]string{"at most 3Q + 2A + 5W"}, fixed(docSeconds(p2))},
		{"architecture Stop semantics: kill-finished on this id's own abandoned launch, one session and each more", arch,
			stopSemanticsTitle, []string{"3Q + 2A + E + 5W (", "2A + 2W ("}, fixed(docSeconds(abandoned), docSeconds(perSession))},
		{"architecture Stop semantics: pause's tmux phase", arch, stopSemanticsTitle,
			[]string{"3Q + 3A + 6W,"}, fixed(docSeconds(pause))},
		{"architecture Stop semantics: pause wait default", arch, stopSemanticsTitle,
			[]string{"`pause.timeout_seconds`, default"}, fixed(strconv.Itoa(config.Default().Pause.TimeoutSeconds))},
		{"architecture caller contract: stopping window and starting-session bound", arch, callerContractClassesTitle,
			[]string{"stopping window is", keyCode(window), "bound", keyCode(start)},
			fixed(itoa(config.DefaultStoppingWindowSeconds), itoa(config.MinStoppingWindowSeconds),
				itoa(config.DefaultStartingSessionSeconds), itoa(config.MinStartingSessionSeconds))},
		{"architecture caller contract: pending grace period default", arch, callerContractClassesTitle,
			[]string{"grace period is"}, fixed(itoa(config.DefaultPendingGraceSeconds))},
		{"architecture caller contract: sweep budget default", arch, callerContractClassesTitle,
			[]string{"time budget is"}, fixed(itoa(config.DefaultSweepBudgetSeconds))},
		{"architecture package inventory: safe minimums", arch, packageInventoryTitle,
			[]string{"Safe minimums: bound", "stopping window", "grace period", "⌉ +"},
			fixed(itoa(config.MinStartingSessionSeconds), itoa(config.MinStoppingWindowSeconds),
				itoa(config.PendingGraceFloorSeconds), itoa(config.PendingGraceMarginSeconds))},
		{"configuration timing settings: grace period minimum rule", conf, timingSettingsTitle,
			[]string{"Grace period minimum.", keyCode(pipe)}, fixed(itoa(config.PendingGraceFloorSeconds), itoa(config.PendingGraceMarginSeconds))},
		{"configuration timing settings: unset grace period", conf, timingSettingsTitle,
			[]string{"Unset gives"}, fixed(itoa(config.DefaultPendingGraceSeconds))},
	}
}

// TestReadmeTimingValuesStatements: each listed statement of a timing
// default, minimum or worst case states, after its anchors, the value
// recomputed from the internal/config constants.
func TestReadmeTimingValuesStatements(t *testing.T) {
	t.Parallel()
	docs := map[string]mdDoc{}
	for _, s := range docTimingStatements() {
		t.Run(s.name, func(t *testing.T) {
			d, ok := docs[s.path]
			if !ok {
				d = readMD(t, s.path)
				docs[s.path] = d
			}
			got, err := statedNumbers(normalised(sectionBody(t, d, s.section)), s.anchors)
			if err != nil {
				t.Fatalf("%s section %q: %v", s.path, s.section, err)
			}
			for i, want := range s.want(got) {
				if got[i] != want {
					t.Errorf("%s section %q, the number after %q: the doc states %s; the constants give %s",
						s.path, s.section, s.anchors[i], got[i], want)
				}
			}
		})
	}
}

// TestReadmeTimingValuesExampleBlock: docs/configuration.md's commented [tmux] example
// block lists exactly the nine keys, in table order, each at its default.
func TestReadmeTimingValuesExampleBlock(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdConfiguration)
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
		if want := []string{k.Name(), itoa(k.DefaultValue())}; got[i][0] != want[0] || got[i][1] != want[1] {
			t.Errorf("[tmux] example entry %d is %s = %s; want %s = %s (table order, the default)", i+1, got[i][0], got[i][1], want[0], want[1])
		}
	}
}

// TestReadmeTimingValuesTable: docs/configuration.md's [tmux] table has one row per
// key, in table order, each with its unit, its default and its safe minimum
// ("none" for a key without one), comparing each cell's leading number.
func TestReadmeTimingValuesTable(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdConfiguration)
	var rows [][]string
	for _, line := range strings.Split(sectionBody(t, d, timingSettingsTitle), "\n") {
		if strings.HasPrefix(line, "| `") {
			cells := strings.Split(strings.Trim(line, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			rows = append(rows, cells)
		}
	}
	keys := config.TmuxKeys()
	if len(rows) != len(keys) {
		t.Fatalf("%s %q table has %d key rows; want %d", d.path, timingSettingsTitle, len(rows), len(keys))
	}
	leadingNum := func(cell string) string {
		if m := leadingNumRe.FindStringSubmatch(cell); m != nil {
			return m[1]
		}
		return ""
	}
	for i, k := range keys {
		row := rows[i]
		t.Run(k.Name(), func(t *testing.T) {
			if len(row) < 4 {
				t.Fatalf("row %d has %d cells; want key, unit, default, minimum: %v", i+1, len(row), row)
			}
			if row[0] != keyCode(k) {
				t.Fatalf("row %d names %s; want %s (table order)", i+1, row[0], keyCode(k))
			}
			if row[1] != k.Unit().String() {
				t.Errorf("unit %q; want %q", row[1], k.Unit())
			}
			if got, want := leadingNum(row[2]), itoa(k.DefaultValue()); got != want {
				t.Errorf("default cell %q states %q; the constant is %s", row[2], got, want)
			}
			if minimum, ok := config.Default().Tmux.Minimum(k); ok {
				if got, want := leadingNum(row[3]), itoa(minimum); got != want {
					t.Errorf("safe minimum cell %q states %q; the constants give %s", row[3], got, want)
				}
			} else if f := strings.Fields(row[3]); len(f) == 0 || f[0] != "none" {
				t.Errorf("safe minimum cell %q; want none: the key has no safe minimum", row[3])
			}
		})
	}
}

// agentMinimums are the per-agent minimum statements of the user docs. Each
// agent's minimum is stated once in the README's Prerequisites and once in
// install.md's Requirements, and nowhere else; pointer is the operator-actions
// item for that agent's launch failure, which links Requirements instead.
var agentMinimums = []struct{ agent, program, pointer string }{
	{"Claude Code", "claude", noExecFormItemTitle},
	{"Codex", "codex", codexOtherCauseItemTitle},
}

// TestReadmeTimingValuesAgentMinimum: for each agent, the README's
// Prerequisites and install.md's Requirements each state its minimum exactly
// once and the same version; no other user doc states it; operator-actions.md's
// item for that agent's launch failure, found through operatorActionsItem,
// points to install.md's Requirements instead of repeating it.
func TestReadmeTimingValuesAgentMinimum(t *testing.T) {
	t.Parallel()
	const version = `([0-9]+\.[0-9]+\.[0-9]+)`
	readme, install, oa := readMD(t, mdTopREADME), readMD(t, mdInstall), readMD(t, mdOperatorActions)
	for _, a := range agentMinimums {
		t.Run(a.agent, func(t *testing.T) {
			t.Parallel()
			prereq := regexp.MustCompile("`" + a.program + "` \\(" + regexp.QuoteMeta(a.agent) + "\\) " + version + ` or later`)
			var versions, stated []string
			for _, s := range []struct {
				d       mdDoc
				section string
			}{{readme, prerequisitesTitle}, {install, requirementsTitle}} {
				ms := prereq.FindAllStringSubmatch(normalised(sectionBody(t, s.d, s.section)), -1)
				if len(ms) != 1 {
					t.Fatalf("%s section %q: %d statements match %q; want 1", s.d.path, s.section, len(ms), prereq)
				}
				versions, stated = append(versions, ms[0][1]), append(stated, fmt.Sprintf("%q", ms[0][0]))
			}
			if versions[0] != versions[1] {
				t.Fatalf("the user docs' %s minimum statements disagree: %s", a.agent, strings.Join(stated, "; "))
			}
			for _, path := range mdUserDocs {
				if path == mdTopREADME || path == mdInstall {
					continue
				}
				d := readMD(t, path)
				if ms := prereq.FindAllString(normalised(strings.Join(d.lines, "\n")), -1); len(ms) > 0 {
					t.Errorf("%s states the %s minimum (%q); only the README and install.md do, others link", path, a.agent, ms[0])
				}
			}
			item := normalised(oa.body(operatorActionsItem(t, oa, a.pointer)))
			if link := "[Requirements](install.md#" + mdAnchor(requirementsTitle) + ")"; !strings.Contains(item, link) {
				t.Errorf("%s section %q lacks the pointer %s to the minimum", oa.path, a.pointer, link)
			}
			if strings.Contains(item, versions[0]) {
				t.Errorf("%s section %q repeats the %s minimum %s instead of linking it", oa.path, a.pointer, a.agent, versions[0])
			}
		})
	}
}

// docConfigKeys are the non-[tmux] keys of docs/configuration.md, each with
// the default it states, from the internal/config constants and Default(), and
// the "1 to N" range cell it states where the key has a maximum (rng empty
// otherwise). noExample marks a key the doc's example block leaves out:
// inject_help_hook, whose default (false) is not what install.sh writes (true),
// and the two [launch] paths, which the example block shows commented out.
func docConfigKeys() []struct {
	key, def, rng string
	noExample     bool
} {
	c := config.Default()
	b := func(v bool) string { return strconv.FormatBool(v) }
	rng := func(max int64) string { return "1 to " + itoa(max) }
	return []struct {
		key, def, rng string
		noExample     bool
	}{
		{"defaults.relay_mode", c.Defaults.RelayMode, "", false},
		{"defaults.expire_retention_days", itoa(int64(c.Defaults.ExpireRetentionDays)), rng(int64(config.MaxExpireRetentionDays)), false},
		{"defaults.disable_ask_user", b(c.Defaults.DisableAskUser), "", false},
		{"defaults.inject_help_hook", b(c.Defaults.InjectHelpHook), "", true},
		{"relay.poll_base_ms", itoa(int64(c.Relay.PollBaseMs)), "", false},
		{"relay.poll_jitter_ms", itoa(int64(c.Relay.PollJitterMs)), "", false},
		{"relay.timeout_seconds", itoa(int64(c.Relay.TimeoutSeconds)), rng(int64(config.MaxRelayTimeoutSeconds)), false},
		{"relay.permission_request_cap", itoa(int64(c.Relay.PermissionRequestCap)), "", false},
		{"pause.timeout_seconds", itoa(int64(c.Pause.TimeoutSeconds)), rng(int64(config.MaxPauseTimeoutSeconds)), false},
		{"pre_trust.lock_wait_seconds", itoa(int64(c.PreTrust.LockWaitSeconds)), rng(int64(config.MaxPreTrustLockWaitSeconds)), false},
		{"store.db_path", c.Store.DbPath, "", false},
		{"store.busy_timeout_ms", itoa(int64(c.Store.BusyTimeoutMs)), rng(int64(config.MaxStoreBusyTimeoutMs)), false},
		{"state.settle_ms", itoa(int64(c.State.SettleMs)), "", false},
		{"launch.claude_path", "unset", "", true},
		{"launch.codex_path", "unset", "", true},
		{"log.error_log_path", c.Log.ErrorLogPath, "", false},
	}
}

// TestReadmeTimingValuesConfigKeys: docs/configuration.md's Keys table has one
// row for each non-[tmux] key, in order, whose Default (and, where the key has
// a maximum, Range) cell states the internal/config value; and the example
// block's value for each key it lists is the default, and it lists every key
// except those marked noExample.
func TestReadmeTimingValuesConfigKeys(t *testing.T) {
	t.Parallel()
	d := readMD(t, mdConfiguration)
	keys := docConfigKeys()
	var rows [][]string
	for _, line := range strings.Split(sectionBody(t, d, "Keys"), "\n") {
		if strings.HasPrefix(line, "| `[") {
			cells := strings.Split(strings.Trim(line, "|"), "|")
			for i := range cells {
				cells[i] = strings.Trim(strings.TrimSpace(cells[i]), "`")
			}
			rows = append(rows, cells)
		}
	}
	if len(rows) != len(keys) {
		t.Fatalf("%s Keys table has %d key rows; want %d", d.path, len(rows), len(keys))
	}
	for i, k := range keys {
		t.Run(k.key, func(t *testing.T) {
			row := rows[i]
			if len(row) < 3 {
				t.Fatalf("row %d has %d cells; want key, range, default: %v", i+1, len(row), row)
			}
			section, name, _ := strings.Cut(k.key, ".")
			if want := "[" + section + "] " + name; row[0] != want {
				t.Fatalf("row %d names %q; want %q", i+1, row[0], want)
			}
			if row[2] != k.def {
				t.Errorf("default cell %q; the constant is %q", row[2], k.def)
			}
			if k.rng != "" && row[1] != k.rng {
				t.Errorf("range cell %q; the constants give %q", row[1], k.rng)
			}
		})
	}

	// The example block, outside [tmux] (TestReadmeTimingValuesExampleBlock).
	example := map[string]string{}
	section, inFence := "", false
	for _, line := range d.lines {
		switch line = strings.TrimSpace(line); {
		case mdFenceRe.MatchString(line):
			inFence = !inFence
		case !inFence, line == "", strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "["):
			section, _, _ = strings.Cut(strings.TrimPrefix(line, "["), "]")
		case section != "tmux":
			kv, _, _ := strings.Cut(line, "#")
			name, val, _ := strings.Cut(kv, "=")
			example[section+"."+strings.TrimSpace(name)] = strings.Trim(strings.TrimSpace(val), `"`)
		}
	}
	listed := 0
	for _, k := range keys {
		got, ok := example[k.key]
		switch {
		case k.noExample:
			if ok {
				t.Errorf("%s example block lists %s = %q; the block leaves it out", d.path, k.key, got)
			}
		case !ok || got != k.def:
			t.Errorf("%s example block gives %s = %q (present: %v); the default is %q", d.path, k.key, got, ok, k.def)
		default:
			listed++
		}
	}
	if len(example) != listed {
		t.Errorf("%s example block has %d non-[tmux] keys; want %d: %v", d.path, len(example), listed, example)
	}
}
