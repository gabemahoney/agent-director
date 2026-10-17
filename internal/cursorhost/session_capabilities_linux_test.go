//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
)

func TestCursorInitializeRefusesBeforeDependentWork(t *testing.T) {
	for _, reply := range []string{
		`{"result":{"protocolVersion":1}}`,
		`{"result":{"protocolVersion":2,"agentCapabilities":{}}}`,
		`{"result":{"protocolVersion":1,"agentCapabilities":null,"AGENTCAPABILITIES":{}}}`,
		`{"result":{"protocolVersion":null,"PROTOCOLVERSION":1,"agentCapabilities":{}}}`,
		`{"error":{"code":-32000,"message":"SYNTHETIC-SECRET\n\u001b","data":"SYNTHETIC-SECRET"}}`,
	} {
		for _, load := range []bool{false, true} {
			t.Run(reply+map[bool]string{false: "/fresh", true: "/load"}[load], func(t *testing.T) {
				scenario := cursorfix.Scenario{InitializeReply: json.RawMessage(reply), Modes: advertisedCursorModes()}
				input := &completeInput{ready: make(chan struct{})}
				var c *cursorSession
				if load {
					c = beginCursorLoadWithOptions(t, scenario, Options{InitialPrompt: "never dispatch", RequestedMode: "plan"}, input.start, nil)
				} else {
					c = beginCursorSessionWithOptions(t, Options{InitialPrompt: "never dispatch", RequestedMode: "plan"}, scenario, openTranscript, input.start)
				}
				err := c.awaitFailure(t, 3*time.Second)
				if strings.Contains(err.Error(), "SYNTHETIC-SECRET") || strings.ContainsAny(err.Error(), "\n\r\x1b") {
					t.Fatalf("unsafe diagnostic: %q", err)
				}
				if input.admit() || bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) {
					t.Fatal("invalid initialize published readiness/admission")
				}
				for _, f := range c.e.runtime.Frames(t) {
					if string(f["method"]) != `"initialize"` {
						t.Fatalf("dependent work escaped: %s", f["method"])
					}
				}
				for _, r := range c.records(t) {
					if r.Kind == "session_start" || r.Kind == "session_loaded" || r.Kind == "submission_dispatched" || r.Kind == "turn_outcome" {
						t.Fatalf("invalid initialize published %s", r.Kind)
					}
				}
				requests, err := c.e.s.PermissionRequestsForSpawn(c.e.binding.InstanceID)
				if err != nil || len(requests) != 0 {
					t.Fatalf("initialize granted permission authority: %+v %v", requests, err)
				}
			})
		}
	}
}

func TestCursorMinimalCapabilitiesAllowFreshPrompt(t *testing.T) {
	for _, caps := range []string{`{}`, `{"loadSession":false}`, `{"sessionCapabilities":{"fork":true,"systemPrompt":[]}}`} {
		t.Run(caps, func(t *testing.T) {
			c := beginCursorSessionWithOptions(t, Options{InitialPrompt: "minimal prompt"}, cursorfix.Scenario{InitializeReply: json.RawMessage(`{"result":{"protocolVersion":1,"PROTOCOLVERSION":null,"agentCapabilities":` + caps + `,"AGENTCAPABILITIES":null}}`)}, openTranscript, terminalLoop)
			c.wait(t, "minimal baseline prompt completed", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor turn complete")) })
			c.stop(t)
			if got := c.prompts(t); len(got) != 1 || got[0] != "minimal prompt" {
				t.Fatalf("minimal prompt: %v", got)
			}
		})
	}
}

func TestCursorCorrelatedOperationFailureSanitized(t *testing.T) {
	reply := json.RawMessage(`{"error":{"code":-32603,"CODE":-32000,"message":"SYNTHETIC-SECRET\n\u001b login required","data":{"token":"SYNTHETIC-SECRET"}}}`)
	for _, operation := range []string{"session/new", "session/load", "session/set_mode", "session/prompt"} {
		t.Run(operation, func(t *testing.T) {
			scenario := cursorfix.Scenario{Modes: advertisedCursorModes()}
			options := Options{InitialPrompt: "initial"}
			switch operation {
			case "session/new":
				scenario.FirstNewReply = reply
			case "session/load":
				scenario.LoadSupport = true
				scenario.LoadReply = reply
			case "session/set_mode":
				options.RequestedMode = "plan"
				scenario.ModeReply = reply
			case "session/prompt":
				scenario.PromptReply = reply
			}
			var c *cursorSession
			if operation == "session/load" {
				c = beginCursorLoad(t, scenario, "initial", terminalLoop)
			} else {
				c = beginCursorSessionWithOptions(t, options, scenario, openTranscript, terminalLoop)
			}
			err := c.awaitFailure(t, 3*time.Second)
			if !strings.Contains(err.Error(), operation+" failed:") || !strings.Contains(err.Error(), "internal error; cause unknown") || strings.Contains(err.Error(), "SYNTHETIC-SECRET") {
				t.Fatalf("wrong failure: %v", err)
			}
			if bytes.Contains(c.pane.bytes(), []byte("SYNTHETIC-SECRET")) || bytes.Contains(c.pane.bytes(), []byte("authentication required")) {
				t.Fatalf("runtime text or alias acquired authority: %q", c.pane.bytes())
			}
			if operation != "session/prompt" && len(c.prompts(t)) != 0 {
				t.Fatal("startup refusal dispatched prompt")
			}
		})
	}
}
