package api_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestREADMELinksResolve parses pkg/api/README.md, extracts every relative
// Markdown link target, and asserts each one resolves to an existing path.
// External http(s) URLs and anchor-only fragments (#section) are skipped.
// Tests run from the package directory (pkg/api/), so relative links are
// resolved from there.
func TestREADMELinksResolve(t *testing.T) {
	data, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("could not read README.md: %v", err)
	}

	// Match inline Markdown links: [link text](target)
	// Does not match reference-style links — none are present in this README.
	linkRe := regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

	matches := linkRe.FindAllSubmatch(data, -1)
	if len(matches) == 0 {
		t.Fatal("no Markdown links found in README.md — regex may be broken")
	}

	for _, m := range matches {
		text := string(m[1])
		target := string(m[2])

		// Strip any trailing fragment (#anchor) before existence check.
		if idx := strings.Index(target, "#"); idx != -1 {
			target = target[:idx]
		}

		// Skip pure anchors (empty after stripping fragment).
		if target == "" {
			continue
		}

		// Skip external URLs.
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			continue
		}

		if _, err := os.Stat(target); os.IsNotExist(err) {
			t.Errorf("link [%s](%s): target %q does not exist (resolved from pkg/api/)", text, target, target)
		} else if err != nil {
			t.Errorf("link [%s](%s): stat(%q) error: %v", text, target, target, err)
		}
	}
}
