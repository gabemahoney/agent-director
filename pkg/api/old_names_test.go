package api_test

// old_names_test.go is SR-4.5's old-names test: no Claude-named name that
// Appendix A renamed appears on a renamed surface. The walker and the
// exception mechanism are in source_scan_test.go. Claude Code's own names
// (CLAUDE_CONFIG_DIR, .claude.json, AskUserQuestion, claude_path / ClaudePath,
// deniedClaudeArgs, ...) are not in oldNames, so they are never flagged.

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// oldNames is every Appendix A old name, as spelled in text and in Go. A text
// hit needs an exact name: no letter, digit or underscore beside it
// (--claude-args hits claude-args). A Go name hits when it contains one.
var oldNames = []string{
	"claude_instance_id", "claude-instance-id", "claude_args", "claude-args", "claude_session_id",
	"disable_askuserquestion", "ClaudeInstanceID", "ClaudeSessionID", "ClaudeArgs", "DisableAskUserQuestion",
	"claudeInstanceID", "claudeSessionID", "claudeArgs", "WithRawClaudeArgs",
}

// oldNameTextRules are the places where an old name may remain (SR-4.5) that
// the text scan reaches. The
// refusal tables outside them (internal/oldnames, the config and template
// loaders' refusals) and the store's column-rename hop are on no scanned
// surface; trail lines written before the upgrade and the CHANGELOG are not
// scanned either.
func oldNameTextRules() *scanRules {
	return newScanRules(
		scanException{file: "docs/architecture.md", heading: "Store upgrade to agent-generic names",
			reason: "SR-4.5: the store upgrade's description names the columns it renames"},
		scanException{file: "pkg/ts-bun-client/src/internal/argv.ts", block: "const OLD_PARAM_KEYS",
			reason: "SR-4.3, SR-4.5: the client's old-key refusal table"},
	)
}

// oldNamesIn returns each old name that occurs in line as an exact name.
func oldNamesIn(line string) []string {
	var found []string
	word := func(r byte) bool { return r == '_' || unicode.IsLetter(rune(r)) || unicode.IsDigit(rune(r)) }
	for _, name := range oldNames {
		for from := 0; ; {
			i := strings.Index(line[from:], name)
			if i < 0 {
				break
			}
			start, end := from+i, from+i+len(name)
			if (start == 0 || !word(line[start-1])) && (end == len(line) || !word(line[end])) {
				found = append(found, "old name "+name)
				break
			}
			from = end
		}
	}
	return found
}

// builtText is v as indented JSON, one field per line, named name.
func builtText(t *testing.T, name string, v any) scanSource {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", name, err)
	}
	return scanSource{name: name, text: string(data)}
}

// TestOldNamesAbsentFromTextSurfaces: help, the admin verb list, the manifest,
// the user docs and package READMEs, the install skill's SKILL.md (every line,
// no exception) and the TypeScript client's sources name no old name outside
// the exceptions.
func TestOldNamesAbsentFromTextSurfaces(t *testing.T) {
	t.Parallel()
	help, err := api.Help()
	if err != nil {
		t.Fatalf("api.Help: %v", err)
	}
	srcs := []scanSource{builtText(t, "api.Help()", help), builtText(t, "adminapi.Verbs", adminapi.Verbs),
		repoFile(t, "pkg/api/manifest/surface.json"), repoFile(t, "README.md"),
		repoFile(t, "pkg/api/README.md"), repoFile(t, "pkg/ts-bun-client/README.md"),
		repoFile(t, installSkillMD)}
	srcs = append(srcs, repoGlob(t, "docs/*.md", func(rel string) bool { return !strings.HasSuffix(rel, "-guide.md") })...)
	srcs = append(srcs, repoTree(t, "pkg/ts-bun-client/src")...)
	rules := oldNameTextRules()

	for _, hit := range scanText(t, srcs, rules, oldNamesIn) {
		t.Error(hit)
	}
	rules.requireAllHit(t)
}

// TestOldNamesAbsentFromGoSurface: no exported identifier, struct field name,
// json or toml tag, or parameter name of an exported function or method names
// an old name, in pkg/api and its subpackages, adminapi's exported functions
// and the internal types pkg/api aliases.
func TestOldNamesAbsentFromGoSurface(t *testing.T) {
	t.Parallel()
	names := goDirNames(t, "pkg/api", true, goScope{all: true})
	names = append(names, goDirNames(t, "internal/adminapi", false, goScope{funcs: true})...)
	for dir, types := range aliasedInternalTypes(t, "pkg/api") {
		names = append(names, goDirNames(t, dir, false, goScope{types: types})...)
	}
	rules := newScanRules()

	for _, n := range names {
		if rules.goExempt(n.file, n.path) {
			continue
		}
		for _, old := range oldNames {
			if strings.Contains(n.text, old) {
				t.Errorf("%s:%d: %s %s names old name %s", n.file, n.line, n.kind, n.path, old)
			}
		}
	}
	rules.requireAllHit(t)
}

// TestOldNamesMatcher: an old name matches as an exact name; Claude Code's own
// names and longer names that contain one (claude_path, ClaudePath,
// claude_args_extra) do not.
func TestOldNamesMatcher(t *testing.T) {
	t.Parallel()
	for line, want := range map[string]int{
		"claude_args = []":                   1,
		"--claude-args":                      1,
		"`claude_session_id`":                1,
		"disable_askuserquestion":            1,
		"[launch] claude_path = \"/x\"":      0,
		"ClaudePath":                         0,
		"CLAUDE_CONFIG_DIR and .claude.json": 0,
		"claude_args_extra":                  0,
		"my_claude_args":                     0,
	} {
		if got := len(oldNamesIn(line)); got != want {
			t.Errorf("oldNamesIn(%q) = %d hits; want %d", line, got, want)
		}
	}
}
