package spawn

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
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
	// written, its lock held by another process included, or the extra
	// env's CLAUDE_CONFIG_DIR is set but not usable, see ConfigDirUsable).
	// The launch proceeds; the agent may stop at the prompt.
	PreTrustFailed PreTrustOutcome = "failed"
)

// preTrustWarn is where PreTrust prints its one warning line for a failed
// attempt, for humans at the CLI. Held as a var so tests can capture it
// without touching os.Stderr (which the test harness drains for the JSON
// error envelope).
var preTrustWarn io.Writer = os.Stderr

// PreTrust is the one best-effort folder-trust pre-trust step every launch
// runs (SR-22.6): plain spawn (Launch) before its insert, spawn with reuse
// before its reset, and resume before its move to pending. off is whether
// pre-trust is off for this launch: the spawn caller's NoPreTrust, or for
// resume the row's recorded NoPreTrust. cfg is the loaded config's
// [pre_trust] table: its effective lock_wait_seconds
// (cfg.EffectiveLockWaitSeconds, 12 s by default; b.kr4) bounds the wait for
// Claude Code's lock on the file while another process holds it, so a launch
// that finds the lock held takes at most that much longer.
//
// When off is true it attempts nothing, opens and creates no file, prints
// nothing and returns PreTrustSkipped. Otherwise it runs preTrustCwd for cwd,
// resolving the target .claude.json from extraEnv (claudeJSONFor: a usable
// CLAUDE_CONFIG_DIR first, then $HOME), and returns PreTrustOK when the entry
// was written. Any failure, a missing file or an unusable CLAUDE_CONFIG_DIR
// included, returns PreTrustFailed and prints exactly one line to
// preTrustWarn saying "pre-trust failed", naming the resolved file (when none
// could be resolved, only the reason: for an unusable CLAUDE_CONFIG_DIR, its
// quoted value) and saying the agent may stop at Claude Code's folder-trust
// prompt; it names no label, token or environment value other than
// CLAUDE_CONFIG_DIR. PreTrust never returns an error and never fails a
// launch.
func PreTrust(cwd string, extraEnv map[string]string, off bool, cfg config.PreTrust) PreTrustOutcome {
	if off {
		return PreTrustSkipped
	}
	// Load has refused a value too large for a time.Duration
	// (config.MaxPreTrustLockWaitSeconds), so the conversion cannot wrap.
	err := preTrustCwd(cwd, extraEnv, time.Duration(cfg.EffectiveLockWaitSeconds())*time.Second)
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
// CLAUDE_CONFIG_DIR (absent or empty).
var claudeJSONPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// ConfigDirUsable reports whether dir, the CLAUDE_CONFIG_DIR value of a
// launch's or row's extra env, can be used to find that launch's Claude Code
// files: only an absolute path can (bugs b.1ba, b.nje). Claude Code resolves
// a relative value against its pane's cwd, while agent-director would resolve
// it against its own caller's cwd, a different directory for each caller, so
// a non-absolute value (relative, `~`-prefixed or whitespace-only) is never
// used. An empty value is not absolute either; it means CLAUDE_CONFIG_DIR is
// not set.
//
// It is the one rule for every reader of the value. Pre-trust's .claude.json
// (claudeJSONFor) tells an empty value from a set but unusable one: empty
// targets $HOME/.claude.json, unusable is refused. The transcript paths
// resume and find-missing compose (pkg/api) treat both alike, falling back to
// ~/.claude.
func ConfigDirUsable(dir string) bool {
	return filepath.IsAbs(dir)
}

// errConfigDirNotAbsolute is the condition behind a CLAUDE_CONFIG_DIR that is
// set but not usable (ConfigDirUsable). claudeJSONFor returns it wrapped with
// the quoted value, so pre-trust reads and writes nothing.
var errConfigDirNotAbsolute = errors.New("is not an absolute path")

// claudeJSONFor resolves the .claude.json file pre-trust targets for a launch
// with extraEnv (bugs b.18k, b.nje):
//   - extraEnv["CLAUDE_CONFIG_DIR"] absent or empty → $HOME/.claude.json (via
//     claudeJSONPath, stubbed by tests).
//   - Usable (ConfigDirUsable) → <CLAUDE_CONFIG_DIR>/.claude.json.
//   - Set but not usable → an error matching errConfigDirNotAbsolute that
//     quotes the value. Falling back to $HOME/.claude.json instead would
//     write the entry into a file the launched Claude Code does not read.
func claudeJSONFor(extraEnv map[string]string) (string, error) {
	dir := extraEnv["CLAUDE_CONFIG_DIR"]
	switch {
	case dir == "":
		path, err := claudeJSONPath()
		if err != nil {
			return "", fmt.Errorf("resolve home: %w", err)
		}
		return path, nil
	case !ConfigDirUsable(dir):
		return "", fmt.Errorf("CLAUDE_CONFIG_DIR %q %w", dir, errConfigDirNotAbsolute)
	}
	return filepath.Join(dir, ".claude.json"), nil
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
// The target file is resolved from extraEnv by claudeJSONFor (bugs b.18k,
// b.nje):
//   - If extraEnv["CLAUDE_CONFIG_DIR"] is absolute → <CLAUDE_CONFIG_DIR>/.claude.json
//   - If it is absent or empty → $HOME/.claude.json (via claudeJSONPath,
//     stubbed by tests)
//   - If it is set but not absolute (ConfigDirUsable) → an error before any
//     file I/O: no file is stat'd, read or written and no lock dir is made.
//
// Behavior (per bugs b.f75, b.zjm and b.6rh):
//
//   - The read-modify-write runs under Claude Code's own lock on the file
//     (lockConfig, configlock.go): take the lock, read the entire file
//     under it, mutate the projects map, write the entire file via
//     temp+rename (writeFileAtomic), release the lock. Claude Code saves
//     the same file under that lock and re-reads it under the lock before
//     each save, so neither side's update is lost to the other, and
//     concurrent agent-director pre-trusts of one file run one at a time.
//     The wait for a held lock is bounded by lockWait (PreTrust passes the
//     effective pre_trust.lock_wait_seconds, b.kr4); when it runs out, or
//     the lock cannot be taken at all, nothing is written and the error
//     makes PreTrust report failed. The same holds when, just before the
//     write, lock.checkHold finds the lock held too long or taken over
//     by another process.
//   - If the file is a symlink (a dotfile-managed link, say), the write
//     goes through it, as Claude Code's own save does: writeFileAtomic
//     replaces the file the link resolves to, through a chain of links,
//     and leaves the link in place. The lock stays the literal
//     <path>.lock of the path claudeJSONFor resolved, with no symlink
//     resolved, matching Claude Code's lock (b.zjm).
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
func preTrustCwd(cwd string, extraEnv map[string]string, lockWait time.Duration) error {
	path, err := claudeJSONFor(extraEnv)
	if err != nil {
		return fmt.Errorf("pre-trust: %w", err)
	}

	if _, err := os.Stat(path); err != nil {
		return claudeJSONReadError(path, err)
	}
	lock, err := lockConfig(path, lockWait)
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

// writeFileAtomic writes data to path via a temp file and os.Rename over the
// file atomicWriteTarget picks: when path is a symlink, the file the link
// resolves to, so the write goes through the link and leaves the link in
// place, as Claude Code's own save does (bug b.6rh); otherwise path itself.
// Renaming over the link instead would replace it with a regular file and
// leave its target stale, splitting a dotfile-managed .claude.json from its
// copy.
//
// The temp file is created in the replaced file's directory, so the rename
// stays within one filesystem even when a link points to another one. It
// is created with O_EXCL and a random suffix so concurrent writers don't
// clobber each other's temp files; on Linux rename(2) is atomic within a
// filesystem, so a reader either sees the old contents or the new contents
// but never a torn write.
//
// On error the temp file is removed best-effort. Mode is the
// permissions of the temp file *before* rename — since we read+rewrite
// an operator-owned file, 0o600 is a sane default that matches Claude
// Code's own permissions on ~/.claude.json (per inspection on the
// smoke-test VM).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	target, err := atomicWriteTarget(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, filepath.Base(target)+".tmp-*")
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
	if err := os.Rename(tmpPath, target); err != nil {
		cleanup()
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// atomicWriteTarget returns the file writeFileAtomic replaces to write path
// (bug b.6rh). When path is a symlink it is the file the link resolves to
// (filepath.EvalSymlinks): through every link of a chain, each link's target
// taken relative to that link's directory when it is not absolute. A link
// that does not resolve (dangling, or a loop) returns an error, so nothing
// is written and the link is never replaced. Any other path is returned as
// is, a path that does not exist included, so a file removed since it was
// read is written again at path.
func atomicWriteTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return path, nil
	case err != nil:
		return "", fmt.Errorf("check symlink: %w", err)
	case info.Mode()&os.ModeSymlink == 0:
		return path, nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve symlink %s: %w", path, err)
	}
	return target, nil
}
