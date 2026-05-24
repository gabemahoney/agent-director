package api_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestREADMEExamplesStayInSync asserts that each ExampleClient_* function's
// labeled region (delimited by // README:start <name> / // README:end comments)
// matches the corresponding Go code block in pkg/api/README.md.
//
// Structural match contract:
//
//   - Each ExampleClient_* function body contains exactly one
//     // README:start <name> / // README:end pair.
//   - The README has one Go (```go) fenced block per ### <Verb> section.
//   - The heading name maps to the function: ### SendKeys → ExampleClient_SendKeys.
//   - Comparison strips common leading whitespace from both sides and
//     normalizes trailing whitespace per line; the // Output: block is excluded
//     from the labeled region and therefore never appears in the comparison.
//
// When the test fails, update both the labeled region and the README block
// together so they stay in sync.
func TestREADMEExamplesStayInSync(t *testing.T) {
	readmeBlocks, err := readmeGoBlocks("README.md")
	if err != nil {
		t.Fatalf("parse README: %v", err)
	}
	if len(readmeBlocks) == 0 {
		t.Fatal("no Go code blocks found under ### headings in README.md — parser may be broken")
	}

	exampleRegions, err := exampleLabeledRegions("example_test.go")
	if err != nil {
		t.Fatalf("parse example_test.go: %v", err)
	}
	if len(exampleRegions) == 0 {
		t.Fatal("no // README:start markers found in example_test.go — labeling may be missing")
	}

	norm := normalizeCodeBlock

	// Every README Go block must have a matching labeled region.
	for fnName, readmeBlock := range readmeBlocks {
		region, ok := exampleRegions[fnName]
		if !ok {
			t.Errorf("README has a Go block for %q (under ### %s) but example_test.go has no // README:start %s marker",
				fnName, verbFromFuncName(fnName), fnName)
			continue
		}
		if norm(readmeBlock) != norm(region) {
			t.Errorf("README block for %s diverges from example_test.go labeled region:\n--- README\n%s\n+++ example_test.go\n%s",
				fnName, norm(readmeBlock), norm(region))
		}
	}

	// Every labeled region must have a matching README block.
	for fnName := range exampleRegions {
		if _, ok := readmeBlocks[fnName]; !ok {
			t.Errorf("example_test.go has // README:start %s but README.md has no Go block under ### %s",
				fnName, verbFromFuncName(fnName))
		}
	}
}

// ── README parser ─────────────────────────────────────────────────────────────

// readmeGoBlocks extracts the Go fenced code blocks from the README, keyed by
// ExampleClient_* function names derived from the preceding ### <Verb> heading.
// Only H3 (###) headings trigger capture; H2 (##) sections are ignored.
// Each heading resets after its first Go block so only one block per verb is
// captured.
func readmeGoBlocks(filename string) (map[string]string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	blocks := make(map[string]string)
	var currentHeading string
	inGoBlock := false
	var blockLines []string

	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "### "):
			currentHeading = strings.TrimSpace(strings.TrimPrefix(line, "### "))

		case line == "```go" && currentHeading != "" && !inGoBlock:
			inGoBlock = true
			blockLines = nil

		case line == "```" && inGoBlock:
			inGoBlock = false
			fnName := "ExampleClient_" + strings.ReplaceAll(currentHeading, " ", "")
			blocks[fnName] = strings.Join(blockLines, "\n")
			currentHeading = "" // consume heading; one Go block per verb

		default:
			if inGoBlock {
				blockLines = append(blockLines, line)
			}
		}
	}
	return blocks, nil
}

// ── example_test.go parser ───────────────────────────────────────────────────

// exampleLabeledRegions uses go/parser to locate every ExampleClient_*
// function in filename and extracts the raw source lines between each
// function's // README:start <name> and // README:end comment markers.
//
// The AST provides structural correctness (markers must be inside a named
// ExampleClient_* function body); the raw source bytes are used for faithful
// text reproduction.
func exampleLabeledRegions(filename string) (map[string]string, error) {
	src, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filename, err)
	}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	// srcLines is 0-indexed: srcLines[n-1] is the text of line n.
	srcLines := strings.Split(string(src), "\n")

	regions := make(map[string]string)

	for _, decl := range f.Decls {
		fdecl, ok := decl.(*ast.FuncDecl)
		if !ok || fdecl.Body == nil {
			continue
		}
		fnName := fdecl.Name.Name
		if !strings.HasPrefix(fnName, "ExampleClient_") {
			continue
		}

		bodyStart := fset.Position(fdecl.Body.Lbrace).Line
		bodyEnd := fset.Position(fdecl.Body.Rbrace).Line

		var startLine, endLine int

		for _, cg := range f.Comments {
			for _, c := range cg.List {
				cLine := fset.Position(c.Pos()).Line
				// Only consider comments inside this function body.
				if cLine <= bodyStart || cLine >= bodyEnd {
					continue
				}
				text := strings.TrimSpace(c.Text)
				if strings.HasPrefix(text, "// README:start ") {
					name := strings.TrimSpace(strings.TrimPrefix(text, "// README:start "))
					if name == fnName {
						startLine = cLine
					}
				} else if text == "// README:end" && startLine > 0 {
					endLine = cLine
				}
			}
		}

		if startLine == 0 {
			// Missing // README:start marker — report as empty so the diff
			// test reports a "no region" failure rather than a silent skip.
			regions[fnName] = ""
			continue
		}
		if endLine == 0 {
			return nil, fmt.Errorf("%s: found // README:start %s on line %d but no matching // README:end in function body",
				filename, fnName, startLine)
		}

		// Extract lines strictly between the two marker lines (exclusive).
		// srcLines is 0-indexed; line N → srcLines[N-1].
		if endLine > startLine+1 {
			lines := srcLines[startLine : endLine-1] // lines after start, before end
			regions[fnName] = strings.Join(lines, "\n")
		} else {
			regions[fnName] = ""
		}
	}
	return regions, nil
}

// ── normalisation helpers ─────────────────────────────────────────────────────

// normalizeCodeBlock strips the common leading whitespace from every non-blank
// line, trims trailing whitespace from each line, and drops leading/trailing
// blank lines. Leading tabs are expanded to 4 spaces before comparison so that
// README blocks (conventionally space-indented) and Go source (tab-indented)
// compare equal. The result is a canonical form suitable for textual comparison
// of code snippets regardless of their original indentation style or level.
func normalizeCodeBlock(s string) string {
	s = strings.TrimRight(s, "\n")
	lines := strings.Split(s, "\n")

	// Expand leading tabs to 4 spaces so that space-indented and
	// tab-indented blocks produce identical canonical forms.
	for i, l := range lines {
		lines[i] = expandLeadingTabs(l)
	}

	// Compute minimum leading-whitespace count across non-blank lines.
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, "\t "))
		if minIndent < 0 || n < minIndent {
			minIndent = n
		}
	}
	if minIndent < 0 {
		minIndent = 0
	}

	result := make([]string, 0, len(lines))
	for _, l := range lines {
		if len(l) >= minIndent {
			l = l[minIndent:]
		}
		result = append(result, strings.TrimRight(l, " \t"))
	}
	return strings.TrimRight(strings.Join(result, "\n"), "\n")
}

// expandLeadingTabs replaces each leading tab character with 4 spaces.
// Only leading tabs (before any non-whitespace character) are expanded;
// tabs elsewhere in the line are left untouched.
func expandLeadingTabs(s string) string {
	i := 0
	for i < len(s) && s[i] == '\t' {
		i++
	}
	if i == 0 {
		return s
	}
	return strings.Repeat("    ", i) + s[i:]
}

// verbFromFuncName strips the "ExampleClient_" prefix to get the verb name,
// used in error messages.  Returns the input unchanged if the prefix is absent.
func verbFromFuncName(fnName string) string {
	const prefix = "ExampleClient_"
	if strings.HasPrefix(fnName, prefix) {
		return fnName[len(prefix):]
	}
	return fnName
}
