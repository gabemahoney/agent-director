package api_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// A25/B14: "create a fresh instance" after an unsupported Cursor lifecycle operation.
func TestAdviceFollow_A25_B14_CursorFreshInstanceRemedy(t *testing.T) {
	// Serial: newSpawnEnv sets HOME, TMUX and the current instance environment.
	for _, tc := range []struct {
		name, rowAgent, agent string
		resume                bool
		state, session        string
	}{
		{"same agent reuse", "cursor", "cursor", false, "ended", "synthetic-session"},
		{"switch to cursor", "claude", "cursor", false, "ended", "synthetic-session"},
		{"switch from cursor", "cursor", "claude", false, "ended", "synthetic-session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSpawnEnv(t)
			id := uuid.NewString()
			if _, err := apitest.SeedSpawn(e.dbPath, id, tc.state, t.TempDir(), "off", tc.session, false, apitest.WithAgent(tc.rowAgent)); err != nil {
				t.Fatal(err)
			}
			before, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.resume {
				_, err = e.c.Resume(api.ResumeParams{InstanceID: id})
			} else {
				_, err = e.c.Spawn(api.SpawnParams{InstanceID: id, Agent: tc.agent, CWD: t.TempDir(), ReuseFinished: true, NoPreTrust: true})
			}
			if err == nil || !strings.Contains(err.Error(), "create a fresh instance") {
				t.Fatalf("Cursor operation refused without literal remedy: %v", err)
			}
			want := "ErrSpawnDeniedFlag"
			if tc.resume {
				want = "ErrSpawnNotResumable"
			}
			assertOneName(t, err, want)
			after, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatal("refused operation changed existing row")
			}
			if len(e.rec.Calls()) != 0 {
				t.Fatal("refused operation touched tmux")
			}
			fresh, err := e.c.Spawn(api.SpawnParams{InstanceID: uuid.NewString(), Agent: tc.agent, CWD: t.TempDir(), NoPreTrust: true})
			if err != nil || fresh.InstanceID == id {
				t.Fatalf("literal fresh-instance remedy failed: %+v %v", fresh, err)
			}
		})
	}
}
