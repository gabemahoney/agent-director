package spawn

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MaxTmuxSessionNameBytes caps caller-supplied --tmux-session-name values
// at the UTF-8 byte length the validator accepts. 64 bytes fits any
// tmux status-bar width and leaves room above the 8-char id suffix the
// synthesized-name path appends; it is NOT a tmux-imposed limit
// (SR-2.2).
const MaxTmuxSessionNameBytes = 64

// deniedClaudeArgs is the set of flags agent-director must own end-to-end.
// --settings would race with our hook synthesis; --resume/--continue would
// bypass our session-id tracking; --print/--output-format exit before any
// hook fires so the supervision model breaks (SRD §7.2 step 3).
//
// --setting-sources is intentionally not on this list: clean-slate Spawns
// (suppressing the user tier) are a supported configuration per SRD §19 Q5.
var deniedClaudeArgs = map[string]struct{}{
	"--settings":      {},
	"--resume":        {},
	"--continue":      {},
	"--print":         {},
	"--output-format": {},
}

// reservedEnvKeyPrefix names the env-var namespace agent-director owns.
// Anything starting with this prefix would alias one of our routing vars
// (AGENT_DIRECTOR_INSTANCE_ID, AGENT_DIRECTOR_RELAY_MODE,
// AGENT_DIRECTOR_LABEL_*) so we reject it at validation time.
const reservedEnvKeyPrefix = "AGENT_DIRECTOR_"

// Validate runs the SRD §7.2 checks in order and short-circuits on the
// first failure. No file or tmux side effects on any error. The Resolved
// value is updated in place when cwd canonicalization succeeds — callers
// that want to preserve the original input should save it before calling.
//
// Validation order (load-bearing — preserves error precedence the SRD
// pins):
//  1. cwd shape, existence, type. Canonicalize via EvalSymlinks so two
//     callers spawning into /foo/bar and /foo/./bar end up with the same
//     row.
//  2. relay_mode is "on" / "off" / "" if set.
//  3. claude_args contains no denied flag (both --flag VALUE and
//     --flag=VALUE forms).
//  4. extra_env contains no AGENT_DIRECTOR_* key.
//  5. labels normalization is a no-op here; the prefix guard in §7.2
//     step 5 lands at env-composition time.
func Validate(r *Resolved) error {
	if err := validateCwd(r); err != nil {
		return err
	}
	if err := validateRelayMode(r.RelayMode); err != nil {
		return err
	}
	if err := validateClaudeArgs(r.ClaudeArgs); err != nil {
		return err
	}
	if err := validateExtraEnv(r.ExtraEnv); err != nil {
		return err
	}
	if r.TmuxSessionNameSupplied {
		if err := validateTmuxSessionName(r.TmuxSessionName); err != nil {
			return err
		}
	}
	return nil
}

// validateTmuxSessionName applies SR-2.1, SR-2.2, SR-2.3. The function
// is only invoked when the caller explicitly supplied a value
// (gated upstream via SpawnParams.TmuxSessionNameSupplied). The
// validator rejects empty, byte length > MaxTmuxSessionNameBytes,
// non-UTF-8, '#', ':', '.', and ASCII control characters
// (\x00-\x1f / \x7f). It does NOT silently rewrite — callers must
// pick a name they want byte-for-byte (contrast with
// SanitizeSessionName, which is a defaulting concern).
func validateTmuxSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: --tmux-session-name was supplied with an empty value", ErrTmuxSessionNameEmpty)
	}
	if len(name) > MaxTmuxSessionNameBytes {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrTmuxSessionNameTooLong, len(name), MaxTmuxSessionNameBytes)
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("%w: not valid UTF-8", ErrTmuxSessionNameInvalid)
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b == '#', b == ':', b == '.':
			return fmt.Errorf("%w: contains reserved character %q", ErrTmuxSessionNameInvalid, b)
		case b <= 0x1f, b == 0x7f:
			return fmt.Errorf("%w: contains ASCII control byte 0x%02x", ErrTmuxSessionNameInvalid, b)
		}
	}
	return nil
}

// validateCwd applies SRD §7.2 step 1 and overwrites r.CWD with the
// canonical path so the launch INSERT records the canonical form.
func validateCwd(r *Resolved) error {
	if r.CWD == "" {
		return fmt.Errorf("%w: cwd is required", ErrCwdMissing)
	}
	// Reject obvious URL shapes — they would canonicalize to nothing useful.
	if strings.Contains(r.CWD, "://") {
		return fmt.Errorf("%w: %q is not a path", ErrCwdNotAPath, r.CWD)
	}
	path := r.CWD
	if strings.HasPrefix(path, "~/") || path == "~" {
		u, err := user.Current()
		if err != nil {
			return fmt.Errorf("%w: resolve home: %v", ErrCwdNotAPath, err)
		}
		if path == "~" {
			path = u.HomeDir
		} else {
			path = filepath.Join(u.HomeDir, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %q is not absolute", ErrCwdNotAPath, r.CWD)
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		// EvalSymlinks distinguishes the "no such file" path from other
		// I/O failures via os.IsNotExist on the unwrapped error.
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %q", ErrCwdNotFound, r.CWD)
		}
		return fmt.Errorf("%w: %v", ErrCwdNotFound, err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %q", ErrCwdNotFound, r.CWD)
		}
		return fmt.Errorf("%w: stat %q: %v", ErrCwdNotFound, canonical, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", ErrCwdNotADirectory, r.CWD)
	}
	r.CWD = canonical
	return nil
}

// validateRelayMode applies SRD §7.2 step 2. Empty is OK — the defaults
// pass will fill it from config.
func validateRelayMode(m string) error {
	switch m {
	case "", "on", "off":
		return nil
	default:
		return fmt.Errorf("%w: %q (want on/off)", ErrRelayModeInvalid, m)
	}
}

// validateClaudeArgs applies SRD §7.2 step 3. Detection handles both forms
// (--flag VALUE and --flag=VALUE) so callers cannot smuggle a denied flag
// past the check by inlining its value.
func validateClaudeArgs(args []string) error {
	for _, a := range args {
		// Slice off the value half of --flag=VALUE before matching so the
		// detection key is the bare flag name.
		key := a
		if i := strings.IndexByte(a, '='); i >= 0 {
			key = a[:i]
		}
		if _, denied := deniedClaudeArgs[key]; denied {
			return fmt.Errorf("%w: %s", ErrSpawnDeniedFlag, key)
		}
	}
	return nil
}

// validateExtraEnv applies SRD §7.2 step 4. The check is case-sensitive
// (POSIX env vars are case-sensitive) and matches the SRD §14.4 carve-out
// for auth env vars — those do NOT carry the AGENT_DIRECTOR_ prefix and
// pass through.
func validateExtraEnv(env map[string]string) error {
	for k := range env {
		if strings.HasPrefix(k, reservedEnvKeyPrefix) {
			return fmt.Errorf("%w: %s", ErrReservedEnvKey, k)
		}
	}
	return nil
}
