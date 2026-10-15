//go:build linux

package cursorhost

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCursorRecoveryCompletionCannotRestartCancellationDeadline(t *testing.T) {
	// Serial: actual Run/report subprocess and verified runtime set environment.
	if testing.Short() {
		t.Skip("exercises the real three-second cancellation deadline")
	}
	barrier, opener := newRecordBarrier(t, "not-yet-armed")
	i := &completeInput{ready: make(chan struct{})}
	completed, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	afterReport := func() {
		once.Do(func() {
			barrier.kind = "input_control"
			close(completed)
			<-resume
		})
	}
	c := beginCursorSessionWithInput(t, "", opener, i.start, afterReport)
	defer barrier.unblock()
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	<-i.ready
	dir := t.TempDir()
	t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "Stop")
	t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", "after")
	t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
	defer os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
	i.emit(terminalInput{interrupt: true})
	c.wait(t, "actual recovery Stop committed", func() bool {
		_, err := os.Stat(filepath.Join(dir, "entered"))
		return err == nil
	})
	var first time.Time
	for _, r := range c.records(t) {
		if r.Kind == "input_control" {
			first = r.Data.ConsumedAt
		}
	}
	if first.IsZero() {
		t.Fatal("first real control lacks protected consumption timestamp")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("actual recovery report did not complete before superseding control")
	}
	// Deliberately spend part of the original budget at the post-await barrier.
	// A reset at the second control would now exceed the original bound below.
	if delay := time.Until(first.Add(1500 * time.Millisecond)); delay > 0 {
		timer := time.NewTimer(delay)
		<-timer.C
	}
	i.emit(terminalInput{interrupt: true})
	close(resume)
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("superseding control did not reach protected Sync")
	}
	err := c.awaitFailure(t, time.Until(first.Add(cancelWait+time.Second)))
	elapsed := time.Since(first)
	if elapsed > cancelWait+time.Second || elapsed < cancelWait-100*time.Millisecond {
		t.Fatalf("original cancellation deadline lost: %s: %v", elapsed, err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("blocked persistence failed for another reason: %v", err)
	}
	var pid int
	rawPID, err := os.ReadFile(c.e.runtime.PID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Sscan(string(rawPID), &pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatal("deadline returned while owned runtime survived")
	}
	row, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if row.State == "ended" {
		t.Fatal("blocked recovery fabricated ended success")
	}
	if bytes.Contains(c.pane.bytes(), []byte("submit a new prompt to continue")) {
		t.Fatal("superseded recovery falsely enabled new input")
	}
	controls := 0
	for _, r := range c.records(t) {
		if r.Kind == "input_control" {
			controls++
		}
		if r.Kind == "session_end" {
			t.Fatal("blocked recovery fabricated intentional ending")
		}
	}
	if controls != 2 {
		t.Fatalf("expected original and superseding protected controls, got %d", controls)
	}
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("session/prompt")) || len(c.prompts(t)) != 0 {
		t.Fatal("failed recovery dispatched unsolicited or replayed prompt")
	}
	t.Logf("completion-first recovery retained original 3s deadline: %s; owned runtime %d terminated; two controls, no ended success or prompt replay", elapsed, pid)
}

func TestCursorControlDeadlineSupervisesBlockedTranscriptSync(t *testing.T) {
	// Serial: Run and the verified runtime fixture own process-wide environment.
	for _, mode := range []string{"idle interrupt", "idle exit", "busy", "start gate"} {
		t.Run(mode, func(t *testing.T) {
			kind, initial := "input_control", ""
			if mode == "busy" {
				initial = "HOLD"
			}
			if mode == "start gate" {
				kind = "submission_started"
			}
			barrier, opener := newRecordBarrier(t, kind)
			c := beginCursorSession(t, initial, opener)
			defer barrier.unblock()
			c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
			if mode == "busy" {
				c.wait(t, "active peer receipt", func() bool { return len(c.prompts(t)) == 1 })
			}
			if mode == "start gate" {
				c.keys(t, "gate-must-stay-unwritten\r")
			} else if mode == "idle exit" {
				c.keys(t, "/exit\r")
			} else {
				c.keys(t, "\x03")
			}
			select {
			case <-barrier.entered:
			case <-time.After(time.Second):
				t.Fatal("real protected Sync did not block")
			}
			started := time.Now()
			if mode == "start gate" {
				c.keys(t, "\x03")
			}
			// Repetition is finite and must not replace the first cancellation deadline.
			c.keys(t, "\x03\x03")
			err := c.awaitFailure(t, cancelWait+time.Second)
			if time.Since(started) > cancelWait+time.Second {
				t.Fatal("repeated control extended deadline")
			}
			t.Logf("blocked %s: %v after %s", kind, err, time.Since(started))
			var pid int
			rawPID, err := os.ReadFile(c.e.runtime.PID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Sscan(string(rawPID), &pid); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(pid, 0); err == nil {
				t.Fatal("gateway returned while owned runtime survived")
			}
			raw, err := os.ReadFile(c.e.runtime.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte("gate-must-stay-unwritten")) {
				t.Fatalf("start record not durable but prompt escaped: %s", raw)
			}
			for _, r := range c.records(t) {
				if r.Kind == "session_end" {
					t.Fatal("blocked persistence fabricated an intentional ended record")
				}
			}
			barrier.unblock()
		})
	}
}

func TestCursorInitialAcceptedPromptHasDispositionBeforeActiveTracking(t *testing.T) {
	// Serial: actual reporter barriers and runtime process use environment.
	for _, failure := range []string{"runtime verification", "UserPromptSubmit"} {
		t.Run(failure, func(t *testing.T) {
			barrier, opener := newRecordBarrier(t, "submission_accepted")
			dir := t.TempDir()
			if failure == "UserPromptSubmit" {
				t.Setenv("AD_TEST_CURSOR_BARRIER_EVENT", "UserPromptSubmit")
				t.Setenv("AD_TEST_CURSOR_BARRIER_PHASE", "before")
				t.Setenv("AD_TEST_CURSOR_BARRIER_DIR", dir)
				defer os.WriteFile(filepath.Join(dir, "release"), nil, 0600)
			}
			c := beginCursorSession(t, "initial-must-not-disappear", opener)
			defer barrier.unblock()
			select {
			case <-barrier.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("initial acceptance not persisted")
			}
			if failure == "UserPromptSubmit" {
				barrier.unblock()
				c.wait(t, "actual prompt hook before commit", func() bool { _, err := os.Stat(filepath.Join(dir, "entered")); return err == nil })
			}
			// The protocol owns a separate writer descriptor, so replacing only the
			// registered parent descriptor invalidates authority without racing child exit.
			b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			null, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer null.Close()
			if err := syscall.Dup3(int(null.Fd()), b.InputFD, syscall.O_CLOEXEC); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Kill(b.RuntimePID, 0); err != nil {
				t.Fatalf("authority fault requires live runtime: %v", err)
			}
			barrier.unblock()
			if failure == "UserPromptSubmit" {
				if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("initial pre-active failure: %v", c.awaitFailure(t, 4*time.Second))
			accepted, disposed := map[string]bool{}, map[string]int{}
			userInputs := 0
			for _, r := range c.records(t) {
				switch r.Kind {
				case "user_input":
					userInputs++
				case "submission_accepted":
					if r.Data.Text == "initial-must-not-disappear" {
						accepted[r.Data.ID] = true
					}
				case "submission_cancelled":
					if r.Data.Started {
						t.Fatal("pre-active failure fabricated execution")
					}
					disposed[r.Data.ID]++
				case "submission_started", "session_end", "turn_outcome":
					t.Fatalf("pre-active failure fabricated %s", r.Kind)
				}
			}
			if failure == "runtime verification" && userInputs != 0 {
				t.Fatal("verification failure happened after submit recording")
			}
			if failure == "UserPromptSubmit" && userInputs != 1 {
				t.Fatal("report failure did not reach submit recording")
			}
			if len(accepted) != 1 {
				t.Fatalf("accepted initial identities: %v", accepted)
			}
			for id := range accepted {
				if disposed[id] != 1 {
					t.Fatalf("accepted initial %s lacks exactly one protected disposition: %v", id, disposed)
				}
			}
			raw, err := os.ReadFile(c.e.runtime.Raw)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "initial-must-not-disappear") {
				t.Fatal("failed initial prompt reached ACP")
			}
		})
	}
}

func TestCursorStartRecordGateRetiresWithoutBytesAndAllowsExplicitInput(t *testing.T) {
	// Serial: real Run and protected file barrier own environment.
	barrier, opener := newRecordBarrier(t, "submission_started")
	c := beginCursorSession(t, "", opener)
	defer barrier.unblock()
	c.wait(t, "ready", func() bool { return bytes.Contains(c.pane.bytes(), []byte("Cursor ready")) })
	before, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	c.keys(t, "retire-at-start-record\r")
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		t.Fatal("protected start Sync not reached")
	}
	raw, err := os.ReadFile(c.e.runtime.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("retire-at-start-record")) {
		t.Fatal("prompt bytes preceded durable start")
	}
	c.keys(t, "\x03^")
	c.wait(t, "control consumed during Sync", func() bool { return bytes.Contains(c.pane.bytes(), []byte("\r\x1b[2K> ^")) })
	barrier.unblock()
	c.wait(t, "known unwritten cancellation and recovery", func() bool {
		n := 0
		for _, r := range c.records(t) {
			if r.Kind == "submission_cancelled" {
				if r.Data.Started {
					t.Fatal("known unwritten cancellation fabricated execution")
				}
				n++
			}
		}
		return n == 1 && bytes.Contains(c.pane.bytes(), []byte("Cursor input cancelled; submit a new prompt to continue"))
	})
	if got := c.prompts(t); len(got) != 0 {
		t.Fatalf("retired prompt reached peer: %q", got)
	}
	c.keys(t, "\x15later-explicit-after-sync\r")
	c.wait(t, "later explicit prompt executes", func() bool { return len(c.prompts(t)) == 1 })
	c.stop(t)
	after, err := c.e.s.GetSpawn(c.e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionID != before.SessionID || after.LifeNumber != before.LifeNumber {
		t.Fatal("known unwritten retirement changed conversation/life")
	}
	got := c.prompts(t)
	if len(got) != 1 || got[0] != "later-explicit-after-sync" {
		t.Fatalf("retired queue replayed: %q", got)
	}
}

func TestCursorInitialPromptIndividualByteBoundary(t *testing.T) {
	// Serial: real Run and verified runtime fixture own environment.
	for _, size := range []int{1024*1024 - 1, 1024 * 1024, 1024*1024 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			text := strings.Repeat("🦋", size/4) + strings.Repeat("x", size%4)
			c := beginCursorSession(t, text, openTranscript)
			if size > 1024*1024 {
				if err := c.awaitFailure(t, time.Second); !strings.Contains(err.Error(), "input exceeds limit") {
					t.Fatal(err)
				}
				if got := c.prompts(t); len(got) != 0 {
					t.Fatal("oversized initial prompt reached ACP")
				}
				for _, r := range c.records(t) {
					if r.Kind == "submission_accepted" {
						t.Fatal("oversized initial accepted")
					}
				}
				return
			}
			c.wait(t, "exact boundary prompt received", func() bool { return len(c.prompts(t)) == 1 })
			if got := c.prompts(t); got[0] != text {
				t.Fatalf("boundary text changed: got %d want %d bytes", len(got[0]), size)
			}
			c.stop(t)
			accepted := 0
			for _, r := range c.records(t) {
				if r.Kind == "submission_accepted" {
					if r.Data.Text != text {
						t.Fatal("protected boundary text changed")
					}
					accepted++
				}
			}
			if accepted != 1 {
				t.Fatalf("boundary acceptance count %d", accepted)
			}
		})
	}
}
