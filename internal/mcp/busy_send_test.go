package mcp_test

// busy_send_test.go pins Task t2.xis.pu.ar's MCP side (SR-8.1): the live
// dispatcher decodes busy send's options with their presence kept (false and
// wait_turn name an option; null and malformed values are refused, never read
// as omitted), so an agent busy send does not cover gets pkg/api's refusal with
// its machine fields; force_delivery, get_force and get_delivery forward their
// results and errors unchanged. pkg/api's tests own the refusals themselves.
// Task t2.xis.oo.tc adds a covered send's raw-key presence, Message ID, Result
// and id_conflict details; Task t2.xis.8a.sv a covered row's conditional pause.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/mcp"
	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busySendOptions are the params whose JSON null MCP refuses (SR-8.1).
var busySendOptions = []string{"send_mode", "confirmed_delivery", "caller_uuid", "force", "confirm_from_transcripts", "only_if_waiting"}

// TestBusySendMCPOptionsReachTheVerb: each busy-send option, sent alone as
// wait_turn or false, reaches the API under its own name on every tool that
// takes it, so an uncovered Codex row's call is ErrCantSend naming exactly
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
		"spawn":         `"cwd":{cwd},"agent":"codex"`,
		"resume":        `"instance_id":"` + id + `"`,
		"make_template": `"name":"mcp-busy","agent":"codex"`,
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
			e.seed(t, id, tc.state, apitest.WithAgent("codex"))
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

// TestBusySendMCPConditionalPause (SR-6.5; M2 P-C4.1, P-C4.5, P-C4.6): pause's
// only_if_waiting true reaches the shared API through the live dispatcher, so a
// covered Claude row in each apitest.PauseSurfaces shape is ErrSpawnNotPausable
// with exactly that shape's err_details: the raw observed_state kept, and
// signal_sent true or omitted as the replayed outcome recorded. The option's
// other values and empty success are TestBusySendMCPOptionsReachTheVerb's and
// TestBusySendMCPNullRefused's.
func TestBusySendMCPConditionalPause(t *testing.T) {
	for _, s := range apitest.PauseSurfaces {
		t.Run(s.Name, func(t *testing.T) {
			e := newEnv(t)
			id := "mcp-pause-" + s.Name
			if err := apitest.SeedPauseSurface(e.storePath, id, s.Name); err != nil {
				t.Fatal(err)
			}
			data := toolErrorData(t, callTool(t, e.d, "pause", `{"instance_id":"`+id+`","only_if_waiting":true}`))
			if data.ErrName != "ErrSpawnNotPausable" || !reflect.DeepEqual(data.ErrDetails, s.Details) {
				t.Errorf("pause = %s: %q, err_details %#v; want ErrSpawnNotPausable with %#v", data.ErrName, data.ErrDescription, data.ErrDetails, s.Details)
			}
		})
	}
}

// TestBusySendMCPCoveredSend (SR-1.3, SR-1.5, SR-5.1): on a Claude row, a
// raw-key param present as false, 0, "" or null, or a null
// delivery_request_id, still reaches the verb as supplied, and text or a
// delivery_request_id that is invalid UTF-8 or holds a lone surrogate escape
// reaches it as malformed, not replaced, so send_keys is ErrInvalidFlags
// naming each and stores no Message or pair; a confirmed send's
// delivery_request_id reaches the store unchanged, as its send_mode
// steer_turn does (SR-1.4), and its Result is the one
// get_delivery returns; its text's U+FFFD is kept, so the same pair with the
// character escaped is a repeat returning that Result, while other text is
// ErrCantSend, id_conflict, with that Result in err_details. force reaches
// the verb (see below).
func TestBusySendMCPCoveredSend(t *testing.T) {
	const id = "mcp-covered-row"
	e := newEnv(t)
	e.seed(t, id, store.StateWaiting) // a Claude row, covered as shipped
	facts := func() apitest.BusySendFacts {
		f, err := apitest.ReadBusySendFacts(e.storePath, id)
		if err != nil {
			t.Fatalf("ReadBusySendFacts: %v", err)
		}
		return f
	}
	before := facts()
	const requestID = "msg 多 🦋"
	confirmed := `"confirmed_delivery":true,"caller_uuid":"` + strings.ToUpper(paramToken) + `"`
	for _, tc := range []struct{ members, want string }{
		{`"text":"hi","no_enter":false`, "a Covered Agent's send takes no no_enter;"},
		{`"text":"hi","n_lines":0`, "a Covered Agent's send takes no n_lines;"},
		{`"text":"hi","key":""`, "a Covered Agent's send takes no key;"},
		{`"text":"hi","expect_pane_sha256":null`, "a Covered Agent's send takes no expect_pane_sha256;"},
		{`"text":"hi",` + confirmed + `,"delivery_request_id":null`, "delivery_request_id, when given, must be nonempty"},
		{"\"text\":\"h\xffi\"", "text must be valid UTF-8"},
		{`"text":"h\ud800i"`, "text must be valid UTF-8"},
		{`"text":"hi",` + confirmed + ",\"delivery_request_id\":\"r\xff1\"", "delivery_request_id must be valid UTF-8"},
		{`"text":"hi",` + confirmed + `,"delivery_request_id":"r\ud800"`, "delivery_request_id must be valid UTF-8"},
	} {
		data := toolErrorData(t, callTool(t, e.d, "send_keys", `{"instance_id":"`+id+`",`+tc.members+`}`))
		if data.ErrName != "ErrInvalidFlags" || !strings.Contains(data.ErrDescription, tc.want) {
			t.Errorf("%q = %s: %q; want ErrInvalidFlags carrying %q", tc.members, data.ErrName, data.ErrDescription, tc.want)
		}
		if after := facts(); !reflect.DeepEqual(after, before) {
			t.Errorf("%q stored:\n got %+v\nwant %+v", tc.members, after, before)
		}
	}

	send := func(textJSON string) *mcp.Response {
		return callTool(t, e.d, "send_keys", `{"instance_id":"`+id+`","text":`+textJSON+`,`+confirmed+`,"delivery_request_id":"`+requestID+`","send_mode":"steer_turn"}`)
	}
	sent := toolText(t, send(`"hi �"`))
	var result map[string]any
	if err := json.Unmarshal([]byte(sent), &result); err != nil || result["caller_uuid"] != paramToken || result["request_id"] != requestID || result["class"] != "held" {
		t.Errorf("Result = %s (%v); want held, with the lowercase caller_uuid and request_id %q", sent, err, requestID)
	}
	if m := facts().Messages; len(m) != 1 || m[0].SendMode != "steer_turn" || m[0].RequestID.String != requestID {
		t.Errorf("stored Messages = %+v; want the one steer_turn Message %q", m, requestID)
	}
	lookup := paramJSON(t, map[string]any{"caller_uuid": paramToken, "delivery_request_id": requestID})
	if got := toolText(t, callTool(t, e.d, "get_delivery", lookup)); got != `{"lookup":"found","result":`+sent+`}` {
		t.Errorf("get_delivery = %s; want found with the send's Result %s", got, sent)
	}
	if got := toolText(t, send(`"hi \`+`ufffd"`)); got != sent { // U+FFFD as a JSON escape
		t.Errorf("repeat with U+FFFD escaped = %s; want the send's Result %s", got, sent)
	}

	data := toolErrorData(t, send(`"other"`))
	want := map[string]any{"class": "cant_send", "reason": "id_conflict", "resend_safe": true, "retry_with": "new_send", "result": result}
	if data.ErrName != "ErrCantSend" || !reflect.DeepEqual(data.ErrDetails, want) {
		t.Errorf("repeat with other text = %s, err_details %#v; want ErrCantSend with %#v", data.ErrName, data.ErrDetails, want)
	}

	// SR-4.1: force reaches the verb. The pair's repeat with force true is the
	// Result with the repeat's own force attempt beside it, refused at once
	// (the row records no route), which get_force returns found; with force
	// false it is the Result alone.
	repeat := func(force string) *mcp.Response {
		return callTool(t, e.d, "send_keys", `{"instance_id":"`+id+`","text":"hi �",`+confirmed+`,"delivery_request_id":"`+requestID+`","force":`+force+`}`)
	}
	var withForce, plain map[string]json.RawMessage
	if err := json.Unmarshal([]byte(toolText(t, repeat("true"))), &withForce); err != nil || json.Unmarshal([]byte(sent), &plain) != nil {
		t.Fatalf("forced repeat: %v", err)
	}
	var force api.ForceResult
	if err := json.Unmarshal(withForce["force_result"], &force); err != nil || force.Outcome != "refused" || force.Reason != "route_unavailable" ||
		len(force.CarriedPairs) != 0 || len(force.CarriedResults) != 0 || force.EndedAt == nil || !force.EndedAt.Equal(force.StartedAt) {
		t.Errorf("force_result = %s (%v); want refused route_unavailable at once, carrying nothing", withForce["force_result"], err)
	}
	found := callToolText(t, e.d, "get_force", `{"force_id":"`+force.ForceID+`"}`)
	if string(found["lookup"]) != `"found"` || string(found["force_result"]) != string(withForce["force_result"]) {
		t.Errorf("get_force = %v; want found with the send's force_result %s", found, withForce["force_result"])
	}
	delete(withForce, "force_result")
	if !reflect.DeepEqual(withForce, plain) {
		t.Errorf("forced repeat's Result = %v; want the send's %s", withForce, sent)
	}
	if got := toolText(t, repeat("false")); got != sent {
		t.Errorf("repeat with force false = %s; want the Result alone %s", got, sent)
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
// on an uncovered Codex row succeeds with its refused force_result, get_force reads
// that same force_result back by its id in upper case, and an unused id or
// pair is unknown with a null payload; an empty delivery_request_id and a null
// caller_uuid, which only MCP can send, are refused, never looked up.
func TestBusySendMCPForceAndLookups(t *testing.T) {
	const id = "mcp-force-row"
	e := newEnv(t)
	e.seed(t, id, store.StateEnded, apitest.WithAgent("codex"))

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

// CR1: a malformed raw lookup cannot alias a retained U+FFFD ID, while real
// replacement characters and valid surrogate pairs retain their identity.
func TestBusySendMCPLookupExactIDs(t *testing.T) {
	e := newEnv(t)
	const id = "mcp-exact-id"
	e.seed(t, id, store.StateWaiting)
	for _, requestID := range []string{"r�", "r🦋"} {
		sent := toolText(t, callTool(t, e.d, "send_keys", paramJSON(t, map[string]any{
			"instance_id": id, "text": "hello", "confirmed_delivery": true,
			"caller_uuid": paramToken, "delivery_request_id": requestID,
		})))
		for _, raw := range map[string][]string{
			"r�": {`"r�"`, `"r\ufffd"`},
			"r🦋": {`"r🦋"`, `"r\ud83e\udd8b"`},
		}[requestID] {
			t.Run(raw, func(t *testing.T) {
				got := toolText(t, callTool(t, e.d, "get_delivery", `{"caller_uuid":"`+paramToken+`","delivery_request_id":`+raw+`}`))
				if got != `{"lookup":"found","result":`+sent+`}` {
					t.Errorf("lookup = %s; want the retained Result %s", got, sent)
				}
			})
		}
	}
	for _, raw := range []string{"\"r\xff\"", `"r\ud800"`, `"r\udc00"`, `"r\ud800x"`} {
		t.Run(raw, func(t *testing.T) {
			for _, d := range []mcp.Dispatcher{e.d, mcp.NewLiveDispatcher(&api.Client{})} {
				// The second client has no store: any attempted read panics.
				data := toolErrorData(t, callTool(t, d, "get_delivery", `{"caller_uuid":"`+paramToken+`","delivery_request_id":`+raw+`}`))
				want := "ErrInvalidFlags: get-delivery: delivery_request_id must be nonempty UTF-8; nothing was read"
				if data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
					t.Errorf("lookup = %s: %q; want ErrInvalidFlags: %q", data.ErrName, data.ErrDescription, want)
				}
			}
		})
	}
}

// Every free-text store key/value must reach its verb without JSON replacement.
// UUID and enum parameters are separately constrained to ASCII by their verbs.
func TestBusySendMCPStoreStringsExact(t *testing.T) {
	for _, v := range exposedVerbs() {
		for _, p := range v.Params {
			if !slices.Contains([]string{"instance_id", "instance_ids", "parent", "request_token", "reason", "label", "cwd", "tmux_session_name", "extra_env", "agent_args", "allow", "deny", "ask", "template", "name", "key"}, p.Name) {
				continue
			}
			for _, bad := range []string{"\"x\xff\"", `"x\ud800"`} {
				t.Run(v.Name+"/"+p.Name+"/"+bad, func(t *testing.T) {
					e, _ := newReuseParamEnv(t)
					args := paramRequiredArgs(t, e, v)
					var raw map[string]json.RawMessage
					if err := json.Unmarshal([]byte(paramJSON(t, args)), &raw); err != nil {
						t.Fatal(err)
					}
					value := bad
					switch p.Name {
					case "instance_ids", "label", "agent_args", "allow", "deny", "ask":
						value = "[" + bad + "]"
					case "extra_env":
						value = `{"K":` + bad + `}`
					}
					raw[p.Name] = json.RawMessage(value)
					encoded, err := json.Marshal(raw)
					if err != nil {
						t.Fatal(err)
					}
					before := readParamState(t, e)
					data := toolErrorData(t, callTool(t, e.d, mcp.ToolName(v.Name), string(encoded)))
					if data.ErrName != "ErrInvalidFlags" || !strings.Contains(data.ErrDescription, p.Name+" must be valid UTF-8") {
						t.Errorf("refusal = %s: %q; want ErrInvalidFlags naming %s", data.ErrName, data.ErrDescription, p.Name)
					}
					if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
						t.Error("refused string changed state")
					}
				})
			}
		}
	}
}

// Repair CR1: typed JSON decoding can retain a string across duplicate/null
// members or merge duplicate objects, while a raw map only keeps the last.
func TestBusySendMCPDuplicateParamsRefused(t *testing.T) {
	for _, tc := range []struct{ name, tool, args, param string }{
		{"delivery surrogate/null", "get_delivery", `{"caller_uuid":"` + paramToken + `","delivery_request_id":"r\ud800","delivery_request_id":null}`, "delivery_request_id"},
		{"delivery bytes/null", "get_delivery", `{"caller_uuid":"` + paramToken + `","delivery_request_id":"r` + "\xff" + `","delivery_request_id":null}`, "delivery_request_id"},
		{"delivery replaced/string", "get_delivery", `{"caller_uuid":"` + paramToken + `","delivery_request_id":"r\ud800","delivery_request_id":"r�"}`, "delivery_request_id"},
		{"instance/null", "get", `{"instance_id":"row\ud800","instance_id":null}`, "instance_id"},
		{"escaped duplicate name", "get", `{"instance_id":"row\ud800","\u0069nstance_id":null}`, "instance_id"},
		{"environment merge", "make_template", `{"name":"duplicate-env","agent":"codex","extra_env":{"K":"r\ud800"},"extra_env":{}}`, "extra_env"},
		{"text/null", "send_keys", `{"instance_id":"duplicate-send","text":"h\ud800","text":null}`, "text"},
		{"text/string", "send_keys", `{"instance_id":"duplicate-send","text":"h\ud800","text":"hello"}`, "text"},
		{"message ID/null", "send_keys", `{"instance_id":"duplicate-send","text":"hello","confirmed_delivery":true,"caller_uuid":"` + paramToken + `","delivery_request_id":"r\ud800","delivery_request_id":null}`, "delivery_request_id"},
		{"ordinary duplicate", "get", `{"instance_id":"row�","instance_id":"row�"}`, "instance_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.seed(t, "row�", store.StateEnded)
			e.seed(t, "duplicate-send", store.StateWaiting)
			toolText(t, callTool(t, e.d, "send_keys", paramJSON(t, map[string]any{
				"instance_id": "duplicate-send", "text": "hello", "confirmed_delivery": true,
				"caller_uuid": paramToken, "delivery_request_id": "r�",
			})))
			before := readParamState(t, e)
			for _, d := range []mcp.Dispatcher{e.d, mcp.NewLiveDispatcher(&api.Client{})} {
				// No store on the second client: no lookup or write may run.
				data := toolErrorData(t, callTool(t, d, tc.tool, tc.args))
				want := `ErrInvalidFlags: ` + mcp.VerbNameFromTool(tc.tool) + `: duplicate parameter "` + tc.param + `"`
				if data.ErrName != "ErrInvalidFlags" || data.ErrDescription != want {
					t.Errorf("duplicate = %s: %q; want ErrInvalidFlags: %q", data.ErrName, data.ErrDescription, want)
				}
			}
			if after := readParamState(t, e); !reflect.DeepEqual(after, before) {
				t.Error("refused duplicate changed state")
			}
		})
	}
}

// Store-key guarding also preserves valid strings, including escaped quotes,
// backslashes, literal escape text, replacement characters and surrogate pairs.
func TestBusySendMCPStoreStringsValid(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"row�", "row🦋", `row\ud800`, `row"quoted`} {
		e.seed(t, id, store.StateEnded)
		got := callToolText(t, e.d, "get", paramJSON(t, map[string]any{"instance_id": id}))
		var found string
		if err := json.Unmarshal(got["instance_id"], &found); err != nil || found != id {
			t.Errorf("get %q = %s (%v); want the exact instance ID", id, got["instance_id"], err)
		}
	}
}

// TestBusySendMCPReaderPayloadsForwardUnchanged: the lookup tools forward a
// dispatcher's found and retention_expired answers byte for byte, nulls and
// omitted receipt fields included (a running force's empty reason and its
// carried_results, shorter than carried_pairs, too), and its store failure as an error (busy
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
			ForceID: paramToken, Outcome: "pending", Reason: "awaiting_admission", StartedAt: since, CarriedPairs: []api.DeliveryPair{earlier},
			CarriedResults: []api.DeliveryResult{}}},
			nil, `{"lookup":"found","force_result":{"force_id":"` + paramToken + `","outcome":"pending","reason":"awaiting_admission",` +
				`"started_at":"2026-11-13T01:02:03.004Z","ended_at":null,"carried_pairs":[{"caller_uuid":"` + paramToken + `","request_id":"r0"}],` +
				`"carried_results":[]}}`},
		{"force running", "get_force", api.GetForceResult{Lookup: "found", ForceResult: &api.ForceResult{
			ForceID: paramToken, Outcome: "running", StartedAt: since, CarriedPairs: []api.DeliveryPair{earlier, {CallerUUID: paramToken, RequestID: "r1"}},
			CarriedResults: []api.DeliveryResult{{CallerUUID: paramToken, RequestID: "r1", Class: "held", Reason: "turn_running", RetryWith: "none",
				Wait: &api.DeliveryWait{Reason: "turn_running", Since: since}, InstanceID: "row-1", Outcome: "not_started"}}}},
			nil, `{"lookup":"found","force_result":{"force_id":"` + paramToken + `","outcome":"running","reason":"",` +
				`"started_at":"2026-11-13T01:02:03.004Z","ended_at":null,"carried_pairs":[{"caller_uuid":"` + paramToken + `","request_id":"r0"},` +
				`{"caller_uuid":"` + paramToken + `","request_id":"r1"}],"carried_results":[{"caller_uuid":"` + paramToken + `","request_id":"r1",` +
				`"class":"held","final":false,"reason":"turn_running","resend_safe":false,"retry_with":"none","wait":{"reason":"turn_running",` +
				`"since":"2026-11-13T01:02:03.004Z"},"forced":false,"instance_id":"row-1","launch_token":null,"evidence_kind":null,` +
				`"outcome":"not_started"}]}}`},
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

// t3.xis.ec.9e.du: the live dispatcher must retain an AD-generated pair in
// structured error data, with no invented force or leaked Message text.
func TestBusySendMCPAcceptedForceFailed(t *testing.T) {
	const id = "mcp-accepted-force-failed"
	e := newEnv(t)
	e.seed(t, id, store.StateWaiting)
	wake := filepath.Join(t.TempDir(), "wake")
	if err := os.WriteFile(wake, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(wake, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendRoute(e.storePath, id, "claude_hooks", wake); err != nil {
		t.Fatal(err)
	}
	remove := storefix.InjectWriteFailure(t, e.storePath, storefix.WriteFailBusySendForceInsert, id)
	data := toolErrorData(t, callTool(t, e.d, "send_keys", paramJSON(t, map[string]any{
		"instance_id": id, "text": "private message", "force": true, "confirmed_delivery": true, "caller_uuid": paramToken,
	})))
	if data.ErrName != "ErrSendAcceptedForceFailed" {
		t.Fatalf("err_name = %s: %s", data.ErrName, data.ErrDescription)
	}
	details, ok := data.ErrDetails.(map[string]any)
	if !ok {
		t.Fatalf("err_details is not an object: %#v", data.ErrDetails)
	}
	result, ok := details["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing accepted Result: %#v", data.ErrDetails)
	}
	request, _ := result["request_id"].(string)
	if parsed, err := uuid.Parse(request); err != nil || parsed.String() != request {
		t.Fatalf("generated Message ID = %q", request)
	}
	lookup := callToolText(t, e.d, "get_delivery", paramJSON(t, map[string]any{"caller_uuid": paramToken, "delivery_request_id": request}))
	if string(lookup["lookup"]) != `"found"` {
		t.Fatalf("lookup = %s", lookup["lookup"])
	}
	var retained map[string]any
	if err := json.Unmarshal(lookup["result"], &retained); err != nil {
		t.Fatal(err)
	}
	wait, ok := retained["wait"].(map[string]any)
	if !ok {
		t.Fatalf("retained wait = %#v", retained)
	}
	want := map[string]any{"caller_uuid": paramToken, "request_id": request, "class": "held", "final": false,
		"reason": "readiness_unknown", "resend_safe": false, "retry_with": "none",
		"wait": map[string]any{"reason": "readiness_unknown", "since": wait["since"]}, "forced": false,
		"instance_id": id, "launch_token": nil, "evidence_kind": nil, "outcome": "not_started"}
	if !reflect.DeepEqual(data.ErrDetails, map[string]any{"result": want}) || !reflect.DeepEqual(retained, want) {
		t.Errorf("error details = %#v; retained = %#v; want %#v", data.ErrDetails, retained, want)
	}
	facts, err := apitest.ReadBusySendFacts(e.storePath, id)
	if err != nil || len(facts.Messages) != 1 || facts.Messages[0].RequestID.String != request || facts.Messages[0].CallerUUID.String != paramToken {
		t.Errorf("accepted facts = %+v, %v", facts, err)
	}
	if got := storefix.ReadBusySendCounts(t, e.storePath); got != (storefix.BusySendCounts{Messages: 1, Pairs: 1}) {
		t.Errorf("records = %+v", got)
	}
	if info, err := os.Stat(wake); err != nil || !info.ModTime().After(time.Unix(1, 0)) {
		t.Errorf("ordinary wake was not touched: %v", err)
	}
	if len(e.rec.Calls())+len(e.rec.SocketCalls()) != 0 {
		t.Error("accepted force failure performed tmux input/probes")
	}
	// The cut also catches the distinct refused-attempt INSERT when no route
	// exists. This stays an unnamed standalone-force error, not an accepted send.
	e.seed(t, "mcp-no-route", store.StateWaiting)
	removeNoRoute := storefix.InjectWriteFailure(t, e.storePath, storefix.WriteFailBusySendForceInsert, "mcp-no-route")
	noRoute := toolErrorData(t, callTool(t, e.d, "force_delivery", `{"instance_id":"mcp-no-route"}`))
	if noRoute.ErrName != "ErrInternal" || noRoute.ErrDetails != nil || !strings.Contains(noRoute.ErrDescription, "injected write failure: busy-send force insert") {
		t.Errorf("route-unavailable force INSERT = %+v", noRoute)
	}
	removeNoRoute()
	// The same real writer proves instance scoping and the fixture remover:
	// another instance works while installed, and this instance works after it.
	e.seed(t, "mcp-other-force", store.StateWaiting)
	for _, target := range []string{"mcp-other-force", id} {
		if target == id {
			remove()
			if err := apitest.SeedBusySendRoute(e.storePath, id, "claude_hooks", ""); err != nil {
				t.Fatal(err)
			}
		}
		got := callToolText(t, e.d, "force_delivery", paramJSON(t, map[string]any{"instance_id": target}))
		var force api.ForceResult
		if err := json.Unmarshal(got["force_result"], &force); err != nil || force.Outcome != "refused" || force.Reason != "route_unavailable" {
			t.Errorf("force after scoped cut on %s = %+v, %v", target, force, err)
		}
	}
}
