package main_test

import (
	"encoding/json"
	"github.com/gabemahoney/agent-director/internal/cursormcp"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCursorMCPMalformedScopeRefusesBeforeStoreSetup(t *testing.T) {
	cases := map[string]string{"empty": "", "malformed": `{`, "missing facts": `{ "Parent":"missing-facts" }`}
	valid := cursormcp.Scope{StorePath: "/synthetic/store.db", StoreID: "store", Home: "/synthetic/home", Parent: "parent", Socket: "/synthetic/tmux", Token: "token", Life: 0, Connection: "connection", GatewayPID: 1, GatewayStart: "start", RuntimePID: 2, RuntimeStart: "start", Receipt: "/synthetic/receipt", Nonce: strings.Repeat("a", 64), Until: time.Now().Add(time.Hour)}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for name, life := range map[string]string{"missing life": "", "null life": "null", "negative life": "-1", "fraction life": "0.5", "string life": `"0"`, "overflow life": "9223372036854775808"} {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		delete(fields, "Life")
		if life != "" {
			fields["Life"] = json.RawMessage(life)
		}
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		cases[name] = string(raw)
	}
	for name, scope := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			explicit := filepath.Join(t.TempDir(), "must-not-create.db")
			bin := os.Getenv("AD_TEST_GUIDANCE_CLI")
			out, errOut, code, _ := execCLI(t, cliOpts{bin: bin, env: homeEnv(home, map[string]string{cursormcp.ScopeEnv: scope})}, "--store-path", explicit, "serve", "--stdio")
			if code == 0 || !strings.Contains(out+errOut, `Cursor MCP scope invalid`) {
				t.Fatalf("malformed scope exit=%d stdout=%s stderr=%s", code, out, errOut)
			}
			if _, err := os.Stat(explicit); !os.IsNotExist(err) {
				t.Fatalf("malformed scope created explicit store: %v", err)
			}
			assertHomeTree(t, home)
		})
	}
}
