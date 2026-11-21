package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/installversion"
	"github.com/gabemahoney/agent-director/internal/store"
)

// This is private hook plumbing, not a public flag or a provider callback.
const (
	claudeStartupWakeArg    = "--claude-startup-wake-job"
	claudeStartupJobCap     = 5 * time.Second
	claudeStartupOpenBusyMS = 100
	// Diagnostics may use this last part of the budget, but never postpone
	// the separate unconditional process exit at the original deadline.
	claudeStartupDiagnosticReserve = 250 * time.Millisecond
)

type claudeStartupWakeJob struct {
	Wake      hook.ClaudeStartupWake
	StorePath string
	HookPID   int
}

// Consumer seam for capture refusal and immutable-child-path tests. The
// running-image algorithm itself lives only in installversion.
var startupWakeExecutable = installversion.CaptureExecutable

// scheduleClaudeStartupWake detaches one bounded re-exec. No pipe from the
// provider is inherited, so it cannot delay SessionStart's output or exit.
// Its stderr is a regular diagnostic file, never a wake-prompt stream.
// Capture uses the running image's stamp, even if publication preceded path
// resolution. An unsafe in-layout capture refuses the hint through this error.
func scheduleClaudeStartupWake(path string, logger *log.Logger, wake hook.ClaudeStartupWake) error {
	if logger == nil {
		return fmt.Errorf("startup wake requires a diagnostic file")
	}
	diagnostic, ok := logger.Writer().(*os.File)
	if !ok {
		return fmt.Errorf("startup wake requires a diagnostic file")
	}
	info, err := diagnostic.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("startup wake requires a regular diagnostic file")
	}
	executable, err := startupWakeExecutable()
	if err != nil {
		return err
	}
	data, err := json.Marshal(claudeStartupWakeJob{Wake: wake, StorePath: path, HookPID: os.Getpid()})
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	p, err := os.StartProcess(executable, []string{executable, "hook", claudeStartupWakeArg, string(data)}, &os.ProcAttr{
		Env: os.Environ(), Files: []*os.File{null, null, diagnostic}, Sys: &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		return err
	}
	return p.Release() // reparented on hook exit; no background waiter survives it
}

func runClaudeStartupWake(args []string) int {
	logger := log.New(os.Stderr, "agent-director-hook ", log.LstdFlags)
	var job claudeStartupWakeJob
	if len(args) != 1 || json.Unmarshal([]byte(args[0]), &job) != nil || job.HookPID <= 0 || job.StorePath == "" ||
		job.Wake.InstanceID == "" || job.Wake.LaunchToken == "" || job.Wake.SessionID == "" ||
		!time.Now().Before(job.Wake.Deadline) || job.Wake.Deadline.After(time.Now().Add(claudeStartupJobCap)) {
		// An expired job has no remaining budget for a diagnostic sink.
		// Attempt the notice without waiting for its lock or write.
		go logger.Print("hook: invalid or expired busy-send startup wake job")
		return hookExitCode
	}
	ctx, cancel := context.WithDeadline(context.Background(), job.Wake.Deadline)
	defer cancel()
	// Opening the existing store has no context API. This process owns only
	// a delayed hint, so its absolute deadline also ends the process if open
	// or a filesystem operation stalls. It can never outlive that budget.
	stop := armClaudeStartupWakeDeadline(job.Wake.Deadline, func() {
		logger.Printf("hook: busy-send startup wake (instance=%s): job still active near deadline", job.Wake.InstanceID)
	}, os.Exit)
	defer stop()
	// Never initialize or migrate a store for a delayed hint. A small open
	// busy timeout and the original absolute deadline bound its store work.
	st, err := store.OpenWithBusyTimeout(job.StorePath, claudeStartupOpenBusyMS)
	if err == nil {
		defer st.Close()
		err = hook.RunClaudeStartupWake(ctx, st, job.Wake, func() bool { return os.Getppid() != job.HookPID },
			hook.DefaultPollClock(), time.Now, logger)
	}
	if err != nil {
		logger.Printf("hook: busy-send startup wake (instance=%s): %v", job.Wake.InstanceID, err)
	}
	return hookExitCode
}

// armClaudeStartupWakeDeadline arms the exit before attempting diagnostics.
// The two callbacks run independently: neither a blocked diagnostic write
// nor a logger mutex held by another write can keep the exit from running.
// diagnostic and exit are seams for a blocked-sink test; production supplies
// the logger call and os.Exit. Stopping timers never waits for a callback.
func armClaudeStartupWakeDeadline(deadline time.Time, diagnostic func(), exit func(int)) func() {
	hard := time.AfterFunc(time.Until(deadline), func() { exit(hookExitCode) })
	notice := time.AfterFunc(time.Until(deadline.Add(-claudeStartupDiagnosticReserve)), diagnostic)
	return func() {
		notice.Stop()
		hard.Stop()
	}
}
