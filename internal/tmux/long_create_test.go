package tmux_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// b.xhh: ordinary creates must cross the client/fake boundary intact even
// without forwarded env. The fake enforces the real tmux command-message cap.
func TestLongCreateAgainstFake(t *testing.T) {
	for _, shape := range []string{"one argument", "many small arguments and env"} {
		t.Run(shape, func(t *testing.T) {
			logPath := filepath.Join(t.TempDir(), "argv.log")
			t.Setenv(faketmuxfix.EnvLog, logPath)
			socket := filepath.Join(t.TempDir(), "sock")
			command := []string{"claude", "--append-system-prompt", strings.Repeat("x", 30000)}
			envs := map[string]string{}
			if shape == "many small arguments and env" {
				command = []string{"claude"}
				for i := 0; i < 180; i++ {
					command = append(command, strings.Repeat("a", 64))
					envs[fmt.Sprintf("EXAMPLE_%03d", i)] = strings.Repeat("e", 64)
				}
			}
			// Include values that must never turn into extra config commands.
			command = append(command, "", "quote'\n\t\x01\x7f雪", `back\slash;`, ";", "#{session_id}")
			c := tmux.New(faketmuxfix.Binary(t), testTimeouts)
			reply, err := c.NewSession(socket, "long-create", t.TempDir(), envs, command, tmuxfix.Token, "id-long", tmuxfix.StoreID)
			if err != nil {
				t.Fatalf("long create must succeed without forwarding: %v", err)
			}
			if reply.SessionID == "" || reply.PaneID == "" {
				t.Fatalf("missing create identity: %+v", reply)
			}
			table := (faketmuxfix.Tables{}).Read(t, socket)
			if len(table.Sessions) != 1 || table.Sessions[0].Label != tmuxfix.LabelValue(tmuxfix.Token, reply.SessionID, "id-long", tmuxfix.StoreID) ||
				len(table.Sessions[0].Panes) != 1 || table.Sessions[0].Panes[0].AdPane != tmuxfix.PaneLabelValue(tmuxfix.Token, reply.PaneID) {
				t.Fatalf("long create did not set exactly its own session and pane labels: %+v", table.Sessions)
			}
			recs := faketmuxfix.ReadLog(t, logPath)
			want := []string{"-u", "-S", socket, "start-server", ";", "source-file", "-"}
			if len(recs) != 1 || !slices.Equal(recs[0][1:], want) {
				t.Fatal("long create must invoke one stdin bootstrap and keep the payload out of the argv log")
			}
		})
	}
}

// b.xhh: select stdin from the entire packed command list, labels/separators
// included. Keep the existing small-create argv tests unchanged.
func TestLongCreateTransportThreshold(t *testing.T) {
	base := []string{"new-session", "-d", "-s", "agent-x", "-c", "/work", "-e", "AGENT_DIRECTOR_INSTANCE_ID=id-1", "-P", "-F", argvCreateFormat, "--", "claude", "",
		";", "set-option", "-F", "-t", "=agent-x:", "@ad_owner", tmuxfix.ChainLabelValue(tmuxfix.Token, "id-1", tmuxfix.StoreID),
		";", "set-option", "-p", "-F", "-t", "=agent-x:", "@ad_pane", tmuxfix.ChainPaneLabelValue(tmuxfix.Token)}
	packed := 0
	for _, arg := range base {
		packed += len(arg) + 1
	}
	for _, size := range []int{8191, 8192, 8193} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			c, r := newScripted(t, exitZero(tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate)))
			payload := strings.Repeat("x", size-packed)
			reply, err := c.NewSession(testSocket, "agent-x", "/work", nil, []string{"claude", payload}, tmuxfix.Token, "id-1", tmuxfix.StoreID)
			if err != nil || reply != tmuxfix.RecordedCreate {
				t.Fatalf("create = %+v, %v", reply, err)
			}
			inv := r.Only()
			if size <= 8192 {
				want := slices.Clone(base)
				want[13] = payload
				if !slices.Equal(argvCommand(t, inv), want) || len(inv.Stdin) != 0 {
					t.Fatal("create at or below switch must retain argv transport")
				}
			} else if !slices.Equal(argvCommand(t, inv), []string{"start-server", ";", "source-file", "-"}) || !strings.Contains(string(inv.Stdin), "'"+payload+"'") {
				t.Fatal("create above 8192 packed bytes must use stdin before invoking tmux")
			}
			if inv.Timeout != testTimeouts.Create || inv.WaitDelay != testTimeouts.WaitDelay {
				t.Fatal("transport changed create time bounds")
			}
		})
	}
}

// b.xhh: the large, non-forwarding route retains existing stdin privacy and
// create-result classification, including labels, uncertainty and timeout.
func TestLongCreateOutcomes(t *testing.T) {
	payload := strings.Repeat("x", 30000)
	cases := []struct {
		name    string
		result  tmux.RunResult
		failure tmux.Failure
		first   string
		want    tmux.CreateReply
	}{
		{name: "reply", result: exitZero(tmuxfix.CreateReplyLine(tmuxfix.RecordedCreate)), want: tmuxfix.RecordedCreate},
		{name: "label failure", result: tmuxfix.Find(tmuxfix.CreateReplies(), "create/reply-then-label-failed").Result(), failure: tmux.FailLabel, want: tmuxfix.RecordedCreate},
		{name: "empty", result: exitZero(""), failure: tmux.FailUnrecognized},
		{name: "cut pipes", result: tmux.RunResult{Status: tmux.RunPipesCut, Stdout: []byte("$1 295"), ExitStatus: 0}, failure: tmux.FailUnrecognized, first: "output cut short: its pipes stayed open past the pipe-close wait"},
		{name: "timeout", result: timedOut(), failure: tmux.FailTimeout},
		{name: "duplicate", result: tmuxfix.Find(tmuxfix.StdinCreateReplies(), "stdin/duplicate").Result(), failure: tmux.FailDuplicate},
		{name: "private diagnostic", result: exited(1, "", "<stdin>:1: unknown command: "+payload+"\n"), failure: tmux.FailUnrecognized, first: "create diagnostic withheld"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, r := newScripted(t, tc.result)
			reply, err := c.NewSession(testSocket, "agent-x", "/work", nil, []string{"claude", payload}, tmuxfix.Token, "id-1", tmuxfix.StoreID)
			if !reflect.DeepEqual(reply, tc.want) {
				t.Errorf("reply = %+v, want %+v", reply, tc.want)
			}
			if tc.failure == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				ce := callError(t, err)
				if ce.Call != tmux.CallCreate || ce.Failure != tc.failure || ce.FirstLine != tc.first {
					t.Errorf("failure/first line mismatch: failure=%v, first line bytes=%d; want %v/%q", ce.Failure, len(ce.FirstLine), tc.failure, tc.first)
				}
			}
			inv := r.Only()
			if !slices.Equal(argvCommand(t, inv), []string{"start-server", ";", "source-file", "-"}) {
				t.Error("large create must use one stdin bootstrap")
			}
		})
	}
}
