package adminapi

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// journalSharedReasons are the reasons both migrate-journal and install.sh
// write: interrupted (each tool's interrupt) and legacy_root_symlink
// (migrate-journal's path check and install.sh's linked install directory).
var journalSharedReasons = map[string]bool{"interrupted": true, "legacy_root_symlink": true}

// TestJournalReasonsDocumented (R3): a reason added to journalActions or to
// install.sh's AD_OUTCOME_REASONS fails here until docs/operator-actions.md
// "Upgrade refusal reasons" gives it exactly one row with a next step.
func TestJournalReasonsDocumented(t *testing.T) {
	t.Parallel()
	script := journalRepoFile(t, "skills/install-agent-director/install.sh")
	found := regexp.MustCompile(`(?m)^AD_OUTCOME_REASONS='([a-z0-9_]+(?: [a-z0-9_]+)*)'$`).FindAllStringSubmatch(script, -1)
	if len(found) != 1 {
		t.Fatalf("install.sh has %d AD_OUTCOME_REASONS='<reason> ...' lines; want exactly 1", len(found))
	}
	if len(journalActions) == 0 {
		t.Fatal("journalActions is empty")
	}
	installer := map[string]bool{}
	for _, reason := range strings.Fields(found[0][1]) {
		installer[reason] = true
	}
	emitted := map[string]bool{}
	for reason := range journalActions {
		emitted[reason] = true
	}
	for reason := range installer {
		if emitted[reason] && !journalSharedReasons[reason] {
			t.Errorf("%s is both a migrate-journal reason and on AD_OUTCOME_REASONS; only %v may be", reason, journalSharedReasons)
		}
		emitted[reason] = true
	}
	for reason := range journalSharedReasons {
		if journalActions[reason] == "" || !installer[reason] {
			t.Errorf("shared reason %s: in journalActions %v, on AD_OUTCOME_REASONS %v; want both", reason, journalActions[reason] != "", installer[reason])
		}
	}

	section := journalDocSection(t)
	rows := journalReasonRows(t, section)
	reasons := make([]string, 0, len(emitted))
	for reason := range emitted {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			got := rows[reason]
			if len(got) != 1 {
				t.Fatalf("%d rows for `%s` in \"Upgrade refusal reasons\"; want exactly 1", len(got), reason)
			}
			if strings.TrimSpace(got[0].next) == "" {
				t.Errorf("`%s`'s row has no next step", reason)
			}
		})
	}
	for reason := range rows {
		if !emitted[reason] {
			t.Errorf("\"Upgrade refusal reasons\" has a row for `%s`, which neither migrate-journal nor install.sh emits", reason)
		}
	}

	// interrupted's one row names each tool's action word for word.
	if row := rows["interrupted"]; len(row) == 1 {
		actions := []string{journalActions["interrupted"],
			journalFixedAction(t, script, "ad_cutover_interrupted_before_start"),
			journalFixedAction(t, script, "ad_cutover_interrupted_committed")}
		for _, action := range actions {
			if !strings.Contains(journalFlat(row[0].line), journalFlat(action)) {
				t.Errorf("`interrupted`'s row lacks the action %q", action)
			}
		}
	}
	for role, action := range map[string]string{
		"agent_director_mutator": "stop_and_restart_after_cutover",
	} {
		if want := role + " (" + action + ")"; !strings.Contains(journalFlat(section), want) {
			t.Errorf("\"Upgrade refusal reasons\" lacks the role %q", want)
		}
	}
	if strings.Contains(section, "unverified_process") {
		t.Error("Upgrade refusal reasons still documents the removed unverified_process role")
	}
}

type journalReasonRow struct{ line, next string }

// journalRepoFile reads a file by its path from the repo root.
func journalRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// journalFlat joins s's words with single spaces, Markdown backticks removed.
func journalFlat(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "`", "")), " ")
}

// journalDocSection is "Upgrade refusal reasons", a level-3 heading under
// "Upgrading the native-input journal", up to the next heading of level 3 or
// above outside a fence.
func journalDocSection(t *testing.T) string {
	t.Helper()
	lines := strings.Split(journalRepoFile(t, "docs/operator-actions.md"), "\n")
	start, parent := -1, ""
	fenced := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		}
		if fenced {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			parent = line
		}
		if start < 0 && line == "### Upgrade refusal reasons" && parent == "## Upgrading the native-input journal" {
			start = i
			continue
		}
		if start >= 0 && (strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ") || strings.HasPrefix(line, "### ")) {
			return strings.Join(lines[start:i], "\n")
		}
	}
	if start < 0 {
		t.Fatal(`docs/operator-actions.md has no "### Upgrade refusal reasons" under "## Upgrading the native-input journal"`)
	}
	return strings.Join(lines[start:], "\n")
}

// journalReasonRows maps each reason in the section's tables to its rows. The
// one row for a relayed administrator err_name ("An `err_name`") is not a
// reason; any other reason cell must be one reason in backticks.
func journalReasonRows(t *testing.T, section string) map[string][]journalReasonRow {
	t.Helper()
	cells := func(line string) []string {
		parts := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		for i := range parts {
			parts[i] = strings.TrimSpace(parts[i])
		}
		return parts
	}
	reasonCell := regexp.MustCompile("^`([a-z0-9_]+)`$")
	rows := map[string][]journalReasonRow{}
	tables, reasonCol, nextCol := 0, -1, -1
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(line, "|") {
			reasonCol, nextCol = -1, -1
			continue
		}
		if reasonCol < 0 {
			if i+1 >= len(lines) || !strings.HasPrefix(lines[i+1], "|---") {
				t.Fatalf("table line %q has no header separator after it", line)
			}
			for j, c := range cells(line) {
				switch c {
				case "Reason":
					reasonCol = j
				case "Next step":
					nextCol = j
				}
			}
			if reasonCol < 0 || nextCol < 0 {
				t.Fatalf("table header %q lacks a Reason or Next step column", line)
			}
			tables++
			continue
		}
		if strings.HasPrefix(line, "|---") {
			continue
		}
		row := cells(line)
		if len(row) <= reasonCol || len(row) <= nextCol {
			t.Fatalf("table row %q has too few cells", line)
		}
		if row[reasonCol] == "An `err_name`" {
			continue
		}
		m := reasonCell.FindStringSubmatch(row[reasonCol])
		if m == nil {
			t.Errorf("row %q: reason cell %q is not one reason in backticks", line, row[reasonCol])
			continue
		}
		rows[m[1]] = append(rows[m[1]], journalReasonRow{line: line, next: row[nextCol]})
	}
	if tables == 0 {
		t.Fatal(`"Upgrade refusal reasons" has no table`)
	}
	return rows
}

// journalFixedAction is the one fixed action text install.sh's function fn
// gives its refusal (--arg action "<text>").
func journalFixedAction(t *testing.T, script, fn string) string {
	t.Helper()
	body := regexp.MustCompile(`(?ms)^`+regexp.QuoteMeta(fn)+`\(\) \{\n(.*?)^\}$`).FindAllStringSubmatch(script, -1)
	if len(body) != 1 {
		t.Fatalf("install.sh defines %s %d times; want 1", fn, len(body))
	}
	found := regexp.MustCompile(`--arg action "([^"$\x60\\]+)"`).FindAllStringSubmatch(body[0][1], -1)
	if len(found) != 1 {
		t.Fatalf("%s has %d fixed --arg action texts; want 1", fn, len(found))
	}
	return found[0][1]
}
