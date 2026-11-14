package api_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.ivj D2/A2: every read row carries a nullable diagnosis; list retains its shape.
func TestFailureReasonReadSurfaces(t *testing.T) {
	t.Parallel()
	const token = "1111111111111111"
	const evidence = `{"v":"missing","life":0,"token":"` + token + `","epoch":0,"at":1000,"checked":1000,"src":"process","failure_reason":"runtime internal error"}`
	dbPath := filepath.Join(t.TempDir(), "state.db")
	cases := []struct{ id, agent, evidence, want string }{
		{"cursor-failed", "cursor", evidence, `"runtime internal error"`},
		{"cursor-legacy", "cursor", "", "null"},
		{"claude", "claude", evidence, "null"},
		{"codex", "codex", evidence, "null"},
	}
	var ids []string
	for _, tc := range cases {
		if _, err := apitest.SeedSpawn(dbPath, tc.id, store.StateMissing, "/tmp", "off", "", true,
			apitest.WithAgent(tc.agent), apitest.WithLaunchIdentity(store.LaunchIdentity{Token: token}),
			apitest.WithPermissionEpoch(0), apitest.WithStateEvidence(tc.evidence)); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tc.id)
	}
	c := settleClientFor(t, dbPath, 0, tmuxfix.NewRecorder(), time.Now)
	// Repeated reads must not consume the reason, including GetMany's get builder.
	for range 2 {
		many, err := c.GetMany(ids)
		if err != nil || len(many.Spawns) != len(cases) || len(many.NotFound) != 0 {
			t.Fatalf("GetMany: %+v %v", many, err)
		}
		for i, tc := range cases {
			got, err := c.Get(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			status, err := c.Status(tc.id)
			if err != nil {
				t.Fatal(err)
			}
			for name, out := range map[string]any{"get": got, "get-many": many.Spawns[i], "status": status} {
				raw, present := jsonField(t, out, "failure_reason")
				if !present || string(raw) != tc.want {
					t.Errorf("%s/%s failure_reason=%s present=%v; want %s", tc.id, name, raw, present, tc.want)
				}
			}
			if got.Agent != tc.agent || many.Spawns[i].Agent != tc.agent {
				t.Fatal("agent support discriminator lost")
			}
			var confirmation map[string]any
			if err := json.Unmarshal([]byte(jsonOf(t, got.Confirmations.State)), &confirmation); err != nil {
				t.Fatal(err)
			}
			var wantReason any
			if tc.evidence == "" {
				wantReason = "no_evidence"
			}
			if confirmation["reason"] != wantReason {
				t.Fatalf("failure text leaked into confirmation reason: %v", confirmation)
			}
		}
	}
	listed, err := c.List(api.ListParams{})
	if err != nil || len(listed.Spawns) != len(cases) {
		t.Fatalf("List: %+v %v", listed, err)
	}
	for _, row := range listed.Spawns {
		if _, present := jsonField(t, row, "failure_reason"); present {
			t.Fatal("list acquired a failure_reason field")
		}
	}
}
