// Package procfix holds the shared start-time process-checker fake (SRD
// SR-20.3): Checker answers internal/tmux's ProcChecker (Appendix F.2) from a
// per-pid process table the test sets, and records what was asked of it.
//
// Each pid in the table is alive with a start time, gone, a zombie (answers
// gone) or unreadable (answers not known); an unlisted pid answers gone.
// start is empty whenever the answer is not alive, as probe's reader
// promises. Tests take start-time values from procstarttimefix, never
// literals. The table may change between calls (a process dies, a pid is
// reused with another start time), and every method is safe for concurrent
// use, like tmuxfix.Recorder.
//
// A process may also carry an environment. It is readable only through Env,
// which counts every read, so a test proves "no environment was read" with
// EnvReads() == 0. The fake deliberately does not mimic
// probe.LivenessChecker: it imports nothing from agent-director, so a
// `go list -deps` check that the code under test does not reach
// internal/probe stays meaningful.
//
// This is a LEAF test-support package (standard library only): internal
// package tests of internal/tmux can import it without an import cycle. The
// compile-time check that *Checker satisfies tmux.ProcChecker lives in this
// package's external test file.
package procfix

import "sync"

// State is what the fake answers for one pid.
type State int

const (
	// StateGone answers known and not alive: no such process. It is also
	// the answer for a pid the table does not list.
	StateGone State = iota
	// StateAlive answers alive with the process's start time.
	StateAlive
	// StateZombie answers exactly like StateGone (a zombie counts as gone).
	StateZombie
	// StateUnreadable answers not known (EACCES, no /proc).
	StateUnreadable
)

// Process is one entry of the fake's process table.
type Process struct {
	// State is the answer StartTime gives for the pid.
	State State
	// Start is the start time StartTime reports when State is StateAlive;
	// ignored otherwise.
	Start string
	// Env is the process environment Env returns when State is StateAlive.
	Env map[string]string
}

// Alive is a running process with the given start time and no environment.
func Alive(start string) Process { return Process{State: StateAlive, Start: start} }

// Gone is a pid with no process.
func Gone() Process { return Process{State: StateGone} }

// Zombie is a process in state Z; it answers gone.
func Zombie() Process { return Process{State: StateZombie} }

// Unreadable is a process whose start time cannot be read.
func Unreadable() Process { return Process{State: StateUnreadable} }

// WithEnv returns p carrying a copy of env as its environment.
func (p Process) WithEnv(env map[string]string) Process {
	p.Env = copyEnv(env)
	return p
}

// Checker is the scriptable process-checker fake. The zero value is an empty
// table (every pid gone); New is the usual constructor.
type Checker struct {
	mu       sync.Mutex
	procs    map[int]Process
	calls    []int
	envReads int
}

// New returns an empty Checker (every pid answers gone).
func New() *Checker { return &Checker{} }

// Set puts pid's process in the table, replacing any earlier entry. Calling
// it between lookups scripts a race (the process dies, the pid is reused).
func (c *Checker) Set(pid int, p Process) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.procs == nil {
		c.procs = map[int]Process{}
	}
	p.Env = copyEnv(p.Env)
	c.procs[pid] = p
}

// StartTime answers tmux.ProcChecker from the table and records pid.
func (c *Checker) StartTime(pid int) (start string, alive bool, known bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, pid)
	p, ok := c.procs[pid]
	if !ok {
		return "", false, true
	}
	switch p.State {
	case StateAlive:
		return p.Start, true, true
	case StateUnreadable:
		return "", false, false
	default:
		return "", false, true
	}
}

// Env returns a copy of pid's environment and counts the read. ok is false
// (and env nil) unless the pid is alive in the table.
func (c *Checker) Env(pid int) (env map[string]string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.envReads++
	p, listed := c.procs[pid]
	if !listed || p.State != StateAlive {
		return nil, false
	}
	return copyEnv(p.Env), true
}

// StartTimeCalls returns the pid of every StartTime call, in call order.
func (c *Checker) StartTimeCalls() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.calls...)
}

// EnvReads returns how many times Env was called.
func (c *Checker) EnvReads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.envReads
}

func copyEnv(env map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}
