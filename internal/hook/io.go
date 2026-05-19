// Package hook implements the `claude-director hook` verb: the lifecycle
// handler Claude Code invokes via the synthesized --settings JSON. The
// handler reads a payload from stdin, classifies the event per SRD §5.2,
// writes a row UPSERT, and exits 0 (state-tracking fail-open).
//
// The relay-mode permission decision envelope (SRD §6.3) is Epic 10's
// deliverable; Epic 3 ships the state-tracking subset.
package hook

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// MaxPayloadBytes is the cap ReadPayload enforces. Claude Code's hook
// payloads are well under 1 MiB in practice; the cap exists to bound
// memory in case a misbehaving event ships a runaway transcript.
const MaxPayloadBytes int64 = 1 << 20 // 1 MiB

// ErrPayloadTooLarge is returned by ReadPayload when stdin exceeds the
// MaxPayloadBytes cap. The hook handler converts this to a silent exit-0
// per the fail-open invariant; the sentinel exists so tests can pin the
// exact rejection path.
var ErrPayloadTooLarge = errors.New("hook: payload exceeds 1 MiB")

// ErrInstanceIDMissing is returned by ResolveInstanceID when the env var
// is unset or empty.
var ErrInstanceIDMissing = errors.New("hook: CLAUDE_DIRECTOR_INSTANCE_ID missing")

// ErrInstanceIDInvalid is returned by ResolveInstanceID when the env var
// value contains characters that would let the hook handler reach a
// different row than the one its env was provisioned for (path
// separators, NULs, control bytes).
var ErrInstanceIDInvalid = errors.New("hook: CLAUDE_DIRECTOR_INSTANCE_ID invalid")

// envInstanceID names the env var the synthesized --settings JSON
// inherits from the tmux session — set once at spawn time, inherited by
// Claude and every subprocess.
const envInstanceID = "CLAUDE_DIRECTOR_INSTANCE_ID"

// ReadPayload reads up to MaxPayloadBytes from stdin and returns the
// resulting blob. Reads exceeding the cap return ErrPayloadTooLarge with
// no buffer (the partial read is discarded — there is nothing useful to
// do with half a JSON document).
//
// The implementation uses io.LimitReader plus a one-byte probe to detect
// the over-cap condition cleanly: if the cap-sized read filled to capacity
// AND there is at least one more byte available, the payload is too large.
// This avoids the ambiguity of "did we just happen to hit the cap exactly
// on a valid payload" — only a payload genuinely larger than the cap is
// rejected.
func ReadPayload(r io.Reader) (json.RawMessage, error) {
	buf, err := io.ReadAll(io.LimitReader(r, MaxPayloadBytes+1))
	if err != nil {
		return nil, fmt.Errorf("hook: read stdin: %w", err)
	}
	if int64(len(buf)) > MaxPayloadBytes {
		return nil, ErrPayloadTooLarge
	}
	return json.RawMessage(buf), nil
}

// ResolveInstanceID reads CLAUDE_DIRECTOR_INSTANCE_ID from the env map.
// The accepted shape is "non-empty string, no path separators, no NUL or
// control bytes" — UUID4 is the production producer (spawn.ApplyDefaults)
// but the hook accepts any matching-shape value so a test rig can
// hand-craft IDs.
func ResolveInstanceID(env func(string) string) (string, error) {
	v := strings.TrimSpace(env(envInstanceID))
	if v == "" {
		return "", ErrInstanceIDMissing
	}
	if strings.ContainsAny(v, "/\\\x00") {
		return "", fmt.Errorf("%w: contains path separator or NUL", ErrInstanceIDInvalid)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: contains control byte", ErrInstanceIDInvalid)
		}
	}
	return v, nil
}

// OSGetenv wraps os.Getenv into the function shape ResolveInstanceID
// expects. Callers pass it from cmd/ at hook dispatch; tests pass a
// closure backed by a map.
func OSGetenv(key string) string { return os.Getenv(key) }
