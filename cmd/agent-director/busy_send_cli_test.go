package main_test

// busy_send_cli_test.go pins Task t2.xis.pu.ar's CLI side (SR-8.1): each
// busy-send flag reaches pkg/api under its own param, an explicit =false or
// wait_turn included, a malformed value is refused rather than dropped, and
// force-delivery, get-force and get-delivery print their results and errors
// unchanged. A call with none of the flags is today's call, which the existing
// CLI tests cover; pkg/api's tests own the refusals themselves. Task
// t2.xis.oo.tc adds a covered send's raw-key presence, Message ID, Result and
// id_conflict details.

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestBusySendCLIFlagsReachTheVerb: on an uncovered Codex row, each busy-send
// flag given alone is ErrCantSend naming exactly its param, with its
// err_details, on every verb that takes it, a reuse included; pause's
// --only-if-waiting is ErrSpawnNotPausable with its cause, and
// --only-if-waiting=false is today's pause; a malformed value is refused as
// malformed.
func TestBusySendCLIFlagsReachTheVerb(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cantSend := map[string]any{"class": "cant_send", "reason": "agent_not_covered", "resend_safe": true, "retry_with": "new_send"}
	base := map[string]func(id, cwd string) []string{
		"send-keys": func(id, _ string) []string { return []string{"send-keys", "--instance-id", id, "--text", "hi"} },
		"spawn":     func(_, cwd string) []string { return withAgent("codex", "spawn", "--cwd", cwd) },
		"reuse": func(id, cwd string) []string {
			return withAgent("codex", "spawn", "--cwd", cwd, "--instance-id", id, "--reuse-finished")
		},
		"resume":        func(id, _ string) []string { return []string{"resume", "--instance-id", id} },
		"make-template": func(_, _ string) []string { return withAgent("codex", "make-template", "--name", "cli-busy") },
		"pause":         func(id, _ string) []string { return []string{"pause", "--instance-id", id} },
	}
	type flagCase struct {
		verb, state        string
		flags              []string
		wantName, wantDesc string
		wantDetails        map[string]any
	}
	// Each option's param and flags; send-keys' take both spellings.
	type option struct {
		param string
		flags []string
	}
	var cases []flagCase
	for _, o := range []option{
		{"send_mode", []string{"--send-mode", "wait_turn"}}, {"confirmed_delivery", []string{"--confirmed-delivery=false"}},
		{"caller_uuid", []string{"--caller-uuid", "AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA"}}, {"force", []string{"--force=false"}},
	} {
		cases = append(cases, flagCase{"send-keys", store.StateWaiting, o.flags, "ErrCantSend", "so " + o.param + " cannot apply", cantSend})
	}
	for _, verb := range []string{"spawn", "resume", "make-template"} {
		for _, o := range []option{
			{"send_mode", []string{"--send-mode=wait_turn"}}, {"confirmed_delivery", []string{"--confirmed-delivery=false"}},
			{"confirm_from_transcripts", []string{"--confirm-from-transcripts=false"}},
		} {
			cases = append(cases, flagCase{verb, store.StateEnded, o.flags, "ErrCantSend", "so " + o.param + " cannot apply", cantSend})
		}
	}
	cases = append(cases,
		flagCase{"reuse", store.StateEnded, []string{"--confirmed-delivery"}, "ErrCantSend", "so confirmed_delivery cannot apply", cantSend},
		flagCase{"pause", store.StateEnded, []string{"--only-if-waiting"}, "ErrSpawnNotPausable", "only_if_waiting needs a Covered Agent",
			map[string]any{"cause": "agent_not_covered"}},
		flagCase{"pause", store.StateEnded, []string{"--only-if-waiting=false"}, "", "", nil},
		flagCase{"send-keys", store.StateWaiting, []string{"--send-mode="}, "ErrInvalidFlags",
			`send-keys: send_mode "" must be wait_turn or steer_turn; nothing was sent or written`, nil},
		flagCase{"send-keys", store.StateWaiting, []string{"--caller-uuid", "{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}"}, "ErrInvalidFlags",
			`send-keys: caller_uuid "{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}" must be a hyphenated hexadecimal UUID (8-4-4-4-12); nothing was sent or written`, nil},
		flagCase{"send-keys", store.StateWaiting, []string{"--force=maybe"}, "ErrInvalidFlags", `invalid boolean value "maybe" for -force: not a boolean`, nil},
		flagCase{"resume", store.StateEnded, []string{"--send-mode=Wait_Turn"}, "ErrInvalidFlags",
			`resume: send_mode "Wait_Turn" must be wait_turn or steer_turn; nothing was sent or written`, nil},
		flagCase{"make-template", store.StateEnded, []string{"--send-mode="}, "ErrTemplateMalformed", `send_mode "" (want wait_turn/steer_turn)`, nil},
	)
	for _, tc := range cases {
		t.Run(tc.verb+" "+strings.Join(tc.flags, " "), func(t *testing.T) {
			home, id, _ := seedRowOnSocket(t, tc.state, apitest.WithAgent("codex"))
			argv := append(base[tc.verb](id, t.TempDir()), tc.flags...)

			stdout, stderr, code := runSpawnCLI(t, home, fakeDir, argv...)

			if tc.wantName == "" {
				if code != 0 || stdout != "{}\n" {
					t.Fatalf("%q exit = %d, stdout = %q, stderr = %q; want today's pause, {}", argv, code, stdout, stderr)
				}
				return
			}
			env := assertOnlyEnvelope(t, stdout, stderr, code, tc.wantName)
			if !strings.Contains(env.ErrDescription, tc.wantDesc) {
				t.Errorf("%q err_description = %q; want it to carry %q", argv, env.ErrDescription, tc.wantDesc)
			}
			var got struct {
				Details map[string]any `json:"err_details"`
			}
			if err := json.Unmarshal([]byte(stderr), &got); err != nil || !reflect.DeepEqual(got.Details, tc.wantDetails) {
				t.Errorf("%q err_details = %#v (%v); want %#v", argv, got.Details, err, tc.wantDetails)
			}
		})
	}
}

// TestBusySendCLICoveredSend (SR-1.3, SR-1.5, SR-5.1): on a Claude row, a
// raw-key flag given at its zero value, or an empty --delivery-request-id,
// still reaches the verb as supplied, so the send is ErrInvalidFlags naming it;
// a confirmed send's --delivery-request-id reaches the store byte for byte, as
// its --send-mode steer_turn does (SR-1.4), and
// its printed Result is the one get-delivery prints; the pair's repeat with
// other text is ErrCantSend, id_conflict, with that Result in err_details.
// --force and force-delivery reach the verbs (see below).
func TestBusySendCLICoveredSend(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, _ := seedRowOnSocket(t, store.StateWaiting) // a Claude row, covered as shipped
	run := func(args ...string) (string, string, int) { return runSpawnCLI(t, home, fakeDir, args...) }
	send := func(text string, flags ...string) []string {
		return slices.Concat([]string{"send-keys", "--instance-id", id, "--text", text}, flags)
	}
	confirmed := []string{"--confirmed-delivery", "--caller-uuid", "AAAAAAAA-AAAA-4AAA-AAAA-AAAAAAAAAAAA"}
	for _, tc := range []struct {
		flags []string
		want  string
	}{
		{[]string{"--no-enter=false"}, "a Covered Agent's send takes no no_enter;"},
		{[]string{"--n-lines=0"}, "a Covered Agent's send takes no n_lines;"},
		{[]string{"--as="}, "a Covered Agent's send takes no as;"},
		{slices.Concat(confirmed, []string{"--delivery-request-id="}), "delivery_request_id, when given, must be nonempty"},
	} {
		stdout, stderr, code := run(send("hi", tc.flags...)...)
		env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
		if !strings.Contains(env.ErrDescription, tc.want) {
			t.Errorf("%q: err_description = %q; want it to carry %q", tc.flags, env.ErrDescription, tc.want)
		}
	}

	const requestID = "msg 多 🦋"
	pair := slices.Concat(confirmed, []string{"--delivery-request-id", requestID})
	sent, stderr, code := run(send("hi", slices.Concat(pair, []string{"--send-mode", "steer_turn"})...)...)
	var result map[string]any
	if code != 0 || json.Unmarshal([]byte(sent), &result) != nil {
		t.Fatalf("confirmed send exit = %d, stdout = %q, stderr = %q; want its Result", code, sent, stderr)
	}
	if result["caller_uuid"] != "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa" || result["request_id"] != requestID || result["class"] != "held" {
		t.Errorf("Result = %v; want held, with the lowercase caller_uuid and request_id %q", result, requestID)
	}
	if facts, err := apitest.ReadBusySendFacts(stateDB(home), id); err != nil || len(facts.Messages) != 1 ||
		facts.Messages[0].SendMode != "steer_turn" || facts.Messages[0].RequestID.String != requestID {
		t.Errorf("stored Messages = %+v (%v); want the one steer_turn Message %q", facts.Messages, err, requestID)
	}
	lookup := []string{"get-delivery", "--caller-uuid", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "--delivery-request-id", requestID}
	if got, stderr, code := run(lookup...); code != 0 || got != `{"lookup":"found","result":`+strings.TrimSuffix(sent, "\n")+"}\n" {
		t.Errorf("get-delivery exit = %d, stdout = %q, stderr = %q; want found with the send's Result %s", code, got, stderr, sent)
	}

	stdout, stderr, code := run(send("other", pair...)...)
	assertOnlyEnvelope(t, stdout, stderr, code, "ErrCantSend")
	var got struct {
		Details map[string]any `json:"err_details"`
	}
	want := map[string]any{"class": "cant_send", "reason": "id_conflict", "resend_safe": true, "retry_with": "new_send", "result": result}
	if err := json.Unmarshal([]byte(stderr), &got); err != nil || !reflect.DeepEqual(got.Details, want) {
		t.Errorf("id_conflict err_details = %#v (%v); want %#v", got.Details, err, want)
	}

	// SR-4.1: --force reaches the verb. The pair's repeat with it prints the
	// Result with the repeat's own force attempt beside it, refused at once
	// (the row records no route), which get-force prints found; with
	// --force=false it prints the Result alone, and force-delivery's own
	// attempt is the same short refusal.
	forced, stderr, code := run(send("hi", slices.Concat(pair, []string{"--force"})...)...)
	var withForce, plain map[string]json.RawMessage
	if code != 0 || json.Unmarshal([]byte(forced), &withForce) != nil || json.Unmarshal([]byte(sent), &plain) != nil {
		t.Fatalf("forced repeat exit = %d, stdout = %q, stderr = %q; want its Result and force_result", code, forced, stderr)
	}
	force := withForce["force_result"]
	delete(withForce, "force_result")
	if !reflect.DeepEqual(withForce, plain) {
		t.Errorf("forced repeat = %s; want the send's Result %s with force_result beside it", forced, sent)
	}
	forceID := cliRefusedForce(t, force, "route_unavailable")
	if got, stderr, code := run("get-force", "--force-id", forceID); code != 0 || got != `{"lookup":"found","force_result":`+string(force)+"}\n" {
		t.Errorf("get-force exit = %d, stdout = %q, stderr = %q; want found with %s", code, got, stderr, force)
	}
	if got, stderr, code := run(send("hi", slices.Concat(pair, []string{"--force=false"})...)...); code != 0 || got != sent {
		t.Errorf("repeat with --force=false exit = %d, stdout = %q, stderr = %q; want the Result alone %q", code, got, stderr, sent)
	}
	stdout, stderr, code = run("force-delivery", "--instance-id", id)
	var standalone struct {
		ForceResult json.RawMessage `json:"force_result"`
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &standalone) != nil {
		t.Fatalf("force-delivery exit = %d, stdout = %q, stderr = %q; want a force_result", code, stdout, stderr)
	}
	cliRefusedForce(t, standalone.ForceResult, "route_unavailable")
}

// cliRefusedForce checks a printed force_result refused, with reason, before
// any hand-over, and returns its force_id: an AD-made lowercase UUID, started
// and ended in one instant, carrying no pair or Result.
func cliRefusedForce(t *testing.T, raw json.RawMessage, reason string) string {
	t.Helper()
	var force map[string]any
	if err := json.Unmarshal(raw, &force); err != nil {
		t.Fatalf("force_result %s: %v", raw, err)
	}
	forceID, _ := force["force_id"].(string)
	started, _ := force["started_at"].(string)
	if parsed, err := uuid.Parse(forceID); err != nil || parsed.String() != forceID {
		t.Errorf("force_id = %q (%v); want a hyphenated lowercase UUID", forceID, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, started); err != nil {
		t.Errorf("started_at = %q: %v", started, err)
	}
	want := map[string]any{"force_id": forceID, "outcome": "refused", "reason": reason,
		"started_at": started, "ended_at": started, "carried_pairs": []any{}, "carried_results": []any{}}
	if !reflect.DeepEqual(force, want) {
		t.Errorf("force_result = %v; want %v", force, want)
	}
	return forceID
}

// TestBusySendCLIAgentlessTemplateKeepsExplicitFalse: make-template with no
// agent saves --send-mode wait_turn and --confirmed-delivery=false as keys,
// the explicit false included, and leaves the unnamed option unset.
func TestBusySendCLIAgentlessTemplateKeepsExplicitFalse(t *testing.T) {
	stdout, stderr, code := runCLI(t, "make-template", "--name", "cli-busy-tpl", "--send-mode", "wait_turn", "--confirmed-delivery=false")
	var res struct {
		Path string `json:"path"`
	}
	if code != 0 || json.Unmarshal([]byte(stdout), &res) != nil {
		t.Fatalf("make-template exit = %d, stdout = %q, stderr = %q; want its path", code, stdout, stderr)
	}
	body, err := os.ReadFile(res.Path)
	if want := "send_mode = \"wait_turn\"\nconfirmed_delivery = false\n"; err != nil || string(body) != want {
		t.Errorf("template = %q (%v); want exactly %q", body, err, want)
	}
}

// TestBusySendCLIForceAndLookups: force-delivery on an uncovered Codex row prints a
// refused, agent_not_covered force_result whose force_id is an AD-made
// lowercase UUID, ending as it starts and carrying no pair; get-force prints
// that same force_result as found, its id given in upper case; an unused id
// or pair prints unknown with a null payload; a missing row, a malformed id
// or pair and a non-UTF-8 delivery_request_id each print their one error.
func TestBusySendCLIForceAndLookups(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home, id, _ := seedRowOnSocket(t, store.StateEnded, apitest.WithAgent("codex"))
	run := func(args ...string) (string, string, int) { return runSpawnCLI(t, home, fakeDir, args...) }

	stdout, stderr, code := run("force-delivery", "--instance-id", id)
	var forced struct {
		ForceResult json.RawMessage `json:"force_result"`
	}
	if code != 0 || stderr != "" || json.Unmarshal([]byte(stdout), &forced) != nil {
		t.Fatalf("force-delivery exit = %d, stdout = %q, stderr = %q; want a force_result", code, stdout, stderr)
	}
	forceID := cliRefusedForce(t, forced.ForceResult, "agent_not_covered")

	unused := uuid.NewString()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"get-force", "--force-id", strings.ToUpper(forceID)}, `{"lookup":"found","force_result":` + string(forced.ForceResult) + `}`},
		{[]string{"get-force", "--force-id", unused}, `{"lookup":"unknown","force_result":null}`},
		{[]string{"get-delivery", "--caller-uuid", unused, "--delivery-request-id", "r1"}, `{"lookup":"unknown","result":null}`},
	} {
		if stdout, stderr, code := run(tc.args...); code != 0 || stdout != tc.want+"\n" {
			t.Errorf("%q exit = %d, stdout = %q, stderr = %q; want %s", tc.args, code, stdout, stderr, tc.want)
		}
	}
	for _, tc := range []struct {
		args               []string
		wantName, wantDesc string // wantDesc "" is not checked
	}{
		{[]string{"force-delivery", "--instance-id", "cli-no-such-row"}, "ErrSpawnNotFound", ""},
		{[]string{"get-force", "--force-id", "not-a-uuid"}, "ErrInvalidFlags",
			`get-force: force_id "not-a-uuid" must be a hyphenated hexadecimal UUID (8-4-4-4-12); nothing was read`},
		{[]string{"get-delivery", "--caller-uuid", "{" + unused + "}", "--delivery-request-id", "r1"}, "ErrInvalidFlags",
			`get-delivery: caller_uuid "{` + unused + `}" must be a hyphenated hexadecimal UUID (8-4-4-4-12); nothing was read`},
		{[]string{"get-delivery", "--caller-uuid", unused, "--delivery-request-id", "\xff"}, "ErrInvalidFlags",
			"get-delivery: delivery_request_id must be nonempty UTF-8; nothing was read"},
	} {
		stdout, stderr, code := run(tc.args...)
		if env := assertOnlyEnvelope(t, stdout, stderr, code, tc.wantName); tc.wantDesc != "" && env.ErrDescription != tc.wantDesc {
			t.Errorf("%q err_description = %q; want %q", tc.args, env.ErrDescription, tc.wantDesc)
		}
	}
}
