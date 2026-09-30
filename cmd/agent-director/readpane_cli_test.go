package main_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The read-pane CLI over test/fake-tmux tables (SR-7.1, SR-7.2, SR-7.5): one
// lookup, one pane listing, one capture by pane id, and nothing changed.

// readPaneSession is a session labelled for row id with token, holding one
// pane paneID that carries token's @ad_pane and captures capture.
func readPaneSession(sessionID, name, token, id, storeID, paneID string, pid int, capture string) faketmuxfix.Session {
	return faketmuxfix.Session{
		ID: sessionID, Created: time.Now().Unix(), Name: name, Label: tmuxfix.LabelValue(token, sessionID, id, storeID),
		Panes: []faketmuxfix.Pane{{ID: paneID, PID: pid, Capture: capture, AdPane: tmuxfix.PaneLabelValue(token, paneID)}},
	}
}

// runReadPane runs read-pane for id under home and fails if the row or the
// fake table on socket changed (SR-7.5).
func runReadPane(t *testing.T, fakeDir, home, id, socket string, flags ...string) (stdout, stderr string, code int) {
	t.Helper()
	rowBefore, tableBefore := rowColumns(t, home, id), faketmuxfix.Tables{}.Read(t, socket)
	args := append([]string{"read-pane", "--claude-instance-id", id}, flags...)
	stdout, stderr, code = runSpawnCLI(t, home, fakeDir, args...)
	if after := rowColumns(t, home, id); !reflect.DeepEqual(after, rowBefore) {
		t.Errorf("row after read-pane = %+v; want unchanged %+v", after, rowBefore)
	}
	if after := (faketmuxfix.Tables{}).Read(t, socket); !reflect.DeepEqual(after.Sessions, tableBefore.Sessions) {
		t.Errorf("sessions after read-pane = %+v; want unchanged %+v", after.Sessions, tableBefore.Sessions)
	}
	return stdout, stderr, code
}

// TestReadPaneCLICapturesByPaneID: the agent's pane, or a lone leftover's, is
// captured by its pane id with the requested lines and -e, never by name.
func TestReadPaneCLICapturesByPaneID(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	ansi := tmuxfix.Find(tmuxfix.Captures(), "capture/ansi").Stdout
	plain := tmuxfix.Find(tmuxfix.Captures(), "capture/P1").Stdout
	const leftoverPane = "%4"
	cases := []struct {
		name     string
		state    string
		leftover bool
		capture  string
		flags    []string
		wantPane string
		wantArgs []string // the capture's argv after the socket
	}{
		{name: "default strips escapes and omits -e", state: store.StateWaiting, capture: ansi,
			wantPane: tmux.StripANSI(ansi), wantArgs: []string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-25"}},
		{name: "ansi keeps escapes and passes -e", state: store.StateWaiting, capture: ansi, flags: []string{"--ansi"},
			wantPane: ansi, wantArgs: []string{"capture-pane", "-p", "-e", "-t", apitest.TestPaneID, "-S", "-25"}},
		{name: "custom line count", state: store.StateWaiting, capture: plain, flags: []string{"--n-lines", "100"},
			wantPane: plain, wantArgs: []string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-100"}},
		{name: "allow pending on a pending row", state: store.StatePending, capture: plain, flags: []string{"--allow-pending"},
			wantPane: plain, wantArgs: []string{"capture-pane", "-p", "-t", apitest.TestPaneID, "-S", "-25"}},
		{name: "lone leftover's token pane", state: store.StateWaiting, leftover: true, capture: plain,
			wantPane: plain, wantArgs: []string{"capture-pane", "-p", "-t", leftoverPane, "-S", "-25"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedKillRow(t, tc.state)
			token, _, storeID := launchIdentity(t, home, id)
			name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
			sess := readPaneSession("$3", name, token, id, storeID, apitest.TestPaneID, apitest.TestPanePID, tc.capture)
			if tc.leftover {
				sess = readPaneSession("$4", "rp-leftover", tmuxfix.OtherToken, id, storeID, leftoverPane, apitest.TestPanePID+1, tc.capture)
			}
			faketmuxfix.Tables{}.Write(t, socket, killTable(sess))

			stdout, stderr, code := runReadPane(t, fakeDir, home, id, socket, tc.flags...)
			if code != 0 || stderr != "" {
				t.Fatalf("read-pane exit = %d, stderr = %q; want 0 and empty", code, stderr)
			}
			var res map[string]any
			if err := json.Unmarshal([]byte(stdout), &res); err != nil {
				t.Fatalf("parse stdout %q: %v", stdout, err)
			}
			if len(res) != 1 || res["pane"] != tc.wantPane {
				t.Errorf("stdout = %s; want exactly {\"pane\":%q}", stdout, tc.wantPane)
			}
			invs := assertInvocationKinds(t, home, "list-sessions", "list-panes", "capture-pane")
			if want := append([]string{"-u", "-S", socket}, tc.wantArgs...); !slices.Equal(invs[2], want) {
				t.Errorf("capture invocation = %q; want %q", invs[2], want)
			}
		})
	}
}

// TestReadPaneCLIRefusals: Gone and two leftovers exit 1 with only the error
// envelope on stderr and capture nothing (SR-7.2).
func TestReadPaneCLIRefusals(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	cases := []struct {
		name      string
		leftovers int
		wantErr   string
		desc      func(id, name string, sessions []apitest.DescSession) apitest.DescCase
	}{
		{name: "gone", wantErr: "ErrTmuxCaptureFailed",
			desc: func(id, name string, _ []apitest.DescSession) apitest.DescCase {
				return apitest.DescPaneGone(apitest.PaneGone{Verb: apitest.PaneReadPane, InstanceID: id, Name: name})
			}},
		{name: "two leftovers", leftovers: 2, wantErr: "ErrTmuxSessionConflict",
			desc: func(id, _ string, sessions []apitest.DescSession) apitest.DescCase {
				return apitest.DescPaneLeftover(apitest.PaneLeftover{Verb: apitest.PaneReadPane, InstanceID: id, Sessions: sessions})
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, id, socket := seedKillRow(t, store.StateWaiting)
			token, _, storeID := launchIdentity(t, home, id)
			name, _ := rowColumns(t, home, id).TmuxSessionName.(string)
			tokens := []string{tmuxfix.OtherToken, tmuxfix.Token}[:tc.leftovers]
			var sessions []faketmuxfix.Session
			var named []apitest.DescSession
			forbid := []string{token, storeID}
			for i, tok := range tokens {
				n := strconv.Itoa(4 + i)
				s := readPaneSession("$"+n, "rp-leftover-"+n, tok, id, storeID, "%"+n, apitest.TestPanePID+1+i, "")
				sessions = append(sessions, s)
				named = append(named, apitest.DescSession{Name: s.Name, ID: s.ID})
				forbid = append(forbid, tok, s.Label)
			}
			faketmuxfix.Tables{}.Write(t, socket, killTable(sessions...))

			stdout, stderr, code := runReadPane(t, fakeDir, home, id, socket)
			if code != 1 || stdout != "" {
				t.Fatalf("read-pane exit = %d, stdout = %q; want 1 and empty (stderr=%q)", code, stdout, stderr)
			}
			env := parseEnvelope(t, stderr)
			if env.ErrName != tc.wantErr {
				t.Errorf("err_name = %q; want %q", env.ErrName, tc.wantErr)
			}
			apitest.AssertDescription(t, env.ErrDescription, tc.desc(id, name, named), forbid...)
			assertInvocationKinds(t, home, "list-sessions")
		})
	}
}

func TestReadPaneCLIErrSpawnNotFound(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"read-pane", "--claude-instance-id", "absent")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrSpawnNotFound" {
		t.Errorf("err_name = %q; want ErrSpawnNotFound", env.ErrName)
	}
	assertInvocationKinds(t, home)
}
