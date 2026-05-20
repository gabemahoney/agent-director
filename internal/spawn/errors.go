// Package spawn implements the parameter-resolution → validation → defaults
// → launch pipeline for a new Spawn. It is the single owner of "how does a
// Spawn get born". Hook handling is internal/hook; SQL is internal/store;
// tmux is internal/tmux. Spawn coordinates the three.
package spawn

import "errors"

// Typed errors per SRD §13.1. The names below are the canonical ones the
// CLI / MCP surfaces emit verbatim; tests match via errors.Is.

// ErrCwdMissing is returned when the caller did not provide a cwd. spawn
// requires cwd because the JSONL path (and so resume) is derived from it.
var ErrCwdMissing = errors.New("ErrCwdMissing")

// ErrCwdNotAPath is returned for cwd values that are not absolute and not a
// "~/" form. URLs ("://"), bare relative paths, and non-path-looking values
// all collapse to this one error — the contract is "give me something
// EvalSymlinks can canonicalize."
var ErrCwdNotAPath = errors.New("ErrCwdNotAPath")

// ErrCwdNotFound is returned when cwd resolved to a path that does not
// exist on disk at validation time.
var ErrCwdNotFound = errors.New("ErrCwdNotFound")

// ErrCwdNotADirectory is returned when cwd resolved to a regular file (or
// other non-directory inode). Spawning into a file would confuse Claude.
var ErrCwdNotADirectory = errors.New("ErrCwdNotADirectory")

// ErrRelayModeInvalid is returned when relay_mode was supplied as something
// other than "on" / "off" / "" (empty falls back to the config default).
var ErrRelayModeInvalid = errors.New("ErrRelayModeInvalid")

// ErrSpawnDeniedFlag is returned when claude_args contains a flag the
// supervisor must own (--settings, --resume, --continue, --print,
// --output-format). The match handles both --flag VALUE and --flag=VALUE
// forms; --setting-sources is deliberately not on this list (SRD §19 Q5).
var ErrSpawnDeniedFlag = errors.New("ErrSpawnDeniedFlag")

// ErrReservedEnvKey is returned when extra_env contains a key matching the
// CLAUDE_DIRECTOR_* prefix (case-sensitive). Auth env vars
// (ANTHROPIC_API_KEY, CLAUDE_CODE_OAUTH_TOKEN) are explicitly allowed.
var ErrReservedEnvKey = errors.New("ErrReservedEnvKey")

// ErrInstanceIdCollision is returned when the caller supplied an explicit
// claude_instance_id that is already in use by a Spawn in a live state
// (anything except `ended` / `missing`). The check is a TOCTOU-safe pair
// with SQLite's PRIMARY KEY constraint at INSERT time.
var ErrInstanceIdCollision = errors.New("ErrInstanceIdCollision")

// ErrTmuxSessionNameEmpty is returned when the caller explicitly supplied
// --tmux-session-name with an empty value. Omitting the flag entirely is
// NOT equivalent (that path falls through to composeSessionName).
var ErrTmuxSessionNameEmpty = errors.New("ErrTmuxSessionNameEmpty")

// ErrTmuxSessionNameInvalid is returned when the caller-supplied
// --tmux-session-name contains any of '#', ':', '.', an ASCII control
// character (\x00-\x1f / \x7f), or is not valid UTF-8. The validator
// does NOT silently rewrite — callers must pick a name they want
// byte-for-byte.
var ErrTmuxSessionNameInvalid = errors.New("ErrTmuxSessionNameInvalid")

// ErrTmuxSessionNameTooLong is returned when the caller-supplied
// --tmux-session-name exceeds MaxTmuxSessionNameBytes UTF-8 bytes.
// The cap is an app-layer convenience (operator readability + room for
// the defaulted-name's 8-char id suffix), not a tmux-imposed limit.
var ErrTmuxSessionNameTooLong = errors.New("ErrTmuxSessionNameTooLong")
