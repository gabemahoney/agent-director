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
package trail

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const trailFilename = "ad-trail.jsonl"

// Writer is a process-lifetime JSONL appender for the agent-director audit
// trail. Obtain the process-singleton via Default(). The zero value is not
// usable.
type Writer struct {
	mu   sync.Mutex
	f    *os.File
	path string
	olog *log.Logger // optional; nil silently discards fallback messages
}

var (
	once          sync.Once
	defaultWriter *Writer
)

// Default returns the process-singleton Writer. The singleton is constructed
// on the first call (with the path resolved from the environment at that
// moment) and reused for the process lifetime.
func Default() *Writer {
	once.Do(func() {
		defaultWriter = &Writer{path: resolvePath()}
	})
	return defaultWriter
}

// Path returns the resolved trail file path without opening the file. The
// directory component is taken from $AGENT_DIRECTOR_STATE_DIR; when that
// variable is absent the default ~/.agent-director/ is used. The filename is
// always "ad-trail.jsonl". Path is safe to call from multiple goroutines.
func Path() string {
	return resolvePath()
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

// resolvePath computes the full trail file path. $AGENT_DIRECTOR_STATE_DIR
// overrides the directory; when absent the default ~/.agent-director/ is used.
func resolvePath() string {
	dir := os.Getenv("AGENT_DIRECTOR_STATE_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".agent-director")
	}
	return filepath.Join(dir, trailFilename)
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
func (w *Writer) Emit(_ context.Context, event string, fields map[string]any) error {
	if event == "" {
		return fmt.Errorf("trail: event is required")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

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
