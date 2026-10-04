package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// errBadArgv marks argv the fake does not understand.
var errBadArgv = errors.New("argv not understood")

// output is what one invocation writes and its exit status.
type output struct {
	stdout, stderr string
	exit           int
}

// socketMain handles argv after the leading -u: -S <socket> then the
// commands. It returns the exit status.
func socketMain(args []string) int {
	logArgv()
	if len(args) < 3 || args[0] != "-S" {
		return exitBadArgv
	}
	socket := args[1]
	cmds := splitCommands(args[2:])
	for _, c := range cmds {
		if len(c) == 0 {
			return exitBadArgv
		}
	}
	call := classify(cmds[0])
	path := faketmuxfix.TablePath(socket, os.Getenv(faketmuxfix.EnvTables))

	var out output
	var inj *faketmuxfix.Injection
	err := faketmuxfix.UpdateTable(path, call == tmux.CallCreate, func(tb *faketmuxfix.Table, _ bool) (bool, error) {
		var changed bool
		inj, changed = takeInjection(tb, call)
		if !runsNormally(inj) {
			return changed, nil
		}
		srv := &server{tb: tb, socket: socket, failChain: inj != nil && inj.Action == faketmuxfix.ActChainFails}
		var err error
		out, err = srv.run(cmds)
		if err != nil {
			return false, err
		}
		if srv.changed {
			tb.Socket = socket
		}
		return changed || srv.changed, nil
	})
	switch {
	case errors.Is(err, errBadArgv):
		return exitBadArgv
	case err != nil:
		return exitBadTable
	}
	if inj == nil {
		return write(out)
	}
	return answerInjected(*inj, socket, out)
}

// runsNormally reports whether the call's normal handling (its effect on
// the table and its answer) runs under injection inj.
func runsNormally(inj *faketmuxfix.Injection) bool {
	if inj == nil || inj.Effect || inj.Action == faketmuxfix.ActChainFails {
		return true
	}
	return inj.Action == faketmuxfix.ActHoldPipes && inj.Entry == ""
}

// answerInjected answers as inj says; normal is the normal answer when it
// was computed.
func answerInjected(inj faketmuxfix.Injection, socket string, normal output) int {
	switch inj.Action {
	case faketmuxfix.ActReply:
		return writeEntry(inj.Entry, socket)
	case faketmuxfix.ActExit:
		return inj.Exit
	case faketmuxfix.ActHang:
		time.Sleep(boundOf(inj.ForMs, faketmuxfix.DefaultHangBound))
		return 1
	case faketmuxfix.ActHoldPipes:
		if err := holdPipes(inj.ForMs); err != nil {
			return exitBadTable
		}
		if inj.Entry != "" {
			return writeEntry(inj.Entry, socket)
		}
		return write(normal)
	case faketmuxfix.ActChainFails:
		return write(normal)
	}
	return exitBadTable
}

// writeEntry writes the named catalogue entry's bytes on their streams and
// returns its exit status.
func writeEntry(name, socket string) int {
	e, ok := faketmuxfix.ResolveEntry(name, socket)
	if !ok {
		return exitBadTable
	}
	return write(output{stdout: e.Stdout, stderr: e.Stderr, exit: e.Exit})
}

// write writes out's streams and returns its exit status.
func write(out output) int {
	_, _ = os.Stdout.WriteString(out.stdout)
	_, _ = os.Stderr.WriteString(out.stderr)
	return out.exit
}

// holdPipes starts a child of this binary that keeps the inherited standard
// output and standard error open for ms milliseconds (capped), and does not
// wait for it.
func holdPipes(ms int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	child := exec.Command(self, holdPipesArg, strconv.Itoa(ms))
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		return err
	}
	return child.Process.Release()
}

// takeInjection returns the first injection matching call, counting down
// (and removing a used-up) limited one; changed reports a table change.
func takeInjection(tb *faketmuxfix.Table, call tmux.Call) (inj *faketmuxfix.Injection, changed bool) {
	if call == "" {
		return nil, false
	}
	for i := range tb.Injections {
		if tb.Injections[i].Call != call {
			continue
		}
		found := tb.Injections[i]
		if found.Times > 0 {
			changed = true
			if tb.Injections[i].Times--; tb.Injections[i].Times == 0 {
				tb.Injections = append(tb.Injections[:i], tb.Injections[i+1:]...)
			}
		}
		return &found, changed
	}
	return nil, false
}

// splitCommands splits argv into commands as tmux's command parser does:
// an argument that ends in an unescaped ";" ends the command, its text
// before the ";" (if any) staying as the command's last argument, so a
// standalone ";" is a plain separator; an argument that ends in `\;` is one
// argument with that backslash removed and is not a separator. Any other
// argument, including one with a ";" elsewhere, is kept as it is. An empty
// command is kept, so the caller can reject it.
func splitCommands(args []string) [][]string {
	cmds := [][]string{{}}
	for _, a := range args {
		body, ends := strings.CutSuffix(a, ";")
		switch {
		case !ends:
			cmds[len(cmds)-1] = append(cmds[len(cmds)-1], a)
		case strings.HasSuffix(body, `\`):
			cmds[len(cmds)-1] = append(cmds[len(cmds)-1], body[:len(body)-1]+";")
		default:
			if body != "" {
				cmds[len(cmds)-1] = append(cmds[len(cmds)-1], body)
			}
			cmds = append(cmds, []string{})
		}
	}
	return cmds
}

// classify names the call kind of an invocation by its first command; ""
// for a command outside the call set.
func classify(cmd []string) tmux.Call {
	switch cmd[0] {
	case "display-message", "list-sessions":
		// The lookup starts with its server identity read (LFR H5; b.47f).
		return tmux.CallLookup
	case "list-panes":
		return tmux.CallListPanes
	case "kill-pane":
		return tmux.CallKillPane
	case "kill-session":
		return tmux.CallKillSession
	case "send-keys":
		for _, a := range cmd[1:] {
			if a == "-l" {
				return tmux.CallSendText
			}
		}
		// Only the Enter send is the Enter call; another key send (pause's
		// C-u, b.9o4) is the key send, so an Enter injection never hits it.
		if cmd[len(cmd)-1] == "Enter" {
			return tmux.CallSendEnter
		}
		return tmux.CallSendKey
	case "capture-pane":
		return tmux.CallCapture
	case "new-session":
		return tmux.CallCreate
	case "set-option":
		return tmux.CallSetLabel
	}
	return ""
}
