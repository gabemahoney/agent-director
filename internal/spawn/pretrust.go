package spawn

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// claudeJSONPath returns the default $HOME/.claude.json path. Held as a var
// so tests can swap it for a temp file without monkey-patching os.UserHomeDir.
// preTrustCwd uses this only when the spawn's ExtraEnv does not contain
// CLAUDE_CONFIG_DIR.
var claudeJSONPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// ErrClaudeJSONMissing is the sentinel surfaced when the resolved .claude.json
// does not exist at pre-trust time. It is intentionally NOT in the §13.1
// error catalog — preTrustCwd swallows it (with a warning to the
// recovery logger) since the trust dialog is unavoidable on a truly-
// fresh machine and we don't want to block the spawn on it.
var ErrClaudeJSONMissing = errors.New("ErrClaudeJSONMissing")

// preTrustCwd flips the spawn's .claude.json projects[<cwd>].hasTrustDialogAccepted
// to true so the spawned Claude Code skips its workspace-trust dialog.
//
// The target file is resolved from extraEnv with this precedence (bug b.18k):
//   - If extraEnv["CLAUDE_CONFIG_DIR"] is non-empty → <CLAUDE_CONFIG_DIR>/.claude.json
//   - Otherwise → $HOME/.claude.json (via claudeJSONPath, stubbed by tests)
//
// Behavior (per bug b.f75):
//
//   - Read the entire file, mutate the projects map, write the entire
//     file via temp+rename. The window for a torn write against the
//     operator's own Claude Code is small but real; last-writer wins
//     is acceptable per the bug's concurrency note.
//   - If the file does not exist (truly-fresh Claude Code install, or a
//     fresh CLAUDE_CONFIG_DIR), return ErrClaudeJSONMissing — Launch
//     swallows that with a warn. The spawn will block on the trust dialog
//     as before. Not our problem to materialize the file out of thin air.
//   - Only the single key hasTrustDialogAccepted is set. We don't touch
//     hasCompletedProjectOnboarding or any other workspace-init keys
//     because those have semantics beyond trust.
//   - cwd must be the canonical absolute path the spawn will be
//     launched in — i.e. r.CWD after Validate's EvalSymlinks.
//
// The function preserves unknown top-level keys and unknown per-project
// keys verbatim via the json.RawMessage typed map. A future Claude Code
// release adding a new key under .projects.<path> will round-trip safely.
func preTrustCwd(cwd string, extraEnv map[string]string) error {
	var path string
	if dir := extraEnv["CLAUDE_CONFIG_DIR"]; dir != "" {
		path = filepath.Join(dir, ".claude.json")
	} else {
		var err error
		path, err = claudeJSONPath()
		if err != nil {
			return fmt.Errorf("pre-trust: resolve home: %w", err)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrClaudeJSONMissing, path)
		}
		return fmt.Errorf("pre-trust: read %s: %w", path, err)
	}

	// Decode into a permissive shape: top-level keys are kept as
	// json.RawMessage so we don't have to enumerate Claude Code's full
	// schema. projects is the only key we actually mutate.
	top := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &top); err != nil {
			return fmt.Errorf("pre-trust: parse %s: %w", path, err)
		}
	}

	projects := map[string]map[string]json.RawMessage{}
	if pj, ok := top["projects"]; ok && len(pj) > 0 {
		if err := json.Unmarshal(pj, &projects); err != nil {
			return fmt.Errorf("pre-trust: parse projects: %w", err)
		}
	}

	entry := projects[cwd]
	if entry == nil {
		entry = map[string]json.RawMessage{}
	}
	entry["hasTrustDialogAccepted"] = json.RawMessage("true")
	projects[cwd] = entry

	pjOut, err := json.Marshal(projects)
	if err != nil {
		return fmt.Errorf("pre-trust: marshal projects: %w", err)
	}
	top["projects"] = pjOut

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return fmt.Errorf("pre-trust: marshal top: %w", err)
	}

	return writeFileAtomic(path, out, 0o600)
}

// writeFileAtomic writes data to path via a temp file in the same
// directory and os.Rename. The temp file is created with O_EXCL and a
// random suffix so concurrent writers don't clobber each other's temp
// files; on Linux rename(2) is atomic within a filesystem, so a reader
// either sees the old contents or the new contents but never a torn
// write.
//
// On error the temp file is removed best-effort. Mode is the
// permissions of the temp file *before* rename — since we read+rewrite
// an operator-owned file, 0o600 is a sane default that matches Claude
// Code's own permissions on ~/.claude.json (per inspection on the
// smoke-test VM).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

