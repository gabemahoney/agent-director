//go:build linux

package api_test

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/google/uuid"
)

// B15: "spawn --agent cursor --cwd \"$PWD\"" with an automatically generated unique instance_id.
func TestAdviceFollow_B15_CursorResumeFreshUniqueInstance(t *testing.T) {
	// Serial: newSpawnEnv owns HOME and process environment.
	for _, kind := range []string{"pending no session", "finished no session", "HOME", "invalid env", "missing transcript"} {
		t.Run(kind, func(t *testing.T) {
			e := newSpawnEnv(t)
			id := uuid.NewString()
			h := cursorfix.NewGatewayHistory(t, id, 1, "latest", "older")
			state, session := store.StateEnded, "latest"
			var env map[string]string
			switch kind {
			case "pending no session":
				state, session = store.StatePending, ""
			case "finished no session":
				session = ""
			case "HOME":
				env = map[string]string{"HOME": "/saved-invalid-home"}
			case "invalid env":
				env = map[string]string{"BROKEN=NAME": "value"}
			case "missing transcript":
				if err := os.Remove(h.Latest); err != nil {
					t.Fatal(err)
				}
			}
			_, err := apitest.SeedSpawn(e.dbPath, id, state, t.TempDir(), "off", session, false, apitest.WithAgent(store.AgentCursor), apitest.WithJsonlPath(h.Latest), apitest.WithExtraEnv(env))
			if err != nil {
				t.Fatal(err)
			}
			before, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.c.Resume(api.ResumeParams{InstanceID: id})
			phrase := `from the intended project directory run agent-director spawn --agent cursor --cwd "$PWD" for a fresh conversation with an automatically generated unique instance_id`
			adviceAssertPhrase(t, err, phrase)
			want := "ErrJsonlMissing"
			switch kind {
			case "pending no session":
				want = "ErrSpawnNotResumable"
			case "finished no session":
				want = "ErrNoSessionId"
			case "HOME", "invalid env":
				want = "ErrReservedEnvKey"
			}
			assertOneName(t, err, want)
			// Pending advice requires the old launch to be reconciled before fresh recovery.
			if kind == "pending no session" {
				cursorReadSQL(t, e.dbPath, `UPDATE spawns SET state='missing' WHERE instance_id=?`, id)
				before, err = apitest.ReadSpawnColumns(e.dbPath, id)
				if err != nil {
					t.Fatal(err)
				}
			}
			cwd := t.TempDir()
			fresh, err := e.c.Spawn(api.SpawnParams{Agent: store.AgentCursor, CWD: cwd})
			if err != nil || fresh.InstanceID == id || fresh.InstanceID == "" {
				t.Fatalf("literal fresh remedy: %+v %v", fresh, err)
			}
			after, err := apitest.ReadSpawnColumns(e.dbPath, id)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("fresh recovery changed original row")
			}
			got, err := os.ReadFile(h.Older)
			if err != nil || !bytes.Equal(got, h.OlderBytes) {
				t.Fatal("fresh recovery changed retained artifact")
			}
			if kind != "missing transcript" {
				got, err = os.ReadFile(h.Latest)
				if err != nil || !bytes.Equal(got, h.LatestBytes) {
					t.Fatal("fresh recovery changed latest artifact")
				}
			}
		})
	}
}
