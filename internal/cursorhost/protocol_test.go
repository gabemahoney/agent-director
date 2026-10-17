package cursorhost

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func completedWrite(t *testing.T, p *protocol) error {
	t.Helper()
	select {
	case err := <-p.writes:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("no protocol write completion")
		return nil
	}
}
func nextFrame(t *testing.T, p *protocol) incoming {
	t.Helper()
	select {
	case f := <-p.frames:
		return f
	case <-time.After(time.Second):
		t.Fatal("no protocol frame")
		return incoming{}
	}
}
func TestProtocolFraming(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		refused   bool
	}{
		{"response", `{"jsonrpc":"2.0","id":1,"result":{}}` + "\n", false},
		{"question string id", `{"jsonrpc":"2.0","id":"question","method":"cursor/ask_question","params":{}}` + "\n", false},
		{"partial", `{"jsonrpc":"2.0","id":1,"result":{}}`, true},
		{"non json", "secret diagnostic\n", true},
		{"wrong protocol", `{"jsonrpc":"1.0","id":1,"result":{}}` + "\n", true},
		{"null id", `{"jsonrpc":"2.0","id":null,"result":{}}` + "\n", true},
		{"object id", `{"jsonrpc":"2.0","id":{},"result":{}}` + "\n", true},
		{"no outcome", `{"jsonrpc":"2.0","id":1}` + "\n", true},
		{"no response id", `{"jsonrpc":"2.0","result":{}}` + "\n", true},
		{"both outcomes", `{"jsonrpc":"2.0","id":1,"result":{},"error":{}}` + "\n", true},
		{"duplicate exact code", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"code":-32000,"message":"secret"}}` + "\n", true},
		{"duplicate exact message", `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"one","message":"two"}}` + "\n", true},
		{"duplicate exact capabilities", `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":null,"agentCapabilities":{}}}` + "\n", true},
		{"oversize", strings.Repeat("x", maxFrameBytes+1) + "\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newProtocol(&bytes.Buffer{}, strings.NewReader(tc.raw))
			t.Cleanup(p.close)
			got := nextFrame(t, p)
			if (got.err != nil) != tc.refused {
				t.Fatalf("frame err %v; refused %v", got.err, tc.refused)
			}
		})
	}
}

func TestProtocolRefusalDoesNotPreventNextRequest(t *testing.T) {
	for _, tc := range []string{"remote error", "unencodable request", "oversize request"} {
		t.Run(tc, func(t *testing.T) {
			var out bytes.Buffer
			p := newProtocol(&out, strings.NewReader(""))
			t.Cleanup(p.close)
			var err error
			switch tc {
			case "remote error":
				id, e := p.request("session/prompt", nil)
				if e != nil {
					t.Fatal(e)
				}
				if e := completedWrite(t, p); e != nil {
					t.Fatal(e)
				}
				_, err = p.response(frame{ID: json.RawMessage(id), Error: json.RawMessage(`{"message":"synthetic-secret"}`)})
				if err != nil && strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatal("remote error leaked raw runtime diagnostic")
				}
			case "unencodable request":
				_, err = p.request("session/prompt", make(chan int))
			case "oversize request":
				_, err = p.request("session/prompt", strings.Repeat("x", maxFrameBytes))
			}
			if err == nil {
				t.Fatal("failed request reported success")
			}
			if tc != "remote error" && out.Len() != 0 {
				t.Fatal("invalid request reached transport")
			}
			if _, err := p.request("session/new", nil); err != nil {
				t.Fatalf("next request blocked after refusal: %v", err)
			}
		})
	}
}

func TestAuthorizationNotificationAndTransportFailures(t *testing.T) {
	for _, notification := range []bool{true, false} {
		t.Run(map[bool]string{true: "notification", false: "broken reply"}[notification], func(t *testing.T) {
			p := newProtocol(failingWriter{}, strings.NewReader(""))
			t.Cleanup(p.close)
			f := frame{ID: json.RawMessage(`"request"`), Method: "session/request_permission", Params: json.RawMessage(`{"options":[]}`)}
			if notification {
				f.ID = nil
			}
			cancel, err := p.deny(f)
			if err != nil || cancel == notification {
				t.Fatalf("denial %v %v", cancel, err)
			}
			if !notification && completedWrite(t, p) == nil {
				t.Fatal("broken transport reported delivery")
			}
		})
	}
}

func TestProtocolCorrelationAndTransport(t *testing.T) {
	var out bytes.Buffer
	p := newProtocol(&out, strings.NewReader(""))
	t.Cleanup(p.close)
	id, err := p.request("initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.request("session/new", nil); err == nil {
		t.Fatal("accepted overlapping request")
	}
	if _, err = p.response(frame{ID: json.RawMessage(`"1"`), Result: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted wrong id type")
	}
	if _, err = p.response(frame{ID: json.RawMessage(id), Result: json.RawMessage(`{"protocolVersion":1}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err = p.response(frame{ID: json.RawMessage(id), Result: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("accepted duplicate response")
	}
	broken := newProtocol(failingWriter{}, strings.NewReader(""))
	t.Cleanup(broken.close)
	if _, err = broken.request("session/prompt", nil); err != nil {
		t.Fatal(err)
	}
	if err = completedWrite(t, broken); err == nil {
		t.Fatal("closed transport reported success")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, bytes.ErrTooLarge }
func TestAuthorizationAlwaysDenied(t *testing.T) {
	for _, tc := range []struct {
		name, method, params, outcome string
		cancel                        bool
	}{
		{"direct question", "cursor/ask_question", `{"questions":[{"text":"Continue?"}]}`, "cancelled", false},
		{"multiple question", "cursor/ask_question", `{"questions":[{},{}]}`, "cancelled", false},
		{"plan", "cursor/create_plan", `{}`, "rejected", false},
		{"reject option", "session/request_permission", `{"options":[{"optionId":"allow","kind":"allow_once"},{"optionId":"deny","kind":"reject_once"}]}`, "selected", false},
		{"question fallback", "session/request_permission", `{"toolCall":{"title":"AskUserQuestion"},"options":[{"optionId":"yes","kind":"allow_once"}]}`, "cancelled", true},
		{"no choice", "session/request_permission", `{"options":[]}`, "cancelled", true},
		{"unknown", "cursor/authorize", `{}`, "", true},
		{"duplicate allow reject", "session/request_permission", `{"options":[{"optionId":"same","kind":"allow_once"},{"optionId":"same","kind":"reject_once"}]}`, "cancelled", true},
		{"duplicate reject allow", "session/request_permission", `{"options":[{"optionId":"same","kind":"reject_once"},{"optionId":"same","kind":"allow_once"}]}`, "cancelled", true},
		{"duplicate after reject", "session/request_permission", `{"options":[{"optionId":"deny","kind":"reject_once"},{"optionId":"x","kind":"allow_once"},{"optionId":"x","kind":"allow_always"}]}`, "cancelled", true},
		{"empty id", "session/request_permission", `{"options":[{"optionId":"deny","kind":"reject_once"},{"optionId":"","kind":"allow_once"}]}`, "cancelled", true},
		{"unknown kind", "session/request_permission", `{"options":[{"optionId":"deny","kind":"reject_once"},{"optionId":"x","kind":"future"}]}`, "cancelled", true},
		{"malformed options", "session/request_permission", `{"options":"bad"}`, "cancelled", true},
		{"malformed option id", "session/request_permission", `{"options":[{"optionId":7,"kind":"reject_once"}]}`, "cancelled", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			p := newProtocol(&out, strings.NewReader(""))
			t.Cleanup(p.close)
			cancel, err := p.deny(frame{ID: json.RawMessage(`"request-7"`), Method: tc.method, Params: json.RawMessage(tc.params)})
			if err != nil || cancel != tc.cancel {
				t.Fatalf("deny = %v %v", cancel, err)
			}
			if err := completedWrite(t, p); err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err = json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if string(got["id"]) != `"request-7"` {
				t.Fatal("lost request identity")
			}
			if tc.outcome == "" {
				if len(got["error"]) == 0 {
					t.Fatal("unknown method authorized")
				}
			} else if !bytes.Contains(got["result"], []byte(`"outcome":"`+tc.outcome+`"`)) {
				t.Fatalf("outcome %s", got["result"])
			}
			if (tc.outcome == "cancelled" && bytes.Contains(got["result"], []byte(`"optionId"`))) || bytes.Contains(got["result"], []byte(`"optionId":"allow"`)) {
				t.Fatal("selected allow")
			}
		})
	}
}

func TestProtocolClosedTransportRejectsEveryEnqueue(t *testing.T) {
	p := newProtocol(&bytes.Buffer{}, strings.NewReader(""))
	p.close()
	// Every call must refuse: a ready queue must not compete with closed authority.
	for i := 0; i < 32; i++ {
		if err := p.sendGuarded(map[string]int{"reply": i}, time.Now().Add(time.Second), nil, nil); err == nil {
			t.Fatalf("closed transport accepted reply %d without a completion/failure path", i)
		}
	}
}

type blockedProtocolWriter struct {
	io.WriteCloser
	entered chan struct{}
	calls   atomic.Int32
}

func (w *blockedProtocolWriter) Write(raw []byte) (int, error) {
	if w.calls.Add(1) == 1 {
		close(w.entered)
	}
	return w.WriteCloser.Write(raw)
}

func TestProtocolCloseCompletesAcceptedFramesExactlyOnce(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "active and queued", true: "concurrent enqueue"}[concurrent], func(t *testing.T) {
			reader, writer := io.Pipe()
			defer reader.Close()
			wire := &blockedProtocolWriter{WriteCloser: writer, entered: make(chan struct{})}
			p := newProtocol(wire, strings.NewReader(""))
			defer p.close()
			type completion struct {
				id        int
				err       error
				reentered error
			}
			const count = 18
			done := make(chan completion, count*2)
			accepted := make([]bool, count)
			enqueue := func(id int) {
				err := p.sendGuarded(map[string]int{"reply": id}, time.Now().Add(time.Second), nil, func(err error) {
					// A callback can reenter admission; retirement must not hold its lock.
					done <- completion{id, err, p.send(map[string]int{"retry": id})}
				})
				accepted[id] = err == nil
			}
			enqueue(0)
			select {
			case <-wire.entered:
			case <-time.After(time.Second):
				t.Fatal("active frame did not reach transport")
			}
			enqueue(1)
			if !accepted[0] || !accepted[1] {
				t.Fatal("active and queued frames were not accepted")
			}
			if concurrent {
				start := make(chan struct{})
				var workers sync.WaitGroup
				for id := 2; id < count; id++ {
					workers.Add(1)
					go func(id int) { defer workers.Done(); <-start; enqueue(id) }(id)
				}
				workers.Add(1)
				go func() { defer workers.Done(); <-start; p.close() }()
				close(start)
				workers.Wait()
			} else {
				p.close()
			}
			want := 0
			for _, ok := range accepted {
				if ok {
					want++
				}
			}
			seen := make(map[int]bool)
			for range want {
				select {
				case got := <-done:
					if !accepted[got.id] || seen[got.id] || got.err == nil || got.reentered == nil {
						t.Fatalf("invalid completion %+v, accepted %v, seen %v", got, accepted[got.id], seen[got.id])
					}
					seen[got.id] = true
				case <-time.After(time.Second):
					t.Fatalf("completed %d of %d accepted frames", len(seen), want)
				}
			}
			select {
			case extra := <-done:
				t.Fatalf("duplicate or unaccepted completion %+v", extra)
			case <-time.After(20 * time.Millisecond):
			}
			if wire.calls.Load() != 1 {
				t.Fatalf("closed transport replayed queued frames: %d writes", wire.calls.Load())
			}
		})
	}
}

func TestProtocolStructuredFailureCauses(t *testing.T) {
	for _, method := range []string{"initialize", "session/new", "session/load", "session/set_mode", "session/prompt"} {
		for _, tc := range []struct{ code, want string }{
			{"-32601", "operation unsupported"}, {"-32700", "format or parameters"}, {"-32600", "format or parameters"}, {"-32602", "format or parameters"},
			{"-32603", "internal error; cause unknown"}, {"-32800", "cancelled"}, {"-32000", "authentication required"}, {"-32002", "resource not found"},
			{"99", "cause unknown"}, {"null", "malformed"}, {"1.5", "malformed"}, {"2147483648", "malformed"}, {`"-32000"`, "malformed"},
		} {
			t.Run(method+"/"+tc.code, func(t *testing.T) {
				p := newProtocol(&bytes.Buffer{}, strings.NewReader(""))
				defer p.close()
				id, err := p.request(method, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = completedWrite(t, p); err != nil {
					t.Fatal(err)
				}
				raw := json.RawMessage(`{"code":` + tc.code + `,"message":"SYNTHETIC-SECRET\nlogin required\u001b","data":{"secret":"SYNTHETIC-SECRET"}}`)
				_, err = p.response(frame{ID: json.RawMessage(id), Error: raw})
				if err == nil || !strings.Contains(err.Error(), method+" failed:") || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "SYNTHETIC-SECRET") || strings.ContainsAny(err.Error(), "\n\r\x1b") {
					t.Fatalf("failure=%v", err)
				}
				if _, err = p.response(frame{ID: json.RawMessage(id), Error: raw}); err == nil {
					t.Fatal("replayed failure accepted")
				}
				if _, err = p.request("session/new", nil); err != nil {
					t.Fatalf("failure did not retire pending request: %v", err)
				}
			})
		}
	}
}

func TestProtocolFailureExactKeys(t *testing.T) {
	for _, field := range []struct {
		name, valid, other string
		invalid            []string
	}{
		{"code", `-32603`, `"message":"SYNTHETIC-SECRET"`, []string{"", "null", `"-32000"`, `1.5`, `2147483648`}},
		{"message", `"SYNTHETIC-SECRET"`, `"code":-32603`, []string{"", "null", "1", "{}"}},
	} {
		for _, alias := range []string{strings.ToUpper(field.name), strings.ToUpper(field.name[:1]) + field.name[1:]} {
			for _, reverse := range []bool{false, true} {
				for _, exact := range append(field.invalid, field.valid) {
					variant := field.valid
					if exact == field.valid {
						variant = `null`
						if field.name == "code" {
							variant = `-32000`
						}
					}
					members := `"` + alias + `":` + variant
					if exact != "" {
						member := `"` + field.name + `":` + exact
						if reverse {
							members += "," + member
						} else {
							members = member + "," + members
						}
					}
					raw := json.RawMessage("{" + field.other + "," + members + "}")
					err := acpResponseFailure("initialize", raw)
					want := "malformed"
					if exact == field.valid {
						want = "internal error; cause unknown"
					}
					if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "authentication required") || strings.Contains(err.Error(), "SYNTHETIC-SECRET") {
						t.Fatalf("%s reverse=%v exact=%s: %v", alias, reverse, exact, err)
					}
				}
				members := `"code":-32000,"` + alias + `":-32603`
				if field.name == "code" {
					if reverse {
						members = `"` + alias + `":-32603,"code":-32000`
					}
					err := acpResponseFailure("initialize", json.RawMessage(`{"message":"secret",`+members+`}`))
					if !strings.Contains(err.Error(), "authentication required") {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

func TestProtocolUnknownOperationCannotSupplyDiagnostic(t *testing.T) {
	err := acpResponseFailure("SYNTHETIC-SECRET\n\x1b", json.RawMessage(`{"code":99,"message":"login required"}`))
	if err.Error() != "Cursor ACP request failed: runtime rejected the request; cause unknown" {
		t.Fatal(err)
	}
}
