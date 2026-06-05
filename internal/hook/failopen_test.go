package hook

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
)

// trailLineCount returns the number of lines currently in the trail file.
// Returns 0 when the file does not yet exist.
func trailLineCount(t *testing.T) int {
	t.Helper()
	path := filepath.Join(os.Getenv("AGENT_DIRECTOR_STATE_DIR"), "ad-trail.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("trailLineCount: open: %v", err)
	}
	defer f.Close()
	var n int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		n++
	}
	return n
}

// trailHookFiredAfter returns the first ad.hook.fired line written after
// prevCount total lines existed in the trail file. Returns nil if none found.
func trailHookFiredAfter(t *testing.T, prevCount int) map[string]any {
	t.Helper()
	path := filepath.Join(os.Getenv("AGENT_DIRECTOR_STATE_DIR"), "ad-trail.jsonl")
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("trailHookFiredAfter: open: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var i int
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) == nil && i >= prevCount {
			if m["event"] == "ad.hook.fired" {
				return m
			}
		}
		i++
	}
	return nil
}

// flakyStore is a HookStore double whose state-tracking calls return
// programmable errors. The relay-side methods (UpsertOpenPermissionRequest,
// GetPermissionRequest) are no-ops here — the fail-open suite only
// exercises the state-tracking path.
type flakyStore struct {
	transitionErr error
	sessionErr    error
	transitionN   int
	sessionN      int
}

func (f *flakyStore) ApplyHookTransition(string, string, bool, string) error {
	f.transitionN++
	return f.transitionErr
}
func (f *flakyStore) SetSessionID(string, string) error {
	f.sessionN++
	return f.sessionErr
}
func (f *flakyStore) UpsertOpenPermissionRequest(_, _, _, _ string, _ int, _ string) error {
	return nil
}
func (f *flakyStore) GetPermissionRequest(_, _ string) (store.PermissionRow, error) {
	return store.PermissionRow{}, nil
}
func (f *flakyStore) DecidePermissionRequest(_, _, _, _, _ string) (bool, error) {
	return false, nil
}

// captureLog wraps a *log.Logger writing to a bytes.Buffer so tests can
// assert *that* an error was logged without pinning the exact phrasing.
func captureLog(t *testing.T) (*log.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return log.New(buf, "", 0), buf
}

// callHandle is a small convenience adapter for the legacy fail-open
// suite: thread context.Background, an io.Discard stdout, and an
// empty HandleConfig.Env (so RELAY_MODE is never "on" — fail-open
// branch).
func callHandle(stdin io.Reader, env func(string) string, st HookStore, logger *log.Logger) error {
	return Handle(context.Background(), stdin, io.Discard, st, HandleConfig{
		Env: env,
		Cfg: config.Relay{},
	}, logger)
}

func TestHandleMissingInstanceIDExitsZero(t *testing.T) {
	logger, buf := captureLog(t)
	stdin := strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	st := &flakyStore{}
	env := func(string) string { return "" } // no env var
	if err := callHandle(stdin, env, st, logger); err != nil {
		t.Fatalf("Handle returned err = %v; want nil (fail-open)", err)
	}
	if st.transitionN != 0 {
		t.Errorf("transition called %d times; want 0", st.transitionN)
	}
	if buf.Len() == 0 {
		t.Errorf("expected an error-log entry; log buffer empty")
	}
}

func TestHandleMalformedPayloadExitsZero(t *testing.T) {
	logger, buf := captureLog(t)
	stdin := strings.NewReader("not json")
	st := &flakyStore{}
	env := func(string) string { return "id-123" }
	if err := callHandle(stdin, env, st, logger); err != nil {
		t.Fatalf("Handle returned err = %v; want nil (fail-open)", err)
	}
	if st.transitionN != 0 {
		t.Errorf("transition called %d times; want 0 (parse failed)", st.transitionN)
	}
	if buf.Len() == 0 {
		t.Errorf("expected log entry for parse failure")
	}
}

func TestHandlePayloadTooLargeExitsZero(t *testing.T) {
	logger, buf := captureLog(t)
	huge := strings.Repeat("a", int(MaxPayloadBytes)+1)
	st := &flakyStore{}
	env := func(string) string { return "id-123" }
	if err := callHandle(strings.NewReader(huge), env, st, logger); err != nil {
		t.Fatalf("Handle returned err = %v; want nil (fail-open)", err)
	}
	if st.transitionN != 0 {
		t.Errorf("transition called %d times; want 0 (oversized)", st.transitionN)
	}
	if buf.Len() == 0 {
		t.Errorf("expected log entry for oversized payload")
	}
}

func TestHandleStoreTransitionErrorExitsZero(t *testing.T) {
	before := trailLineCount(t)
	logger, buf := captureLog(t)
	stdin := strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	st := &flakyStore{transitionErr: errors.New("db unreachable")}
	env := func(string) string { return "id-123" }
	if err := callHandle(stdin, env, st, logger); err != nil {
		t.Fatalf("Handle returned err = %v; want nil (fail-open)", err)
	}
	if st.transitionN != 1 {
		t.Errorf("transition called %d times; want 1", st.transitionN)
	}
	if !strings.Contains(buf.String(), "apply transition") {
		t.Errorf("log missing transition-error entry; got %q", buf.String())
	}
	// Trail: exactly one ad.hook.fired line with upsert_outcome="error"
	// (flakyStore doesn't implement outcomeTransitioner; error path sets UpsertError).
	row := trailHookFiredAfter(t, before)
	if row == nil {
		t.Fatal("no ad.hook.fired in trail after fail-open transition error")
	}
	if row["upsert_outcome"] != "error" {
		t.Errorf("upsert_outcome = %v; want \"error\"", row["upsert_outcome"])
	}
	if _, ok := row["tool_input"]; ok {
		t.Error("tool_input present in trail line (must be dropped)")
	}
}

func TestHandleSessionIDErrorExitsZero(t *testing.T) {
	logger, _ := captureLog(t)
	stdin := strings.NewReader(`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`)
	st := &flakyStore{sessionErr: errors.New("db unreachable")}
	env := func(string) string { return "id-123" }
	if err := callHandle(stdin, env, st, logger); err != nil {
		t.Fatalf("Handle returned err = %v; want nil (fail-open)", err)
	}
	if st.sessionN != 1 {
		t.Errorf("session-id called %d times; want 1", st.sessionN)
	}
}

func TestHandleHappyPathWritesBothColumns(t *testing.T) {
	logger, _ := captureLog(t)
	stdin := strings.NewReader(`{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`)
	st := &flakyStore{}
	env := func(string) string { return "id-123" }
	if err := callHandle(stdin, env, st, logger); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if st.transitionN != 1 {
		t.Errorf("transition called %d times; want 1", st.transitionN)
	}
	if st.sessionN != 1 {
		t.Errorf("session-id called %d times; want 1", st.sessionN)
	}
}

func TestHandleNilLoggerDoesNotPanic(t *testing.T) {
	stdin := strings.NewReader(`{"hook_event_name":"SessionStart"}`)
	st := &flakyStore{}
	env := func(string) string { return "id-123" }
	if err := callHandle(stdin, env, st, nil); err != nil {
		t.Fatalf("Handle returned %v; want nil", err)
	}
}

func TestReadPayloadEnforcesCap(t *testing.T) {
	exact := bytes.Repeat([]byte{'x'}, int(MaxPayloadBytes))
	if _, err := ReadPayload(bytes.NewReader(exact)); err != nil {
		t.Fatalf("ReadPayload at cap: %v", err)
	}
	over := bytes.Repeat([]byte{'x'}, int(MaxPayloadBytes)+1)
	_, err := ReadPayload(bytes.NewReader(over))
	if !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err = %v; want ErrPayloadTooLarge", err)
	}
}

func TestReadPayloadEmpty(t *testing.T) {
	p, err := ReadPayload(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ReadPayload(empty): %v", err)
	}
	if len(p) != 0 {
		t.Errorf("empty payload returned %d bytes", len(p))
	}
}

func TestResolveInstanceIDRejectsPathSeparator(t *testing.T) {
	_, err := ResolveInstanceID(func(string) string { return "abc/def" })
	if !errors.Is(err, ErrInstanceIDInvalid) {
		t.Fatalf("err = %v; want ErrInstanceIDInvalid", err)
	}
}

func TestResolveInstanceIDRejectsNul(t *testing.T) {
	_, err := ResolveInstanceID(func(string) string { return "abc\x00def" })
	if !errors.Is(err, ErrInstanceIDInvalid) {
		t.Fatalf("err = %v; want ErrInstanceIDInvalid", err)
	}
}

func TestResolveInstanceIDMissingErr(t *testing.T) {
	_, err := ResolveInstanceID(func(string) string { return "" })
	if !errors.Is(err, ErrInstanceIDMissing) {
		t.Fatalf("err = %v; want ErrInstanceIDMissing", err)
	}
}

