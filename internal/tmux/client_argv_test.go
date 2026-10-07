package tmux_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// Argv, target, create-chain and timeout-class tests for the socket-taking
// calls (SR-2.1, SR-3.5, SR-13.1); exec_mechanics_test.go has the client
// environment's (SR-2.2, SR-3.12).

const (
	argvIdentityFormat = "ad-server\t#{pid}\t#{start_time}"
	argvLookupFormat   = "#{session_id}\t#{session_created}\t#{pid}\t#{start_time}\t#{session_name}\t#{@ad_owner}"
	argvPanesFormat    = "#{session_id}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_pid}\t#{@ad_pane}"
	argvCreateFormat   = "#{session_id} #{pid} #{start_time} #{pane_id} #{pane_pid}"
)

// argvCase drives one client method and states the argv (after "-u -S
// <socket>") of each invocation it must make, and its timeout class.
type argvCase struct {
	name    string
	script  []tmux.RunResult
	call    func(c *tmux.Client) error
	want    [][]string
	timeout time.Duration
}

// argvCases covers every socket-taking method, answered with success.
func argvCases() []argvCase {
	createReply := exitZero(tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate))
	return slices.Concat([]argvCase{
		{
			// b.47f: the server identity read comes first, so even an empty
			// listing names the server that answered.
			name: "lookup",
			script: []tmux.RunResult{exitZero(tmuxfix.IdentityLine(tmuxfix.RecordedCreate.ServerPID,
				tmuxfix.RecordedCreate.ServerStart) + "\n")},
			call: func(c *tmux.Client) error { _, err := c.Lookup(testSocket); return err },
			want: [][]string{{"display-message", "-p", argvIdentityFormat,
				";", "list-sessions", "-F", argvLookupFormat,
				";", "show-options", "-gqv", "@ad_owner",
				";", "show-options", "-sqv", "@ad_owner",
				";", "show-options", "-gwqv", "@ad_owner"}},
			timeout: testTimeouts.Query,
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
			// b.9o4: pause's line clear names the key, without -l, so tmux
			// sends C-u rather than typing it.
			name:    "key send",
			script:  []tmux.RunResult{exitZero("")},
			call:    func(c *tmux.Client) error { return c.SendKeyPane(testSocket, "%3", "C-u") },
			want:    [][]string{{"send-keys", "-t", "%3", "C-u"}},
			timeout: testTimeouts.Action,
		},
	}, argvTextCases(), []argvCase{
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
				return c.SetLabel(testSocket, "$2", "%17", tmuxfix.Token, "id#1", tmuxfix.StoreID)
			},
			want: [][]string{{"set-option", "-t", "$2", "@ad_owner",
				tmuxfix.LabelValue(tmuxfix.Token, "$2", "id#1", tmuxfix.StoreID),
				";", "set-option", "-p", "-t", "%17", "@ad_pane", tmuxfix.PaneLabelValue(tmuxfix.Token, "%17")}},
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
		{
			// b.ukw: every caller value ending in ";" is escaped; the chain's
			// =<name>: target ends in ":" and is not.
			name:   "create with caller values ending in ;",
			script: []tmux.RunResult{createReply},
			call: func(c *tmux.Client) error {
				_, err := c.NewSession(testSocket, "n;", "/w;", map[string]string{"K": "v;"},
					[]string{"claude", "x;", "run-shell", "touch /f"}, tmuxfix.Token, "id;", tmuxfix.StoreID)
				return err
			},
			want: [][]string{{"new-session", "-d", "-s", `n\;`, "-c", `/w\;`,
				"-e", `AGENT_DIRECTOR_INSTANCE_ID=id\;`, "-e", `K=v\;`,
				"-P", "-F", argvCreateFormat, "--", "claude", `x\;`, "run-shell", "touch /f",
				";", "set-option", "-F", "-t", "=n;:", "@ad_owner",
				tmuxfix.ChainLabelValue(tmuxfix.Token, "id;", tmuxfix.StoreID),
				";", "set-option", "-p", "-F", "-t", "=n;:", "@ad_pane", tmuxfix.ChainPaneLabelValue(tmuxfix.Token)}},
			timeout: testTimeouts.Create,
		},
		{
			// b.dsx: the -c cwd and the label's instance id are format-escaped;
			// the -e entries, which tmux does not expand, are passed raw.
			name:   "create with format sequences in cwd and id",
			script: []tmux.RunResult{createReply},
			call: func(c *tmux.Client) error {
				_, err := c.NewSession(testSocket, "proj-abc", "/w/x#(touch m)#[y]#S", map[string]string{"K": "#(v)"},
					[]string{"claude"}, tmuxfix.Token, "id#(x)##[a#", tmuxfix.StoreID)
				return err
			},
			want: [][]string{{"new-session", "-d", "-s", "proj-abc", "-c", "/w/x##(touch m)#[y]##S",
				"-e", "AGENT_DIRECTOR_INSTANCE_ID=id#(x)##[a#", "-e", "K=#(v)",
				"-P", "-F", argvCreateFormat, "--", "claude",
				";", "set-option", "-F", "-t", "=proj-abc:", "@ad_owner",
				"ad1 " + tmuxfix.Token + " #{session_id} id##(x)##[a## " + tmuxfix.StoreID,
				";", "set-option", "-p", "-F", "-t", "=proj-abc:", "@ad_pane", tmuxfix.ChainPaneLabelValue(tmuxfix.Token)}},
			timeout: testTimeouts.Create,
		},
		{
			// b.dsx: an unchained ($ name) create format-escapes the -c cwd too,
			// and the escape composes with b.ukw's: "#;" is sent as "##\;".
			name:   "create, $ name, with format sequences and a final ; in cwd",
			script: []tmux.RunResult{createReply},
			call: func(c *tmux.Client) error {
				_, err := c.NewSession(testSocket, "x$", "/w/x#(touch m)#;", nil,
					[]string{"claude"}, tmuxfix.Token, "agent-1", tmuxfix.StoreID)
				return err
			},
			want: [][]string{{"new-session", "-d", "-s", "x$", "-c", `/w/x##(touch m)##\;`,
				"-e", "AGENT_DIRECTOR_INSTANCE_ID=agent-1", "-P", "-F", argvCreateFormat, "--", "claude"}},
			timeout: testTimeouts.Create,
		},
	})
}

// argvTextCases: a text is sent unchanged after "-l --" as one text call, no
// Enter call, however it looks (SR-2.1 "Text" row, SR-20.7): tmux flags
// ("-t%5" must not retarget to %5), a keysym, or empty (send-keys' Enter-only
// send, b.9o4). A final ";" alone is escaped (b.ukw; escape_semicolon_test.go
// has the rule's cases).
func argvTextCases() []argvCase {
	var cases []argvCase
	for _, row := range []struct{ text, sent string }{
		{"-x", "-x"}, {"--", "--"}, {"-t%5", "-t%5"}, {"C-c", "C-c"}, {"", ""}, {"a;", `a\;`}, {`a\;`, `a\\;`}, {"a;b", "a;b"},
	} {
		cases = append(cases, argvCase{
			name:    fmt.Sprintf("text %q", row.text),
			script:  []tmux.RunResult{exitZero("")},
			call:    func(c *tmux.Client) error { return c.SendKeysPane(testSocket, "%3", row.text, false) },
			want:    [][]string{{"send-keys", "-t", "%3", "-l", "--", row.sent}},
			timeout: testTimeouts.Action,
		})
	}
	return cases
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

// TestNewSessionChain: a name without $ or \ (the catalogue's, a plain one,
// one with a #) is created with the chain ';' set-option -F -t =<name>:
// @ad_owner <five-field value> ';' set-option -p -F -t =<name>: @ad_pane
// '<token> #{pane_id}', after the command or none; only the id is
// format-escaped, and the store id ends the session label once, unchanged. A
// $ or \ name (the catalogue's N1, N5 and F3 forms, a leading and an embedded
// one) is one unchained new-session whose reply is returned even on a
// non-zero exit.
func TestNewSessionChain(t *testing.T) {
	names := map[string]bool{"proj-abc": false, "n#1": false, "x$": true, "$7": true, `\lead`: true, `mid\dle`: true}
	for _, n := range tmuxfix.StoredNames() {
		names[n.Raw] = n.LabelByID
	}
	labels := []struct {
		id, store string
		command   []string
	}{
		{"agent-1", tmuxfix.StoreID, nil}, {"#a##", tmuxfix.StoreID, []string{"sh", "-c", "exit 0"}},
		{"agent x#y", tmuxfix.OtherStoreID, nil},
		{"id#2", "st#re", []string{"claude"}}, // not a store id: the client never doubles the store id's '#'
	}
	for name, byID := range names {
		for _, l := range labels {
			t.Run(name+"/"+l.id, func(t *testing.T) {
				if tmux.NeedsLabelByID(name) != byID {
					t.Fatalf("NeedsLabelByID(%q) = %v, want %v", name, !byID, byID)
				}
				exit := 0
				if byID {
					exit = 1
				}
				client, runner := newScripted(t, exited(exit, tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate), ""))
				got, err := client.NewSession(testSocket, name, "/work", nil, l.command, tmuxfix.Token, l.id, l.store)
				if err != nil || got != tmuxfix.RecordedCreate {
					t.Fatalf("NewSession = %+v, %v; want %+v, nil", got, err, tmuxfix.RecordedCreate)
				}
				owner := tmuxfix.ChainLabelValue(tmuxfix.Token, l.id, l.store)
				want := slices.Concat([]string{"new-session", "-d", "-s", name, "-c", "/work",
					"-e", "AGENT_DIRECTOR_INSTANCE_ID=" + l.id, "-P", "-F", argvCreateFormat, "--"}, l.command)
				if !byID {
					want = slices.Concat(want, []string{";", "set-option", "-F", "-t", "=" + name + ":", "@ad_owner", owner,
						";", "set-option", "-p", "-F", "-t", "=" + name + ":", "@ad_pane", tmuxfix.ChainPaneLabelValue(tmuxfix.Token)})
				}
				if args := argvCommand(t, runner.Only()); !slices.Equal(args, want) {
					t.Errorf("argv:\n got %q\nwant %q", args, want)
				}
				if !strings.HasSuffix(owner, " "+l.store) || strings.Count(owner, l.store) != 1 {
					t.Errorf("session label value %q does not end with store id %q exactly once", owner, l.store)
				}
			})
		}
	}
}
