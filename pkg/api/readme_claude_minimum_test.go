package api_test

// readme_claude_minimum_test.go pins the Claude Code version the user docs
// state to the version the project verified (b.xmn): the README and install.md
// name the lowest version the launch options were verified against, and
// hooks.md's idle-prompt Notification limit covers the versions from that
// minimum up to the release that fixed it. The verified minimum is read from
// the doc comment of internal/spawn/claudeoptions.go's claudeOptionTable, the
// one place the source records it, so a raise there fails these tests until the
// docs follow. TestReadmeTimingValuesAgentMinimum checks the two minimum
// statements agree and equal it (through claudeCodeVerifiedMinimum); this file
// checks the idle-prompt limit. Run with -run TestReadmeClaudeCode.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strings"
	"testing"
)

const (
	// claudeCodeIdlePromptFixed is the first Claude Code release whose
	// idle-prompt Notification waits for background subagents to finish.
	claudeCodeIdlePromptFixed = "2.1.288"
	// claudeCodeIdlePromptLastAffected is the release before it.
	claudeCodeIdlePromptLastAffected = "2.1.287"

	mdHooks         = "../../docs/hooks.md"
	idlePromptTitle = "The idle-prompt Notification"

	// srcClaudeOptions is the file whose claudeOptionTable doc comment states
	// the verified Claude Code minimum.
	srcClaudeOptions = "../../internal/spawn/claudeoptions.go"
	claudeOptionVar  = "claudeOptionTable"
)

// claudeCodeVerifiedMinimumRe finds the verified minimum in the doc comment:
// "...verified independently at minimum 2.1.280."
var claudeCodeVerifiedMinimumRe = regexp.MustCompile(`verified independently at minimum ([0-9]+\.[0-9]+\.[0-9]+)`)

// claudeCodeVerifiedMinimum returns the lowest Claude Code version the launch
// options were verified against, read from the doc comment of claudeOptionTable
// in internal/spawn/claudeoptions.go. It fails the test when the declaration,
// its doc comment or the statement is missing, so a reworded comment is not
// silently skipped.
func claudeCodeVerifiedMinimum(t *testing.T) string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), srcClaudeOptions, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", srcClaudeOptions, err)
	}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, s := range g.Specs {
			v, ok := s.(*ast.ValueSpec)
			if !ok || len(v.Names) != 1 || v.Names[0].Name != claudeOptionVar {
				continue
			}
			doc := v.Doc
			if doc == nil {
				doc = g.Doc
			}
			if doc == nil {
				t.Fatalf("%s: %s has no doc comment", srcClaudeOptions, claudeOptionVar)
			}
			text := strings.Join(strings.Fields(doc.Text()), " ")
			m := claudeCodeVerifiedMinimumRe.FindStringSubmatch(text)
			if m == nil {
				t.Fatalf("%s: the doc comment of %s does not match %q: %q", srcClaudeOptions, claudeOptionVar, claudeCodeVerifiedMinimumRe, text)
			}
			return m[1]
		}
	}
	t.Fatalf("%s declares no %s", srcClaudeOptions, claudeOptionVar)
	return ""
}

// TestReadmeClaudeCodeIdlePromptLimit: hooks.md's idle-prompt Notification
// section states the limit for the versions the minimum admits: the
// Notification can fire while a background subagent runs on the minimum
// through the last affected release, and later releases wait. The intro
// paragraph names the first release that waits and points at "Limits".
func TestReadmeClaudeCodeIdlePromptLimit(t *testing.T) {
	t.Parallel()
	minimum := claudeCodeVerifiedMinimum(t)
	body := normalised(sectionBody(t, readMD(t, mdHooks), idlePromptTitle))
	for _, c := range []struct{ name, pattern string }{
		{"intro names the first release that waits",
			`Before Claude Code ` + regexp.QuoteMeta(claudeCodeIdlePromptFixed) + ` it can fire while a background subagent is still running`},
		{"limit covers the minimum through the last affected release",
			`Claude Code ` + regexp.QuoteMeta(minimum) + ` through ` + regexp.QuoteMeta(claudeCodeIdlePromptLastAffected) +
				`, the idle-prompt Notification can fire while a background subagent is still running`},
		{"limit says later releases wait",
			regexp.QuoteMeta(claudeCodeIdlePromptFixed) + ` and later wait until background subagents finish`},
		{"limit says the row reads waiting until the subagent's next tool hook",
			`the row reads ` + "`waiting`" + `.*until the subagent's next tool hook`},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !regexp.MustCompile(c.pattern).MatchString(body) {
				t.Errorf("%s section %q does not match %q", mdHooks, idlePromptTitle, c.pattern)
			}
		})
	}
	if regexp.MustCompile(`The idle-prompt Notification waits until background subagents finish\.`).MatchString(body) {
		t.Errorf("%s section %q states the idle-prompt Notification always waits for background subagents; it does not before %s",
			mdHooks, idlePromptTitle, claudeCodeIdlePromptFixed)
	}
}
