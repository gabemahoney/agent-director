//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorQuestionPolicyPreservesIndependentHold(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	// Question signatures are source-derived; malformed variants and policy combinations are synthetic.
	cases := []struct {
		name, method string
		params       func(string) []byte
	}{
		{"direct", "cursor/ask_question", func(string) []byte {
			return []byte(`{"toolCallId":"AskQuestion","title":"Continue","questions":[{"id":"q1","prompt":"Continue?","options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}],"allowMultiple":false}]}`)
		}},
		{"multiple", "cursor/ask_question", func(string) []byte {
			return []byte(`{"toolCallId":"AskQuestion","questions":[{"id":"q1","prompt":"One","options":[{"id":"yes","label":"Yes"}],"allowMultiple":false},{"id":"q2","prompt":"Two","options":[{"id":"yes","label":"Yes"}],"allowMultiple":false}]}`)
		}},
		{"multiselect", "cursor/ask_question", func(string) []byte {
			return []byte(`{"toolCallId":"AskQuestion","questions":[{"id":"q1","prompt":"Pick","allowMultiple":true,"options":[{"id":"yes","label":"Yes"},{"id":"no","label":"No"}]}]}`)
		}},

		{"source-derived fallback", "session/request_permission", func(s string) []byte {
			return []byte(fmt.Sprintf(`{"sessionId":%q,"toolCall":{"toolCallId":"AskQuestion_q0","title":"Continue?","kind":"other","status":"pending","content":[{"type":"content","content":{"type":"text","text":"Continue?"}}]},"options":[{"optionId":"yes","name":"Yes","kind":"allow_once"},{"optionId":"no","name":"No","kind":"allow_once"},{"optionId":"__ask_question_skip__","name":"Skip","kind":"reject_once"}]}`, s))
		}},
		{"fallback tool ID", "session/request_permission", func(s string) []byte { return cursorfix.Permission(s, "call_q1", "execute") }},
		{"fallback AskQuestion", "session/request_permission", func(s string) []byte { return cursorfix.Permission(s, "AskQuestion-7", "execute") }},
		{"fallback ask prefix", "session/request_permission", func(s string) []byte { return cursorfix.Permission(s, "ask_7", "execute") }},
		{"fallback skip", "session/request_permission", func(s string) []byte {
			return bytes.Replace(cursorfix.Permission(s, "fallback", "execute"), []byte(`"allow-always"`), []byte(`"__ask_question_skip__"`), 1)
		}},
		{"multiple allow once", "session/request_permission", func(s string) []byte {
			return bytes.Replace(cursorfix.Permission(s, "fallback", "execute"), []byte(`"allow_always"`), []byte(`"allow_once"`), 1)
		}},
		{"ambiguous other", "session/request_permission", func(s string) []byte { return cursorfix.Permission(s, "fallback", "other") }},
		{"malformed options", "session/request_permission", func(string) []byte { return []byte(`{"toolCall":{"title":"AskUserQuestion"},"options":"unknown"}`) }},
	}
	for _, relay := range []string{"on", "off"} {
		for _, disabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("relay_%s/disabled_%v", relay, disabled), func(t *testing.T) {
				e := newRequestEnv(t, relay, "synthetic-session")
				e.rep.cfg.Defaults.DisableAskUser = disabled
				wire := &observedWire{}
				var pane bytes.Buffer
				c := e.controller(t, wire, &pane)
				hold := e.admit(t, c, `"genuine"`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "genuine", "execute"))
				// Enabling policies are opaque supplied input, not admission authority.
				for i, tc := range cases {
					t.Run(tc.name, func(t *testing.T) {
						raw := tc.params(e.binding.SessionID)
						if tc.method == "session/request_permission" {
							raw = bytes.Replace(raw, []byte(`"rawInput":null`), []byte(`"rawInput":{"policy":"allow","permissionMode":"bypassPermissions","allow":true}`), 1)
						}
						f := frame{ID: []byte(fmt.Sprintf(`"question-%d"`, i)), Method: tc.method, Params: raw}
						err := c.admit(f, e.binding.SessionID)
						var policy policyRejection
						if !errors.As(err, &policy) {
							t.Fatalf("question did not reach policy boundary: %v", err)
						}
						if len(c.requests) != 1 || len(c.menu) != 3 {
							t.Fatal("question acquired pane authority")
						}
						cancel, err := c.protocol.deny(f)
						if err != nil {
							t.Fatal(err)
						}
						if err = completedWrite(t, c.protocol); err != nil {
							t.Fatal(err)
						}
						if tc.method == "cursor/ask_question" && cancel {
							t.Fatal("supported direct cancellation would cancel independent requests")
						}
						if cancel {
							c.cancel()
						}
						// Wire rejections can select a reject option or cancel; never an allow.
						for _, line := range bytes.Split(bytes.TrimSpace(wire.bytes()), []byte{'\n'}) {
							var reply struct {
								Result struct {
									Outcome struct{ Outcome, OptionID string }
								}
							}
							if err = json.Unmarshal(line, &reply); err != nil {
								t.Fatal(err)
							}
							if strings.HasPrefix(reply.Result.Outcome.OptionID, "allow") || reply.Result.Outcome.Outcome == "accepted" || reply.Result.Outcome.Outcome == "" {
								t.Fatalf("question authorized or unsupported method-only rejection: %s", line)
							}
						}
						pr := e.request(t, hold.token)
						if pr.Closed() || pr.ProvenGone() || pr.Decision != "" || !pr.DeliveredAt.IsZero() {
							t.Fatal("question denial cleared independent durable hold")
						}
					})
				}

				token, err := randomID()
				if err != nil {
					t.Fatal(err)
				}
				zero := int64(0)
				_, err = api.Decide(e.s, api.RelayView{}, api.DecideParams{InstanceID: e.binding.InstanceID, RequestToken: token, Decision: "allow", MaxWaitMs: &zero})
				want := store.ErrNoOpenPermissionRequest
				if relay == "off" {
					want = api.ErrRelayModeOff
				}
				if !errors.Is(err, want) {
					t.Fatalf("allow attempt acquired question authority: %v", err)
				}
				rows, err := e.s.PermissionRequestsForSpawn(e.binding.InstanceID)
				if err != nil || len(rows) != 1 {
					t.Fatalf("question rows inserted: %+v %v", rows, err)
				}
			})
		}
	}
}

func TestCursorCompetingDecisionsStayRequestScoped(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, first := range []string{"API allow", "API deny", "native allow", "native deny", "timeout"} {
		t.Run(first, func(t *testing.T) {
			e := newRequestEnv(t, "on", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			permission := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
			plan := e.admit(t, c, `"1"`, "cursor/create_plan", []byte(`{"toolCallId":"plan","name":"Plan","overview":"Overview","plan":"Steps"}`))
			switch first {
			case "API allow", "API deny":
				e.decide(t, permission.token, strings.TrimPrefix(strings.ToLower(first), "api "))
				c.choose(permission.token, "allow-always")
			case "native allow":
				c.choose(permission.token, "allow-once")
			case "native deny":
				c.choose(permission.token, "reject-once")
			case "timeout":
				permission.timeout = time.Now().Add(-time.Millisecond)
			}
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			if err := completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}
			before := wire.bytes()
			c.choose(permission.token, "allow-always")
			c.tick()
			if !bytes.Equal(before, wire.bytes()) || bytes.Count(before, []byte{'\n'}) != 1 {
				t.Fatalf("request answered more than once: %s", wire.bytes())
			}
			pr := e.request(t, plan.token)
			if pr.Decision != "" || pr.PaneAnswered() || pr.ProvenGone() {
				t.Fatal("plan inherited permission decision")
			}
			c.choose(plan.token, "reject")
			if err := c.tick(); err != nil {
				t.Fatal(err)
			}
			if err := completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(wire.bytes()[len(before):], []byte(`"id":"1"`)) || !bytes.Contains(wire.bytes()[len(before):], []byte(`"outcome":"rejected"`)) {
				t.Fatalf("plan correlated incorrectly: %s", wire.bytes())
			}
		})
	}
}

func TestCursorConcurrentAPIPaneArbitration(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH; store writers race on their own connections.
	e := newRequestEnv(t, "on", "synthetic-session")
	wire := &observedWire{}
	pane := &observedWire{}
	c := e.controller(t, wire, pane)
	r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "shell", "execute"))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("request after arbitration: %+v; queued=%v terminal=%v sent=%v; wire=%s", e.request(t, r.token), r.queued, r.terminal.Load(), r.sent.Load(), wire.bytes())
		}
	})
	other := e.admit(t, c, `2`, "cursor/create_plan", []byte(`{"toolCallId":"plan","name":"Plan","overview":"Overview","plan":"Steps"}`))
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, verdict := range []string{"allow", "deny"} {
		go func(d string) {
			<-start
			_, err := e.s.DecideRelayRequest(e.binding.InstanceID, r.token, d, store.DecisionReasonOperator, store.WriterProcessDecide, time.Time{}, time.Second)
			results <- err
		}(verdict)
	}
	c.choose(r.token, "reject-once")
	close(start)
	if err := c.tick(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err := c.tick(); err != nil {
		t.Fatal(err)
	}
	// A competing transaction may make the final authority check fail closed.
	// Guard rejection completes through the request callback, without a pipe write.
	until := time.Now().Add(4 * time.Second)
	for !r.sent.Load() && (!r.terminal.Load() || !bytes.Contains(pane.bytes(), []byte("response failed or retired"))) && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if !r.sent.Load() && !r.terminal.Load() {
		t.Fatal("accepted response has no success or failure completion")
	}
	winning := e.request(t, r.token)
	if winning.Decision != "allow" && winning.Decision != "deny" {
		t.Fatalf("arbitration recorded no winning verdict: %+v", winning)
	}
	if r.sent.Load() {
		if err := completedWrite(t, c.protocol); err != nil {
			t.Fatal(err)
		}
		want := "reject-once"
		if winning.Decision == "allow" {
			want = "allow-once"
		}
		if bytes.Count(wire.bytes(), []byte{'\n'}) != 1 || !bytes.Contains(wire.bytes(), []byte(`"optionId":"`+want+`"`)) {
			t.Fatalf("reply disagrees with first writer: %s", wire.bytes())
		}
	} else if len(wire.bytes()) != 0 || !bytes.Contains(pane.bytes(), []byte("response failed or retired")) {
		t.Fatal("failed final authority check wrote bytes or hid its failure")
	}
	if winning.DeliveredAt.IsZero() && winning.PaneAnswer != store.PaneAnswerSent {
		t.Fatal("response acquired neither durable relay ack nor native claim")
	}
	before := wire.bytes()
	c.tick()
	c.choose(r.token, "allow-always")
	c.tick()
	if !bytes.Equal(before, wire.bytes()) {
		t.Fatalf("competing writers replayed their answer: %s", wire.bytes())
	}
	if pr := e.request(t, other.token); pr.Decision != "" || pr.ProvenGone() {
		t.Fatal("race touched independent plan")
	}
}
