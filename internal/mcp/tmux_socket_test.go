package mcp_test

import (
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/mcp"
	api "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// mcpSocketShape is one seeded row shape; sock builds the row's recorded
// socket from the store dir ("" for none), and opt seeds it.
type mcpSocketShape struct {
	name, id, state string
	sock            func(dir string) string
	opt             func(sock string) apitest.SpawnOption
}

func customSocket(dir string) string { return filepath.Join(dir, "tmux-custom", "default") }
func testSocket(string) string       { return apitest.TestSocket }
func noSocket(string) string         { return "" }

func withSocket(sock string) apitest.SpawnOption { return apitest.WithTmuxSocket(sock) }
func preRelease(string) apitest.SpawnOption      { return apitest.WithNoLaunchToken() }

var mcpSocketShapes = []mcpSocketShape{
	{"waiting custom socket", "sock-waiting", "waiting", customSocket, withSocket},
	{"pending custom socket", "sock-pending", "pending", customSocket, withSocket},
	{"ended custom socket", "sock-ended", "ended", customSocket, withSocket},
	{"waiting default test socket", "sock-default", "waiting", testSocket, withSocket},
	{"waiting pre-release row", "sock-prerelease", "waiting", noSocket, preRelease},
	{"ended pre-release row", "sock-prerelease-ended", "ended", noSocket, preRelease},
}

// newSocketShapesDispatcher seeds every mcpSocketShapes row into a temp store
// and returns a live dispatcher over a real Client on it, plus the store dir
// the custom socket paths are built from.
func newSocketShapesDispatcher(t *testing.T) (mcp.Dispatcher, string) {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "state.db")
	cfgPath := filepath.Join(dir, "config.toml")
	apitest.WriteTmuxConfig(t, cfgPath)
	for _, sh := range mcpSocketShapes {
		if _, err := apitest.SeedSpawn(storePath, sh.id, sh.state, "/tmp", "off", "", true, sh.opt(sh.sock(dir))); err != nil {
			t.Fatalf("seed %s: %v", sh.id, err)
		}
	}
	client, err := api.New(api.Options{StorePath: storePath, ConfigPath: cfgPath, CreateIfMissing: true})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return mcp.NewLiveDispatcher(client), dir
}

// TestTmuxSocketMCP pins SR-3.3/SR-16.1 (AC-LKP-22) on MCP: the get tool text
// carries tmux_socket, the row's recorded socket in any state, and omits the
// key for a row from before this release; status and list texts never carry
// it (SR-5.1 "get also shows tmux_socket").
func TestTmuxSocketMCP(t *testing.T) {
	d, dir := newSocketShapesDispatcher(t)
	for _, verb := range []string{"get", "status", "list"} {
		for _, sh := range mcpSocketShapes {
			t.Run(verb+"/"+sh.name, func(t *testing.T) {
				want := ""
				if verb == "get" {
					want = sh.sock(dir)
				}
				raw, present := mcpVerbObject(t, d, verb, sh.id)["tmux_socket"]
				switch {
				case want == "" && present:
					t.Errorf("%s tmux_socket = %s; want key absent", verb, raw)
				case want != "" && string(raw) != `"`+want+`"`:
					t.Errorf("%s tmux_socket = %s (present=%v); want %q", verb, raw, present, want)
				}
			})
		}
	}
}
