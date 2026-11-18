package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestClaudeLiveCommandFacts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		exit    int
		err     error
		timeout bool
	}{{"success", 0, nil, false}, {"exit", 7, nil, false}, {"spawn", -1, errors.New("synthetic missing executable"), false}, {"deadline", -1, context.DeadlineExceeded, true}} {
		t.Run(tc.name, func(t *testing.T) {
			r, clock := runnerFixture(t)
			started := clock.Now()
			argv := []string{"/synthetic/tool", "two words", "line\n雪", "$(literal)"}
			ctx := context.Background()
			if tc.timeout {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			r.Commands.Execute = func(ctx context.Context, got, environment []string, out, errout io.Writer) (int, error) {
				if !reflect.DeepEqual(got, argv) || !reflect.DeepEqual(environment, r.Commands.Environment) {
					t.Errorf("argv/environment changed: %q %q", got, environment)
				}
				if d, ok := ctx.Deadline(); !ok || d.After(time.Now().Add(2*time.Second)) {
					t.Error("command has no bounded deadline")
				}
				_, _ = io.WriteString(out, "exact\x00stdout\n")
				_, _ = io.WriteString(errout, "exact stderr\n")
				clock.Advance(time.Second)
				return tc.exit, tc.err
			}
			row, stdout, err := r.Commands.Run(ctx, "facts", 2*time.Second, argv...)
			if (err != nil) != (tc.exit != 0) || row.ExitCode != tc.exit || row.Timeout != tc.timeout || row.Started != started || row.Ended != clock.Now() || !reflect.DeepEqual(row.Argv, argv) {
				t.Fatalf("command facts: %+v, %v", row, err)
			}
			if tc.exit == -1 && row.SpawnError != tc.err.Error() {
				t.Fatalf("lost spawn error: %+v", row)
			}
			argv[1] = "caller mutation"
			if row.Argv[1] != "two words" {
				t.Fatal("command log aliases caller argv")
			}
			if string(stdout) != "exact\x00stdout\n" || string(readFixture(t, filepath.Join(r.Evidence.Root, row.Stdout))) != string(stdout) || string(readFixture(t, filepath.Join(r.Evidence.Root, row.Stderr))) != "exact stderr\n" {
				t.Fatal("output evidence differs")
			}
			if err := r.Commands.Finish(); err != nil {
				t.Fatal(err)
			}
			logged := readJSON[Command](t, filepath.Join(r.Evidence.Root, "commands.jsonl"))
			if !reflect.DeepEqual(logged, row) {
				t.Fatalf("logged command differs: %+v", logged)
			}
		})
	}
}

func TestClaudeLiveCommandRefusesUnboundedOrLostEvidence(t *testing.T) {
	for _, limit := range []time.Duration{0, -1, 5*time.Minute + 1} {
		r, _ := runnerFixture(t)
		if _, _, err := r.Commands.Run(context.Background(), "unbounded", limit, "never-executed"); err == nil {
			t.Fatalf("accepted limit %v", limit)
		}
	}
	r, _ := runnerFixture(t)
	r.Commands.Execute = func(context.Context, []string, []string, io.Writer, io.Writer) (int, error) { return 0, nil }
	putFixture(t, filepath.Join(r.Evidence.Root, "raw/commands/0001.stdout"), []byte("prior"))
	if _, _, err := r.Commands.Run(context.Background(), "cannot-record", time.Second, "not-executed"); err == nil {
		t.Fatal("successful command hid failed evidence write")
	}
}

type startBarrier struct {
	sync.Once
	started chan int
	input   bytes.Buffer
}

func (b *startBarrier) Write(p []byte) (int, error) {
	b.input.Write(p)
	if !bytes.Contains(b.input.Bytes(), []byte("\n")) {
		return len(p), nil
	}
	pid, err := strconv.Atoi(strings.TrimSpace(b.input.String()))
	if err != nil {
		return 0, err
	}
	b.Do(func() { b.started <- pid })
	return len(p), nil
}

func TestClaudeLiveExecCancellationReaps(t *testing.T) {
	if testing.Short() {
		t.Skip("real subprocess cancellation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	barrier := &startBarrier{started: make(chan int, 1)}
	type outcome struct {
		exit int
		err  error
	}
	done := make(chan outcome, 1)
	var stderr bytes.Buffer
	go func() {
		exit, err := Exec(ctx, []string{os.Args[0], "runner-cancel-helper"}, os.Environ(), barrier, &stderr)
		done <- outcome{exit, err}
	}()
	var pid int
	select {
	case pid = <-barrier.started:
		cancel()
	case <-ctx.Done():
		t.Fatal("helper never reached start barrier")
	}
	select {
	case result := <-done:
		if result.exit != -1 || result.err == nil || stderr.Len() != 0 {
			t.Fatalf("cancel: exit=%d err=%v stderr=%q", result.exit, result.err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled subprocess not reaped")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("owned child remains after Exec returned: %v", err)
	}
}

func TestClaudeLiveEnvironmentOwnership(t *testing.T) {
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "foreign")
	t.Setenv("AGENT_DIRECTOR_LAUNCH_TOKEN", "foreign-token")
	t.Setenv("TMUX", "foreign-socket,1,2")
	t.Setenv("TMUX_PANE", "%9")
	t.Setenv("RUNNER_TEST_ORDINARY", "retained")
	got := Environment("/private/socket")
	var routing []string
	for _, item := range got {
		if strings.HasPrefix(item, "AGENT_DIRECTOR_") || strings.HasPrefix(item, "TMUX_PANE=") {
			t.Fatalf("inherited identity: %s", strings.SplitN(item, "=", 2)[0])
		}
		if strings.HasPrefix(item, "TMUX=") || strings.HasPrefix(item, "RUNNER_TEST_ORDINARY=") {
			routing = append(routing, item)
		}
	}
	b, _ := json.Marshal(routing)
	if string(b) != `["RUNNER_TEST_ORDINARY=retained","TMUX=/private/socket,0,0"]` {
		t.Fatalf("invocation routing = %s", b)
	}
}
