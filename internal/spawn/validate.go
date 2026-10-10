package spawn

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
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
// Any env-var name (envVarName) starting with this prefix would alias one of
// our routing vars (AGENT_DIRECTOR_INSTANCE_ID, AGENT_DIRECTOR_RELAY_MODE,
// AGENT_DIRECTOR_LABEL_*) so we reject it at validation time.
const reservedEnvKeyPrefix = "AGENT_DIRECTOR_"

// ReservedHomeEnvKey is HOME, the one exact env-var name outside
// reservedEnvKeyPrefix that extra_env may not set, whatever its value, an
// empty one included (bug b.nas). The agent's hooks run a bare
// `agent-director hook`, which resolves its config and store from the pane's
// HOME, so an extra-env HOME would send the agent's hook events to another
// agent-director store and leave the spawner's row pending until
// find-missing marks it missing. It is matched against the name tmux sets
// (envVarName), not the raw key, so a key such as "HOME=/x" is refused too
// (ReservedHomeKey). Spawn validation (validateExtraEnv) refuses it, and
// resume refuses a row whose stored extra env has it (a row spawned before
// the refusal), both with ErrReservedEnvKey.
const ReservedHomeEnvKey = "HOME"

// ReservedHomeReason is why HOME is refused in extra_env, the one text
// spawn's and resume's ErrReservedEnvKey descriptions share.
const ReservedHomeReason = "the agent's hook resolves agent-director's config and store from HOME, so its events would reach another agent-director store and the row would stay pending until find-missing marks it missing"

// ReservedHomeAlternative is what a caller who set HOME in extra_env sets
// instead, shared like ReservedHomeReason.
const ReservedHomeAlternative = "to give the agent its own Claude Code config, set an absolute CLAUDE_CONFIG_DIR in extra_env instead"

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
//  4. extra_env contains no key whose env-var name (the part before the
//     first '=', the name tmux sets) is HOME or starts with AGENT_DIRECTOR_.
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
// non-UTF-8, '#', ':', '.', '$', '\', and ASCII control characters
// (\x00-\x1f / \x7f). '$' and '\' are rejected because tmux cannot
// match such a name exactly: a target such as "=$7:" reads as the
// session id $7, and a backslash goes through tmux's escaping (SR-9.2).
// It does NOT silently rewrite — callers must pick a name they want
// byte-for-byte (contrast with SanitizeSessionName, which is a
// defaulting concern). The empty refusal names the param in its one
// spelling, manifest.TmuxSessionNameSpelling (b.ro3), since MCP and the
// TypeScript client reach it too.
func validateTmuxSessionName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: %s was supplied with an empty value", ErrTmuxSessionNameEmpty, manifest.TmuxSessionNameSpelling)
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
		case b == '#', b == ':', b == '.', b == '$', b == '\\':
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

// validateExtraEnv applies SRD §7.2 step 4. Both reserved-key rules apply to
// each key's env-var name (envVarName), the name tmux actually sets, not the
// raw key. The checks are case-sensitive (POSIX env vars are case-sensitive)
// and match the SRD §14.4 carve-out for auth env vars — those do NOT carry
// the AGENT_DIRECTOR_ prefix and pass through.
//
// HOME is checked first (ReservedHomeKey, bug b.nas): a key whose name is
// HOME, whatever its value, an empty one included, is refused, the message
// quoting the key as given and saying it sets HOME, why that is refused
// (ReservedHomeReason) and what to set instead (ReservedHomeAlternative).
// Then a key whose name has the AGENT_DIRECTOR_ prefix is refused, quoted.
// Each rule names the first matching key in sorted order, so the error is
// stable. env is the merged extra env (a template's keys included), so a
// template that sets HOME is refused too. Malformed keys (empty, or with '='
// but no reserved name) are not refused here.
func validateExtraEnv(env map[string]string) error {
	if k, ok := ReservedHomeKey(env); ok {
		return fmt.Errorf("%w: extra_env key %q sets %s, which is reserved: %s; remove %q from extra_env, and %s",
			ErrReservedEnvKey, k, ReservedHomeEnvKey, ReservedHomeReason, k, ReservedHomeAlternative)
	}
	if k, ok := firstEnvKey(env, func(name string) bool { return strings.HasPrefix(name, reservedEnvKeyPrefix) }); ok {
		return fmt.Errorf("%w: %q", ErrReservedEnvKey, k)
	}
	return nil
}

// ReservedHomeKey returns the first key of env, in sorted order, whose
// env-var name (envVarName: the part before the first '=') is HOME
// (ReservedHomeEnvKey), and true; "" and false when no key sets HOME. The
// exact key HOME matches, and so does any key such as "HOME=/x", which tmux
// would launch with HOME set to "/x=<value>". It is the one HOME rule for
// spawn validation (validateExtraEnv) and resume's refusal of a stored row
// (pkg/api), so both refuse the same keys and name the same one.
func ReservedHomeKey(env map[string]string) (string, bool) {
	return firstEnvKey(env, func(name string) bool { return name == ReservedHomeEnvKey })
}

// envVarName returns the name of the env var tmux sets for an extra_env key:
// the part before the first '=', or the whole key when it has none. tmux is
// given each entry as key+"="+value (internal/tmux sortedEnvFlags) and splits
// it at the first '=', so the key "HOME=/x" sets HOME, not "HOME=/x".
func envVarName(key string) string {
	if i := strings.IndexByte(key, '='); i >= 0 {
		return key[:i]
	}
	return key
}

// firstEnvKey returns the smallest key of env (the first in sorted order)
// whose env-var name (envVarName) satisfies match, and true; "" and false
// when none does. Taking the smallest, not the first in map order, keeps the
// key a refusal names stable across runs.
func firstEnvKey(env map[string]string, match func(name string) bool) (string, bool) {
	first, found := "", false
	for k := range env {
		if match(envVarName(k)) && (!found || k < first) {
			first, found = k, true
		}
	}
	return first, found
}
