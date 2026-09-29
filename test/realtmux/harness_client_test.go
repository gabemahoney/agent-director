package realtmux_test

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/tmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/google/uuid"
)

// The production client, stream recording, creates, raw session starters,
// generators and catalogue assertions (SRD SR-20.4).
//
// Stream observation: newRecordingClient gives tmux.New a wrapper script as
// its binary that runs the real tmux unchanged and records argv, both
// streams and the exit status of every call the production client makes.

// clientTimeouts bound the production client for real tmux under load, with
// a short pipe-close wait.
var clientTimeouts = tmux.Timeouts{Query: 10 * time.Second, Action: 10 * time.Second, Create: 20 * time.Second, WaitDelay: 250 * time.Millisecond}

// newClient builds the production client exactly as production does (tmux on
// PATH) with clientTimeouts.
func newClient() *tmux.Client { return tmux.New("", clientTimeouts) }

// newClientWith builds the production client with the given binary ("" is
// tmux on PATH) and timeouts.
func newClientWith(binary string, to tmux.Timeouts) *tmux.Client { return tmux.New(binary, to) }

// callLog reads what the recording wrapper saw, in call order.
type callLog struct{ dir string }

// recordedCall is one production-client invocation: its argv after the
// binary and what tmux printed and returned.
type recordedCall struct {
	Args []string
	rawResult
}

// newRecordingClient returns a production client whose binary is a wrapper
// running the real tmux unchanged, and the log of every call it makes. The
// wrapper is /bin/sh, so environment-sensitive tests use newClient instead.
func newRecordingClient(t testing.TB) (*tmux.Client, *callLog) {
	t.Helper()
	if tmuxPath == "" {
		t.Skip("tmux is not on PATH")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
d='` + dir + `'
n=$(( $(cat "$d/seq" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$d/seq"
printf '%s\0' "$@" > "$d/$n.args"
'` + tmuxPath + `' "$@" > "$d/$n.out" 2> "$d/$n.err"
s=$?
cat "$d/$n.out"
cat "$d/$n.err" >&2
echo "$s" > "$d/$n.exit"
exit "$s"
`
	wrapper := filepath.Join(dir, "tmux-recorder")
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		t.Fatalf("write recording wrapper: %v", err)
	}
	return newClientWith(wrapper, clientTimeouts), &callLog{dir: dir}
}

// calls returns every recorded invocation in order.
func (l *callLog) calls(t testing.TB) []recordedCall {
	t.Helper()
	seq, err := os.ReadFile(filepath.Join(l.dir, "seq"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	n, convErr := strconv.Atoi(strings.TrimSpace(string(seq)))
	if err != nil || convErr != nil {
		t.Fatalf("read call count: %v %v", err, convErr)
	}
	out := make([]recordedCall, 0, n)
	for i := 1; i <= n; i++ {
		read := func(ext string) string {
			b, err := os.ReadFile(filepath.Join(l.dir, strconv.Itoa(i)+ext))
			if err != nil {
				t.Fatalf("recorded call %d has no %s: %v", i, ext, err)
			}
			return string(b)
		}
		exit, err := strconv.Atoi(strings.TrimSpace(read(".exit")))
		if err != nil {
			t.Fatalf("recorded call %d: exit status: %v", i, err)
		}
		args := strings.Split(strings.TrimSuffix(read(".args"), "\x00"), "\x00")
		out = append(out, recordedCall{Args: args, rawResult: rawResult{Stdout: read(".out"), Stderr: read(".err"), Exit: exit}})
	}
	return out
}

// last returns the most recent recorded invocation.
func (l *callLog) last(t testing.TB) recordedCall {
	t.Helper()
	all := l.calls(t)
	if len(all) == 0 {
		t.Fatalf("the recording client made no call")
	}
	return all[len(all)-1]
}

// stubCommand is the long-lived pane command: tmux execs it directly (more
// than one argv element), so the pane pid is the stub itself.
func stubCommand() []string { return []string{"sleep", "3600"} }

// newToken returns a fresh launch token: 16 lowercase hex.
func newToken(t testing.TB) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("token: %v", err)
	}
	return hex.EncodeToString(b)
}

// newInstanceID returns prefix + "-" + a UUID.
func newInstanceID(prefix string) string { return prefix + "-" + uuid.NewString() }

// uniqueName returns a fresh session name.
func uniqueName() string { return "rt-" + uuid.NewString()[:8] }

// createSpec is a production create; zero fields take defaults: a unique
// name, the private TMUX_TMPDIR as cwd, stubCommand, a fresh token, a
// UUID-suffixed instance id and newClient.
type createSpec struct {
	Name, Cwd         string
	Envs              map[string]string
	Command           []string
	Token, InstanceID string
	Client            *tmux.Client
}

// created is a create's filled-in spec and its reply.
type created struct {
	createSpec
	Reply tmux.CreateReply
}

// create runs the production NewSession on the bound socket and tracks the
// server and pane it reports.
func (r *realTmux) create(t testing.TB, spec createSpec) (created, error) {
	t.Helper()
	if spec.Name == "" {
		spec.Name = uniqueName()
	}
	if spec.Cwd == "" {
		spec.Cwd = r.Dir
	}
	if spec.Command == nil {
		spec.Command = stubCommand()
	}
	if spec.Token == "" {
		spec.Token = newToken(t)
	}
	if spec.InstanceID == "" {
		spec.InstanceID = newInstanceID("agent")
	}
	if spec.Client == nil {
		spec.Client = newClient()
	}
	r.tr.addSocket(r.Socket)
	reply, err := spec.Client.NewSession(r.Socket, spec.Name, spec.Cwd, spec.Envs, spec.Command, spec.Token, spec.InstanceID)
	r.tr.addProc(reply.ServerPID)
	r.tr.addProc(reply.PanePID)
	return created{createSpec: spec, Reply: reply}, err
}

// mustCreate is create failing the test on any error.
func (r *realTmux) mustCreate(t testing.TB, spec createSpec) created {
	t.Helper()
	c, err := r.create(t, spec)
	if err != nil {
		t.Fatalf("production create on %s: %s", r.Socket, describe(err))
	}
	return c
}

// rawSession is a session started by the raw runner.
type rawSession struct {
	ID, PaneID         string
	PanePID, ServerPID int
}

// startSession starts a session with the raw runner: no label, no -e.
func (r *realTmux) startSession(t testing.TB, name string) rawSession {
	t.Helper()
	return r.raw().startSession(t, name, "")
}

// startSessionWithID is startSession with -e AGENT_DIRECTOR_INSTANCE_ID=<id>.
func (r *realTmux) startSessionWithID(t testing.TB, name, instanceID string) rawSession {
	t.Helper()
	return r.raw().startSession(t, name, instanceID)
}

// startSession runs new-session detached with stubCommand in the private
// TMUX_TMPDIR ("" name: unique; "" instanceID: no -e) and tracks what it
// started.
func (c *rawCmd) startSession(t testing.TB, name, instanceID string) rawSession {
	t.Helper()
	if name == "" {
		name = uniqueName()
	}
	args := []string{"new-session", "-d", "-s", name, "-c", c.rt.Dir}
	if instanceID != "" {
		args = append(args, "-e", "AGENT_DIRECTOR_INSTANCE_ID="+instanceID)
	}
	args = append(args, "-P", "-F", "#{session_id}\t#{pane_id}\t#{pane_pid}\t#{pid}", "--")
	out := c.must(t, append(args, stubCommand()...)...)
	f := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(f) != 4 {
		t.Fatalf("new-session reply has %d fields, want 4: %q", len(f), out)
	}
	panePID, err1 := strconv.Atoi(f[2])
	serverPID, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil {
		t.Fatalf("new-session reply pids: %q", out)
	}
	c.rt.tr.addProc(serverPID)
	c.rt.tr.addProc(panePID)
	return rawSession{ID: f[0], PaneID: f[1], PanePID: panePID, ServerPID: serverPID}
}

// assertTriple compares an observed run with a catalogue entry's standard
// output, standard error and exit status; label values are never printed.
func assertTriple(t testing.TB, got rawResult, want tmuxfix.Entry) {
	t.Helper()
	if got.Stdout != want.Stdout {
		t.Errorf("%s: stdout = %q, want %q", want.Name, redact(got.Stdout), redact(want.Stdout))
	}
	if got.Stderr != want.Stderr {
		t.Errorf("%s: stderr = %q, want %q", want.Name, redact(got.Stderr), redact(want.Stderr))
	}
	if got.Exit != want.Exit {
		t.Errorf("%s: exit status = %d, want %d", want.Name, got.Exit, want.Exit)
	}
}

// assertCallError checks err against the entry's typed outcome for call: nil
// when the entry records success, else a *tmux.CallError with that Call,
// Failure and Socket (and FirstLine for an unrecognised reply).
func assertCallError(t testing.TB, err error, call tmux.Call, want tmuxfix.Entry) {
	t.Helper()
	wantF, ok := want.Want[call]
	if !ok {
		t.Fatalf("catalogue entry %s records no outcome for the %s call", want.Name, call)
	}
	if wantF == 0 {
		if err != nil {
			t.Errorf("%s, %s call: error %s, want success", want.Name, call, describe(err))
		}
		return
	}
	var ce *tmux.CallError
	if !errors.As(err, &ce) {
		t.Errorf("%s, %s call: error %s, want a *tmux.CallError (%s)", want.Name, call, describe(err), wantF)
		return
	}
	if ce.Call != call || ce.Failure != wantF || ce.Socket != want.Socket {
		t.Errorf("%s: got {Call %q, Failure %s, Socket %q}, want {Call %q, Failure %s, Socket %q}",
			want.Name, ce.Call, ce.Failure, ce.Socket, call, wantF, want.Socket)
	}
	if wantF == tmux.FailUnrecognized && ce.FirstLine != want.FirstLine {
		t.Errorf("%s, %s call: FirstLine = %q, want %q", want.Name, call, redact(ce.FirstLine), redact(want.FirstLine))
	}
}

// describe prints an error without any label value.
func describe(err error) string {
	if err == nil {
		return "<nil>"
	}
	return redact(err.Error())
}
