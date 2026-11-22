package main_test

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// b.xhh: raw argv is refused at both real tmux boundaries, before any table
// effect or consumption of a create injection. Count argv after global options.
func TestArgvCommandMessageLimit(t *testing.T) {
	for _, size := range []int{16364, 16365, 16380, 16381} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			socket := newSocket(t)
			args := []string{"new-session", "-d", "-s", "raw", "--", "claude", ""}
			n := 0
			for _, a := range args {
				n += len(a) + 1
			}
			args[len(args)-1] = strings.Repeat("x", size-n)
			if size > 16364 {
				tables.Inject(t, socket, faketmuxfix.ExitCode(tmux.CallCreate, 23).FirstN(1))
			}
			before := tables.Read(t, socket)
			code, stderr := runFake(t, socket, args...)
			if size == 16364 {
				if code != 0 || stderr != "" || len(tables.Read(t, socket).Sessions) != 1 {
					t.Fatalf("packed size %d must create: exit=%d stderr=%q", size, code, stderr)
				}
				return
			}
			entry := "create/failed-to-send-command"
			if size > 16380 {
				entry = "create/command-too-long"
			}
			want := tmuxfix.Find(tmuxfix.CreateReplies(), entry)
			if code != want.Exit || stderr != want.Stderr {
				t.Fatalf("packed size %d: exit=%d stderr=%q; want %d %q", size, code, stderr, want.Exit, want.Stderr)
			}
			if after := tables.Read(t, socket); !reflect.DeepEqual(after, before) {
				t.Fatal("argv refusal changed table or consumed injection")
			}
			if code, stderr := runFake(t, socket, "new-session", "-d", "-s", "short", "--", "claude"); code != 23 || stderr != "" {
				t.Fatalf("next short create lost its injection: exit=%d stderr=%q", code, stderr)
			}
		})
	}
}

// b.xhh: the exact bootstrap uses existing create/label/injection handlers,
// supports cold and existing servers and logs only argv, never stdin.
func TestSourceFileCreateAndInjection(t *testing.T) {
	for _, shape := range []string{"cold", "existing server", "create exit", "chain failure"} {
		t.Run(shape, func(t *testing.T) {
			socket := newSocket(t)
			logPath := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv(faketmuxfix.EnvLog, logPath)
			if shape == "existing server" {
				tables.Write(t, socket, seeded())
			}
			if shape == "create exit" {
				tables.Inject(t, socket, faketmuxfix.ExitCode(tmux.CallCreate, 23).FirstN(1))
			}
			if shape == "chain failure" {
				tables.Inject(t, socket, faketmuxfix.ChainFails().FirstN(1))
			}
			// This is quoteConfigArg's grammar: octal apostrophe/LF, literal
			// backslash, semicolon, format syntax and UTF-8 inside quotes.
			id := "id'\n\\;雪"
			input := "'new-session' '-d' '-s' 'stdin' '-P' '-F' '#{session_id}' '--' 'claude' '' '" + strings.Repeat("x", 70000) + "' ; " +
				"'set-option' '-F' '-t' '=stdin:' '@ad_owner' 'ad1 " + tmuxfix.Token + " #{session_id} id'\\047''\\012'\\;雪 " + tmuxfix.StoreID + "' ; " +
				"'set-option' '-p' '-F' '-t' '=stdin:' '@ad_pane' '" + tmuxfix.Token + " #{pane_id}'\n"
			argv := []string{"-u", "-S", socket, "start-server", ";", "source-file", "-"}
			cmd := exec.Command(faketmuxfix.Binary(t), argv...)
			cmd.Stdin = strings.NewReader(input)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			wantCode := 0
			if shape == "create exit" {
				wantCode = 23
			}
			if shape == "chain failure" {
				wantCode = 1
			}
			if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != wantCode {
				t.Fatalf("stdin create: %v stdout=%q stderr=%q; want exit %d", err, stdout.String(), stderr.String(), wantCode)
			}
			tb := tables.Read(t, socket)
			if shape == "create exit" {
				if tb.Server != nil || len(tb.Sessions) != 0 || len(tb.Injections) != 0 {
					t.Fatal("injected create exit had an effect or was not consumed")
				}
			} else {
				createdID := strings.TrimSpace(stdout.String())
				var created *faketmuxfix.Session
				for i := range tb.Sessions {
					if tb.Sessions[i].ID == createdID {
						created = &tb.Sessions[i]
					}
				}
				if created == nil || len(created.Panes) != 1 {
					t.Fatal("stdin did not create one pane")
				}
				if shape == "chain failure" {
					if created.Label != "" || created.Panes[0].AdPane != "" || stderr.String() != chainFailureText() {
						t.Fatal("chain failure did not stop labels")
					}
				} else if created.Label != tmuxfix.LabelValue(tmuxfix.Token, createdID, id, tmuxfix.StoreID) || created.Panes[0].AdPane != tmuxfix.PaneLabelValue(tmuxfix.Token, created.Panes[0].ID) {
					t.Fatalf("stdin labels differ: %+v", created)
				}
			}
			recs := readLog(t, logPath)
			if len(recs) != 1 || !slices.Equal(recs[0][1:], argv) {
				t.Fatal("stdin leaked into argv log")
			}
		})
	}
}

func chainFailureText() string {
	return tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-then-label-failed").Stderr
}
