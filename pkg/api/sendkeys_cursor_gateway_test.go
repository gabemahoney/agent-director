//go:build linux

package api_test

// sendkeys_cursor_gateway_test.go — b.bqd: a Cursor row's plain send with
// Enter reads its gateway's verdict on that Enter from the row's protected
// gateway transcript; a refusal of its text is ErrSubmissionUnconfirmed
// (gateway_refused).
// The transcripts are cursorfix.NewGatewayHistory's, under TestMain's HOME; a
// stand-in gateway appends records as internal/cursorhost's recorder does.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/cursortranscript"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The reasons the gateway records for input refused under a hold, and for
// provisional input a control retired.
const (
	cursorGatewayReason = "starting, held, stale or cancellation"
	cursorHeldReason    = "control retired provisional input"
)

// atWrite as a consumed_at stands for the call's clock when the stand-in
// gateway writes the record (cursorGatewayEnv.write), as the gateway stamps
// input it consumes. The fake tmux charges each call to that clock, so it is
// later than the call's start.
var atWrite = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// gatewayRecord is one gateway transcript line of kind for launch token about
// a submitted text consumed at at (no consumed_at when zero): an accepted or
// held submission carries the text, a refusal its reason and, unless text is
// "" (an older gateway, or input already recorded held), the text's
// text_sha256. Every record the tests write is built here.
func gatewayRecord(token, kind, text string, at time.Time) string {
	data := map[string]any{"submission_id": "submission-1"}
	if !at.IsZero() {
		data["consumed_at"] = at
	}
	switch kind {
	case cursortranscript.KindSubmissionAccepted, cursortranscript.KindInputHeld:
		data["text"] = text
	case cursortranscript.KindSubmissionRefused, cursortranscript.KindHeldInputRefused:
		data["reason"] = cursorHeldReason
		if kind == cursortranscript.KindSubmissionRefused {
			data["reason"], data["count"] = cursorGatewayReason, 0
		}
		if text != "" {
			data["text_sha256"] = cursortranscript.TextSHA256(text)
		}
	}
	raw, err := json.Marshal(map[string]any{"sequence": 2, "kind": kind, "instance_id": "gateway-instance",
		"life_number": 1, "launch_token": token, "origin": "launch", "data": data})
	if err != nil {
		panic(err)
	}
	return string(raw) + "\n"
}

// withGatewayData is the gateway record line with fields set in its data, for
// the other record shapes the merged gateway writes (b.x9o).
func withGatewayData(line string, fields map[string]any) string {
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		panic(err)
	}
	data := rec["data"].(map[string]any)
	for k, v := range fields {
		data[k] = v
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	return string(raw) + "\n"
}

// appendGateway appends data, whole lines or not, to the transcript at path.
func appendGateway(t *testing.T, path string, data ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(data, "")); err != nil {
		t.Fatal(err)
	}
}

// gatewayAfterInput runs write once, when rec's pane first takes an Enter or a
// key (a text without Enter included), as the gateway records its verdict on
// that input.
func gatewayAfterInput(rec *tmuxfix.Recorder, write func()) {
	done := false
	rec.AfterCall(tmuxfix.AnyCall, func(c tmuxfix.SocketCall, _ error) {
		if !done && (c.Call == tmux.CallSendEnter || c.Call == tmux.CallSendKey || c.Call == tmux.CallSendText && !c.PressEnter) {
			done = true
			write()
		}
	})
}

// cursorGatewayEnv is newSubmissionEnv (its poll clock counts e.slept) with
// one waiting row r, a protected gateway transcript at path and the text a
// call sends (skaText unless changed).
type cursorGatewayEnv struct {
	*submissionEnv
	r          killRow
	path, text string
}

// newCursorGatewayEnv seeds r as agent's row whose jsonl_path is jsonl(path),
// or path when jsonl is nil ("" seeds none).
func newCursorGatewayEnv(t *testing.T, agent string, jsonl func(t *testing.T, path string) string) *cursorGatewayEnv {
	t.Helper()
	e := &cursorGatewayEnv{submissionEnv: newSubmissionEnv(t), text: skaText}
	id := "cursor-gateway-" + uuid.NewString()[:8]
	e.path = cursorfix.NewGatewayHistory(t, id, 1, "gateway-session", "earlier-session").Latest
	row := e.path
	if jsonl != nil {
		row = jsonl(t, e.path)
	}
	var opts []apitest.SpawnOption
	if row != "" {
		opts = append(opts, apitest.WithJsonlPath(row))
	}
	e.r = e.seedRow(t, killRowSpec{ID: id, Opts: agentRowOpts(agent, opts...)})
	return e
}

// record is gatewayRecord of kind for r's launch about e.text, consumed when
// written (atWrite).
func (e *cursorGatewayEnv) record(kind string) string {
	return gatewayRecord(e.r.Token, kind, e.text, atWrite)
}

// write appends data to the transcript, atWrite stamped with the call's clock.
func (e *cursorGatewayEnv) write(t *testing.T, data ...string) {
	from, _ := json.Marshal(atWrite)
	now, _ := json.Marshal(e.clock.Now())
	appendGateway(t, e.path, strings.ReplaceAll(strings.Join(data, ""), string(from), string(now)))
}

// atInput has the gateway write data once the pane takes the call's input.
func (e *cursorGatewayEnv) atInput(t *testing.T, data ...string) {
	gatewayAfterInput(e.rec, func() { e.write(t, data...) })
}

// atFirstSleep has the gateway write data during the call's first poll wait.
func (e *cursorGatewayEnv) atFirstSleep(t *testing.T, data ...string) {
	sleep, done := e.env.Sleep, false
	e.env.Sleep = func(d time.Duration) {
		sleep(d)
		if !done {
			done = true
			e.write(t, data...)
		}
	}
}

// assertGatewayRefused fails unless err is ErrSubmissionUnconfirmed with
// outcome gateway_refused for bytes typed and Enter sent, the gateway's
// reason and the row's state as read, on the wire too.
func assertGatewayRefused(t *testing.T, err error, bytes int, reason, state string) {
	t.Helper()
	d := assertDeliveryError(t, err, "gateway_refused", bytes, true)
	want := api.SubmissionUnconfirmedDetails{Outcome: "gateway_refused", Phase: "enter_attempted", BytesAttempted: bytes,
		EnterMayHaveBeenSent: true, Reason: reason, KeysSent: true, RowState: state}
	if d != want {
		t.Errorf("details = %+v; want %+v", d, want)
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"outcome": "gateway_refused", "phase": "enter_attempted", "bytes_attempted": float64(bytes),
		"enter_may_have_been_sent": true, "reason": reason, "keys_sent": true, "submission_confirmed": false,
		"row_state": state}; !reflect.DeepEqual(wire, want) {
		t.Errorf("wire details = %s; want %v", b, want)
	}
}

// TestSendKeysCursorGatewayVerdict (b.bqd): after a Cursor row's plain send
// with Enter, an Enter-only send included, send-keys reads the gateway's
// transcript from its end before the send, judging complete lines only, for up
// to 2 s on the call's clock and at most 20 waits. A refusal of the row's
// launch whose text_sha256 is the text's (any text's for an Enter-only send)
// is gateway_refused with the gateway's reason; the text accepted or held, a
// control, another launch's refusal, an unattributed refusal or no verdict
// keeps success. Another text's verdicts, and verdicts on input consumed
// before the call, are passed over, as are b.x9o's input_observed and
// submission_cancelled. The keys are sent once either way.
func TestSendKeysCursorGatewayVerdict(t *testing.T) {
	t.Parallel()
	const (
		accepted = cursortranscript.KindSubmissionAccepted
		refused  = cursortranscript.KindSubmissionRefused
		held     = cursortranscript.KindInputHeld
		retired  = cursortranscript.KindHeldInputRefused
		control  = cursortranscript.KindInputControl
		draft    = "a draft left on the line"
	)
	// before is a consumed_at just before the call's start.
	before := func(e *cursorGatewayEnv) time.Time { return e.clock.Now().Add(-time.Millisecond) }
	for _, tc := range []struct {
		name    string
		text    string
		gateway func(t *testing.T, e *cursorGatewayEnv)
		reason  string // a refusal's; "" wants success
		slept   time.Duration
	}{
		{name: "refused", text: skaText, reason: cursorGatewayReason,
			gateway: func(t *testing.T, e *cursorGatewayEnv) { e.atInput(t, e.record(refused)) }},
		{name: "held input refused", text: skaText, reason: cursorHeldReason,
			gateway: func(t *testing.T, e *cursorGatewayEnv) { e.atInput(t, e.record(retired)) }},
		{name: "another text's verdicts passed over", text: skaText, reason: cursorGatewayReason,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, gatewayRecord(e.r.Token, retired, "other", atWrite), gatewayRecord(e.r.Token, accepted, "other", atWrite), e.record(refused))
			}},
		{name: "verdicts on earlier input passed over", text: skaText, reason: cursorGatewayReason,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				// A control, and an identical text's refusal, written late.
				e.atInput(t, gatewayRecord(e.r.Token, control, "", before(e)), gatewayRecord(e.r.Token, retired, e.text, before(e)), e.record(refused))
			}},
		{name: "Enter-only send refused", reason: cursorGatewayReason,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, gatewayRecord(e.r.Token, retired, draft, before(e)), gatewayRecord(e.r.Token, refused, draft, atWrite))
			}},
		{name: "Enter-only send, unattributed refusal", slept: 2 * time.Second,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, gatewayRecord(e.r.Token, retired, "", time.Time{}))
			}},
		{name: "refusal completed after a wait", text: skaText, reason: cursorGatewayReason, slept: 100 * time.Millisecond,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				line := e.record(refused)
				e.atInput(t, e.record("update"), line[:20])
				e.atFirstSleep(t, line[20:])
			}},
		{name: "accepted", text: skaText,
			gateway: func(t *testing.T, e *cursorGatewayEnv) { e.atInput(t, e.record(accepted)) }},
		{name: "held", text: skaText,
			gateway: func(t *testing.T, e *cursorGatewayEnv) { e.atInput(t, e.record(held)) }},
		{name: "taken as a control", text: skaText,
			gateway: func(t *testing.T, e *cursorGatewayEnv) { e.atInput(t, e.record(control)) }},
		{name: "another launch's refusal", text: skaText,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, gatewayRecord(tmuxfix.OtherToken, refused, e.text, atWrite))
			}},
		{name: "a refusal before the send", text: skaText,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.write(t, e.record(refused))
				e.atInput(t, e.record(accepted))
			}},
		{name: "unattributed refusals", text: skaText, slept: 2 * time.Second,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, gatewayRecord(e.r.Token, refused, "", time.Time{}), gatewayRecord(e.r.Token, retired, "", time.Time{}))
			}},
		{name: "refusal never completed", text: skaText, slept: 2 * time.Second,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, strings.TrimSuffix(e.record(refused), "\n"))
			}},
		{name: "no verdict", text: skaText, slept: 2 * time.Second,
			gateway: func(*testing.T, *cursorGatewayEnv) {}},
		// b.x9o: records the merged gateway writes that are not this call's
		// refusal. An accepted submission without durable admission is still
		// accepted; input_observed and submission_cancelled are passed over.
		{name: "accepted without durable admission", text: skaText,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t, withGatewayData(e.record(accepted), map[string]any{"origin": "main", "durable_admission": false}))
			}},
		{name: "b.x9o non-verdict records passed over", text: skaText, slept: 2 * time.Second,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.atInput(t,
					withGatewayData(e.record(cursortranscript.KindInputObserved), map[string]any{"text": e.text, "durable_admission": false}),
					withGatewayData(e.record("submission_cancelled"), map[string]any{"reason": "gateway stopped", "started": false}))
			}},
		{name: "no verdict on a frozen clock", text: skaText, slept: 2 * time.Second,
			gateway: func(t *testing.T, e *cursorGatewayEnv) {
				e.env.Sleep = func(d time.Duration) {
					if e.slept += d; e.slept > 2*time.Second {
						t.Fatalf("waited %v on a frozen clock; want the watch to stop after 20 waits", e.slept)
					}
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCursorGatewayEnv(t, store.AgentCursor, nil)
			e.text = tc.text
			tc.gateway(t, e)

			err := e.send(api.SendKeysParams{InstanceID: e.r.ID, Text: e.text})

			if tc.reason != "" {
				assertGatewayRefused(t, err, len(tc.text), tc.reason, store.StateWaiting)
			} else if err != nil {
				t.Fatalf("send-keys: %v; want success", err)
			}
			if e.slept != tc.slept {
				t.Errorf("waited %v; want %v", e.slept, tc.slept)
			}
			if tc.text == "" {
				e.assertEnterOnly(t, e.r.Socket, e.r.Spawn.Identity.PaneID)
			} else {
				e.assertDelivered(t, e.r.Socket, e.r.Spawn.Identity.PaneID, tc.text)
			}
		})
	}
}

// TestSendKeysCursorPaneHashAfterAdmission (b.x9o, b.bqd): a Cursor row's text
// with Enter and a pane hash compares the hash again after input admission and
// before its first write (typeCursorText). A pane that changed after admission
// is ErrPaneChanged with nothing typed and no gateway watch; an unchanged pane
// gets the text and Enter once and the gateway's verdict.
func TestSendKeysCursorPaneHashAfterAdmission(t *testing.T) {
	t.Parallel()
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unchanged", true: "changed after admission"}[changed], func(t *testing.T) {
			t.Parallel()
			e := newCursorGatewayEnv(t, store.AgentCursor, nil)
			e.atInput(t, e.record(cursortranscript.KindSubmissionRefused))
			const pane = "caller inspected this pane\n"
			e.rec.SetCapture(e.r.Socket, e.r.Spawn.Identity.PaneID, pane)
			captures := 0
			e.rec.AfterCall(tmux.CallCapture, func(tmuxfix.SocketCall, error) { captures++ })
			e.rec.AfterCall(tmux.CallListPanes, func(tmuxfix.SocketCall, error) {
				// Admission makes no tmux call: a listing after the caller's hash
				// check is the post-admission target recheck before the first write.
				if changed && captures == 1 {
					e.rec.SetCapture(e.r.Socket, e.r.Spawn.Identity.PaneID, "a new dialog appeared\n")
				}
			})

			err := e.send(api.SendKeysParams{InstanceID: e.r.ID, Text: e.text, ExpectPaneSHA256: api.PaneSHA256(pane)})

			if changed {
				assertOneName(t, err, "ErrPaneChanged")
				e.assertNothingSent(t)
				if captures != 2 || e.slept != 0 {
					t.Fatalf("captures=%d, waited %v; want the post-admission capture refusing with no gateway watch", captures, e.slept)
				}
				return
			}
			assertGatewayRefused(t, err, len(e.text), cursorGatewayReason, store.StateWaiting)
			e.assertPaneCalls(t, paneHashSendCalls...)
			assertTextSent(t, e.rec, e.r.Socket, e.r.Spawn.Identity.PaneID, e.text)
			assertEnterSent(t, e.rec, e.r.Socket, e.r.Spawn.Identity.PaneID)
		})
	}
}

// TestSendKeysCursorGatewayNotWatched (b.bqd): send-keys keeps success with no
// wait, though the gateway records a refusal of the row's launch after the
// input, when the row's transcript cannot be opened as a protected gateway
// transcript, and for a send without Enter, a key, a pane answer and a Claude
// or Codex row.
func TestSendKeysCursorGatewayNotWatched(t *testing.T) {
	t.Parallel()
	cursor := store.AgentCursor
	plain := skaParams
	for _, tc := range []struct {
		name   string
		agent  string
		jsonl  func(t *testing.T, path string) string
		params func(killRow) api.SendKeysParams
	}{
		{name: "no jsonl_path", agent: cursor, params: plain,
			jsonl: func(*testing.T, string) string { return "" }},
		{name: "missing transcript", agent: cursor, params: plain,
			jsonl: func(_ *testing.T, path string) string {
				return filepath.Join(filepath.Dir(path), strings.ReplaceAll(uuid.NewString(), "-", "")+".jsonl")
			}},
		{name: "transcript outside the gateway's directory", agent: cursor, params: plain,
			jsonl: func(t *testing.T, path string) string {
				out := filepath.Join(t.TempDir(), filepath.Base(path))
				if err := os.WriteFile(out, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return out
			}},
		{name: "transcript not private", agent: cursor, params: plain,
			jsonl: func(t *testing.T, path string) string {
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			}},
		{name: "no_enter", agent: cursor,
			params: func(r killRow) api.SendKeysParams {
				return api.SendKeysParams{InstanceID: r.ID, Text: skaText, NoEnter: true}
			}},
		{name: "named key", agent: cursor,
			params: func(r killRow) api.SendKeysParams { return api.SendKeysParams{InstanceID: r.ID, Key: "Escape"} }},
		{name: "character key", agent: cursor,
			params: func(r killRow) api.SendKeysParams { return api.SendKeysParams{InstanceID: r.ID, Key: "y"} }},
		{name: "Claude row", agent: "claude", params: plain},
		{name: "Codex row", agent: "codex", params: plain},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newCursorGatewayEnv(t, tc.agent, tc.jsonl)
			e.atInput(t, e.record(cursortranscript.KindSubmissionRefused))

			if err := e.send(tc.params(e.r)); err != nil || e.slept != 0 {
				t.Fatalf("send-keys = %v after waiting %v; want success with no wait", err, e.slept)
			}
		})
	}
	// paEnv's Cursor row is read with the transcript as its jsonl_path.
	t.Run("Cursor pane answer", func(t *testing.T) {
		t.Parallel()
		p := newPAEnvAgent(t, cursor, "")
		path := cursorfix.NewGatewayHistory(t, p.r.ID, 1, "gateway-session", "earlier-session").Latest
		gatewayAfterInput(p.rec, func() {
			appendGateway(t, path, gatewayRecord(p.r.Token, cursortranscript.KindSubmissionRefused, "1", time.Time{}))
		})
		withPath := func(row api.Spawn) api.Spawn { row.JSONLPath = path; return row }
		// b.x9o: input admission rereads the row, so both reads carry the path.
		s := &nativeSubmissionStore{SendKeysStore: p.sks, Persistence: p.store, get: withPath,
			reread: func(row api.Spawn) (api.Spawn, error) { return withPath(row), nil }}
		var slept time.Duration
		env := api.SendKeysEnv{Relay: p.view(), Self: func() api.ProcessIdentity { return p.self }, IntentHold: p.hold,
			Sleep: func(d time.Duration) { slept += d; p.now = p.now.Add(d) }}

		if _, err := api.SendKeys(s, p.rec, env, p.answer(storefix.TestRequestTokenA, "allow", "1")); err != nil || slept != 0 {
			t.Fatalf("pane answer = %v after waiting %v; want success with no wait", err, slept)
		}
	})
}
