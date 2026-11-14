// success_busy_send.go holds the success cases of busy send's verbs (Task
// t2.xis.pu.ar): force-delivery, get-force and get-delivery, appended to
// successCases, and the check of a refused force_result.
package envelope_diff

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// busySendSuccessCases: standalone force-delivery on an uncovered row
// succeeds on both sides with a refused force_result, whose id and times
// nondeterministic.json excludes and refusedForceResult checks; get-force
// reads a force the seed stored through the public writer, found and alike on
// both sides; get-delivery of a pair no Message holds is unknown with a null
// result.
var busySendSuccessCases = []successCase{
	{
		verb: "force-delivery",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			_, dbPath := apitest.OpenStoreWithRow(t, "id-force-1", "cd-force-1", store.StateEnded, "off")
			return filepath.Dir(dbPath), map[string]any{"id": "id-force-1"}
		},
		params: func(ctx map[string]any) map[string]any { return map[string]any{"instance_id": ctx["id"]} },
		cliArgv: func(ctx map[string]any) []string {
			return []string{"force-delivery", "--instance-id", ctx["id"].(string)}
		},
		want: map[string]any{"force_result": fieldCheck(refusedForceResult)},
	},
	{
		verb: "get-force",
		seed: seedStoredForce,
		params: func(ctx map[string]any) map[string]any {
			return map[string]any{"force_id": ctx["force_id"]}
		},
		cliArgv: func(ctx map[string]any) []string {
			return []string{"get-force", "--force-id", ctx["force_id"].(string)}
		},
		want: map[string]any{"lookup": "found", "force_result": fieldCheck(refusedForceResult)},
	},
	{
		verb: "get-delivery",
		seed: func(t *testing.T) (string, map[string]any) {
			t.Helper()
			return filepath.Dir(apitest.SeedEmptyStore(t)), nil
		},
		params: func(_ map[string]any) map[string]any {
			return map[string]any{"caller_uuid": "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "delivery_request_id": "r1"}
		},
		cliArgv: func(_ map[string]any) []string {
			return []string{"get-delivery", "--caller-uuid", "aaaaaaaa-aaaa-4aaa-aaaa-aaaaaaaaaaaa", "--delivery-request-id", "r1"}
		},
		want: map[string]any{"lookup": "unknown", "result": nil},
	},
}

// seedStoredForce seeds an ended row and stores one refused force of it
// through the public writer, Client.ForceDelivery; ctx carries its force_id.
func seedStoredForce(t *testing.T) (string, map[string]any) {
	t.Helper()
	_, dbPath := apitest.OpenStoreWithRow(t, "id-force-2", "cd-force-2", store.StateEnded, "off")
	c, err := api.New(api.Options{StorePath: dbPath, ConfigPath: filepath.Join(filepath.Dir(dbPath), "config.toml")})
	if err != nil {
		t.Fatalf("seedStoredForce: api.New: %v", err)
	}
	defer c.Close()
	res, err := c.ForceDelivery(api.ForceDeliveryParams{InstanceID: "id-force-2"})
	if err != nil {
		t.Fatalf("seedStoredForce: ForceDelivery: %v", err)
	}
	return filepath.Dir(dbPath), map[string]any{"force_id": res.ForceResult.ForceID}
}

// refusedForceResult checks a force_result refused before any hand-over
// (SR-4.1, SR-9.2): exactly its six keys, an AD-made lowercase UUID
// force_id, outcome refused, reason agent_not_covered, a timestamp started_at
// it ended at, and no carried pair.
func refusedForceResult(got any) error {
	m, ok := got.(map[string]any)
	if !ok {
		return errors.New("not an object")
	}
	id, _ := m["force_id"].(string)
	if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
		return fmt.Errorf("force_id %q is not a hyphenated lowercase UUID", id)
	}
	started, _ := m["started_at"].(string)
	if _, err := time.Parse(time.RFC3339Nano, started); err != nil {
		return fmt.Errorf("started_at: %w", err)
	}
	want := map[string]any{"force_id": id, "outcome": "refused", "reason": "agent_not_covered",
		"started_at": started, "ended_at": started, "carried_pairs": []any{}}
	if !reflect.DeepEqual(m, want) {
		return fmt.Errorf("want %v", want)
	}
	return nil
}
