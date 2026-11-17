package envelope_diff

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// One ready stand-in represents a covering build, without adding another
// selector/seeding authority. Real publication validates BOTH staged facts
// against the anchor/floor before writing ready; each call reads its outcome
// from a test-owned external file, leaving immutable version bytes unchanged.
func resumeStandIn(t *testing.T, home string) (reply, received string) {
	t.Helper()
	dir := t.TempDir()
	reply, received = filepath.Join(dir, "reply"), filepath.Join(dir, "received")
	core, digest := store.BusySendCoreContractIdentity()
	facts := installversion.Facts{FormatVersion: 1, Version: "covering-adapter", Commit: "fixture", CoveredAgents: []string{"claude", "codex"}, CoreVersion: core, CoreDeclarationsSHA256: digest}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\n--install-version-facts|version-facts) printf '%s\\n' '" + string(encoded) + "';;\n--resume-delegation-job) cat >> '" + received + "'; cat '" + reply + "';;\n*) exit 71;;\nesac\n"
	runtime, admin := filepath.Join(dir, "runtime"), filepath.Join(dir, "admin")
	for _, path := range []string{runtime, admin} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (installversion.Engine{Root: layout.Dir(home)}).Publish(context.Background(), runtime, admin, facts); err != nil {
		t.Fatal(err)
	}
	return reply, received
}

// b.kii / SR-9.1: CLI resumeHandlerWith and the Go Client each forward the
// same recipient outcome. The sender's selector/failure matrix lives in api.
func TestResumeSurfaceForwarding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	db := filepath.Join(layout.Dir(home), "state.db")
	if _, err := apitest.SeedSpawn(db, "delegated-row", store.StateEnded, t.TempDir(), "off", "", true, apitest.WithAgent("codex")); err != nil {
		t.Fatal(err)
	}
	if err := apitest.SeedBusySendCoverage(db, "delegated-row", store.BusySendCovered, store.BusySendCovered); err != nil {
		t.Fatal(err)
	}
	reply, received := resumeStandIn(t, home)
	c, err := api.New(api.Options{StorePath: db})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, tc := range []struct {
		name, field, payload string
		failure              bool
	}{
		{"success", "result", `{"instance_id":"delegated-row","pre_trust":"skipped","future":false,"nullable":null,"large":9007199254740993}`, false},
		{"cover unavailable", "error", `{"err_name":"ErrSpawnNotResumable","err_description":"no covering runtime","err_details":{"cause":"covering_version_unavailable","provider":"codex"}}`, true},
		{"unknown name", "error", `{"err_name":"ErrFutureResume","err_description":"future refusal","err_details":{"future":false,"nullable":null,"large":9007199254740993}}`, true},
		{"unknown outcome", "error", `{"err_name":"ErrInternal","err_description":"outcome unknown; check get"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(reply, []byte(`{"response_format":1,"`+tc.field+`":`+tc.payload+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(received, nil, 0600); err != nil {
				t.Fatal(err)
			}
			cli, code := runCLI(t, buildCLI(t), db, "resume", "--instance-id", "delegated-row", "--confirmed-delivery=false", "--confirm-from-transcripts=false")
			goResult, failed := dispatchResume(c, map[string]any{"instance_id": "delegated-row", "confirmed_delivery": false, "confirm_from_transcripts": false})
			if (code != 0) != tc.failure || failed != tc.failure || strings.TrimSpace(string(cli)) != tc.payload || string(goResult) != tc.payload {
				t.Fatalf("CLI (%d) %s\nGo (%v) %s\nwant %s", code, cli, failed, goResult, tc.payload)
			}
			calls, err := os.ReadFile(received)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if len(lines) != 2 {
				t.Fatalf("delegate invocations %d; want one per interface", len(lines))
			}
			for _, line := range lines {
				request, err := resumedelegate.DecodeRequest(strings.NewReader(line))
				if err != nil {
					t.Fatal(err)
				}
				var params map[string]json.RawMessage
				if err := json.Unmarshal(request.Params, &params); err != nil {
					t.Fatal(err)
				}
				if string(params["confirmed_delivery"]) != "false" || string(params["confirm_from_transcripts"]) != "false" {
					t.Fatalf("explicit false lost: %s", request.Params)
				}
			}
			if _, err := c.Version(); err != nil {
				t.Fatalf("library no longer usable: %v", err)
			}
		})
	}
}
