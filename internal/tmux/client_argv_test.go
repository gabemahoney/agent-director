package tmux_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Argv, target, create-chain, timeout-class and client-environment tests for
// the socket-taking calls (SR-2.1, SR-2.2, SR-3.5, SR-3.12, SR-13.1).

const (
	argvLookupFormat = "#{session_id}\t#{session_created}\t#{pid}\t#{start_time}\t#{session_name}\t#{@ad_owner}"
	argvPanesFormat  = "#{session_id}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_pid}\t#{@ad_pane}"
	argvCreateFormat = "#{session_id} #{pid} #{start_time} #{pane_id} #{pane_pid}"
)

// argvCase drives one client method and states the argv (after "-u -S
// <socket>") of each invocation it must make, and its timeout class.
type argvCase struct {
	name       string
	script     []tmux.RunResult
	call       func(c *tmux.Client) error
	want       [][]string
	timeout    time.Duration
	scopeReads int
}

// argvCases covers every socket-taking method, answered with success.
func argvCases() []argvCase {
	createReply := exitZero(tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate))
	return slices.Concat([]argvCase{
		{
			name:   "lookup",
			script: []tmux.RunResult{exitZero("")},
			call:   func(c *tmux.Client) error { _, err := c.Lookup(testSocket); return err },
			want: [][]string{{"list-sessions", "-F", argvLookupFormat,
				";", "show-options", "-gqv", "@ad_owner",
				";", "show-options", "-sqv", "@ad_owner",
				";", "show-options", "-gwqv", "@ad_owner"}},
			timeout:    testTimeouts.Query,
			scopeReads: 3,
		},
		{
			name:    "pane listing",
			script:  []tmux.RunResult{exitZero("")},
			call:    func(c *tmux.Client) error { _, err := c.ListPanes(testSocket); return err },
			want:    [][]string{{"list-panes", "-a", "-F", argvPanesFormat}},
			timeout: testTimeouts.Query,
		},
		{
			name:    "pane kill",
			script:  []tmux.RunResult{exitZero("")},
			call:    func(c *tmux.Client) error { return c.KillPane(testSocket, "%3") },
			want:    [][]string{{"kill-pane", "-t", "%3"}},
			timeout: testTimeouts.Action,
		},
		{
			name:    "session kill",
			script:  []tmux.RunResult{exitZero("")},
			call:    func(c *tmux.Client) error { return c.KillSessionID(testSocket, "$2") },
			want:    [][]string{{"kill-session", "-t", "$2"}},
			timeout: testTimeouts.Action,
		},
		{
			name:    "text then Enter",
			script:  []tmux.RunResult{exitZero(""), exitZero("")},
			call:    func(c *tmux.Client) error { return c.SendKeysPane(testSocket, "%3", "hello world", true) },
			want:    [][]string{{"send-keys", "-t", "%3", "-l", "--", "hello world"}, {"send-keys", "-t", "%3", "Enter"}},
			timeout: testTimeouts.Action,
		},
		{
			name:    "text without Enter",
			script:  []tmux.RunResult{exitZero("")},
			call:    func(c *tmux.Client) error { return c.SendKeysPane(testSocket, "%3", "hello", false) },
			want:    [][]string{{"send-keys", "-t", "%3", "-l", "--", "hello"}},
			timeout: testTimeouts.Action,
		},
		{
			name:    "keysym-like text stays literal",
			script:  []tmux.RunResult{exitZero(""), exitZero("")},
			call:    func(c *tmux.Client) error { return c.SendKeysPane(testSocket, "%3", "C-c", true) },
			want:    [][]string{{"send-keys", "-t", "%3", "-l", "--", "C-c"}, {"send-keys", "-t", "%3", "Enter"}},
			timeout: testTimeouts.Action,
		},
	}, argvDashTextCases(), argvSemicolonTextCases(), []argvCase{
		{
			name:   "capture",
			script: []tmux.RunResult{exitZero("")},
			call: func(c *tmux.Client) error {
				_, err := c.CapturePaneID(testSocket, "%3", 50, false)
				return err
			},
			want:    [][]string{{"capture-pane", "-p", "-t", "%3", "-S", "-50"}},
			timeout: testTimeouts.Action,
		},
		{
			name:   "capture with escapes",
			script: []tmux.RunResult{exitZero("")},
			call: func(c *tmux.Client) error {
				_, err := c.CapturePaneID(testSocket, "%3", 7, true)
				return err
			},
			want:    [][]string{{"capture-pane", "-p", "-e", "-t", "%3", "-S", "-7"}},
			timeout: testTimeouts.Action,
		},
		{
			name:   "label by id",
			script: []tmux.RunResult{exitZero("")},
			call: func(c *tmux.Client) error {
				return c.SetLabel(testSocket, "$2", "%2", tmuxfix.Token, "id#1", tmuxfix.StoreID)
			},
			want: [][]string{{"set-option", "-t", "$2", "@ad_owner",
				tmuxfix.LabelValue(tmuxfix.Token, "$2", "id#1", tmuxfix.StoreID),
				";", "set-option", "-p", "-t", "%2", "@ad_pane", tmuxfix.PaneLabelValue(tmuxfix.Token, "%2")}},
			timeout: testTimeouts.Action,
		},
		{
			name:   "label by id, other pane, token, spaced id and store",
			script: []tmux.RunResult{exitZero("")},
			call: func(c *tmux.Client) error {
				return c.SetLabel(testSocket, "$2", "%17", tmuxfix.OtherToken, "agent x#1", tmuxfix.OtherStoreID)
			},
			want: [][]string{{"set-option", "-t", "$2", "@ad_owner",
				tmuxfix.LabelValue(tmuxfix.OtherToken, "$2", "agent x#1", tmuxfix.OtherStoreID),
				";", "set-option", "-p", "-t", "%17", "@ad_pane", tmuxfix.PaneLabelValue(tmuxfix.OtherToken, "%17")}},
			timeout: testTimeouts.Action,
		},
		{
			name:   "create",
			script: []tmux.RunResult{createReply},
			call: func(c *tmux.Client) error {
				envs := map[string]string{"ZED": "z", "ABC": "a b", "AGENT_DIRECTOR_INSTANCE_ID": "stale"}
				_, err := c.NewSession(testSocket, "proj-abc", "/work", envs,
					[]string{"claude", "--resume", "x"}, tmuxfix.Token, "agent-1", tmuxfix.StoreID)
				return err
			},
			want: [][]string{{"new-session", "-d", "-s", "proj-abc", "-c", "/work",
				"-e", "AGENT_DIRECTOR_INSTANCE_ID=agent-1", "-e", "ABC=a b", "-e", "ZED=z",
				"-P", "-F", argvCreateFormat, "--", "claude", "--resume", "x",
				";", "set-option", "-F", "-t", "=proj-abc:", "@ad_owner",
				tmuxfix.ChainLabelValue(tmuxfix.Token, "agent-1", tmuxfix.StoreID),
				";", "set-option", "-p", "-F", "-t", "=proj-abc:", "@ad_pane", tmuxfix.ChainPaneLabelValue(tmuxfix.Token)}},
			timeout: testTimeouts.Create,
		},
	})
}

// argvTextRow is a SendKeysPane text and the text element its text call must carry.
type argvTextRow struct{ text, sent string }

// argvTextCases: each text gives exactly one text call
// "send-keys -t %3 -l -- <sent>", then the Enter call only when asked.
func argvTextCases(rows []argvTextRow) []argvCase {
	var cases []argvCase
	for _, row := range rows {
		for _, enter := range []bool{true, false} {
			want := [][]string{{"send-keys", "-t", "%3", "-l", "--", row.sent}}
			script := []tmux.RunResult{exitZero("")}
			name := fmt.Sprintf("text %q without Enter", row.text)
			if enter {
				want = append(want, []string{"send-keys", "-t", "%3", "Enter"})
				script = append(script, exitZero(""))
				name = fmt.Sprintf("text %q then Enter", row.text)
			}
			cases = append(cases, argvCase{
				name:    name,
				script:  script,
				call:    func(c *tmux.Client) error { return c.SendKeysPane(testSocket, "%3", row.text, enter) },
				want:    want,
				timeout: testTimeouts.Action,
			})
		}
	}
	return cases
}

// argvDashTextCases: a text that looks like tmux flags (or is an ordinary
// text) is sent unchanged after "-l --" (SR-2.1 "Text" row, SR-20.7).
// "-t%5" must not retarget to %5.
func argvDashTextCases() []argvCase {
	var rows []argvTextRow
	for _, text := range []string{"-x", "--", "-l", "-t%5", "plain text"} {
		rows = append(rows, argvTextRow{text, text})
	}
	return argvTextCases(rows)
}

// argvSemicolonTextCases: a text ending in ";" gets one backslash before that
// final ";" so tmux does not read it as a command separator; a ";" anywhere
// else passes through unescaped.
func argvSemicolonTextCases() []argvCase {
	return argvTextCases([]argvTextRow{
		{`;`, `\;`},
		{`a;`, `a\;`},
		{`a ;`, `a \;`},
		{`a\;`, `a\\;`},
		{`-x;`, `-x\;`},
		{`;a`, `;a`},
		{`a;b`, `a;b`},
	})
}

// argvRun drives c through a scripted client and returns its invocations.
func argvRun(t *testing.T, c argvCase) []tmux.Invocation {
	t.Helper()
	client, runner := newScripted(t, c.script...)
	if err := c.call(client); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	return runner.Calls()
}

// argvCommand checks the "-u -S <socket>" prefix and returns the rest.
func argvCommand(t *testing.T, inv tmux.Invocation) []string {
	t.Helper()
	if len(inv.Args) < 3 || inv.Args[0] != "-u" || inv.Args[1] != "-S" || inv.Args[2] != testSocket {
		t.Fatalf("argv does not begin with -u -S %s: %q", testSocket, inv.Args)
	}
	return inv.Args[3:]
}

// TestSocketCallArgv: every call is "-u -S <socket>" then its exact SR-2.1
// command, with its class timeout and the pipe-close wait.
func TestSocketCallArgv(t *testing.T) {
	for _, c := range argvCases() {
		t.Run(c.name, func(t *testing.T) {
			calls := argvRun(t, c)
			if len(calls) != len(c.want) {
				t.Fatalf("got %d calls, want %d: %v", len(calls), len(c.want), calls)
			}
			for i, inv := range calls {
				if got := argvCommand(t, inv); !slices.Equal(got, c.want[i]) {
					t.Errorf("call %d argv:\n got %q\nwant %q", i+1, got, c.want[i])
				}
				if inv.Timeout != c.timeout {
					t.Errorf("call %d timeout = %v, want %v", i+1, inv.Timeout, c.timeout)
				}
				if inv.WaitDelay != testTimeouts.WaitDelay {
					t.Errorf("call %d wait delay = %v, want %v", i+1, inv.WaitDelay, testTimeouts.WaitDelay)
				}
			}
		})
	}
}

// TestSocketCallTargetsAreIDs: targets are ids (or the create chain's
// =<name>:), no probe commands, no format naming AGENT_DIRECTOR_*, and
// show-options only as the lookup's three scope reads.
func TestSocketCallTargetsAreIDs(t *testing.T) {
	for _, c := range argvCases() {
		t.Run(c.name, func(t *testing.T) {
			for _, inv := range argvRun(t, c) {
				args := argvCommand(t, inv)
				for i, a := range args {
					switch {
					case a == "has-session" || a == "show-environment":
						t.Errorf("forbidden command %q in %q", a, args)
					case strings.HasSuffix(a, ":0.0"):
						t.Errorf("window.pane suffix in %q", a)
					case strings.Contains(a, "#{") && strings.Contains(a, "AGENT_DIRECTOR_"):
						t.Errorf("format names an AGENT_DIRECTOR_ variable: %q", a)
					}
					if a == "-t" && i+1 < len(args) && !argvIsIDTarget(args[i+1]) {
						t.Errorf("target %q is not an id or =<name>:", args[i+1])
					}
				}
				if n := argvCount(args, "show-options"); n != c.scopeReads {
					t.Errorf("show-options appears %d times, want %d", n, c.scopeReads)
				}
			}
		})
	}
}

// argvIsIDTarget accepts a session or pane id, or an exact-match =<name>: target.
func argvIsIDTarget(target string) bool {
	if strings.HasPrefix(target, "$") || strings.HasPrefix(target, "%") {
		return true
	}
	return strings.HasPrefix(target, "=") && strings.HasSuffix(target, ":") && len(target) > 2
}

// count returns how many elements of args equal s.
func argvCount(args []string, s string) int {
	n := 0
	for _, a := range args {
		if a == s {
			n++
		}
	}
	return n
}

// argvChainedNames returns the create names that take the chained label: the
// catalogue's names without $ or \, plus a plain name and one with a #.
func argvChainedNames() []string {
	names := []string{"proj-abc", "n#1"}
	for _, n := range tmuxfix.StoredNames() {
		if !n.LabelByID {
			names = append(names, n.Raw)
		}
	}
	return names
}

// TestNewSessionChain: the chain is ';' set-option -F -t =<name>: @ad_owner
// with the five-field value, then ';' set-option -p -F -t =<name>: @ad_pane
// '<token> #{pane_id}'; # is doubled in the id only, the store id ends the
// session label once and unchanged, and it never reaches the pane label.
func TestNewSessionChain(t *testing.T) {
	labels := []struct{ id, store string }{
		{"agent-1", tmuxfix.StoreID}, {"id#1", tmuxfix.StoreID}, {"#a##", tmuxfix.StoreID},
		{"agent-ü1", tmuxfix.StoreID}, {"agent x#y", tmuxfix.OtherStoreID},
		{"agent 0123456789abcdef", tmuxfix.StoreID},
		{"id#2", "st#re"}, // not a store id: shows the client never doubles the store id's '#'
	}
	commands := map[string][]string{"command": {"sh", "-c", "exit 0"}, "no command": nil}
	for _, name := range argvChainedNames() {
		for _, l := range labels {
			for cmdName, command := range commands {
				t.Run(name+"/"+l.id+"/"+cmdName, func(t *testing.T) {
					if tmux.NeedsLabelByID(name) {
						t.Fatalf("NeedsLabelByID(%q) = true, want false", name)
					}
					client, runner := newScripted(t, exitZero(tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate)))
					if _, err := client.NewSession(testSocket, name, "/work", nil, command, tmuxfix.Token, l.id, l.store); err != nil {
						t.Fatalf("NewSession: %v", err)
					}
					args := argvCommand(t, runner.Only())
					head := []string{"new-session", "-d", "-s", name, "-c", "/work",
						"-e", "AGENT_DIRECTOR_INSTANCE_ID=" + l.id, "-P", "-F", argvCreateFormat, "--"}
					owner := []string{";", "set-option", "-F", "-t", "=" + name + ":", "@ad_owner",
						tmuxfix.ChainLabelValue(tmuxfix.Token, l.id, l.store)}
					pane := []string{";", "set-option", "-p", "-F", "-t", "=" + name + ":", "@ad_pane",
						tmuxfix.ChainPaneLabelValue(tmuxfix.Token)}
					want := slices.Concat(head, command, owner, pane)
					if !slices.Equal(args, want) {
						t.Errorf("argv:\n got %q\nwant %q", args, want)
					}
					if n := argvCount(args, ";"); n != 2 {
						t.Errorf("got %d ';' separators, want 2", n)
					}
					if value := args[len(args)-len(pane)-1]; !strings.HasSuffix(value, " "+l.store) || strings.Count(value, l.store) != 1 {
						t.Errorf("session label value %q does not end with store id %q exactly once", value, l.store)
					}
					if value := args[len(args)-1]; strings.Contains(value, l.store) || strings.Contains(value, l.id) {
						t.Errorf("pane label value %q carries the store id or instance id", value)
					}
				})
			}
		}
	}
}

// argvLabelByIDNames returns the catalogue's $ and \ names plus F3's forms and a
// leading and an embedded backslash.
func argvLabelByIDNames() []string {
	names := []string{"x$", "$7", `\lead`, `mid\dle`}
	for _, n := range tmuxfix.StoredNames() {
		if n.LabelByID {
			names = append(names, n.Raw)
		}
	}
	return names
}

// TestNewSessionNoChainForDollarOrBackslash: a $ or \ name is one invocation
// with no ';' and no set-option, and the reply is returned whatever the exit.
func TestNewSessionNoChainForDollarOrBackslash(t *testing.T) {
	reply := tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate)
	for _, name := range argvLabelByIDNames() {
		for _, exit := range []int{0, 1} {
			t.Run(fmt.Sprintf("%s/exit%d", name, exit), func(t *testing.T) {
				if !tmux.NeedsLabelByID(name) {
					t.Fatalf("NeedsLabelByID(%q) = false, want true", name)
				}
				client, runner := newScripted(t, exited(exit, reply, ""))
				got, err := client.NewSession(testSocket, name, "/work", nil,
					[]string{"claude"}, tmuxfix.Token, "id#1", tmuxfix.StoreID)
				if err != nil || got != tmuxfix.RecordedCreate {
					t.Fatalf("NewSession = %+v, %v; want %+v, nil", got, err, tmuxfix.RecordedCreate)
				}
				args := argvCommand(t, runner.Only())
				want := []string{"new-session", "-d", "-s", name, "-c", "/work",
					"-e", "AGENT_DIRECTOR_INSTANCE_ID=id#1", "-P", "-F", argvCreateFormat, "--", "claude"}
				if !slices.Equal(args, want) {
					t.Errorf("argv:\n got %q\nwant %q", args, want)
				}
				if slices.Contains(args, ";") || slices.Contains(args, "set-option") {
					t.Errorf("chained label present for %q: %q", name, args)
				}
				if strings.Contains(strings.Join(args, " "), tmuxfix.StoreID) {
					t.Errorf("store id in an unchained create: %q", args)
				}
			})
		}
	}
}

// argvIsLocale reports whether an environment name is a locale variable.
func argvIsLocale(name string) bool {
	return name == "LANG" || name == "LANGUAGE" || strings.HasPrefix(name, "LC_")
}

// argvUnsetLocale removes every locale variable for the rest of the test.
func argvUnsetLocale(t *testing.T) {
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		if argvIsLocale(name) {
			t.Setenv(name, value)
			os.Unsetenv(name)
		}
	}
}

// TestSocketCallEnvironment: every call's environment is the process one
// minus every AGENT_DIRECTOR_* variable; locale is passed through, never added.
func TestSocketCallEnvironment(t *testing.T) {
	cases := []struct {
		name       string
		locale     func(t *testing.T)
		wantLocale []string
	}{
		{"locale set", func(t *testing.T) {
			argvUnsetLocale(t)
			t.Setenv("LC_ALL", "C")
			t.Setenv("LANG", "en_US.UTF-8")
		}, []string{"LANG=en_US.UTF-8", "LC_ALL=C"}},
		{"locale absent", argvUnsetLocale, nil},
	}
	for _, lc := range cases {
		t.Run(lc.name, func(t *testing.T) {
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "leak-id")
			t.Setenv("AGENT_DIRECTOR_HOME", "/leak/home")
			t.Setenv("AGENT_DIRECTOR_EMPTY", "")
			t.Setenv("AGENT_DIRECTORX", "kept")
			lc.locale(t)
			var want []string
			for _, kv := range os.Environ() {
				if !strings.HasPrefix(kv, "AGENT_DIRECTOR_") {
					want = append(want, kv)
				}
			}
			for _, c := range argvCases() {
				for _, inv := range argvRun(t, c) {
					if !slices.Equal(inv.Env, want) {
						t.Errorf("%s: environment is not the process one minus AGENT_DIRECTOR_*:\n got %q\nwant %q", c.name, inv.Env, want)
					}
					if got := argvLocaleEntries(inv.Env); !slices.Equal(got, lc.wantLocale) {
						t.Errorf("%s: locale entries = %q, want %q", c.name, got, lc.wantLocale)
					}
				}
			}
		})
	}
}

// argvLocaleEntries returns env's locale entries, sorted.
func argvLocaleEntries(env []string) []string {
	var out []string
	for _, kv := range env {
		if name, _, _ := strings.Cut(kv, "="); argvIsLocale(name) {
			out = append(out, kv)
		}
	}
	slices.Sort(out)
	return out
}
