package mcp_test

// busy_send_test.go pins Task t2.xis.pu.ar's MCP side (SR-8.1): the live
// dispatcher decodes busy send's options with their presence kept (false and
// wait_turn name an option; null and malformed values are refused, never read
// as omitted), so an agent busy send does not cover gets pkg/api's refusal with
// its machine fields; force_delivery, get_force and get_delivery forward their
// results and errors unchanged. pkg/api's tests own the refusals themselves.

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/gabemahoney/agent-director/internal/store"
	api "github.com/gabemahoney/agent-director/pkg/api"
)

// busySendOptions are the params whose JSON null MCP refuses (SR-8.1).
var busySendOptions = []string{"send_mode", "confirmed_delivery", "caller_uuid", "force", "confirm_from_transcripts", "only_if_waiting"}

// TestBusySendMCPOptionsReachTheVerb: each busy-send option, sent alone as
// wait_turn or false, reaches the API under its own name on every tool that
// takes it, so an uncovered Claude row's call is ErrCantSend naming exactly
// that option, with its err_details; pause's only_if_waiting true is
// ErrSpawnNotPausable with its cause, and false is today's pause; malformed
// values are refused as malformed, not dropped.
func TestBusySendMCPOptionsReachTheVerb(t *testing.T) {
	const id = "mcp-busy-row"
	cantSend := map[string]any{"class": "cant_send", "reason": "agent_not_covered", "resend_safe": true, "retry_with": "new_send"}
	type busyCase struct {
		name, tool, state, extra string // extra: the JSON members after the tool's base args
		wantName, wantDesc       string
		wantDetails              map[string]any
	}
	base := map[string]string{
		"send_keys":     `"instance_id":"` + id + `","text":"hi"`,
		"spawn":         `"cwd":{cwd},"agent":"claude"`,
		"resume":        `"instance_id":"` + id + `"`,
		"make_template": `"name":"mcp-busy","agent":"claude"`,
		"pause":         `"instance_id":"` + id + `"`,
	}
	var cases []busyCase
	for _, o := range []struct{ name, value string }{
		{"send_mode", `"wait_turn"`}, {"confirmed_delivery", "false"},
		{"caller_uuid", `"AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA"`}, {"force", "false"},
	} {
		cases = append(cases, busyCase{o.name, "send_keys", store.StateWaiting, `"` + o.name + `":` + o.value,
			"ErrCantSend", "so " + o.name + " cannot apply", cantSend})
	}
	for _, tool := range []string{"spawn", "resume", "make_template"} {
		for _, o := range []struct{ name, value string }{
			{"send_mode", `"wait_turn"`}, {"confirmed_delivery", "false"}, {"confirm_from_transcripts", "false"},
		} {
			cases = append(cases, busyCase{o.name, tool, store.StateEnded, `"` + o.name + `":` + o.value,
				"ErrCantSend", "so " + o.name + " cannot apply", cantSend})
		}
	}
	cases = append(cases,
		busyCase{"reuse", "spawn", store.StateEnded, `"instance_id":"` + id + `","reuse_finished":true,"confirmed_delivery":true`,
			"ErrCantSend", "so confirmed_delivery cannot apply", cantSend},
		busyCase{"only_if_waiting", "pause", store.StateEnded, `"only_if_waiting":true`,
			"ErrSpawnNotPausable", "only_if_waiting needs a Covered Agent", map[string]any{"cause": "agent_not_covered"}},
		busyCase{"only_if_waiting false", "pause", store.StateEnded, `"only_if_waiting":false`, "", "", nil},
		busyCase{"empty send_mode", "send_keys", store.StateWaiting, `"send_mode":""`,
			"ErrInvalidFlags", `send_mode "" must be wait_turn or steer_turn`, nil},
		busyCase{"braced caller_uuid", "send_keys", store.StateWaiting, `"caller_uuid":"{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}"`,
			"ErrInvalidFlags", `caller_uuid "{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}" must be a hyphenated hexadecimal UUID`, nil},
		busyCase{"miscased send_mode", "resume", store.StateEnded, `"send_mode":"Wait_Turn"`,
			"ErrInvalidFlags", `send_mode "Wait_Turn" must be wait_turn or steer_turn`, nil},
		busyCase{"miscased send_mode", "make_template", store.StateEnded, `"send_mode":"Wait_Turn"`,
			"ErrTemplateMalformed", `send_mode "Wait_Turn"`, nil},
	)
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seed(t, id, tc.state)
			cwd, _ := json.Marshal(t.TempDir())
			args := "{" + strings.ReplaceAll(base[tc.tool], "{cwd}", string(cwd)) + "," + tc.extra + "}"

			resp := callTool(t, e.d, tc.tool, args)

			if tc.wantName == "" {
				if text := toolText(t, resp); text != "{}" {
					t.Errorf("%s %s = %s; want today's pause, {}", tc.tool, args, text)
				}
				return
			}
			data := toolErrorData(t, resp)
			if data.ErrName != tc.wantName || !strings.Contains(data.ErrDescription, tc.wantDesc) {
				t.Errorf("%s %s = %s: %q; want %s naming %q", tc.tool, args, data.ErrName, data.ErrDescription, tc.wantName, tc.wantDesc)
			}
			if tc.wantDetails != nil && !reflect.DeepEqual(data.ErrDetails, tc.wantDetails) {
				t.Errorf("err_details = %#v; want %#v", data.ErrDetails, tc.wantDetails)
			}
		})
	}
}

// TestBusySendMCPNullRefused: a JSON null for any busy-send option, on every
// tool that takes one, is ErrInvalidFlags naming the param and its expected
// value, never an omitted option or today's behaviour, and nothing runs. A
// send or launch setting adds that omitting it leaves it unset; pause's
// only_if_waiting adds that nothing was ended, advising no pause.
// (get_delivery's required caller_uuid is a lookup key, not an option:
// TestBusySendMCPForceAndLookups refuses its null.)
func TestBusySendMCPNullRefused(t *testing.T) {
	sites := 0
	for _, v := range exposedVerbs() {
		for _, p := range v.Params {
			if p.Required || !slices.Contains(busySendOptions, p.Name) {
				continue
			}
			sites++
			t.Run(v.Name+"/"+p.Name, func(t *testing.T) {
				e, _ := newReuseParamEnv(t)
				args := paramRequiredArgs(t, e, v)
				args[p.Name] = nil
				before := readParamState(t, e)
				added := "omit it to leave it unset"
				if p.Name == "only_if_waiting" {
					added = "nothing was ended"
				}
				want := paramRefusalPrefix(v, p) + "must be " + paramShapes[p.Type].want + ", not null; " + added

				data := toolErrorData(t, callTool(t, e.d, mcp.ToolName(v.Name), paramJSON(t, args)))

				if data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
					t.Errorf("refusal = %s: %q\nwant ErrInvalidFlags: %q", data.ErrName, data.ErrDescription, want)
				}
				if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
					t.Errorf("the refused call changed state:\n got %+v\nwant %+v", after, before)
				}
			})
		}
	}
	// send-keys' four, spawn's, resume's and make-template's three, pause's one.
	if sites != 14 {
		t.Errorf("busy-send option sites = %d; want 14", sites)
	}
}

// TestBusySendMCPForceAndLookups: through the live dispatcher, force_delivery
// on an uncovered row succeeds with its refused force_result, get_force reads
// that same force_result back by its id in upper case, and an unused id or
// pair is unknown with a null payload; an empty delivery_request_id and a null
// caller_uuid, which only MCP can send, are refused, never looked up.
func TestBusySendMCPForceAndLookups(t *testing.T) {
	const id = "mcp-force-row"
	e := newEnv(t)
	e.seed(t, id, store.StateEnded)

	forced := callToolText(t, e.d, "force_delivery", `{"instance_id":"`+id+`"}`)
	var force api.ForceResult
	if err := json.Unmarshal(forced["force_result"], &force); err != nil || force.Outcome != "refused" || force.Reason != "agent_not_covered" {
		t.Fatalf("force_delivery = %s (%v); want a refused, agent_not_covered force_result", forced["force_result"], err)
	}
	found := callToolText(t, e.d, "get_force", `{"force_id":"`+strings.ToUpper(force.ForceID)+`"}`)
	if string(found["lookup"]) != `"found"` || string(found["force_result"]) != string(forced["force_result"]) {
		t.Errorf("get_force = %v; want found with force_delivery's force_result %s", found, forced["force_result"])
	}
	for _, tc := range []struct{ tool, args, want string }{
		{"get_force", `{"force_id":"` + paramToken + `"}`, `{"lookup":"unknown","force_result":null}`},
		{"get_delivery", `{"caller_uuid":"` + paramToken + `","delivery_request_id":"r1"}`, `{"lookup":"unknown","result":null}`},
	} {
		if got := toolText(t, callTool(t, e.d, tc.tool, tc.args)); got != tc.want {
			t.Errorf("%s %s = %s; want %s", tc.tool, tc.args, got, tc.want)
		}
	}
	for _, tc := range []struct{ args, want string }{
		{`{"caller_uuid":"` + paramToken + `","delivery_request_id":""}`, "delivery_request_id must be nonempty UTF-8"},
		{`{"caller_uuid":null,"delivery_request_id":"r1"}`, `caller_uuid "" must be a hyphenated hexadecimal UUID`},
	} {
		data := toolErrorData(t, callTool(t, e.d, "get_delivery", tc.args))
		if data.ErrName != "ErrInvalidFlags" || !strings.Contains(data.ErrDescription, tc.want) {
			t.Errorf("get_delivery %s = %s: %q; want ErrInvalidFlags carrying %q", tc.args, data.ErrName, data.ErrDescription, tc.want)
		}
	}
}

// TestBusySendMCPReaderPayloadsForwardUnchanged: the lookup tools forward a
// dispatcher's found and retention_expired answers byte for byte, nulls and
// omitted receipt fields included, and its store failure as an error (busy
// stays ErrStoreBusy), never a lookup.
func TestBusySendMCPReaderPayloadsForwardUnchanged(t *testing.T) {
	since := time.Date(2026, 11, 13, 1, 2, 3, 4e6, time.UTC)
	earlier := api.DeliveryPair{CallerUUID: paramToken, RequestID: "r0"}
	for _, tc := range []struct {
		name, tool string
		result     any
		err        error
		want       string // the result text, or the err_name
	}{
		{"delivery found", "get_delivery", api.GetDeliveryResult{Lookup: "found", Result: &api.DeliveryResult{
			CallerUUID: paramToken, RequestID: "r1", Class: "queued", Reason: "earlier_message", RetryWith: "none",
			Wait: &api.DeliveryWait{Reason: "earlier_message", Since: since, Earlier: &earlier}, InstanceID: "row-1", Outcome: "submission_unconfirmed"}},
			nil, `{"lookup":"found","result":{"caller_uuid":"` + paramToken + `","request_id":"r1","class":"queued","final":false,` +
				`"reason":"earlier_message","resend_safe":false,"retry_with":"none","wait":{"reason":"earlier_message",` +
				`"since":"2026-11-13T01:02:03.004Z","earlier":{"caller_uuid":"` + paramToken + `","request_id":"r0"}},"forced":false,` +
				`"instance_id":"row-1","launch_token":null,"evidence_kind":null,"outcome":"submission_unconfirmed"}}`},
		{"delivery expired", "get_delivery", api.GetDeliveryResult{Lookup: "retention_expired"}, nil, `{"lookup":"retention_expired","result":null}`},
		{"force found", "get_force", api.GetForceResult{Lookup: "found", ForceResult: &api.ForceResult{
			ForceID: paramToken, Outcome: "pending", Reason: "awaiting_admission", StartedAt: since, CarriedPairs: []api.DeliveryPair{earlier}}},
			nil, `{"lookup":"found","force_result":{"force_id":"` + paramToken + `","outcome":"pending","reason":"awaiting_admission",` +
				`"started_at":"2026-11-13T01:02:03.004Z","ended_at":null,"carried_pairs":[{"caller_uuid":"` + paramToken + `","request_id":"r0"}]}}`},
		{"force expired", "get_force", api.GetForceResult{Lookup: "retention_expired"}, nil, `{"lookup":"retention_expired","force_result":null}`},
		{"store busy", "get_force", nil, fmt.Errorf("get-force: %w", store.ErrStoreBusy), "ErrStoreBusy"},
		{"store failure", "get_delivery", nil, errors.New("read busy_send_results: disk I/O error"), "ErrInternal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDispatcher{result: tc.result, err: tc.err}
			resp := callTool(t, d, tc.tool, `{}`)
			if tc.err != nil {
				if data := toolErrorData(t, resp); data.ErrName != tc.want {
					t.Errorf("%s error = %s: %q; want %s", tc.tool, data.ErrName, data.ErrDescription, tc.want)
				}
				return
			}
			if got := toolText(t, resp); got != tc.want {
				t.Errorf("%s text =\n %s\nwant\n %s", tc.tool, got, tc.want)
			}
		})
	}
}
