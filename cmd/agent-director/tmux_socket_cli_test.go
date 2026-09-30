package main_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// socketCLIPath is the explicit recorded socket the seeded rows carry; it
// differs from apitest.TestSocket so the test sees the row's own value.
const socketCLIPath = "/tmp/agent-director-socket-cli/explicit"

// socketShape is one seeded row shape for the tmux_socket CLI tests; want is
// the socket get must print, or "" when the key must be absent.
type socketShape struct {
	name, id, state string
	opt             apitest.SpawnOption
	want            string
}

// socketShapes covers rows recording an explicit socket (live, pending and
// ended: get shows it in any state) and pre-release rows recording none.
func socketShapes() []socketShape {
	return []socketShape{
		{"waiting explicit socket", "sock-waiting", "waiting", apitest.WithTmuxSocket(socketCLIPath), socketCLIPath},
		{"pending explicit socket", "sock-pending", "pending", apitest.WithTmuxSocket(socketCLIPath), socketCLIPath},
		{"ended explicit socket", "sock-ended", "ended", apitest.WithTmuxSocket(socketCLIPath), socketCLIPath},
		{"waiting pre-release", "sock-pre-waiting", "waiting", apitest.WithNoLaunchToken(), ""},
		{"ended pre-release", "sock-pre-ended", "ended", apitest.WithNoLaunchToken(), ""},
	}
}

// TestTmuxSocketCLISeeded pins SR-3.3/SR-16.1/SR-5.1 (AC-LKP-22) on the CLI:
// get prints the row's recorded tmux_socket, omits the key for a pre-release
// row, and status and list never carry it; every verb exits 0.
func TestTmuxSocketCLISeeded(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	if _, stderr, code := runSpawnCLI(t, home, fakeDir, "list"); code != 0 {
		t.Fatalf("bootstrap list exit = %d; stderr=%s", code, stderr)
	}
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	for _, sh := range socketShapes() {
		if _, err := apitest.SeedSpawn(dbPath, sh.id, sh.state, "/tmp", "off", "", false, sh.opt); err != nil {
			t.Fatalf("seed %s: %v", sh.id, err)
		}
	}
	for _, verb := range []string{"get", "status", "list"} {
		for _, sh := range socketShapes() {
			want := sh.want
			if verb != "get" {
				want = ""
			}
			t.Run(verb+"/"+sh.name, func(t *testing.T) {
				assertSocketField(t, verbObject(t, home, fakeDir, verb, sh.id), want)
			})
		}
	}
}

// assertSocketField checks obj's tmux_socket against want ("" = key absent);
// a present value must be a JSON string.
func assertSocketField(t *testing.T, obj map[string]json.RawMessage, want string) {
	t.Helper()
	raw, present := obj["tmux_socket"]
	if !present {
		if want != "" {
			t.Errorf("tmux_socket absent; want %q", want)
		}
		return
	}
	if want == "" {
		t.Errorf("tmux_socket = %s; want key absent", raw)
		return
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("tmux_socket = %s; want a JSON string", raw)
	}
	if got != want {
		t.Errorf("tmux_socket = %q; want %q", got, want)
	}
}
