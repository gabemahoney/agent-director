package main_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// serveSession is a running `serve --stdio` whose stdin stays open between
// requests; stdout is read line by line so each request gets its reply.
type serveSession struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	stderr strings.Builder
	nextID int
	done   bool
}

// startServe starts `serve --stdio` in home; the caller registers kill as cleanup.
func startServe(t *testing.T, home string) *serveSession {
	t.Helper()
	s := &serveSession{cmd: exec.Command(binaryPath, "serve", "--stdio"), lines: make(chan string, 8), nextID: 1}
	s.cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
	s.cmd.Stderr = &s.stderr
	var err error
	if s.in, err = s.cmd.StdinPipe(); err != nil {
		t.Fatalf("StdinPipe: %v", err)
	}
	out, err := s.cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			s.lines <- sc.Text()
		}
		close(s.lines)
	}()
	return s
}

// kill stops a session that was not stopped cleanly.
func (s *serveSession) kill() {
	if s != nil && !s.done {
		s.done = true
		_ = s.in.Close()
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}

// mcpReply is the part of a JSON-RPC reply the steps assert on.
type mcpReply struct {
	Result *struct {
		ProtocolVersion string `json:"protocolVersion"`
		Content         []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
		Data    struct {
			ErrName string `json:"err_name"`
		} `json:"data"`
	} `json:"error"`
}

// request writes one request line and returns its reply, bounded by surfaceDeadline.
func (s *serveSession) request(t *testing.T, line string) mcpReply {
	t.Helper()
	if _, err := io.WriteString(s.in, line); err != nil {
		t.Fatalf("write request: %v", err)
	}
	select {
	case raw, ok := <-s.lines:
		if !ok {
			t.Fatalf("serve closed stdout before replying to %q", line)
		}
		var r mcpReply
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatalf("parse reply %q: %v", raw, err)
		}
		return r
	case <-time.After(surfaceDeadline):
		t.Fatalf("no reply within %v to %q", surfaceDeadline, line)
	}
	return mcpReply{}
}

// initialize checks the MCP initialize request is answered.
func (s *serveSession) initialize(t *testing.T) {
	t.Helper()
	if r := s.request(t, mcpInitialize); r.Error != nil || r.Result == nil || r.Result.ProtocolVersion == "" {
		t.Fatalf("initialize reply = %+v; want a result with a protocolVersion", r)
	}
}

// listIncludes calls the store-backed `list` tool and checks it succeeds and
// names the seeded row.
func (s *serveSession) listIncludes(t *testing.T, id string) {
	t.Helper()
	s.nextID++
	r := s.request(t, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"list","arguments":{}}}`+"\n", s.nextID))
	if r.Error != nil {
		t.Fatalf("list tool err_name=%q message=%q; want success", r.Error.Data.ErrName, r.Error.Message)
	}
	if r.Result == nil || len(r.Result.Content) != 1 || !strings.Contains(r.Result.Content[0].Text, id) {
		t.Fatalf("list tool result = %+v; want one text part naming %s", r.Result, id)
	}
}

// stop closes stdin and checks serve exits 0 within surfaceDeadline.
func (s *serveSession) stop(t *testing.T) {
	t.Helper()
	_ = s.in.Close()
	deadline := time.After(surfaceDeadline)
	for open := true; open; {
		select {
		case _, open = <-s.lines:
		case <-deadline:
			t.Fatalf("serve still running %v after stdin closed", surfaceDeadline)
		}
	}
	s.done = true
	if err := s.cmd.Wait(); err != nil {
		t.Fatalf("serve exit: %v (stderr=%q)", err, s.stderr.String())
	}
}

// refusalNamed returns the tmuxRefusals row called name.
func refusalNamed(t *testing.T, name string) tmuxRefusal {
	t.Helper()
	for _, rc := range tmuxRefusals() {
		if rc.name == name {
			return rc
		}
	}
	t.Fatalf("no tmuxRefusals row %q", name)
	return tmuxRefusal{}
}

// TestTmuxConfigServeReadsAtStartup pins that `serve --stdio` reads [tmux] once
// at startup: a running server keeps serving after a refused rewrite (SR-4.1).
func TestTmuxConfigServeReadsAtStartup(t *testing.T) {
	t.Parallel()
	const offset = 100
	start := []apitest.TmuxSetting{
		apitest.TmuxInt(config.TmuxQueryTimeoutMs, config.DefaultQueryTimeoutMs+offset),
		apitest.TmuxInt(config.TmuxActionTimeoutMs, config.DefaultActionTimeoutMs+offset),
		apitest.TmuxInt(config.TmuxCreateTimeoutMs, config.DefaultCreateTimeoutMs+offset),
		apitest.TmuxInt(config.TmuxPipeCloseWaitMs, config.DefaultPipeCloseWaitMs+offset),
	}
	rc := refusalNamed(t, "stopping_window_below_minimum")

	home := t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StatePending, "", "", "", true)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	h := refusedHome{home: home, cfgPath: filepath.Join(directorDir(home), "config.toml"), instanceID: id}
	apitest.WriteTmuxConfig(t, h.cfgPath, start...)

	var srv *serveSession
	t.Cleanup(func() { srv.kill() })
	steps := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"starts_and_serves", func(t *testing.T) {
			srv = startServe(t, home)
			srv.initialize(t)
			srv.listIncludes(t, id)
		}},
		{"keeps_serving_after_refused_rewrite", func(t *testing.T) {
			apitest.WriteTmuxConfig(t, h.cfgPath, append(start, rc.bad...)...)
			stdout, stderr, code := runCLIWithHome(t, home, "list")
			if code != 1 {
				t.Errorf("CLI list exit=%d want 1", code)
			}
			assertConfigRefused(t, rc, h, stdout, stderr, code)
			srv.listIncludes(t, id)
		}},
		{"restart_refuses", func(t *testing.T) {
			srv.stop(t)
			stdout, stderr, code, timedOut := runBounded(t, home, nil, mcpInitialize, true, surfaceDeadline, "serve", "--stdio")
			if timedOut {
				t.Fatalf("restarted serve still running after %v; stdout=%q", surfaceDeadline, stdout)
			}
			assertConfigRefused(t, rc, h, stdout, stderr, code)
		}},
		{"serves_after_fix", func(t *testing.T) {
			apitest.WriteTmuxConfig(t, h.cfgPath, append(start, rc.fix...)...)
			fixed := startServe(t, home)
			t.Cleanup(fixed.kill)
			fixed.initialize(t)
			fixed.listIncludes(t, id)
			fixed.stop(t)
		}},
	}
	for _, s := range steps {
		if !t.Run(s.name, s.run) {
			t.FailNow()
		}
	}
}
