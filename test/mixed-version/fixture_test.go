package mixedversion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
	"github.com/google/uuid"
)

type generation struct{ name, runtime, admin, driver, version, commit string }
type buildPair struct {
	source, root, client string
	generations          [2]generation
	err                  error
}

var qualificationBuild buildPair
var buildOnce sync.Once

func TestMain(m *testing.M) {
	sandboxguard.Require()
	code := m.Run()
	if qualificationBuild.root != "" {
		_ = os.RemoveAll(qualificationBuild.root)
	}
	os.Exit(code)
}

// Each generation is linked separately from one frozen copy of the final
// source. Distinct stamps change the executables, never the permanent core.
func builds(t *testing.T) *buildPair {
	t.Helper()
	if testing.Short() {
		t.Skip("mixed-version qualification builds two retained generations")
	}
	if os.Getenv("AGENT_DIRECTOR_TEST_SANDBOX") != "1" {
		t.Fatal("mixed-version qualification requires the scheduler sandbox")
	}
	buildOnce.Do(func() {
		b := &qualificationBuild
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
		b.root, b.err = os.MkdirTemp("", "mixed-version-build-")
		if b.err != nil {
			return
		}
		b.source = filepath.Join(b.root, "source")
		b.err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "bin" || entry.Name() == ".scratch") {
				return filepath.SkipDir
			}
			dest := filepath.Join(b.source, rel)
			if entry.IsDir() {
				return os.MkdirAll(dest, 0700)
			}
			if entry.Type()&os.ModeSymlink != 0 {
				link, err := os.Readlink(path)
				if err != nil {
					return err
				}
				return os.Symlink(link, dest)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, data, info.Mode().Perm())
		})
		if b.err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", filepath.Join(b.source, "test/driver/busy-send-mixed/runtime.sh"), b.source, b.root)
		cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0")
		output, err := cmd.CombinedOutput()
		if err != nil {
			b.err = fmt.Errorf("generation builds: %w\n%s", err, output)
			return
		}
		b.client = filepath.Join(b.source, "pkg/ts-bun-client/dist/index.js")
		if _, b.err = os.Stat(b.client); b.err != nil {
			return
		}
		for i, name := range []string{"old", "new"} {
			dir := filepath.Join(b.root, name)
			stamp := "9.0.0-mixed-" + name
			retained := filepath.Join(b.root, "retained-home/.config/agent-director/versions", layout.VersionID(stamp))
			b.generations[i] = generation{name, filepath.Join(retained, "agent-director"), filepath.Join(retained, "agent-director-admin"), filepath.Join(dir, "driver"), stamp, strings.Repeat(fmt.Sprint(i+1), 40)}
		}
	})
	if qualificationBuild.err != nil {
		t.Fatal(qualificationBuild.err)
	}
	return &qualificationBuild
}

type reply struct {
	Result        json.RawMessage           `json:"result"`
	Error         string                    `json:"error"`
	Barrier       string                    `json:"barrier"`
	ErrName       string                    `json:"err_name"`
	ErrDetails    json.RawMessage           `json:"err_details"`
	UnknownCommit bool                      `json:"unknown_commit"`
	StoreBusy     bool                      `json:"store_busy"`
	Guard         *store.BusySendGuardError `json:"guard"`
	Refusal       *store.BusySendRefusal    `json:"refusal"`
}

type process struct {
	t      *testing.T
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *json.Decoder
	done   chan error
	stderr bytes.Buffer
}

func startProcess(t *testing.T, env []string, executable string, args ...string) *process {
	t.Helper()
	p := &process{t: t, cmd: exec.Command(executable, args...), done: make(chan error, 1)}
	p.cmd.Env, p.cmd.Stderr = env, &p.stderr
	var err error
	p.in, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.out = json.NewDecoder(out)
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { p.done <- p.cmd.Wait() }()
	t.Cleanup(func() {
		_ = p.in.Close()
		select {
		case err := <-p.done:
			if err != nil && !t.Failed() {
				t.Errorf("%s: %v: %s", executable, err, p.stderr.String())
			}
		case <-time.After(3 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.done
			t.Errorf("%s did not exit after stdin closed: %s", executable, p.stderr.String())
		}
	})
	return p
}

func (p *process) write(v any) {
	p.t.Helper()
	if err := json.NewEncoder(p.in).Encode(v); err != nil {
		p.t.Fatal(err)
	}
}

func (p *process) read(v any) {
	p.t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.out.Decode(v) }()
	select {
	case err := <-done:
		if err != nil {
			p.t.Fatalf("%s output: %v", p.cmd.Path, err)
		}
	case <-time.After(30 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		p.t.Fatalf("%s output timed out", p.cmd.Path)
	}
}

func (p *process) call(op string, params any) reply {
	p.t.Helper()
	p.write(map[string]any{"op": op, "params": params})
	var r reply
	p.read(&r)
	return r
}

func decode[T any](t *testing.T, data json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func value[T any](t *testing.T, r reply) T {
	t.Helper()
	if r.Error != "" {
		t.Fatalf("driver error: %+v", r)
	}
	return decode[T](t, r.Result)
}

func exact(t *testing.T, got, want any) {
	t.Helper()
	a, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("got %s\nwant %s", a, b)
	}
}

type fixture struct {
	b                            *buildPair
	home, db, id, token, session string
	env                          []string
	drivers                      [2]*process
}

func newFixture(t *testing.T, state ...string) *fixture {
	t.Helper()
	f := &fixture{b: builds(t), home: t.TempDir(), id: "mixed-" + uuid.NewString(), token: strings.ReplaceAll(uuid.NewString(), "-", ""), session: uuid.NewString()}
	f.db = filepath.Join(f.home, ".config/agent-director/state.db")
	f.env = append(os.Environ(), "HOME="+f.home, "TMUX=", "TMUX_PANE=", "AGENT_DIRECTOR_INSTANCE_ID=")
	for i, g := range f.b.generations {
		p := startProcess(t, f.env, g.driver, "store", f.db)
		var ready map[string]any
		p.read(&ready)
		if ready["version"] != g.version || ready["commit"] != g.commit {
			t.Fatalf("wrong driver generation: %v", ready)
		}
		f.drivers[i] = p
	}
	initial := "working"
	if len(state) > 0 {
		initial = state[0]
	}
	value[string](t, f.drivers[0].call("seed", map[string]any{"ID": f.id, "Session": f.session, "Token": f.token, "State": initial, "Owned": len(state) > 0, "Wake": filepath.Join(f.home, "wake")}))
	return f
}

func (f *fixture) caller(t *testing.T, generationIndex int, surface string) func(string, map[string]any) reply {
	t.Helper()
	g := f.b.generations[generationIndex]
	if surface == "cli" {
		return func(op string, p map[string]any) reply {
			verb := map[string]string{"send": "send-keys", "lookup": "get-delivery", "force-lookup": "get-force", "get": "get", "get-many": "get-many", "resume": "resume"}[op]
			args := []string{"--store-path", f.db, verb}
			for k, v := range p {
				flag := "--" + strings.ReplaceAll(k, "_", "-")
				if ids, ok := v.([]string); ok {
					args = append(args, ids...)
					continue
				}
				if b, ok := v.(bool); ok {
					args = append(args, flag+"="+fmt.Sprint(b))
				} else {
					args = append(args, flag, fmt.Sprint(v))
				}
			}
			cmd := exec.Command(g.runtime, args...)
			cmd.Env = f.env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				var failure struct {
					ErrName    string          `json:"err_name"`
					ErrDetails json.RawMessage `json:"err_details"`
				}
				_ = json.Unmarshal(bytes.Split(stderr.Bytes(), []byte{'\n'})[0], &failure)
				return reply{Error: fmt.Sprintf("%v: %s", err, stderr.String()), ErrName: failure.ErrName, ErrDetails: failure.ErrDetails}
			}
			return reply{Result: out}
		}
	}
	var p *process
	if surface == "go" {
		p = startProcess(t, f.env, g.driver, "go", f.db)
	} else if surface == "ts" {
		p = startProcess(t, f.env, "bun", filepath.Join(f.b.source, "test/driver/busy-send-mixed/callers.ts"), f.b.client, g.runtime, f.db)
	} else {
		p = startProcess(t, f.env, g.runtime, "--store-path", f.db, "serve", "--stdio")
		id := 0
		rpc := func(method string, params any) json.RawMessage {
			id++
			p.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
			for {
				var r struct {
					ID     int             `json:"id"`
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				p.read(&r)
				if r.ID == id {
					if r.Error != nil {
						var wire struct {
							Data json.RawMessage `json:"data"`
						}
						if err := json.Unmarshal(r.Error, &wire); err != nil || wire.Data == nil {
							t.Fatalf("MCP error: %s", r.Error)
						}
						synthetic, _ := json.Marshal(map[string]any{"isError": true, "content": []map[string]any{{"text": string(wire.Data)}}})
						return synthetic
					}
					return r.Result
				}
			}
		}
		rpc("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "mixed-version", "version": "1"}})
		p.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		return func(op string, params map[string]any) reply {
			verb := map[string]string{"send": "send-keys", "lookup": "get-delivery", "force-lookup": "get-force", "get": "get", "get-many": "get-many", "resume": "resume"}[op]
			var r struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			must := rpc("tools/call", map[string]any{"name": verb, "arguments": params})
			if err := json.Unmarshal(must, &r); err != nil {
				t.Fatal(err)
			}
			if len(r.Content) != 1 {
				return reply{Error: string(must)}
			}
			if r.IsError {
				var failure reply
				_ = json.Unmarshal([]byte(r.Content[0].Text), &failure)
				failure.Error = string(must)
				return failure
			}
			return reply{Result: json.RawMessage(r.Content[0].Text)}
		}
	}
	var ready map[string]any
	p.read(&ready)
	if ready["ready"] != true {
		t.Fatalf("caller not ready: %v", ready)
	}
	return func(op string, params map[string]any) reply { return p.call(op, params) }
}

func recordFixture(t *testing.T, writer int) (*fixture, [2]*process, map[string]string) {
	t.Helper()
	f := &fixture{b: builds(t), home: t.TempDir()}
	f.db = filepath.Join(f.home, "state.db")
	f.env = append(os.Environ(), "HOME="+f.home, "TMUX=", "AGENT_DIRECTOR_INSTANCE_ID=", "AD_MIXED_STORE_PATH="+f.db)
	var helpers [2]*process
	for i, g := range f.b.generations {
		p := startProcess(t, f.env, filepath.Join(filepath.Dir(g.driver), "store-driver"), "-test.run=^TestMixedVersionStoreDriver$")
		var ready map[string]any
		p.read(&ready)
		if ready["version"] != g.version || ready["commit"] != g.commit {
			t.Fatalf("wrong store helper generation: %v", ready)
		}
		helpers[i] = p
	}
	return f, helpers, value[map[string]string](t, helpers[writer].call("seed-records", nil))
}

func fixtureSet(t *testing.T, p *process, table, key, id string, fields map[string]any) {
	t.Helper()
	got := value[int](t, p.call("fixture-set", map[string]any{"Table": table, "Key": key, "ID": id, "Set": fields}))
	if got != 1 {
		t.Fatalf("fixture changed %d rows", got)
	}
}
