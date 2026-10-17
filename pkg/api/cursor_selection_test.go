//go:build linux

package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestCursorPublicProfileRefusal(t *testing.T) {
	// Serial: shared spawn fixture sets HOME and captures whole-store mutation evidence.
	for _, env := range []map[string]string{{"CURSOR_API_KEY": ""}, {"CURSOR_API_KEY": "synthetic-secret-private"}, {"AGENT_CLI_CREDENTIAL_STORE": "memory"}, {"AGENT_CLI_CREDENTIAL_STORE": ""}, {"CURSOR_API_KEY": "synthetic-secret-private", "AGENT_CLI_CREDENTIAL_STORE": "memory"}} {
		for _, operation := range []string{"spawn", "reuse", "template", "generic template use"} {
			t.Run(operation, func(t *testing.T) {
				e := newSpawnEnv(t)
				p := api.SpawnParams{Agent: "cursor", CWD: t.TempDir(), InstanceID: "selection-refusal", ExtraEnv: env, NoPreTrust: true}
				if operation == "reuse" {
					if _, err := apitest.SeedSpawn(e.dbPath, p.InstanceID, store.StateEnded, p.CWD, "off", "old-session", false, apitest.WithAgent("cursor"), apitest.WithExtraEnv(map[string]string{"CURSOR_CONFIG_DIR": "/old/config"})); err != nil {
						t.Fatal(err)
					}
					p.ReuseFinished = true
				}
				if operation == "generic template use" {
					if _, err := e.c.MakeTemplate(api.MakeTemplateParams{Name: "generic", ExtraEnv: env}); err != nil {
						t.Fatal(err)
					}
					p.Template, p.ExtraEnv = "generic", nil
				}
				before := listIDs(t, e.c)
				mark := trailMark(t)
				var saved apitest.SpawnColumns
				if operation == "reuse" {
					var err error
					saved, err = apitest.ReadSpawnColumns(e.dbPath, p.InstanceID)
					if err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if operation == "template" {
					_, err = e.c.MakeTemplate(api.MakeTemplateParams{Name: "refused", Agent: "cursor", ExtraEnv: env})
				} else {
					_, err = e.c.Spawn(p)
				}
				assertOneName(t, err, "ErrReservedEnvKey")
				if strings.Contains(errText(err), "synthetic-secret-private") {
					t.Fatal("secret leaked through refusal")
				}
				assertNoTmuxCalls(t, e.rec)
				assertNoTrailSince(t, mark, p.InstanceID)
				if !reflect.DeepEqual(before, listIDs(t, e.c)) {
					t.Fatal("refusal changed rows")
				}
				if operation == "reuse" {
					after, err := apitest.ReadSpawnColumns(e.dbPath, p.InstanceID)
					if err != nil || !reflect.DeepEqual(saved, after) {
						t.Fatalf("reuse refusal changed row: %v", err)
					}
				}
				if operation == "template" {
					if _, err := os.Stat(filepath.Join(adConfigDir(e.home), "templates", "refused.toml")); !os.IsNotExist(err) {
						t.Fatalf("refused template written: %v", err)
					}
				}
			})
		}
	}
}

func TestCursorTemplateDirectorySelection(t *testing.T) {
	// Serial: spawn fixture owns process-wide HOME.
	e := newSpawnEnv(t)
	installCursorFixtureGateway(t)
	if _, err := e.c.MakeTemplate(api.MakeTemplateParams{Name: "selected", Agent: "cursor", ExtraEnv: map[string]string{"CURSOR_CONFIG_DIR": "/template/config", "CURSOR_DATA_DIR": "/template/data"}}); err != nil {
		t.Fatal(err)
	}
	result, err := e.c.Spawn(api.SpawnParams{Template: "selected", CWD: t.TempDir(), ExtraEnv: map[string]string{"CURSOR_CONFIG_DIR": "/call/config with spaces"}, NoPreTrust: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := e.rec.SocketCallsOf(tmux.CallCreate)
	if len(calls) != 1 || calls[0].Envs["CURSOR_CONFIG_DIR"] != "/call/config with spaces" || calls[0].Envs["CURSOR_DATA_DIR"] != "/template/data" {
		t.Fatalf("resolved selection lost: %+v", calls)
	}
	row, err := apitest.ReadSpawnColumns(e.dbPath, result.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.ExtraEnv.(string), "/call/config with spaces") {
		t.Fatal("resolved selection not protected")
	}
}

func TestCursorReuseDirectoryRestoration(t *testing.T) {
	// Serial: reusable fixture sets HOME and owns trail assertions.
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "new request", true: "determinate restore"}[fail], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			installCursorFixtureGateway(t)
			e := newReuseEnv(t)
			r := e.seedReusable(t, agentGone, reuseRowSpec{Bare: true, Opts: []apitest.SpawnOption{apitest.WithAgent("cursor"), apitest.WithRawAgentArgs(`[]`), apitest.WithExtraEnv(map[string]string{"CURSOR_CONFIG_DIR": "/old/config"})}})
			before := e.columns(t, r.ID)
			p := reuseParams(t, r, reuseRequest{NoPreTrust: true})
			p.Agent = "cursor"
			p.ExtraEnv = map[string]string{"CURSOR_CONFIG_DIR": "/new/config"}
			if fail {
				e.rec.Script(tmuxfix.AnySocket, tmuxfix.Script{Failure: tmux.FailUnrecognized, ExitStatus: 1, Times: 1}, tmux.CallCreate)
			}
			_, _, err := e.reuse(t, p)
			if fail {
				before.LaunchStartedAt, before.RowVersion = nil, before.RowVersion.(int64)+2
				if err == nil || !reflect.DeepEqual(before, e.columns(t, r.ID)) {
					t.Fatalf("failed reuse lost saved environment: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				calls := e.rec.SocketCallsOf(tmux.CallCreate)
				if len(calls) != 1 || calls[0].Envs["CURSOR_CONFIG_DIR"] != "/new/config" {
					t.Fatal("reuse ignored new selection")
				}
			}
		})
	}
}

func cursorSelectionJSON(env map[string]string) string {
	raw, _ := json.Marshal(env)
	return string(raw)
}
