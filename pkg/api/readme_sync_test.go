package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestREADMEExamplesStayInSync: each ExampleClient_<Verb> function's region
// between "// README:start <func>" and "// README:end" (example_test.go)
// matches the first ```go block under the "### <Verb>" heading of
// pkg/api/README.md (normalizeCodeBlock); every block has a region and every
// ExampleClient_ function a block. Update both together.
func TestREADMEExamplesStayInSync(t *testing.T) {
	t.Parallel()
	blocks, regions := readmeGoBlocks(t, "README.md"), exampleLabeledRegions(t, "example_test.go")
	if len(blocks) == 0 || len(regions) == 0 {
		t.Fatalf("README.md has %d Go blocks under ### headings, example_test.go %d ExampleClient_ functions; want both (a parser is broken)",
			len(blocks), len(regions))
	}
	for fn, block := range blocks {
		region, ok := regions[fn]
		if !ok {
			t.Errorf("README has a Go block under ### %s but example_test.go has no // README:start %s marker",
				strings.TrimPrefix(fn, "ExampleClient_"), fn)
		} else if b, r := normalizeCodeBlock(block), normalizeCodeBlock(region); b != r {
			t.Errorf("README block for %s diverges from example_test.go labeled region:\n--- README\n%s\n+++ example_test.go\n%s", fn, b, r)
		}
	}
	for fn := range regions {
		if _, ok := blocks[fn]; !ok {
			t.Errorf("example_test.go has %s but README.md has no Go block under ### %s", fn, strings.TrimPrefix(fn, "ExampleClient_"))
		}
	}
}

// readmeGoBlocks maps ExampleClient_<Verb> to the first ```go block after
// each "### <Verb>" heading of filename (spaces dropped from the verb).
func readmeGoBlocks(t *testing.T, filename string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	blocks := map[string]string{}
	heading, in := "", false
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "### "):
			heading = strings.TrimSpace(strings.TrimPrefix(line, "### "))
		case line == "```go" && heading != "" && !in:
			in, lines = true, nil
		case line == "```" && in:
			in = false
			blocks["ExampleClient_"+strings.ReplaceAll(heading, " ", "")] = strings.Join(lines, "\n")
			heading = "" // one block per verb
		case in:
			lines = append(lines, line)
		}
	}
	return blocks
}

// exampleLabeledRegions maps each ExampleClient_ function of filename to the
// source lines strictly between its body's "// README:start <func>" and
// "// README:end" comments ("" when it has no start marker).
func exampleLabeledRegions(t *testing.T, filename string) map[string]string {
	t.Helper()
	src, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}
	lines := strings.Split(string(src), "\n")
	regions := map[string]string{}
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || !strings.HasPrefix(fd.Name.Name, "ExampleClient_") {
			continue
		}
		from, to := fset.Position(fd.Body.Lbrace).Line, fset.Position(fd.Body.Rbrace).Line
		start, end := 0, 0
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				line, text := fset.Position(c.Pos()).Line, strings.TrimSpace(c.Text)
				if line <= from || line >= to {
					continue
				}
				if name, ok := strings.CutPrefix(text, "// README:start "); ok && strings.TrimSpace(name) == fd.Name.Name {
					start = line
				} else if text == "// README:end" && start > 0 {
					end = line
				}
			}
		}
		switch {
		case start == 0:
			regions[fd.Name.Name] = ""
		case end == 0:
			t.Fatalf("%s: // README:start %s on line %d has no // README:end in the function body", filename, fd.Name.Name, start)
		default:
			regions[fd.Name.Name] = strings.Join(lines[start:end-1], "\n")
		}
	}
	return regions
}

// normalizeCodeBlock expands leading tabs to 4 spaces, strips the common
// leading whitespace of the non-blank lines and each line's trailing
// whitespace, and drops trailing newlines, so a space-indented README block
// and tab-indented Go source compare equal.
func normalizeCodeBlock(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	indent := -1
	for i, l := range lines {
		tabs := len(l) - len(strings.TrimLeft(l, "\t"))
		lines[i] = strings.Repeat("    ", tabs) + l[tabs:]
		if n := len(lines[i]) - len(strings.TrimLeft(lines[i], "\t ")); strings.TrimSpace(lines[i]) != "" && (indent < 0 || n < indent) {
			indent = n
		}
	}
	for i, l := range lines {
		if len(l) >= indent && indent > 0 {
			l = l[indent:]
		}
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
