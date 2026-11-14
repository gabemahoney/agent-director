package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// paneParent is a hook's parent process: the row's recorded pane process.
type paneParent struct {
	pid   int
	start string
}

func (p paneParent) StartTime(pid int) (string, bool, bool) {
	if pid != p.pid {
		return "", false, true
	}
	return p.start, true, true
}
func (paneParent) CommandName(int) (string, bool) { return "claude", true }
func (paneParent) PPID(int) (int, bool)           { return 0, false }

// wakeBuffer is a hook's error stream in memory that takes a write deadline,
// as the route requires of its output (code repair 2, R2-2).
type wakeBuffer struct{ bytes.Buffer }

func (*wakeBuffer) SetWriteDeadline(time.Time) error { return nil }

// brokenStderr is a hook's error stream that takes a write deadline but no byte.
type brokenStderr struct{}

func (brokenStderr) Write([]byte) (int, error)        { return 0, errors.New("write |1: broken pipe") }
func (brokenStderr) SetWriteDeadline(time.Time) error { return nil }

// fireClaudeHook runs Claude hook event with fields, in session, for id's row
// from its own pane process with a readable helper identity and stderr, as
// the agent's settings run it; it must write nothing on stdout.
func fireClaudeHook(t *testing.T, e *killEnv, id, session, event string, fields map[string]any, stderr io.Writer) error {
	t.Helper()
	row, err := e.st.GetSpawn(id)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"hook_event_name": event, "session_id": session, "transcript_path": "/x/" + session + ".jsonl"}
	maps.Copy(payload, fields)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	p := paneParent{pid: row.Identity.PanePID, start: row.Identity.PaneStarttime}
	hc := hook.HandleConfig{
		Env: func(k string) string {
			if k == "AGENT_DIRECTOR_INSTANCE_ID" {
				return id
			}
			return ""
		},
		ParentPID:           func() int { return p.pid },
		ParentProc:          p,
		Self:                func() store.ProcessIdentity { return store.ProcessIdentity{PID: 4242, Starttime: "4242"} },
		Stderr:              stderr,
		ClaudeStartupPrompt: spawn.HasClaudeStartupPrompt, // as cmd/agent-director wires it
	}
	var stdout bytes.Buffer
	err = hook.Handle(context.Background(), bytes.NewReader(raw), &stdout, e.st, hc, nil)
	if stdout.Len() != 0 {
		t.Errorf("%s hook stdout = %q; want nothing", event, stdout.String())
	}
	return err
}

// coveredSend sends p through api.SendKeys on the real store with the
// native-input fixture's backend installed (newNativeSubmissionEnv), failing
// unless it is accepted; it returns the stored Result.
func coveredSend(t *testing.T, e *nativeSubmissionEnv, p api.SendKeysParams) api.DeliveryResult {
	t.Helper()
	res, err := api.SendKeys(e.st, e.rec, e.env, p)
	if err != nil || res.Delivery == nil {
		t.Fatalf("SendKeys(%q) = %+v, %v; want an accepted Result", p.Text, res, err)
	}
	return *res.Delivery
}

// assertNoInputPath fails unless no call reached either input path (SR-3.9):
// the native-input backend was never opened and the pane got no text, Enter
// or key.
func assertNoInputPath(t *testing.T, e *nativeSubmissionEnv) {
	t.Helper()
	inputs := len(e.rec.SocketCallsOf(tmux.CallSendText)) + len(e.rec.SocketCallsOf(tmux.CallSendEnter)) + len(e.rec.SocketCallsOf(tmux.CallSendKey))
	if e.b.opened != 0 || inputs != 0 {
		t.Errorf("native-input opens %d, pane inputs %d; want neither input path reached", e.b.opened, inputs)
	}
}

// TestBusySendClaudeHookRoute (SR-3.9, SR-5.6; M2 P-C4.2): a covered Claude
// agent's Messages reach it only through its own hooks. Once its wake wrote a
// prompt, get and status show awaiting_turn_start 1, and reading them changes
// nothing; the agent's exact sighting brings it to 0; a wake whose output
// failed leaves it 1. No send and no wake reaches the native composer or the
// pane: delivered, with output that failed, or with no route recorded.
func TestBusySendClaudeHookRoute(t *testing.T) {
	t.Parallel()
	ne := newNativeSubmissionEnv(t)
	e := ne.killEnv
	c, _ := e.client(t)
	session := uuid.NewString()
	r := e.seedRow(t, killRowSpec{SessionID: session})
	noRoute := e.seedRow(t, killRowSpec{SessionID: session})
	seedBusyCovered(t, e, r.ID, noRoute.ID)
	wake, _ := busyWake(t, e, r.ID)
	changed := map[string]any{"file_path": wake, "event": "change"}
	awaiting := func(want int64) {
		t.Helper()
		row, err := c.Get(r.ID)
		if err != nil || row.Delivery == nil || row.Delivery.AwaitingTurnStart != want {
			t.Errorf("get delivery = %+v, %v; want awaiting_turn_start %d", row.Delivery, err, want)
		}
		status, err := c.Status(r.ID)
		if err != nil || status.Delivery == nil || status.Delivery.AwaitingTurnStart != want {
			t.Errorf("status delivery = %+v, %v; want awaiting_turn_start %d", status.Delivery, err, want)
		}
	}

	coveredSend(t, ne, api.SendKeysParams{InstanceID: r.ID, Text: "hello"})
	awaiting(0)
	var stderr wakeBuffer
	const body = "[agent-director: message 1 of 1]\nhello"
	if err := fireClaudeHook(t, e, r.ID, session, "FileChanged", changed, &stderr); !errors.Is(err, hook.ErrRewake) || stderr.String() != body {
		t.Fatalf("wake = %v, wrote %q; want ErrRewake with %q", err, stderr.String(), body)
	}
	facts := busyFacts(t, e.dbPath, r.ID)
	awaiting(1)
	awaiting(1)
	if got := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(got, facts) {
		t.Errorf("busy-send core after get and status = %+v; want unchanged %+v", got, facts)
	}
	prompt := "<task-notification>\n<summary>Stop hook feedback</summary>\n</task-notification>\n<system-reminder>\n" +
		`Stop hook blocking error from command "FileChanged": ` + body + "\n</system-reminder>"
	if err := fireClaudeHook(t, e, r.ID, session, "UserPromptSubmit", map[string]any{"prompt": prompt}, nil); err != nil {
		t.Fatal(err)
	}
	awaiting(0)

	if err := fireClaudeHook(t, e, r.ID, session, "Stop", nil, nil); err != nil {
		t.Fatal(err)
	}
	coveredSend(t, ne, api.SendKeysParams{InstanceID: r.ID, Text: "again"})
	if err := fireClaudeHook(t, e, r.ID, session, "FileChanged", changed, brokenStderr{}); err != nil {
		t.Errorf("wake whose output failed = %v; want nil (exit 0)", err)
	}
	awaiting(1)

	held := coveredSend(t, ne, api.SendKeysParams{InstanceID: noRoute.ID, Text: "no route"})
	stderr.Reset()
	if err := fireClaudeHook(t, e, noRoute.ID, session, "FileChanged", changed, &stderr); err != nil || stderr.Len() != 0 ||
		held.Wait == nil || held.Wait.Reason != "route_unavailable" {
		t.Errorf("wake with no route = %v, wrote %q, Message %+v; want nothing written, Held route_unavailable", err, stderr.String(), held)
	}
	assertNoInputPath(t, ne)
}
