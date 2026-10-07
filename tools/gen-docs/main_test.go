package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/adminapi"
	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// generateDocs runs generate into a fresh temp root, after prep (if any) has
// seeded its docs dir, and returns that docs dir.
func generateDocs(t *testing.T, prep func(docs string)) string {
	t.Helper()
	docs := filepath.Join(t.TempDir(), "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if prep != nil {
		prep(docs)
	}
	if err := generate(filepath.Dir(docs)); err != nil {
		t.Fatalf("generate: %v", err)
	}
	return docs
}

// readDoc returns path's content.
func readDoc(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// retiredPendingConcepts matches the concepts SR-20.6 retires, in hyphen and
// space spellings (twin: pkg/api/manifest/pending_meaning_test.go).
var retiredPendingConcepts = regexp.MustCompile(`(?i)resume[-\s]?starting|young[-\s]claim|launched[-\s]mark|claim[-\s]withdrawal`)

// TestGenerate_References: each agent reference opens with the do-not-edit
// header, ends in exactly one newline, carries its format's sections and the
// manifest's texts (help's, and the pending texts of SR-20.6, AC-DOC-17)
// with no retired concept, and a second run writes the same bytes (the
// contract the CI drift gate relies on).
func TestGenerate_References(t *testing.T) {
	docs := generateDocs(t, nil)
	help, _ := manifest.Lookup("help")
	status, _ := manifest.Lookup("status")
	spawn, _ := manifest.Lookup("spawn")
	var stateText string
	for _, f := range status.ResultFields {
		if f.Name == "state" {
			stateText = f.Description
		}
	}
	if len(help.ResultFields) == 0 || !strings.Contains(stateText, "has not reported in") {
		t.Fatalf("preconditions: help has %d result fields; status state text %q", len(help.ResultFields), stateText)
	}
	texts := []string{"help", help.Description, help.ResultFields[0].Name, status.Description, stateText, spawn.Description}
	first := map[string]string{}
	for name, marks := range map[string][]string{
		"cli-reference.md": {"# CLI reference", "### Parameters", "### Result", "### Errors"},
		"mcp-reference.md": {"# MCP reference", "Tool:", "### Input schema", "### Output schema"},
	} {
		got := readDoc(t, filepath.Join(docs, name))
		first[name] = got
		if !strings.HasPrefix(got, genHeader+"\n") {
			t.Errorf("%s: missing or misplaced do-not-edit header", name)
		}
		if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
			t.Errorf("%s: want exactly one trailing newline", name)
		}
		for _, want := range append(marks, texts...) {
			mustContain(t, name, got, want)
		}
		for i, line := range strings.Split(got, "\n") {
			if m := retiredPendingConcepts.FindString(line); m != "" {
				t.Errorf("%s:%d names retired concept %q: %s", name, i+1, m, line)
			}
		}
	}
	if err := generate(filepath.Dir(docs)); err != nil {
		t.Fatalf("generate#2: %v", err)
	}
	for name, got := range first {
		if readDoc(t, filepath.Join(docs, name)) != got {
			t.Errorf("%s not byte-identical across two runs", name)
		}
	}
}

// TestGenerate_MCPRespectsExposedVerb guards b.gk8: mcp-reference.md has a
// tool section for exactly the verbs mcp.ExposedVerb admits (never hook,
// serve or trail-emit).
func TestGenerate_MCPRespectsExposedVerb(t *testing.T) {
	got := readDoc(t, filepath.Join(generateDocs(t, nil), "mcp-reference.md"))
	seen := map[bool]bool{}
	for _, v := range manifest.Verbs {
		exposed := mcp.ExposedVerb(v.Name)
		seen[exposed] = true
		if strings.Contains(got, "## Tool: "+v.Name+"\n") != exposed {
			t.Errorf("mcp-reference.md: tool section for %q present = %t; want %t (ExposedVerb)", v.Name, !exposed, exposed)
		}
	}
	if !seen[true] || !seen[false] {
		t.Fatal("the manifest needs both an exposed and an excluded verb for this guard (precondition)")
	}
}

// TestGenerate_OperatorActionsAbsent: neither generated nor committed agent
// reference names the kill opt-in, the admin binary or its kill-finished verb,
// or has a delete section (SR-6.8, b.vqr), and each still documents kill with
// kill_sent.
func TestGenerate_OperatorActionsAbsent(t *testing.T) {
	sections := map[string]string{"cli-reference.md": "## ", "mcp-reference.md": "## Tool: "}
	for _, dir := range []string{generateDocs(t, nil), filepath.Join("..", "..", "docs")} {
		for name, prefix := range sections {
			path := filepath.Join(dir, name)
			t.Run(path, func(t *testing.T) {
				got := readDoc(t, path)
				for i, line := range strings.Split(got, "\n") {
					if m := apitest.OperatorActionNames.FindString(line); m != "" {
						t.Errorf("%s:%d names %q; nothing shown to agents may name the kill opt-in or agent-director-admin: %s", path, i+1, m, line)
					}
					if line == prefix+"delete" {
						t.Errorf("%s:%d has a delete section; delete is an agent-director-admin verb only", path, i+1)
					}
				}
				_, section, ok := strings.Cut(got, prefix+"kill\n")
				if !ok {
					t.Fatalf("%s has no %q section; the absence checks would pass vacuously", path, prefix+"kill")
				}
				section, _, _ = strings.Cut(section, "\n## ")
				mustContain(t, path+" kill section", section, "kill_sent")
			})
		}
	}
}

// TestGenerate_AdminReference: the generated and the committed
// docs/admin-reference.md open, after the do-not-edit header, with the
// human-approval statement and document the global flags and every
// agent-director-admin verb.
func TestGenerate_AdminReference(t *testing.T) {
	if len(adminapi.Verbs) == 0 || len(adminapi.GlobalFlags) == 0 {
		t.Fatal("adminapi.Verbs or adminapi.GlobalFlags is empty; the per-item checks would pass vacuously")
	}
	for _, path := range []string{filepath.Join(generateDocs(t, nil), "admin-reference.md"), filepath.Join("..", "..", "docs", "admin-reference.md")} {
		t.Run(path, func(t *testing.T) {
			raw := readDoc(t, path)
			lines := strings.Split(raw, "\n")
			if lines[0] != genHeader {
				t.Errorf("%s: first line = %q; want the do-not-edit header", path, lines[0])
			}
			var content []string
			for _, l := range lines[1:] {
				if l != "" {
					content = append(content, l)
				}
			}
			if len(content) == 0 || content[0] != adminapi.ApprovalStatement {
				t.Errorf("%s: first content line = %q; want the human-approval statement %q", path, content, adminapi.ApprovalStatement)
			}
			mustContain(t, path, raw, "\n## Global flags\n")
			for _, f := range adminapi.GlobalFlags {
				mustContain(t, path, raw, "\n- `"+f.Name+"`: "+f.Description+"\n")
			}
			for _, v := range adminapi.Verbs {
				mustContain(t, path, raw, "\n## "+v.Name+"\n")
			}
		})
	}
}

// TestGenerate_OverwritesManualEdit: a hand edit to a generated doc is
// clobbered by the next generate run.
func TestGenerate_OverwritesManualEdit(t *testing.T) {
	docs := generateDocs(t, func(docs string) {
		if err := os.WriteFile(filepath.Join(docs, "cli-reference.md"), []byte("HAND EDITED CONTENT\n"), 0o644); err != nil {
			t.Fatalf("write stub: %v", err)
		}
	})
	if strings.Contains(readDoc(t, filepath.Join(docs, "cli-reference.md")), "HAND EDITED CONTENT") {
		t.Error("generate did not overwrite hand-edited content")
	}
}

func mustContain(t *testing.T, label, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("%s: missing expected substring %q", label, needle)
	}
}
