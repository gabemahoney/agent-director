package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type Command struct {
	Name       string    `json:"name"`
	Argv       []string  `json:"argv"`
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended"`
	ExitCode   int       `json:"exit_code"`
	Timeout    bool      `json:"timeout"`
	SpawnError string    `json:"spawn_error,omitempty"`
	Stdout     string    `json:"stdout"`
	Stderr     string    `json:"stderr"`
}

// Execute is the only subprocess seam. Implementations must honor cancellation
// and return only when their process and output readers have stopped.
type Execute func(context.Context, []string, []string, io.Writer, io.Writer) (int, error)

func Exec(ctx context.Context, argv, environment []string, out, errout io.Writer) (int, error) {
	if len(argv) == 0 {
		return -1, errors.New("empty command")
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Env, c.Stdout, c.Stderr = environment, out, errout
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error { return syscall.Kill(-c.Process.Pid, syscall.SIGKILL) }
	c.WaitDelay = time.Second
	err := c.Run()
	if c.ProcessState == nil {
		return -1, err
	}
	return c.ProcessState.ExitCode(), err
}

type Commands struct {
	Evidence    *Evidence
	Execute     Execute
	Environment []string
	Now         func() time.Time
	mu          sync.Mutex
	seq         int
	rows        []Command
}

type limitedBuffer struct{ b []byte }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(b.b)+len(p) > MaxArtifactBytes {
		return 0, errors.New("command output size limit")
	}
	b.b = append(b.b, p...)
	return len(p), nil
}

func (c *Commands) Run(ctx context.Context, name string, limit time.Duration, argv ...string) (Command, []byte, error) {
	if limit <= 0 || limit > 5*time.Minute {
		return Command{}, nil, errors.New("command requires a limit at most five minutes")
	}
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()
	row := Command{Name: name, Argv: append([]string(nil), argv...), Started: c.Now(), ExitCode: -1,
		Stdout: fmt.Sprintf("raw/commands/%04d.stdout", seq), Stderr: fmt.Sprintf("raw/commands/%04d.stderr", seq)}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var out, errout limitedBuffer
	exit, err := c.Execute(ctx, argv, c.Environment, &out, &errout)
	row.Ended, row.ExitCode, row.Timeout = c.Now(), exit, ctx.Err() != nil
	if exit == -1 && err != nil {
		row.SpawnError = err.Error()
	}
	logErr := errors.Join(c.Evidence.Put(row.Stdout, out.b), c.Evidence.Put(row.Stderr, errout.b))
	c.mu.Lock()
	c.rows = append(c.rows, row)
	c.mu.Unlock()
	if err == nil && exit != 0 {
		err = fmt.Errorf("%s exited %d", name, exit)
	}
	return row, out.b, errors.Join(err, logErr)
}

func (c *Commands) Finish() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b []byte
	for _, r := range c.rows {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b = append(b, append(line, '\n')...)
	}
	return c.Evidence.Put("commands.jsonl", b)
}

// Environment does not record inherited values. Identity and tmux routing
// belong to this invocation, never the worker which happens to launch it.
func Environment(socket string) []string {
	var out []string
	for _, v := range os.Environ() {
		key := v
		for i, ch := range key {
			if ch == '=' {
				key = key[:i]
				break
			}
		}
		if len(key) >= 15 && key[:15] == "AGENT_DIRECTOR_" {
			continue
		}
		if key == "TMUX" || key == "TMUX_PANE" {
			continue
		}
		out = append(out, v)
	}
	return append(out, "TMUX="+socket+",0,0")
}
