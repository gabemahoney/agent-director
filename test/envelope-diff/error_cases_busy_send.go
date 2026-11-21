// error_cases_busy_send.go holds busy send's error rows (Task t2.xis.pu.ar),
// appended to errorCases: each option's refusal on an uncovered Codex row,
// ErrCantSend's and pause's err_details pinned on both sides, the new verbs'
// error paths, a refused covered send with force, which forces nothing
// (Task t2.xis.p8.y2), and a covered row's conditional pause in each
// apitest.PauseSurfaces shape (Task t2.xis.8a.sv). Each row sends the same
// call through both runners, so matching descriptions show the same options
// reached the API.
package envelope_diff

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// cantSendDetails is ErrCantSend's err_details for agent_not_covered (SR-9.4).
var cantSendDetails = map[string]any{"class": "cant_send", "reason": "agent_not_covered", "resend_safe": true, "retry_with": "new_send"}

var busySendErrorCases = append([]errorCase{
	acceptedForceFailureRow(),
	busySendRow("send-keys", "ErrCantSend", busySendSeedRow("id-busy-keys", store.StateWaiting),
		map[string]any{"instance_id": "id-busy-keys", "text": "hi", "send_mode": "wait_turn", "confirmed_delivery": false},
		[]string{"send-keys", "--instance-id", "id-busy-keys", "--text", "hi", "--send-mode", "wait_turn", "--confirmed-delivery=false"},
		cantSendDetails, "so send_mode, confirmed_delivery cannot apply"),
	busySendRow("spawn", "ErrCantSend", busySendSeedNone,
		map[string]any{"cwd": "/tmp", "agent": "codex", "send_mode": "steer_turn", "confirm_from_transcripts": false},
		[]string{"spawn", "--cwd", "/tmp", "--agent", "codex", "--send-mode", "steer_turn", "--confirm-from-transcripts=false"},
		cantSendDetails, "so send_mode, confirm_from_transcripts cannot apply"),
	busySendRow("resume", "ErrCantSend", busySendSeedRow("id-busy-resume", store.StateEnded),
		map[string]any{"instance_id": "id-busy-resume", "confirmed_delivery": true},
		[]string{"resume", "--instance-id", "id-busy-resume", "--confirmed-delivery"},
		cantSendDetails, "so confirmed_delivery cannot apply"),
	busySendRow("resume", "ErrInvalidFlags", busySendSeedRow("id-busy-resume", store.StateEnded),
		map[string]any{"instance_id": "id-busy-resume", "send_mode": ""},
		[]string{"resume", "--instance-id", "id-busy-resume", "--send-mode="},
		nil, `send_mode "" must be wait_turn or steer_turn`),
	busySendRow("make-template", "ErrCantSend", busySendSeedNone,
		map[string]any{"name": "busy-tpl", "agent": "codex", "confirm_from_transcripts": true},
		[]string{"make-template", "--name", "busy-tpl", "--agent", "codex", "--confirm-from-transcripts"},
		cantSendDetails, "so confirm_from_transcripts cannot apply"),
	busySendRow("pause", "ErrSpawnNotPausable", busySendSeedRow("id-busy-pause", store.StateEnded),
		map[string]any{"instance_id": "id-busy-pause", "only_if_waiting": true},
		[]string{"pause", "--instance-id", "id-busy-pause", "--only-if-waiting"},
		map[string]any{"cause": "agent_not_covered"}, "only_if_waiting needs a Covered Agent"),
	busySendRow("send-keys", "ErrCantSend", func(t *testing.T) string {
		t.Helper()
		return seedBusyRow(t, "id-busy-force", store.StateWaiting, apitest.WithAgent("claude"))
	},
		map[string]any{"instance_id": "id-busy-force", "text": "bad\rtext", "force": true},
		[]string{"send-keys", "--instance-id", "id-busy-force", "--text", "bad\rtext", "--force"},
		map[string]any{"class": "cant_send", "reason": "text_kind", "resend_safe": false, "retry_with": "none"},
		"nothing was stored, sent or typed"),
	busySendRow("force-delivery", "ErrSpawnNotFound", busySendSeedNone,
		map[string]any{"instance_id": "nonexistent-id"}, []string{"force-delivery", "--instance-id", "nonexistent-id"}, nil),
	busySendRow("get-force", "ErrInvalidFlags", busySendSeedNone,
		map[string]any{"force_id": "not-a-uuid"}, []string{"get-force", "--force-id", "not-a-uuid"},
		nil, `force_id "not-a-uuid" must be a hyphenated hexadecimal UUID`),
	busySendRow("get-delivery", "ErrInvalidFlags", busySendSeedNone,
		map[string]any{"caller_uuid": "{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}", "delivery_request_id": "r1"},
		[]string{"get-delivery", "--caller-uuid", "{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}", "--delivery-request-id", "r1"},
		nil, `caller_uuid "{aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa}" must be a hyphenated hexadecimal UUID`),
}, pauseSurfaceRows()...)

// pauseSurfaceRows are pause --only-if-waiting on a covered Claude row in each
// apitest.PauseSurfaces shape (SR-6.5), its exact err_details on both sides.
func pauseSurfaceRows() []errorCase {
	var rows []errorCase
	for _, s := range apitest.PauseSurfaces {
		id := "id-pause-" + s.Name
		rows = append(rows, busySendRow("pause", "ErrSpawnNotPausable", func(t *testing.T) string {
			t.Helper()
			dbPath := filepath.Join(t.TempDir(), "state.db")
			if err := apitest.SeedPauseSurface(dbPath, id, s.Name); err != nil {
				t.Fatal(err)
			}
			return filepath.Dir(dbPath)
		},
			map[string]any{"instance_id": id, "only_if_waiting": true},
			[]string{"pause", "--instance-id", id, "--only-if-waiting"}, s.Details))
	}
	return rows
}

// busySendRow is an error row of verb whose fixture store seed builds and
// whose call is params for the Client and argv for the CLI; details, when
// set, are its exact err_details, and require, when given, phrases both
// descriptions carry.
func busySendRow(verb, errName string, seed func(t *testing.T) string, params map[string]any, argv []string,
	details map[string]any, require ...string) errorCase {
	ec := errorCase{
		verb: verb, errName: errName, details: details,
		seed:    func(t *testing.T) (string, map[string]any) { return seed(t), nil },
		params:  func(map[string]any) map[string]any { return params },
		cliArgv: func(map[string]any) []string { return argv },
	}
	if len(require) > 0 {
		ec.desc = func(map[string]any) (apitest.DescCase, []string) {
			return apitest.DescCase{Name: verb + " " + errName, Require: require}, nil
		}
	}
	return ec
}

// busySendSeedNone is an empty store's directory.
func busySendSeedNone(t *testing.T) string {
	t.Helper()
	return filepath.Dir(apitest.SeedEmptyStore(t))
}

// busySendSeedRow seeds a store holding one Codex row id in state, which
// busy send does not cover, and returns its directory.
func busySendSeedRow(id, state string) func(t *testing.T) string {
	return func(t *testing.T) string {
		t.Helper()
		return seedBusyRow(t, id, state, apitest.WithAgent("codex"))
	}
}

// t3.xis.ec.9e.du: admission succeeds in both processes; the persistent force
// INSERT cut survives copyFixtureStore into the real CLI process.
func acceptedForceFailureRow() errorCase {
	const id = "id-accepted-force-failed"
	const caller = "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa"
	ec := busySendRow("send-keys", "ErrSendAcceptedForceFailed", busySendSeedNone,
		map[string]any{"instance_id": id, "text": "private message", "force": true, "confirmed_delivery": true, "caller_uuid": caller, "delivery_request_id": "accepted-1"},
		[]string{"send-keys", "--instance-id", id, "--text", "private message", "--force", "--confirmed-delivery", "--caller-uuid", caller, "--delivery-request-id", "accepted-1"}, nil)
	ec.seed = func(t *testing.T) (string, map[string]any) {
		dir := seedBusyRow(t, id, store.StateWaiting, apitest.WithAgent("claude"))
		db := filepath.Join(dir, "state.db")
		// A recorded route/wake proves the accepted send still notifies the agent.
		wake := filepath.Join(t.TempDir(), "wake")
		if err := os.WriteFile(wake, nil, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(wake, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		if err := apitest.SeedBusySendRoute(db, id, "claude_hooks", wake); err != nil {
			t.Fatal(err)
		}
		storefix.InjectWriteFailure(t, db, storefix.WriteFailBusySendForceInsert, id)
		return dir, map[string]any{"wake": wake}
	}
	ec.detailsFor = func(t *testing.T, db string, ctx map[string]any) map[string]any {
		if got := storefix.ReadBusySendCounts(t, db); got != (storefix.BusySendCounts{Messages: 1, Pairs: 1}) {
			t.Errorf("accepted records = %+v; want one Message/pair, no force/input", got)
		}
		info, err := os.Stat(ctx["wake"].(string))
		if err != nil || !info.ModTime().After(time.Unix(1, 0)) {
			t.Fatalf("ordinary admission wake was not touched: %v", err)
		}
		// Reset the endpoint so the second runner must notify independently.
		if err := os.Chtimes(ctx["wake"].(string), time.Unix(1, 0), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
		c, err := api.New(api.Options{StorePath: db, ConfigPath: filepath.Join(filepath.Dir(db), "absent.toml")})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		got, err := c.GetDelivery(api.GetDeliveryParams{CallerUUID: caller, DeliveryRequestID: "accepted-1"})
		if err != nil || got.Result == nil || got.Result.Wait == nil {
			t.Fatalf("retained Result = %+v, %v", got, err)
		}
		return map[string]any{"result": map[string]any{
			"caller_uuid": caller, "request_id": "accepted-1", "class": "held", "final": false,
			"reason": "readiness_unknown", "resend_safe": false, "retry_with": "none",
			"wait":   map[string]any{"reason": "readiness_unknown", "since": got.Result.Wait.Since.Format(time.RFC3339Nano)},
			"forced": false, "instance_id": id, "launch_token": nil, "evidence_kind": nil, "outcome": "not_started",
		}}
	}
	return ec
}
