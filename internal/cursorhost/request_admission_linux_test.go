//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

func TestCursorCapturedAdmission(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, tc := range cursorfix.Authorizations(t) {
		t.Run(tc.Name, func(t *testing.T) {
			var params map[string]json.RawMessage
			json.Unmarshal(tc.Params, &params)
			var session string
			json.Unmarshal(params["sessionId"], &session)
			if session == "" {
				session = "plan-session"
			}
			e := newRequestEnv(t, "on", session)
			wire := &observedWire{}
			var pane bytes.Buffer
			c := e.controller(t, wire, &pane)
			r := e.admit(t, c, string(tc.ID), tc.Method, tc.Params)
			got, err := api.GetPermission(e.s, api.RelayView{}, api.GetPermissionParams{RequestToken: r.token})
			if err != nil {
				t.Fatal(err)
			}
			if got.ToolInput != string(tc.Params) {
				t.Fatalf("params changed: %s", got.ToolInput)
			}
			pr := e.request(t, r.token)
			if pr.MainTurnEndEligible || pr.AgentID != "" || pr.Hook != (store.ProcessIdentity{}) || pr.SettledAt.IsZero() || !pr.DeliveredAt.IsZero() {
				t.Fatalf("invented authority/evidence: %+v", pr)
			}
			if !strings.Contains(pane.String(), pr.ToolName) {
				t.Fatal("actual title absent from pane")
			}
			if err = c.tick(); err != nil {
				t.Fatal(err)
			}
			if len(wire.bytes()) != 0 {
				t.Fatal("request answered before decision")
			}
			e.decide(t, r.token, "allow")
			if err = c.tick(); err != nil {
				t.Fatal(err)
			}
			if err = completedWrite(t, c.protocol); err != nil {
				t.Fatal(err)
			}
			want := `"optionId":"allow-once"`
			if tc.Method == "cursor/create_plan" {
				want = `"outcome":"accepted"`
			}
			if !bytes.Contains(wire.bytes(), []byte(want)) || bytes.Contains(wire.bytes(), []byte(`"allow-always"`)) {
				t.Fatalf("wrong API option: %s", wire.bytes())
			}
		})
	}
}

func TestCursorSyntheticAdmissionPreservesOpaqueFields(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	c := e.controller(t, &observedWire{}, io.Discard)
	for i, kind := range []string{"read", "edit", "delete", "search", "fetch", "execute"} {
		raw := cursorfix.Permission(e.binding.SessionID, fmt.Sprint("tool-", i), kind)
		raw = bytes.Replace(raw, []byte(`"rawInput":null`), []byte(`"rawInput": { "path":null, "nested":[1,true,"多行"], "absentIsDifferent":{} }, "rawOutput":null,"locations":[],"content":null`), 1)
		r := e.admit(t, c, fmt.Sprint(i), "session/request_permission", raw)
		if got := e.request(t, r.token); got.ToolInput != string(raw) || got.ToolUseID != fmt.Sprint("tool-", i) {
			t.Fatalf("opaque envelope changed: %+v", got)
		}
	}
	for i, optional := range []string{"", `,"todos":null,"isProject":null,"phases":null`} {
		raw := []byte(fmt.Sprintf(`{ "toolCallId":"plan-%d", "name":"Plan name", "overview":"Overview", "plan":"Actual plan"%s }`, i, optional))
		r := e.admit(t, c, fmt.Sprintf(`"plan-%d"`, i), "cursor/create_plan", raw)
		if got := e.request(t, r.token); got.ToolInput != string(raw) {
			t.Fatalf("plan absence/null changed: %s", got.ToolInput)
		}
	}
}

func TestCursorAdmissionIdentityAndBounds(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	for _, mode := range []string{"numeric and string", "equivalent number", "active duplicate", "answered reuse", "tool duplicate", "active bound", "connection bound"} {
		t.Run(mode, func(t *testing.T) {
			e := newRequestEnv(t, "off", "synthetic-session")
			wire := &observedWire{}
			c := e.controller(t, wire, io.Discard)
			r := e.admit(t, c, `1`, "session/request_permission", cursorfix.Permission(e.binding.SessionID, "first", "execute"))
			id := `1`
			tool := "second"
			want := false
			switch mode {
			case "numeric and string":
				id = `"1"`
				want = true
			case "equivalent number":
				id = `1e0`
			case "answered reuse":
				c.choose(r.token, "reject-once")
				c.tick()
				if err := completedWrite(t, c.protocol); err != nil {
					t.Fatal(err)
				}
			case "tool duplicate":
				id = `2`
				tool = "first"
			case "active bound":
				for i := 2; i <= 32; i++ {
					e.admit(t, c, fmt.Sprint(i), "session/request_permission", cursorfix.Permission(e.binding.SessionID, fmt.Sprint("tool", i), "execute"))
				}
				id = `33`
			case "connection bound":
				for i := 2; i <= 4096; i++ {
					c.seenIDs[fmt.Sprint("s:old", i)] = true
				}
				id = `"next"`
			}
			before := len(c.requests)
			err := c.admit(frame{ID: []byte(id), Method: "session/request_permission", Params: cursorfix.Permission(e.binding.SessionID, tool, "execute")}, e.binding.SessionID)
			if (err == nil) != want {
				t.Fatalf("admission %v; expected success %v", err, want)
			}
			if !want && len(c.requests) != before {
				t.Fatal("duplicate replaced active request")
			}
		})
	}
}

func TestCursorMalformedAdmission(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	cases := []struct {
		name   string
		change func(string) string
	}{
		{"duplicate nested", func(s string) string {
			return strings.Replace(s, `"kind":"allow_once"`, `"kind":"allow_always","kind":"allow_once"`, 1)
		}},
		{"duplicate IDs", func(s string) string { return strings.Replace(s, `"allow-always"`, `"allow-once"`, 1) }},
		{"duplicate kind", func(s string) string { return strings.Replace(s, `"allow_always"`, `"allow_once"`, 1) }},
		{"no once approval", func(s string) string { return strings.Replace(s, `"kind":"allow_once"`, `"kind":"reject_always"`, 1) }},
		{"no once rejection", func(s string) string { return strings.Replace(s, `"kind":"reject_once"`, `"kind":"reject_always"`, 1) }},
		{"missing name", func(s string) string { return strings.Replace(s, `"name":"Allow once",`, ``, 1) }},
		{"unknown option field", func(s string) string {
			return strings.Replace(s, `"name":"Allow once"`, `"name":"Allow once","future":true`, 1)
		}},
		{"unsupported kind", func(s string) string { return strings.Replace(s, `"allow_always"`, `"future"`, 1) }},
		{"numeric option", func(s string) string { return strings.Replace(s, `"optionId":"allow-once"`, `"optionId":7`, 1) }},
		{"stale session", func(s string) string {
			return strings.Replace(s, `"sessionId":"synthetic-session"`, `"sessionId":"old"`, 1)
		}},
		{"unknown title shape", func(s string) string { return strings.Replace(s, `"kind":"execute"`, `"kind":"other"`, 1) }},
		{"nonpending", func(s string) string { return strings.Replace(s, `"status":"pending"`, `"status":"completed"`, 1) }},
	}
	e := newRequestEnv(t, "on", "synthetic-session")
	wire := &observedWire{}
	c := e.controller(t, wire, io.Discard)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := tc.change(string(cursorfix.Permission(e.binding.SessionID, fmt.Sprint("bad", i), "execute")))
			start := time.Now()
			err := c.admit(frame{ID: []byte(fmt.Sprint(i)), Method: "session/request_permission", Params: []byte(raw)}, e.binding.SessionID)
			if err == nil || time.Since(start) > time.Second {
				t.Fatalf("malformed not bounded: %v", err)
			}
		})
	}
	rows, err := e.s.PermissionRequestsForSpawn(e.binding.InstanceID)
	if err != nil || len(rows) != 0 || len(wire.bytes()) != 0 || c.open() {
		t.Fatalf("malformed request admitted/answered: %+v %v", rows, err)
	}
}

func TestCursorUnsupportedIDsNeverAdmit(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "on", "synthetic-session")
	wire := &observedWire{}
	c := e.controller(t, wire, io.Discard)
	for _, id := range []string{`null`, `true`, `{}`, `[]`, `0.5`, `9007199254740992`, `"` + strings.Repeat("x", 513) + `"`} {
		t.Run(id[:min(len(id), 24)], func(t *testing.T) {
			if err := c.admit(frame{ID: []byte(id), Method: "session/request_permission", Params: cursorfix.Permission(e.binding.SessionID, "shell", "execute")}, e.binding.SessionID); err == nil {
				t.Fatal("unsupported id admitted")
			}
		})
	}
	if len(c.requests) != 0 || len(wire.bytes()) != 0 {
		t.Fatal("bad IDs created answerable requests")
	}
}

func TestCursorMalformedPlansNeverAdmit(t *testing.T) {
	// Serial: shared verified pane fixture sets HOME and PATH.
	e := newRequestEnv(t, "on", "synthetic-session")
	wire := &observedWire{}
	c := e.controller(t, wire, io.Discard)
	for i, fields := range []string{
		`"name":"Plan","overview":"Overview"`,
		`"name":null,"overview":"Overview","plan":"Steps"`,
		`"name":"Plan","plan":"Steps"`,
		`"name":"Plan","overview":"Overview","plan":[]`,
		`"name":"Plan","overview":"Overview","plan":"Steps","todos":{}`,
		`"name":"Plan","overview":"Overview","plan":"Steps","phases":{}`,
		`"name":"Plan","overview":"Overview","plan":"Steps","isProject":"false"`,
		`"name":"Plan","overview":"Overview","plan":"Steps","sessionId":"synthetic-session"`,
		`"name":"Plan","overview":"Overview","plan":"Steps","options":[]`,
		`"name":"Plan","name":"Replaced","overview":"Overview","plan":"Steps"`,
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			raw := []byte(`{"toolCallId":"plan",` + fields + `}`)
			if err := c.admit(frame{ID: []byte(fmt.Sprint(i)), Method: "cursor/create_plan", Params: raw}, e.binding.SessionID); err == nil {
				t.Fatalf("malformed/invented plan admitted: %s", raw)
			}
		})
	}
	rows, err := e.s.PermissionRequestsForSpawn(e.binding.InstanceID)
	if err != nil || len(rows) != 0 || len(wire.bytes()) != 0 {
		t.Fatalf("malformed plan created response authority: %+v %v", rows, err)
	}
}
