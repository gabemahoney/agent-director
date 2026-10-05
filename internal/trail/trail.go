// Package trail writes append-only JSONL audit events to an on-disk trail file
// for the agent-director process. It is the single write path for all ad.*
// audit events and must never use SQLite, BoltDB, or any storage indirection
// (SR-A-7.1, SR-A-7.18).
//
// # Invariants
//
//   - One file descriptor per process, lazy-opened on first Emit (SR-A-7.6).
//   - Sync flush per Emit so tail -f sees lines within ~100ms (SR-A-7.15).
//   - "tool_input" fields are silently dropped before serialization and must
//     never appear in the trail. Callers must not attempt to log tool_input.
//   - No buffering, no rotation, no retention, no schema-version field
//     (SR-A-7.4, SR-A-7.5, SR-A-7.16, SR-A-7.18).
//   - On write or sync failure the writer attempts a single
//     ad.trail_meta.emit_failed envelope (with original_event and error_class
//     fields); if that also fails a single line is written to the operational
//     logger. The original error is always returned to the caller so verbs can
//     fail-open per SR-A-3.2.
//   - The trail file is only ever written at an absolute path under the user's
//     home directory. When no home directory can be determined (see
//     resolvePath) the writer has no path: every Emit writes no file anywhere,
//     writes one line to the operational logger and returns an error wrapping
//     errNoHome. It never falls back to a path relative to the working
//     directory or to another home source.
package trail

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const trailFilename = "ad-trail.jsonl"

// errNoHome reports that no absolute home directory could be determined, so
// the trail has no path and nothing is written.
var errNoHome = errors.New("trail: no home directory, trail not written")

// Writer is a process-lifetime JSONL appender for the agent-director audit
// trail. Obtain the process-singleton via Default(). The zero value is not
// usable.
type Writer struct {
	mu      sync.Mutex
	f       *os.File
	path    string      // absolute trail path; "" when no home directory was found
	pathErr error       // why path is "" (from resolvePath); nil when path is set
	olog    *log.Logger // optional; nil silently discards fallback messages
}

var (
	once          sync.Once
	defaultWriter *Writer
)

// Default returns the process-singleton Writer. The singleton is constructed
// on the first call (with the path resolved from the user's home directory at
// that moment, see resolvePath) and reused for the process lifetime. When no
// home directory can be determined at that moment the singleton has no path
// for the rest of the process: every Emit fails soft and writes no file (it
// never writes relative to the working directory).
func Default() *Writer {
	once.Do(func() {
		p, err := resolvePath()
		defaultWriter = &Writer{path: p, pathErr: err}
	})
	return defaultWriter
}

// Path returns the resolved trail file path without opening the file. The
// directory component is always ~/.agent-director/ (the user's home directory).
// The filename is always "ad-trail.jsonl". When no home directory can be
// determined (see resolvePath) Path returns "", meaning the trail is not
// written; it never returns a relative path. Path is safe to call from
// multiple goroutines.
func Path() string {
	p, _ := resolvePath() // "" is the documented no-home result
	return p
}

// SetLogger wires an operational logger into the process-singleton Writer.
// Fail-soft fallback messages (write failures, ts-substitution warnings) are
// sent to l. Safe to call before the first Emit.
func SetLogger(l *log.Logger) {
	w := Default()
	w.mu.Lock()
	w.olog = l
	w.mu.Unlock()
}

// Emit writes a single audit event to the trail via the process-singleton
// Writer. It is a convenience wrapper for Default().Emit.
func Emit(ctx context.Context, event string, fields map[string]any) error {
	return Default().Emit(ctx, event, fields)
}

// resolvePath computes the full trail file path,
// <home>/.agent-director/ad-trail.jsonl, where home is os.UserHomeDir() ($HOME
// on Unix).
//
// When no home directory can be determined — os.UserHomeDir fails (on Unix,
// HOME unset or empty) or returns a path that is not absolute — it returns ""
// and an error wrapping errNoHome, and the trail fails soft with no file
// written. It deliberately does not fall back to another home source such as
// the passwd entry: an empty HOME is often deliberate isolation, and the
// passwd home holds the real ~/.agent-director (the b.8dr incident class). It
// never returns a relative path, so no trail is ever written under the
// working directory.
func resolvePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", errNoHome, err)
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("%w: home directory %q is not absolute", errNoHome, home)
	}
	return filepath.Join(home, ".agent-director", trailFilename), nil
}

// Emit writes a single audit event envelope to the trail file.
//
// event must be non-empty; the ad.* namespace is conventional. Fields are
// merged into the envelope at the top level; any "tool_input" key is silently
// dropped before serialization (binding invariant — tool_input must never
// appear in the trail).
//
// Fail-soft: on any write or sync error, Emit attempts one
// ad.trail_meta.emit_failed envelope. If that also fails, one line is written
// to the operational logger. The original error is always returned so callers
// can fail-open per SR-A-3.2.
//
// When the writer has no absolute path (no home directory, see resolvePath),
// Emit writes no file, attempts no meta envelope, writes exactly one line to
// the operational logger and returns an error wrapping errNoHome.
func (w *Writer) Emit(_ context.Context, event string, fields map[string]any) error {
	if event == "" {
		return fmt.Errorf("trail: event is required")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if !filepath.IsAbs(w.path) {
		err := w.noPathErr()
		w.operLog("trail: no trail path; original=%s error_class=no_home cause=%v", event, err)
		return err
	}

	line, err := buildEnvelope(event, fields, w.olog)
	if err != nil {
		return fmt.Errorf("trail: build envelope: %w", err)
	}

	if err := w.ensureOpen(); err != nil {
		w.failSoftLocked(event, "open_failed", err)
		return err
	}

	if _, err := w.f.Write(line); err != nil {
		w.failSoftLocked(event, "write_failed", err)
		return err
	}
	if err := w.f.Sync(); err != nil {
		w.failSoftLocked(event, "sync_failed", err)
		return err
	}
	return nil
}

// noPathErr is the error Emit returns when w has no absolute path: the
// resolvePath error when there is one, else an errNoHome naming the path.
func (w *Writer) noPathErr() error {
	if w.pathErr != nil {
		return w.pathErr
	}
	return fmt.Errorf("%w: trail path %q is not absolute", errNoHome, w.path)
}

// ensureOpen opens the trail file if not already open. Callers must hold w.mu.
func (w *Writer) ensureOpen() error {
	if w.f != nil {
		return nil
	}
	dir := filepath.Dir(w.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("trail: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("trail: open %s: %w", w.path, err)
	}
	w.f = f
	return nil
}

// failSoftLocked attempts to write one ad.trail_meta.emit_failed envelope to
// the already-open fd. On any failure it falls back to the operational log.
// Callers must hold w.mu.
func (w *Writer) failSoftLocked(originalEvent, errorClass string, cause error) {
	meta := map[string]any{
		"original_event": originalEvent,
		"error_class":    errorClass,
	}
	line, err := buildEnvelope("ad.trail_meta.emit_failed", meta, nil)
	if err != nil {
		w.operLog("trail: build meta-envelope: %v; original=%s error_class=%s cause=%v",
			err, originalEvent, errorClass, cause)
		return
	}
	if w.f == nil {
		w.operLog("trail: fd nil, cannot write meta; original=%s error_class=%s cause=%v",
			originalEvent, errorClass, cause)
		return
	}
	if _, werr := w.f.Write(line); werr != nil {
		w.operLog("trail: meta write failed: %v; original=%s error_class=%s cause=%v",
			werr, originalEvent, errorClass, cause)
		return
	}
	_ = w.f.Sync() // best-effort sync of meta-event
}

// operLog writes a single message to the operational logger when set.
func (w *Writer) operLog(format string, args ...any) {
	if w.olog != nil {
		w.olog.Printf(format, args...)
	}
}
