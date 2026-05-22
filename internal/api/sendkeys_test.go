package api_test

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/api"
	"github.com/gabemahoney/agent-director/internal/store"
)

// recordingTmux records each (session, text, pressEnter) triple its
// SendKeys is asked to emit, in order. The literal-text-then-real-Enter
// split is owned by *tmux.Client; the api verb makes exactly one
// SendKeys call per send-keys invocation.
type recordingTmux struct {
	calls   []recordedSend
	failOn  int // 0-based call index that should return err; -1 to never fail
	failErr error
}

type recordedSend struct {
	name       string
	text       string
	pressEnter bool
}

func (r *recordingTmux) SendKeys(name, text string, pressEnter bool) error {
	idx := len(r.calls)
	r.calls = append(r.calls, recordedSend{name: name, text: text, pressEnter: pressEnter})
	if r.failOn >= 0 && idx == r.failOn {
		return r.failErr
	}
	return nil
}

// newTmux constructs a recording fake that never fails.
func newTmux() *recordingTmux { return &recordingTmux{failOn: -1} }

// openStoreWithRow inserts a single Spawn row at the requested state and
// relay_mode and returns the open Store. The helper keeps each test case
// terse: the test author names a state, the helper handles InsertPending +
// ApplyHookTransition mechanics.
func openStoreWithRow(t *testing.T, id, sessionName, state, relayMode string) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := store.OpenOrInit(path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.InsertPending(store.Spawn{
		ClaudeInstanceID: id,
		CWD:              "/tmp",
		TmuxSessionName:  sessionName,
		RelayMode:        relayMode,
	}); err != nil {
		t.Fatalf("InsertPending: %v", err)
	}
	if state != store.StatePending {
		if err := s.ApplyHookTransition(id, state, false); err != nil {
			t.Fatalf("ApplyHookTransition(%s): %v", state, err)
		}
	}
	return s
}

func TestSendKeysSingleLineWithEnter(t *testing.T) {
	s := openStoreWithRow(t, "id-1", "cd-tmp", store.StateWaiting, "off")
	tmux := newTmux()

	if _, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-1",
		Text:             "hello",
	}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	want := []recordedSend{
		{name: "cd-tmp", text: "hello", pressEnter: true},
	}
	if !reflect.DeepEqual(tmux.calls, want) {
		t.Fatalf("calls = %v; want %v", tmux.calls, want)
	}
}

func TestSendKeysMultilinePreservesLF(t *testing.T) {
	// Per reference/send-keys-research.md case #3: LF in the text payload
	// composes a newline in Claude's input box and does NOT submit. Only
	// the trailing Enter submits — so a multi-line text must produce
	// exactly two tmux calls (the text once, Enter once).
	s := openStoreWithRow(t, "id-2", "cd-tmp", store.StateWorking, "off")
	tmux := newTmux()

	if _, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-2",
		Text:             "line one\nline two",
	}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	want := []recordedSend{
		{name: "cd-tmp", text: "line one\nline two", pressEnter: true},
	}
	if !reflect.DeepEqual(tmux.calls, want) {
		t.Fatalf("calls = %v; want %v", tmux.calls, want)
	}
}

func TestSendKeysStripsCR(t *testing.T) {
	// Per reference/send-keys-research.md "CR caveat": a CR (0x0D) byte
	// anywhere in the payload would submit the partial buffer at that
	// position — split one logical message into multiple submissions.
	// The verb strips CRs pre-send so only the trailing Enter submits.
	s := openStoreWithRow(t, "id-3", "cd-tmp", store.StateWaiting, "off")
	tmux := newTmux()

	if _, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-3",
		Text:             "ab\rcd\r\nef",
	}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// CR stripped; LF preserved. Composed text is "abcd\nef".
	want := []recordedSend{
		{name: "cd-tmp", text: "abcd\nef", pressEnter: true},
	}
	if !reflect.DeepEqual(tmux.calls, want) {
		t.Fatalf("calls = %v; want %v", tmux.calls, want)
	}
}

func TestSendKeysRejectsPendingState(t *testing.T) {
	// pending Spawns have not received their first SessionStart hook;
	// the TUI is not yet up. Treating pending as interactive would let
	// a caller race the bootstrap window.
	s := openStoreWithRow(t, "id-5", "cd-tmp", store.StatePending, "off")
	tmux := newTmux()

	_, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-5",
		Text:             "hi",
	})
	if !errors.Is(err, api.ErrSpawnNotInteractive) {
		t.Fatalf("err = %v; want ErrSpawnNotInteractive", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("tmux was called for non-interactive state: %v", tmux.calls)
	}
}

func TestSendKeysRejectsEndedState(t *testing.T) {
	s := openStoreWithRow(t, "id-6", "cd-tmp", store.StateEnded, "off")
	tmux := newTmux()

	_, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-6",
		Text:             "hi",
	})
	if !errors.Is(err, api.ErrSpawnNotInteractive) {
		t.Fatalf("err = %v; want ErrSpawnNotInteractive", err)
	}
}

func TestSendKeysCheckPermissionWithRelayOn(t *testing.T) {
	// relay_mode=on AND state=check_permission means the relay path (Epic
	// 10) owns the answer. Sending pane-side keystrokes would race the
	// decide() write. Return the stub guard error.
	s := openStoreWithRow(t, "id-7", "cd-tmp", store.StateCheckPermission, "on")
	tmux := newTmux()

	_, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-7",
		Text:             "1",
	})
	if !errors.Is(err, api.ErrSendKeysWhileRelayed) {
		t.Fatalf("err = %v; want ErrSendKeysWhileRelayed", err)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("tmux was called while relay guard tripped: %v", tmux.calls)
	}
}

func TestSendKeysCheckPermissionWithRelayOff(t *testing.T) {
	// relay_mode=off means no relay is consuming the modal — the
	// orchestrator drives the answer via send-keys directly. The
	// state-precondition guard must allow this combination.
	s := openStoreWithRow(t, "id-8", "cd-tmp", store.StateCheckPermission, "off")
	tmux := newTmux()

	if _, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-8",
		Text:             "1",
	}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	want := []recordedSend{
		{name: "cd-tmp", text: "1", pressEnter: true},
	}
	if !reflect.DeepEqual(tmux.calls, want) {
		t.Fatalf("calls = %v; want %v", tmux.calls, want)
	}
}

func TestSendKeysSpawnNotFound(t *testing.T) {
	s := openStoreWithRow(t, "id-9", "cd-tmp", store.StateWaiting, "off")
	tmux := newTmux()

	_, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "absent",
		Text:             "hi",
	})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
}

func TestSendKeysPropagatesTmuxError(t *testing.T) {
	// A transport-layer tmux failure must surface to the caller without
	// being remapped to a verb-surface error. errors.Is must still see
	// the underlying tmux sentinel.
	s := openStoreWithRow(t, "id-10", "cd-tmp", store.StateWaiting, "off")
	tmux := &recordingTmux{
		failOn:  0,
		failErr: errSentinel,
	}

	_, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-10",
		Text:             "hi",
	})
	if !errors.Is(err, errSentinel) {
		t.Fatalf("err = %v; want errSentinel chain", err)
	}
	// Exactly one SendKeys attempt — the tmux client owns the
	// literal-text-then-Enter split internally now, so api-side
	// recording sees a single failed call rather than two attempts.
	if len(tmux.calls) != 1 {
		t.Fatalf("calls = %v; want exactly 1 (single SendKeys attempt)", tmux.calls)
	}
}

// errSentinel stands in for tmux.ErrTmuxSendKeys without importing the
// tmux package directly into this test (the verb only sees
// SendKeysTmux.SendKeys's error return, so any sentinel proves the chain
// is preserved).
var errSentinel = errors.New("test sentinel")

func TestSendKeysEmptyTextSubmits(t *testing.T) {
	// An empty text is a valid "submit the existing buffer" call — the
	// equivalent of pressing Enter at the keyboard when the user already
	// has text composed. The verb sends an empty text then the Enter.
	// (tmux will accept an empty argv for send-keys as a no-op; we mirror
	// that.)
	s := openStoreWithRow(t, "id-11", "cd-tmp", store.StateWaiting, "off")
	tmux := newTmux()

	if _, err := api.SendKeys(s, tmux, api.SendKeysParams{
		ClaudeInstanceID: "id-11",
		Text:             "",
	}); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}
	want := []recordedSend{
		{name: "cd-tmp", text: "", pressEnter: true},
	}
	if !reflect.DeepEqual(tmux.calls, want) {
		t.Fatalf("calls = %v; want %v", tmux.calls, want)
	}
}
