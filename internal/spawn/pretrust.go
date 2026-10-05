package spawn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PreTrustOutcome is what one pre-trust step did for a launch (SR-22.6 "The
// field"). Its values are the words of the public pre_trust result field.
type PreTrustOutcome string

const (
	// PreTrustOK: the folder-trust entry was written, so Claude Code should
	// not show its folder-trust prompt for the directory.
	PreTrustOK PreTrustOutcome = "ok"
	// PreTrustSkipped: pre-trust was off for this launch (the spawn caller's
	// opt-out, or for resume the opt-out the row records), so nothing was
	// attempted.
	PreTrustSkipped PreTrustOutcome = "skipped"
	// PreTrustFailed: pre-trust was attempted and did not write the entry
	// (the .claude.json file is missing, or could not be read, parsed or
	// written, its lock held by another process included). The launch
	// proceeds; the agent may stop at the prompt.
	PreTrustFailed PreTrustOutcome = "failed"
)

// preTrustWarn is where PreTrust prints its one warning line for a failed
// attempt, for humans at the CLI. Held as a var so tests can capture it
// without touching os.Stderr (which the test harness drains for the JSON
// error envelope).
var preTrustWarn io.Writer = os.Stderr

// PreTrust is the one best-effort folder-trust pre-trust step every launch
// runs (SR-22.6): plain spawn (Launch) before its insert, and resume before
// its move to pending. off is whether pre-trust is off for this launch: the
// spawn caller's NoPreTrust, or for resume the row's recorded NoPreTrust.
//
// When off is true it attempts nothing, opens and creates no file, prints
// nothing and returns PreTrustSkipped. Otherwise it runs preTrustCwd for cwd,
// resolving the target .claude.json from extraEnv (CLAUDE_CONFIG_DIR first,
// then $HOME), and returns PreTrustOK when the entry was written. Any failure,
// a missing file included, returns PreTrustFailed and prints exactly one line
// to preTrustWarn saying "pre-trust failed", naming the resolved file and
// saying the agent may stop at Claude Code's folder-trust prompt; it names no
// label, token or other environment value. PreTrust never returns an error
// and never fails a launch.
func PreTrust(cwd string, extraEnv map[string]string, off bool) PreTrustOutcome {
	if off {
		return PreTrustSkipped
	}
	err := preTrustCwd(cwd, extraEnv)
	if err == nil {
		return PreTrustOK
	}
	reason := err.Error()
	if errors.Is(err, ErrClaudeJSONMissing) {
		reason = "file does not exist"
	}
	if path, perr := claudeJSONFor(extraEnv); perr == nil {
		fmt.Fprintf(preTrustWarn, "agent-director: pre-trust failed for %s (%s); the agent may stop at Claude Code's folder-trust prompt\n", path, reason)
	} else {
		fmt.Fprintf(preTrustWarn, "agent-director: pre-trust failed (%s); the agent may stop at Claude Code's folder-trust prompt\n", reason)
	}
	return PreTrustFailed
}

// claudeJSONPath returns the default $HOME/.claude.json path. Held as a var
// so tests can swap it for a temp file without monkey-patching os.UserHomeDir.
// claudeJSONFor uses this only when the launch's extra env does not set
// CLAUDE_CONFIG_DIR.
var claudeJSONPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// claudeJSONFor resolves the .claude.json file pre-trust targets for a launch
// with extraEnv (bug b.18k): <CLAUDE_CONFIG_DIR>/.claude.json when
// extraEnv["CLAUDE_CONFIG_DIR"] is non-empty, otherwise $HOME/.claude.json
// (via claudeJSONPath, stubbed by tests).
func claudeJSONFor(extraEnv map[string]string) (string, error) {
	if dir := extraEnv["CLAUDE_CONFIG_DIR"]; dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	return claudeJSONPath()
}

// ErrClaudeJSONMissing is the sentinel preTrustCwd returns, wrapped with the
// resolved path, when the .claude.json file does not exist (a fresh Claude
// Code install or a fresh CLAUDE_CONFIG_DIR). It is intentionally NOT in the
// §13.1 error catalog: PreTrust reports it as PreTrustFailed with a
// "pre-trust failed" warning line and the launch proceeds, since the agent
// can still answer the folder-trust prompt.
var ErrClaudeJSONMissing = errors.New("ErrClaudeJSONMissing")

// preTrustCwd flips the spawn's .claude.json projects[<cwd>].hasTrustDialogAccepted
// to true so the spawned Claude Code skips its workspace-trust dialog.
//
// It is the write behind PreTrust, which turns any error it returns into
// PreTrustFailed; it never runs when pre-trust is off for the launch.
//
// The target file is resolved from extraEnv by claudeJSONFor (bug b.18k):
//   - If extraEnv["CLAUDE_CONFIG_DIR"] is non-empty → <CLAUDE_CONFIG_DIR>/.claude.json
//   - Otherwise → $HOME/.claude.json (via claudeJSONPath, stubbed by tests)
//
// Behavior (per bugs b.f75 and b.zjm):
//
//   - The read-modify-write runs under Claude Code's own lock on the file
//     (lockConfig, configlock.go): take the lock, read the entire file
//     under it, mutate the projects map, write the entire file via
//     temp+rename, release the lock. Claude Code saves the same file under
//     that lock and re-reads it under the lock before each save, so
//     neither side's update is lost to the other, and concurrent
//     agent-director pre-trusts of one file run one at a time. The wait
//     for a held lock is bounded (configLockWait); when it runs out, or
//     the lock cannot be taken at all, nothing is written and the error
//     makes PreTrust report failed. The same holds when, just before the
//     write, lock.checkHold finds the lock held too long or taken over
//     by another process.
//   - If the file does not exist (truly-fresh Claude Code install, or a
//     fresh CLAUDE_CONFIG_DIR), return ErrClaudeJSONMissing wrapped with
//     the path, before taking the lock, so neither the file nor the lock
//     dir is created; PreTrust reports that as failed and the launch
//     proceeds, so the agent may stop at the folder-trust prompt. Not our
//     problem to materialize the file out of thin air.
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
	path, err := claudeJSONFor(extraEnv)
	if err != nil {
		return fmt.Errorf("pre-trust: resolve home: %w", err)
	}

	if _, err := os.Stat(path); err != nil {
		return claudeJSONReadError(path, err)
	}
	lock, err := lockConfig(path)
	if err != nil {
		return fmt.Errorf("pre-trust: %w", err)
	}
	defer lock.unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return claudeJSONReadError(path, err)
	}
	out, err := withTrustedCwd(raw, cwd, path)
	if err != nil {
		return err
	}
	preTrustBeforeCommit()
	if err := lock.checkHold(); err != nil {
		return fmt.Errorf("pre-trust: %w", err)
	}
	return writeFileAtomic(path, out, 0o600)
}

// preTrustBeforeCommit runs in preTrustCwd under the lock, after the new
// content is built and just before lock.checkHold. It does nothing; tests
// swap it to stand in for another process breaking or taking over the lock
// at that point.
var preTrustBeforeCommit = func() {}

// claudeJSONReadError is preTrustCwd's error for a failed stat or read of the
// .claude.json file at path: ErrClaudeJSONMissing wrapped with the path when
// the file does not exist, else the read error with context.
func claudeJSONReadError(path string, err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrClaudeJSONMissing, path)
	}
	return fmt.Errorf("pre-trust: read %s: %w", path, err)
}

// withTrustedCwd returns the .claude.json content raw (read from path, named
// in errors) with projects[cwd].hasTrustDialogAccepted set to true and every
// other key kept. Empty content is treated as an empty object.
func withTrustedCwd(raw []byte, cwd, path string) ([]byte, error) {
	// Decode into a permissive shape: top-level keys are kept as
	// json.RawMessage so we don't have to enumerate Claude Code's full
	// schema. projects is the only key we actually mutate.
	top := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &top); err != nil {
			return nil, fmt.Errorf("pre-trust: parse %s: %w", path, err)
		}
	}

	projects := map[string]map[string]json.RawMessage{}
	if pj, ok := top["projects"]; ok && len(pj) > 0 {
		if err := json.Unmarshal(pj, &projects); err != nil {
			return nil, fmt.Errorf("pre-trust: parse projects: %w", err)
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
		return nil, fmt.Errorf("pre-trust: marshal projects: %w", err)
	}
	top["projects"] = pjOut

	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("pre-trust: marshal top: %w", err)
	}
	return out, nil
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
