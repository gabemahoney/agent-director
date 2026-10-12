package realtmux_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// A literal follow of the documented per-Codex-folder setup (E4-T5; SRD SR-10.2
// and SR-14.4; docs/settings.md "Set up agent-director in a Codex folder"). The
// two TOML snippets are read from the doc at test time. The MCP entry is
// started the way Codex starts it: the entry's command and args, an
// environment of Codex's fixed default list plus the names `env_vars` forwards,
// valued from the pane's environment. A stand-in Codex row (codex_standin_test.go)
// is the caller; its pane gives the values. The help entry's command is run
// through each shell the image has, with a Codex SessionStart payload on stdin.

const (
	codexSettingsDoc     = "../../docs/settings.md"
	codexSetupHeading    = "## Set up agent-director in a Codex folder"
	codexSetupDocHome    = "/home/you" // the doc's placeholder for the user's home
	codexServerName      = "agent-director"
	codexHelpEnvAssigned = "AGENT_DIRECTOR_AGENT=codex"
)

// codexMCPDefaultEnv is the fixed list of names Codex gives a stdio MCP server
// besides the entry's env_vars (its CA variables are left out: nothing here
// reads them).
var codexMCPDefaultEnv = []string{"HOME", "LOGNAME", "PATH", "SHELL", "USER", "LANG", "LC_ALL", "TERM", "TMPDIR", "TZ"}

// codexSetup is the documented setup: the MCP entry and the help entry.
type codexSetup struct {
	Command string
	Args    []string
	EnvVars []string

	HelpCommand string
	HelpLimit   int
}

// setupBlock is one TOML snippet of the section. Keys the snippets carry that
// this struct lacks are reported by documentedCodexSetup.
type setupBlock struct {
	MCPServers map[string]struct {
		Command string   `toml:"command"`
		Args    []string `toml:"args"`
		EnvVars []string `toml:"env_vars"`
	} `toml:"mcp_servers"`
	Hooks map[string][]struct {
		Hooks []struct {
			Type  string `toml:"type"`
			Cmd   string `toml:"command"`
			Limit int    `toml:"additionalContextLimit"`
		} `toml:"hooks"`
	} `toml:"hooks"`
}

var tomlFence = regexp.MustCompile("(?s)```toml\n(.*?)```")

// documentedCodexSetup parses the two snippets of the settings.md section, and
// fails the test unless they hold exactly the MCP entry and one SessionStart
// help entry, nothing more.
func documentedCodexSetup(t testing.TB) codexSetup {
	t.Helper()
	raw, err := os.ReadFile(codexSettingsDoc)
	if err != nil {
		t.Fatalf("read the settings doc: %v", err)
	}
	doc := string(raw)
	start := strings.Index(doc, "\n"+codexSetupHeading+"\n")
	if start < 0 {
		t.Fatalf("docs/settings.md has no section %q", codexSetupHeading)
	}
	section := doc[start+1:]
	if end := strings.Index(section[len(codexSetupHeading):], "\n## "); end >= 0 {
		section = section[:len(codexSetupHeading)+end]
	}
	fences := tomlFence.FindAllStringSubmatch(section, -1)
	if len(fences) != 2 {
		t.Fatalf("the section holds %d toml snippets, want 2 (the MCP entry and the help entry)", len(fences))
	}
	var s codexSetup
	var haveMCP, haveHelp bool
	for _, f := range fences {
		var b setupBlock
		md, err := toml.Decode(f[1], &b)
		if err != nil {
			t.Fatalf("a snippet of the section is not TOML: %v\n%s", err, f[1])
		}
		if u := md.Undecoded(); len(u) != 0 {
			t.Errorf("the snippet carries keys the setup does not expect: %v\n%s", u, f[1])
		}
		switch {
		case len(b.MCPServers) > 0:
			e, ok := b.MCPServers[codexServerName]
			if !ok || len(b.MCPServers) != 1 || haveMCP {
				t.Fatalf("MCP snippet: servers %v, want exactly [mcp_servers.%s] once", b.MCPServers, codexServerName)
			}
			s.Command, s.Args, s.EnvVars, haveMCP = e.Command, e.Args, e.EnvVars, true
		case len(b.Hooks) > 0:
			groups := b.Hooks["SessionStart"]
			if len(b.Hooks) != 1 || len(groups) != 1 || len(groups[0].Hooks) != 1 || haveHelp {
				t.Fatalf("help snippet: %+v, want one SessionStart group with one handler and no other event", b.Hooks)
			}
			h := groups[0].Hooks[0]
			if h.Type != "command" {
				t.Errorf("help handler type %q, want command", h.Type)
			}
			s.HelpCommand, s.HelpLimit, haveHelp = h.Cmd, h.Limit, true
		default:
			t.Fatalf("a snippet holds neither an MCP entry nor hooks:\n%s", f[1])
		}
	}
	if !haveMCP || !haveHelp {
		t.Fatalf("the section lacks its MCP entry (%v) or its help entry (%v)", haveMCP, haveHelp)
	}
	return s
}

// TestCodexSetupSnippetsNameTheInstalledBinaryAndWhatItNeeds: the MCP entry runs
// the installed binary's `serve --stdio` and forwards exactly the three names
// AD reads its caller's id and tmux server from; the help entry, on
// SessionStart only, runs the installed binary's help with
// AGENT_DIRECTOR_AGENT=codex set, which is what makes help print Codex's form.
func TestCodexSetupSnippetsNameTheInstalledBinaryAndWhatItNeeds(t *testing.T) {
	s := documentedCodexSetup(t)
	if want := layout.Join(codexSetupDocHome, layout.BinDir, layout.BinaryName); s.Command != want {
		t.Errorf("MCP command %q, want the installed path %q", s.Command, want)
	}
	if want := []string{"serve", "--stdio"}; !slices.Equal(s.Args, want) {
		t.Errorf("MCP args %q, want %q", s.Args, want)
	}
	if got, want := sorted(s.EnvVars), []string{"AGENT_DIRECTOR_INSTANCE_ID", "TMUX", "TMUX_TMPDIR"}; !slices.Equal(got, want) {
		t.Errorf("MCP env_vars %q, want exactly %q", s.EnvVars, want)
	}
	if want := codexHelpEnvAssigned + ` "` + layout.Join("$HOME", layout.BinDir, layout.BinaryName) + `" help`; s.HelpCommand != want {
		t.Errorf("help command %q, want %q", s.HelpCommand, want)
	}
}

// sorted is a sorted copy of in.
func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// installBinary copies the built CLI to the installed path under home and
// returns that path.
func installBinary(t testing.TB, built, home string) string {
	t.Helper()
	dst := layout.Join(home, layout.BinDir, layout.BinaryName)
	data, err := os.ReadFile(built)
	if err != nil {
		t.Fatalf("read the built CLI: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		t.Fatalf("make the install directory: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("install the CLI: %v", err)
	}
	return dst
}

// stdioMCP is a running stdio MCP server and its JSON-RPC lines.
type stdioMCP struct {
	t      testing.TB
	in     io.WriteCloser
	lines  chan string
	stderr string // the file the server's standard error goes to
	id     int
}

// startStdioMCP starts command with args in dir under exactly env, and
// registers its cleanup.
func startStdioMCP(t testing.TB, command string, args, env []string, dir string) *stdioMCP {
	t.Helper()
	m := &stdioMCP{t: t, lines: make(chan string, 8), stderr: filepath.Join(t.TempDir(), "stderr")}
	errFile, err := os.Create(m.stderr)
	if err != nil {
		t.Fatalf("create the stderr file: %v", err)
	}
	cmd := exec.Command(command, args...)
	cmd.Env, cmd.Dir, cmd.Stderr = env, dir, errFile
	if m.in, err = cmd.StdinPipe(); err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", command, err)
	}
	t.Cleanup(func() {
		_ = m.in.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = errFile.Close()
	})
	go func() {
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			m.lines <- sc.Text()
		}
		close(m.lines)
	}()
	return m
}

// mcpResult is the part of a reply the case reads.
type mcpResult struct {
	Result *struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"result"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// request sends one JSON-RPC request and returns its reply, failing the test
// on no reply within a minute.
func (m *stdioMCP) request(method string, params any) mcpResult {
	m.t.Helper()
	m.id++
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.id, "method": method, "params": params})
	if err != nil {
		m.t.Fatalf("encode %s: %v", method, err)
	}
	if _, err := m.in.Write(append(line, '\n')); err != nil {
		m.t.Fatalf("write %s: %v", method, err)
	}
	select {
	case raw, ok := <-m.lines:
		errText, _ := os.ReadFile(m.stderr)
		if !ok {
			m.t.Fatalf("the server closed stdout before replying to %s (stderr %q)", method, errText)
		}
		var r mcpResult
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			m.t.Fatalf("reply to %s is not JSON: %v: %.300q", method, err, raw)
		}
		if r.Error != nil || r.Result == nil {
			m.t.Fatalf("%s failed: %.300q (stderr %q)", method, raw, errText)
		}
		return r
	case <-time.After(time.Minute):
		m.t.Fatalf("no reply to %s within a minute", method)
	}
	return mcpResult{}
}

// callTool calls the tool name with args and returns its text.
func (m *stdioMCP) callTool(name string, args map[string]any) string {
	m.t.Helper()
	r := m.request("tools/call", map[string]any{"name": name, "arguments": args})
	if len(r.Result.Content) != 1 {
		m.t.Fatalf("%s result holds %d parts, want 1", name, len(r.Result.Content))
	}
	return r.Result.Content[0].Text
}

// TestCodexMCPEntryGivesTheChildItsParentAndServer follows the documented MCP
// entry. A stand-in Codex row is the caller; the entry's server is started with
// Codex's default environment plus the names env_vars forwards, valued from the
// caller's pane; a child spawned over MCP then has the caller as parent_id and
// runs on the caller's tmux server. The control starts the same server without
// the forwarded names (TMUX_TMPDIR names another private directory, so it never
// resolves to the default socket): no parent_id, another tmux server.
func TestCodexMCPEntryGivesTheChildItsParentAndServer(t *testing.T) {
	setup := documentedCodexSetup(t)
	for _, tc := range []struct {
		name      string
		forwarded bool
	}{
		{name: "documented entry forwards the caller's identity and tmux server", forwarded: true},
		{name: "without the forwarded names", forwarded: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			other := newRealTmux(t) // before the fixture, so the fixture's world is the process's
			f := newCodexFix(t)
			onPath(t, program(t, "codex", codexStubScript))
			installBinary(t, f.bin, f.home)

			caller := f.spawnCodex(t)
			rec := codexRecordOf(f.root, caller.ID)
			rec.waitRecorded(t)
			paneEnv := rec.nulList(t, "env")

			var env []string
			for _, name := range codexMCPDefaultEnv {
				if v, ok := envValue(paneEnv, name); ok {
					env = append(env, name+"="+v)
				}
			}
			if v, _ := envValue(env, "HOME"); v != f.home {
				t.Fatalf("the pane's HOME %q, want the test's %q", v, f.home)
			}
			if tc.forwarded {
				for _, name := range setup.EnvVars {
					v, ok := envValue(paneEnv, name)
					if !ok {
						t.Fatalf("the caller's pane has no %s to forward", name)
					}
					env = append(env, name+"="+v)
				}
				if v, _ := envValue(env, "AGENT_DIRECTOR_INSTANCE_ID"); v != caller.ID {
					t.Fatalf("the pane's AGENT_DIRECTOR_INSTANCE_ID %q, want the caller %q", v, caller.ID)
				}
			} else {
				env = append(env, "TMUX_TMPDIR="+other.Dir)
			}

			command := strings.ReplaceAll(setup.Command, codexSetupDocHome, f.home)
			srv := startStdioMCP(t, command, setup.Args, env, f.CWD)
			srv.request("initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
				"clientInfo": map[string]any{"name": "codex-setup-test", "version": "0"}})
			childID := newInstanceID("mcp")
			text := srv.callTool("spawn", map[string]any{"cwd": f.CWD, "agent": "claude", "instance_id": childID, "no_pre_trust": true})
			if !strings.Contains(text, childID) {
				t.Fatalf("spawn result %q does not name %s", text, childID)
			}

			row := readRow(t, f.DBPath, childID)
			callerServer := caller.rt.formatInt(t, caller.Row.PaneID.(string), "#{pid}")
			if tc.forwarded {
				child := f.launched(t, childID)
				if row.ParentID != caller.ID {
					t.Errorf("the child's parent_id = %v, want the caller %s", row.ParentID, caller.ID)
				}
				if row.TmuxSocket != caller.Row.TmuxSocket {
					t.Errorf("the child's socket %v, want the caller's %v", row.TmuxSocket, caller.Row.TmuxSocket)
				}
				if got := child.rt.formatInt(t, row.PaneID.(string), "#{pid}"); got != callerServer {
					t.Errorf("the child runs on tmux server %d, want the caller's %d", got, callerServer)
				}
				return
			}
			socket, _ := row.TmuxSocket.(string)
			if row.ParentID != nil {
				t.Errorf("the child's parent_id = %v, want none without AGENT_DIRECTOR_INSTANCE_ID", row.ParentID)
			}
			if socket == "" || socket == caller.Row.TmuxSocket || !within(socket, other.Dir) {
				t.Fatalf("the child's socket %q, want one under %s and not the caller's %v", socket, other.Dir, caller.Row.TmuxSocket)
			}
			world := other.at(t, socket)
			got := world.formatInt(t, row.PaneID.(string), "#{pid}")
			world.trackServer(got)
			world.trackPane(world.formatInt(t, row.PaneID.(string), "#{pane_pid}"))
			if got == callerServer {
				t.Errorf("the child runs on the caller's tmux server %d, want another", got)
			}
		})
	}
}

// injectHelpHook turns [defaults] inject_help_hook on for the fixture's store,
// so a Codex launch carries the help entry.
func (f *codexFix) injectHelpHook(t *testing.T) {
	t.Helper()
	cfgPath := filepath.Join(f.home, ".config", "agent-director", "config.toml")
	file, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open the config: %v", err)
	}
	if _, err := file.WriteString("\n[defaults]\ninject_help_hook = true\n"); err != nil {
		t.Fatalf("write the config: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close the config: %v", err)
	}
	reopened, err := api.New(api.Options{StorePath: f.DBPath, ConfigPath: cfgPath})
	if err != nil {
		t.Fatalf("reopen the client with inject_help_hook: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	f.API = reopened
}

// TestCodexHelpEntryMatchesTheLaunchesOwn: the limit and the program of the
// documented help entry are those of the help entry a Codex launch carries with
// inject_help_hook on, read from the recorded launch of a stand-in Codex.
func TestCodexHelpEntryMatchesTheLaunchesOwn(t *testing.T) {
	setup := documentedCodexSetup(t)
	f := newCodexFix(t)
	onPath(t, program(t, "codex", codexStubScript))
	f.injectHelpHook(t)

	s := f.spawnCodex(t)
	rec := codexRecordOf(f.root, s.ID)
	rec.waitRecorded(t)
	helps := rec.launch(t).HooksOfKind(codexfix.HookHelp)
	if len(helps) != 1 {
		t.Fatalf("the launch carries %d help entries, want 1", len(helps))
	}
	h := helps[0]
	if h.Event != "SessionStart" || !h.HasLimit || h.Limit != setup.HelpLimit {
		t.Errorf("launch help entry: event %s, limit %d (given %v); the documented limit is %d", h.Event, h.Limit, h.HasLimit, setup.HelpLimit)
	}
	if want := layout.Join(f.home, layout.BinDir, layout.BinaryName); h.Exec != want {
		t.Errorf("launch help entry runs %q, want the installed path %q", h.Exec, want)
	}
}

// homeTree lists every path under home, relative.
func homeTree(t testing.TB, home string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(home, func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != home {
			rel, _ := filepath.Rel(home, p)
			out = append(out, rel)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk %s: %v", home, err)
	}
	return out
}

// TestCodexHelpEntryPrintsStructuredHelpThroughEveryShell runs the documented
// help command word for word, through each shell the image has, with a Codex
// SessionStart payload on stdin and the built binary at the installed path under
// a throwaway HOME: exactly one structured JSON object holding the whole help
// list within the documented limit, exit 0, no stderr, nothing created.
func TestCodexHelpEntryPrintsStructuredHelpThroughEveryShell(t *testing.T) {
	setup := documentedCodexSetup(t)
	home := t.TempDir()
	bin := installBinary(t, buildHookCLI(t, t.TempDir()), home)
	payload, err := os.ReadFile(codexSessionStartFixture)
	if err != nil {
		t.Fatalf("read the Codex SessionStart fixture: %v", err)
	}
	env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
	run := func(shell string, stdin io.Reader, command string) (stdout, stderr string, exit int) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, shell, "-c", command)
		cmd.Env, cmd.Dir, cmd.Stdin = env, t.TempDir(), stdin
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		err := cmd.Run()
		if ctx.Err() != nil {
			t.Fatalf("%s -c %q: no exit within 30s", shell, command)
		}
		exit = 0
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("%s -c %q: %v", shell, command, err)
		}
		return out.String(), errb.String(), exit
	}
	// The plain list, the same binary's help with no Codex signal.
	plain, _, code := run("sh", bytes.NewReader(nil), `"$HOME/`+layout.DirRel+`/bin/`+layout.BinaryName+`" help`)
	if code != 0 || plain == "" {
		t.Fatalf("plain help: exit %d, %d bytes", code, len(plain))
	}
	plain = strings.TrimSuffix(plain, "\n")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the installed binary: %v", err)
	}
	before := homeTree(t, home)

	var shells []string
	for _, name := range []string{"sh", "bash", "dash", "zsh", "ksh"} {
		if p, err := exec.LookPath(name); err == nil {
			shells = append(shells, p)
		} else if name == "sh" || name == "bash" {
			t.Fatalf("the image has no %s", name)
		}
	}
	for _, shell := range shells {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			stdout, stderr, code := run(shell, bytes.NewReader(payload), setup.HelpCommand)
			if code != 0 || stderr != "" {
				t.Fatalf("exit %d, stderr %q; want exit 0 and nothing on stderr", code, stderr)
			}
			if !strings.HasSuffix(stdout, "\n") || strings.Count(stdout, "\n") != 1 {
				t.Errorf("stdout is %d lines, want one JSON line", strings.Count(stdout, "\n"))
			}
			var out struct {
				Hook map[string]string `json:"hookSpecificOutput"`
			}
			dec := json.NewDecoder(strings.NewReader(stdout))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&out); err != nil || dec.More() {
				t.Fatalf("stdout is not exactly one object with only hookSpecificOutput (err %v): %.300q", err, stdout)
			}
			if len(out.Hook) != 2 || out.Hook["hookEventName"] != "SessionStart" {
				t.Fatalf("hookSpecificOutput = %v, want hookEventName SessionStart and additionalContext", out.Hook)
			}
			ctx := out.Hook["additionalContext"]
			if ctx != plain {
				t.Errorf("additionalContext is %d bytes, want the whole help list (%d bytes)", len(ctx), len(plain))
			}
			if units := (len(ctx) + 3) / 4; units > setup.HelpLimit {
				t.Errorf("additionalContext counts about %d units, over the documented limit %d", units, setup.HelpLimit)
			}
			if after := homeTree(t, home); !slices.Equal(after, before) {
				t.Errorf("HOME changed: before %v, after %v", before, after)
			}
		})
	}
}
