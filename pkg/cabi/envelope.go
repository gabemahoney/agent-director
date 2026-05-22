package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/gabemahoney/agent-director/pkg/api/errnames"
)

// maxSanitizedLen is the maximum byte-length of a sanitized ErrInternal
// description written into the cross-boundary envelope. Messages exceeding
// this limit are truncated with a trailing "..." marker.
const maxSanitizedLen = 512

// absPathRe matches absolute filesystem path segments of the form
// /[a-zA-Z0-9_\-./]+ — e.g. /home/user/foo.go, /tmp/bar,
// /usr/local/go/src/runtime. These are replaced with "<path>" before an
// ErrInternal description crosses the C boundary.
var absPathRe = regexp.MustCompile(`/[a-zA-Z0-9_\-./]+`)

// stackFrameRe matches Go stack-frame location lines, e.g.
//
//	\tgithub.com/foo/bar.go:42 +0x1a0
var stackFrameRe = regexp.MustCompile(`(?m)^\t[^\t]+\.go:\d+.*$`)

// goroutineHeaderRe matches goroutine header lines emitted by runtime/debug.Stack,
// e.g. "goroutine 1 [running]:".
var goroutineHeaderRe = regexp.MustCompile(`(?m)^goroutine \d+ \[.*\]:?$`)

// cabiLogger and cabiLoggerOnce guard the lazy-initialized slog logger used
// by debugLog. The logger is created on first use when AGENT_DIRECTOR_DEBUG
// is non-empty; otherwise debugLog returns immediately without allocating.
var (
	cabiLogger     *slog.Logger
	cabiLoggerOnce sync.Once
)

// debugLog emits msg at Warn level to the cabi slog logger when
// AGENT_DIRECTOR_DEBUG is non-empty. The logger is initialized on first call
// (lazy); if the env var is not set, nothing is allocated and the call
// returns immediately. Use this to emit the un-sanitized error chain before
// the sanitized form is written into the C-boundary envelope.
func debugLog(msg string, args ...any) {
	if os.Getenv("AGENT_DIRECTOR_DEBUG") == "" {
		return
	}
	cabiLoggerOnce.Do(func() {
		cabiLogger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	})
	cabiLogger.Warn(msg, args...)
}

// successEnvelope marshals payload to a flat JSON object whose top-level keys
// are the exported fields of the payload struct (or map entries). A nil
// payload produces the empty object "{}". On marshal failure — which should
// never occur for well-typed structs — it falls back to an ErrInternal
// envelope.
//
// Note: returns Go []byte only. Conversion to *C.char via C.CString happens
// in lifecycle.go where cgo is in scope.
func successEnvelope(payload any) []byte {
	if payload == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return errorEnvelope("ErrInternal", "envelope marshal failed")
	}
	return b
}

// errorEnvelope builds a JSON object with exactly two fields — err_name and
// err_description — and returns it as a byte slice. The caller is responsible
// for sanitizing the description before passing it when err_name is
// "ErrInternal" (see classifyAndEnvelope).
//
// Note: returns Go []byte only. Conversion to *C.char via C.CString happens
// in lifecycle.go where cgo is in scope.
func errorEnvelope(name, desc string) []byte {
	type errEnv struct {
		ErrName        string `json:"err_name"`
		ErrDescription string `json:"err_description"`
	}
	b, err := json.Marshal(errEnv{ErrName: name, ErrDescription: desc})
	if err != nil {
		// Last-resort fallback — should never happen for plain strings.
		return []byte(`{"err_name":"ErrInternal","err_description":"envelope marshal failed"}`)
	}
	return b
}

// classifyAndEnvelope classifies err via pkg/api/errnames.Classify and returns
// the appropriate JSON envelope bytes. When the error is unrecognized and
// Classify falls back to "ErrInternal":
//  1. The full un-sanitized error chain (%+v) is emitted via debugLog for
//     post-mortem debugging (visible only when AGENT_DIRECTOR_DEBUG is set).
//  2. The description is sanitized — absolute paths stripped, stack-frame
//     lines removed, length capped — before being written into the envelope.
//
// Documented errors (those matched in errnames.Catalog) are returned as-is
// with no sanitization.
//
// Note: returns Go []byte only. Conversion to *C.char via C.CString happens
// in lifecycle.go where cgo is in scope.
func classifyAndEnvelope(err error) []byte {
	name, desc := errnames.Classify(err)
	if name == "ErrInternal" {
		// Log full unsanitized chain before stripping it from the envelope.
		debugLog("cabi: unrecognized internal error", "error", err)
		desc = sanitizeInternalError(desc)
	}
	return errorEnvelope(name, desc)
}

// sanitizeInternalError strips information that must not cross the C boundary
// in an ErrInternal envelope:
//
//  1. Go stack-frame location lines (tab-indented entries matching
//     \t[^\t]+\.go:\d+) are removed.
//  2. Goroutine header lines (e.g. "goroutine 1 [running]:") are removed.
//  3. Blank-line runs left by removals above are collapsed.
//  4. Absolute filesystem path segments matching /[a-zA-Z0-9_\-./]+ are
//     replaced with "<path>".
//  5. The result is trimmed of surrounding whitespace.
//  6. The final string is capped at maxSanitizedLen bytes; a "..." suffix is
//     appended when truncation occurs.
//
// The function is unexported but accessible to _test.go files in this package.
// It is deterministic: the same input always produces the same output.
func sanitizeInternalError(msg string) string {
	// Step 1 — remove stack-frame location lines.
	msg = stackFrameRe.ReplaceAllString(msg, "")

	// Step 2 — remove goroutine header lines.
	msg = goroutineHeaderRe.ReplaceAllString(msg, "")

	// Step 3 — collapse triple-newline runs left by the removals.
	for strings.Contains(msg, "\n\n\n") {
		msg = strings.ReplaceAll(msg, "\n\n\n", "\n\n")
	}

	// Step 4 — replace absolute filesystem path segments.
	msg = absPathRe.ReplaceAllString(msg, "<path>")

	// Step 5 — trim surrounding whitespace.
	msg = strings.TrimSpace(msg)

	// Step 6 — cap length.
	if len(msg) > maxSanitizedLen {
		msg = msg[:maxSanitizedLen] + "..."
	}

	return msg
}
