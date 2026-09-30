// success_pretrust.go holds the pre-trust fixture and check the spawn and
// resume success cases use (SR-22.6 "The field"). Both launch verbs report
// pre_trust; with no .claude.json in a run's HOME both sides would compare
// "failed", so each run's home gets the same .claude.json lacking the
// folder-trust entry and both sides must report "ok".
package envelope_diff

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// plantClaudeJSON is an extraSetup hook: it writes a .claude.json with no
// folder-trust entries into homeDir, the file pre-trust targets when the
// launch's extra env does not set CLAUDE_CONFIG_DIR.
func plantClaudeJSON(t *testing.T, homeDir string, _ map[string]any) {
	t.Helper()
	path := filepath.Join(homeDir, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"projects":{}}`), 0o600); err != nil {
		t.Fatalf("plantClaudeJSON: write %s: %v", path, err)
	}
}

// preTrustMismatch returns nil when the success envelope carries pre_trust
// equal to want, and an error naming what it found otherwise (the field
// missing, not a string, or another value). The envelope diff alone cannot
// catch a field that disappears from both sides.
func preTrustMismatch(envelope []byte, want string) error {
	var m map[string]any
	if err := json.Unmarshal(envelope, &m); err != nil {
		return fmt.Errorf("unmarshal envelope: %w", err)
	}
	raw, ok := m["pre_trust"]
	if !ok {
		return fmt.Errorf("pre_trust missing; want %q", want)
	}
	got, ok := raw.(string)
	if !ok {
		return fmt.Errorf("pre_trust = %v (%T); want string %q", raw, raw, want)
	}
	if got != want {
		return fmt.Errorf("pre_trust = %q; want %q", got, want)
	}
	return nil
}
