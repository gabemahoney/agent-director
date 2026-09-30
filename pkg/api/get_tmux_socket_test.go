package api_test

// get_tmux_socket_test.go covers tmux_socket (SR-3.3, SR-16.1, AC-LKP-22):
// get shows the row's recorded socket in any state and omits the key (never
// null or "") on a row from before this release; status and list never carry it.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// socketShape is one seeded row: its state, seed options, and the socket get
// must show ("" = none recorded, key absent).
type socketShape struct {
	name   string
	state  string
	opts   []apitest.SpawnOption
	socket string
}

// id is the shape's instance id in the shared store.
func (s socketShape) id() string { return "sock-" + strings.ReplaceAll(s.name, " ", "-") }

// socketShapes covers an explicit socket (live, pending and finished rows), the
// seed default socket, and pre-release rows (live and finished) with none.
func socketShapes() []socketShape {
	const work = "/tmp/tmux-1000/work sock"
	return []socketShape{
		{"explicit waiting", store.StateWaiting, []apitest.SpawnOption{apitest.WithTmuxSocket(work)}, work},
		{"explicit pending", store.StatePending, []apitest.SpawnOption{apitest.WithTmuxSocket(work)}, work},
		{"explicit ended", store.StateEnded, []apitest.SpawnOption{apitest.WithTmuxSocket(work)}, work},
		{"explicit missing", store.StateMissing, []apitest.SpawnOption{apitest.WithTmuxSocket(work)}, work},
		{"default working", store.StateWorking, nil, apitest.TestSocket},
		{"pre-release waiting", store.StateWaiting, []apitest.SpawnOption{apitest.WithNoLaunchToken()}, ""},
		{"pre-release ended", store.StateEnded, []apitest.SpawnOption{apitest.WithNoLaunchToken()}, ""},
	}
}

// newSocketClient returns a Client over a store seeded with every shape.
func newSocketClient(t *testing.T, shapes []socketShape) *api.Client {
	t.Helper()
	c, _ := newTestClientWithRows(t, func(dbPath string) {
		for _, s := range shapes {
			if _, err := apitest.SeedSpawn(dbPath, s.id(), s.state, "", "", "", true, s.opts...); err != nil {
				t.Fatalf("SeedSpawn %s: %v", s.name, err)
			}
		}
	})
	return c
}

// socketJSON returns v's JSON tmux_socket value and whether the key is present.
func socketJSON(t *testing.T, v any) (json.RawMessage, bool) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	raw, ok := m["tmux_socket"]
	return raw, ok
}

// assertGetSocket checks Get's typed field and JSON key against want ("" =
// the key absent).
func assertGetSocket(t *testing.T, c *api.Client, id, want string) {
	t.Helper()
	r, err := c.Get(id)
	if err != nil {
		t.Fatalf("Get(%s): %v", id, err)
	}
	if r.TmuxSocket != want {
		t.Errorf("TmuxSocket = %q; want %q", r.TmuxSocket, want)
	}
	raw, ok := socketJSON(t, r)
	switch {
	case want == "" && ok:
		t.Errorf("tmux_socket = %s; want the key absent", raw)
	case want == "":
	case !ok:
		t.Errorf("tmux_socket absent; want %q", want)
	case string(raw) != strconv.Quote(want):
		t.Errorf("tmux_socket = %s; want %q", raw, want)
	}
}

// TestTmuxSocketGetByRow: get shows the recorded socket in any state, and no
// key on a row that records none.
func TestTmuxSocketGetByRow(t *testing.T) {
	shapes := socketShapes()
	c := newSocketClient(t, shapes)
	for _, s := range shapes {
		t.Run(s.name, func(t *testing.T) { assertGetSocket(t, c, s.id(), s.socket) })
	}
}

// TestTmuxSocketStatusAndListOmit: status and list carry no tmux_socket key on
// any row, including those that record a socket.
func TestTmuxSocketStatusAndListOmit(t *testing.T) {
	shapes := socketShapes()
	c := newSocketClient(t, shapes)
	lr, err := c.List(api.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(lr.Spawns) != len(shapes) {
		t.Errorf("List rows = %d; want %d", len(lr.Spawns), len(shapes))
	}
	for _, r := range lr.Spawns {
		if raw, ok := socketJSON(t, r); ok {
			t.Errorf("list row %s tmux_socket = %s; want the key absent", r.ClaudeInstanceID, raw)
		}
	}
	for _, s := range shapes {
		t.Run("status/"+s.name, func(t *testing.T) {
			r, err := c.Status(s.id())
			if err != nil {
				t.Fatalf("Status(%s): %v", s.id(), err)
			}
			if raw, ok := socketJSON(t, r); ok {
				t.Errorf("status tmux_socket = %s; want the key absent", raw)
			}
		})
	}
}

// TestTmuxSocketAfterSpawn: after a spawn, get shows the socket the launch
// resolved (TMUX unset, so the default socket under TMUX_TMPDIR).
func TestTmuxSocketAfterSpawn(t *testing.T) {
	env := newSpawnEnv(t)
	res, err := env.c.Spawn(api.SpawnParams{CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	assertGetSocket(t, env.c, res.ClaudeInstanceID, env.socket)
}
