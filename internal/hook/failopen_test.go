package hook

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/gabemahoney/claude-director/internal/config"
	"github.com/gabemahoney/claude-director/internal/store"
)

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

func (f *flakyStore) ApplyHookTransition(string, string, bool) error {
	f.transitionN++
	return f.transitionErr
}
func (f *flakyStore) SetSessionID(string, string) error {
	f.sessionN++
	return f.sessionErr
}
func (f *flakyStore) UpsertOpenPermissionRequest(string, string, string) error {
	return nil
}
func (f *flakyStore) GetPermissionRequest(string) (store.PermissionRow, error) {
	return store.PermissionRow{}, nil
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

