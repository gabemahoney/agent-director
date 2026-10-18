// Package cursormcp owns Cursor's session-scoped registration of the existing
// AD stdio MCP server. Association is routing, not permission authority.
package cursormcp

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// ScopeEnv is private launch plumbing, not a public configuration switch.
const ScopeEnv = "AGENT_DIRECTOR_CURSOR_MCP_SCOPE"

type Env struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Stdio uses only standard ACP session-setup fields. No shell is involved.
type Stdio struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Env     []Env    `json:"env"`
}

// Scope binds one initialization observation to the actual opened store,
// current protected runtime launch and a single session-setup attempt. Session
// identity is confirmed separately by ACP before this observation is admitted.
type Scope struct {
	StorePath    string
	StoreID      string
	Home         string
	Parent       string
	Socket       string
	Token        string
	Life         int64
	Connection   string
	GatewayPID   int
	GatewayStart string
	RuntimePID   int
	RuntimeStart string
	Receipt      string
	Nonce        string
	Until        time.Time
}

type Observation struct {
	dir      string
	listener *net.UnixListener
	scope    Scope
	mu       sync.Mutex
	closed   bool
	active   *net.UnixConn
}

// New creates a fresh private observation for every session/new or session/load.
// The caller supplies the existing operation deadline, never a renewed budget.
func New(exe, home string, s *store.Store, row store.Spawn, b store.CursorRuntime, until time.Time) (*Observation, Stdio, error) {
	fail := func() (*Observation, Stdio, error) {
		return nil, Stdio{}, fmt.Errorf("Cursor guidance setup unavailable")
	}
	if !filepath.IsAbs(exe) || !filepath.IsAbs(home) || !filepath.IsAbs(s.Path()) || row.InstanceID != b.InstanceID || row.Identity.Token != b.Token || row.LifeNumber != b.Life || !filepath.IsAbs(row.Identity.Socket) || strings.Contains(row.Identity.Socket, ",") || b.Connection == "" || !time.Now().Before(until) {
		return fail()
	}
	dir, err := os.MkdirTemp("/tmp", "ad-mcp-")
	if err != nil {
		return fail()
	}
	if err = os.Chmod(dir, 0700); err != nil {
		os.Remove(dir)
		return fail()
	}
	path := filepath.Join(dir, "init.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		os.Remove(dir)
		return fail()
	}
	o := &Observation{dir: dir, listener: listener}
	if err = os.Chmod(path, 0600); err != nil {
		o.Close()
		return fail()
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		o.Close()
		return fail()
	}
	o.scope = Scope{StorePath: s.Path(), StoreID: s.StoreID(), Home: home, Parent: row.InstanceID, Socket: row.Identity.Socket, Token: b.Token, Life: b.Life, Connection: b.Connection, GatewayPID: b.GatewayPID, GatewayStart: b.GatewayStart, RuntimePID: b.RuntimePID, RuntimeStart: b.RuntimeStart, Receipt: path, Nonce: hex.EncodeToString(nonce[:]), Until: until}
	encoded, err := json.Marshal(o.scope)
	if err != nil {
		o.Close()
		return fail()
	}
	registration := Stdio{Name: "agent-director", Command: exe, Args: []string{"--home", home, "--store-path", s.Path(), "--create-if-missing=false", "serve", "--stdio"}, Env: []Env{
		{Name: "AGENT_DIRECTOR_INSTANCE_ID", Value: row.InstanceID},
		{Name: "AGENT_DIRECTOR_AGENT", Value: "cursor"},
		{Name: "TMUX", Value: row.Identity.Socket + "," + strconv.Itoa(row.Identity.ServerPID) + ",0"},
		{Name: "TMUX_PANE", Value: row.Identity.PaneID},
		{Name: "TMUX_TMPDIR", Value: ""},
		{Name: ScopeEnv, Value: string(encoded)},
	}}
	return o, registration, nil
}

// Wait accepts only the current attempt's post-initialize client acknowledgement.
// The gateway's await loop retains control/death supervision while this blocks.
func (o *Observation) Wait() error {
	if err := o.listener.SetDeadline(o.scope.Until); err != nil {
		return fmt.Errorf("Cursor guidance observation unavailable")
	}
	for {
		conn, err := o.listener.AcceptUnix()
		if err != nil {
			return fmt.Errorf("Cursor guidance initialization unconfirmed")
		}
		o.mu.Lock()
		if o.closed {
			o.mu.Unlock()
			conn.Close()
			return fmt.Errorf("Cursor guidance observation closed")
		}
		o.active = conn
		o.mu.Unlock()
		_ = conn.SetDeadline(o.scope.Until)
		line, err := bufio.NewReaderSize(conn, 128).ReadSlice('\n')
		conn.Close()
		o.mu.Lock()
		o.active = nil
		closed := o.closed
		o.mu.Unlock()
		if closed {
			return fmt.Errorf("Cursor guidance observation closed")
		}
		if err == nil && string(line) == o.scope.Nonce+"\n" && time.Now().Before(o.scope.Until) {
			return nil
		}
	}
}

func (o *Observation) Close() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.closed = true
	if o.active != nil {
		o.active.Close()
	}
	o.mu.Unlock()
	o.listener.Close()
	// This is our newly created private directory, containing only this socket.
	os.Remove(filepath.Join(o.dir, "init.sock"))
	os.Remove(o.dir)
}

func Parse(raw string) (Scope, error) {
	// The first stored life is zero. Require an explicit integer so an omitted
	// or null life cannot accidentally select that legitimate first life.
	var decoded struct {
		Scope
		Life *int64
	}
	if len(raw) > 16384 || json.Unmarshal([]byte(raw), &decoded) != nil || decoded.Life == nil || *decoded.Life < 0 {
		return Scope{}, fmt.Errorf("Cursor MCP scope invalid")
	}
	scope := decoded.Scope
	scope.Life = *decoded.Life
	if scope.Parent == "" || scope.Token == "" || scope.StoreID == "" || scope.Connection == "" || scope.GatewayPID <= 0 || scope.RuntimePID <= 0 || scope.GatewayStart == "" || scope.RuntimeStart == "" || len(scope.Nonce) != 64 || scope.Until.IsZero() || !filepath.IsAbs(scope.StorePath) || !filepath.IsAbs(scope.Home) || !filepath.IsAbs(scope.Socket) || strings.Contains(scope.Socket, ",") || !filepath.IsAbs(scope.Receipt) {
		return Scope{}, fmt.Errorf("Cursor MCP scope invalid")
	}
	if _, err := hex.DecodeString(scope.Nonce); err != nil {
		return Scope{}, fmt.Errorf("Cursor MCP scope invalid")
	}
	return scope, nil
}

// Binding verifies protected records on every server start and acknowledgement.
// SessionID is deliberately read from the current record: the same MCP process
// can remain useful after publication, but cannot certify another attempt.
func (scope Scope) Binding(s *store.Store) (store.Spawn, store.CursorRuntime, error) {
	row, e := s.GetSpawn(scope.Parent)
	b, err := s.ReadCursorRuntime(scope.Parent)
	if e != nil || err != nil || s.Path() != scope.StorePath || s.StoreID() != scope.StoreID || row.Agent != store.AgentCursor || row.State == store.StateEnded || row.State == store.StateMissing || row.Identity.Token != scope.Token || row.Identity.Socket != scope.Socket || row.Identity.PanePID != scope.GatewayPID || row.Identity.PaneStarttime != scope.GatewayStart || row.LifeNumber != scope.Life || b.Token != scope.Token || b.Life != scope.Life || b.Connection != scope.Connection || b.GatewayPID != scope.GatewayPID || b.GatewayStart != scope.GatewayStart || b.RuntimePID != scope.RuntimePID || b.RuntimeStart != scope.RuntimeStart || b.Terminated {
		return store.Spawn{}, store.CursorRuntime{}, fmt.Errorf("Cursor MCP scope retired")
	}
	return row, b, nil
}

// Acknowledge is called only after the MCP client sends notifications/initialized.
// A restarted server may outlive the admission observer; it still provides the
// same instructions, but cannot retroactively authorize a later operation.
func (scope Scope) Acknowledge() error {
	if !time.Now().Before(scope.Until) {
		return nil
	}
	until := minTime(scope.Until, time.Now().Add(time.Second))
	conn, err := net.DialTimeout("unix", scope.Receipt, time.Until(until))
	if err != nil {
		return fmt.Errorf("Cursor MCP initialization observation unavailable")
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(until)
	if _, err = fmt.Fprintln(conn, scope.Nonce); err != nil {
		return fmt.Errorf("Cursor MCP initialization observation unavailable")
	}
	return nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
