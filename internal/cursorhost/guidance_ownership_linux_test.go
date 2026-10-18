//go:build linux

package cursorhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// heldGuidanceQuery uses the production exec collector and the existing fake's
// complete replies; only an owned descendant's open pipe makes them uncertain.
func heldGuidanceQuery(t *testing.T, query string, partial bool, consumeOnce ...bool) string {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "hold")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	output := ""
	if partial {
		output = fmt.Sprintf("%q \"$@\" | head -c 16\n", faketmuxfix.Binary(t))
	}
	condition := fmt.Sprintf("[ -f '%s' ]", marker)
	if len(consumeOnce) > 0 && consumeOnce[0] {
		// Rename claims the injection before pipe holding, without a Go-side release.
		condition = fmt.Sprintf("mv '%s' '%s.consumed' 2>/dev/null", marker, marker)
	}
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
 *" %s "*)
  if %s; then
   %ssleep 5 &
   echo $! >> '%s/pids'
   exit 0
  fi;;
esac
exec %q "$@"
`, query, condition, output, dir, faketmuxfix.Binary(t))
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		data, _ := os.ReadFile(filepath.Join(dir, "pids"))
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return marker
}

func TestGuidanceOwnershipActualCutRecoveryRequiresCompleteCurrentFacts(t *testing.T) {
	// Serial: requestEnv and the executable pipe holder set HOME/PATH.
	// b.ftd: captured lookup/list cuts with both empty and partial stdout.
	if testing.Short() {
		t.Skip("actual pipe-cut recovery matrix")
	}
	for _, query := range []string{"list-sessions", "list-panes"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", query, partial), func(t *testing.T) {
				e := newRequestEnv(t, "off", "synthetic-session")
				row, err := e.s.GetSpawn(e.binding.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				marker := heldGuidanceQuery(t, query, partial)
				cfg := config.Default()
				first := make(chan error, 1)
				completeVerification := make(chan struct{}, 1)
				release := make(chan struct{})
				done := make(chan error, 1)
				go func() {
					firstAttempt := true
					done <- VerifyGuidanceOwnership(context.Background(), time.Now().Add(2*time.Second), func() error {
						err := VerifyRuntime(e.binding, false)
						if err == nil {
							err = VerifyPane(e.s, row, cfg)
						}
						if err == nil {
							err = VerifyRuntime(e.binding, false)
						}
						if err == nil && !firstAttempt {
							select {
							case completeVerification <- struct{}{}:
							default:
							}
						}
						if firstAttempt {
							firstAttempt = false
							first <- err
							<-release
						}
						return err
					})
				}()
				err = <-first
				var call *tmux.CallError
				var uncertain *paneUncertain
				if !errors.As(err, &uncertain) || !errors.As(err, &call) || !call.QueryInterrupted() || call.HadStdout != partial || call.ExitStatus != 0 || call.Failure != tmux.FailUnrecognized {
					close(release)
					t.Fatalf("actual held query accepted or lost interruption evidence: %v (%+v)", err, call)
				}
				select {
				case err := <-done:
					t.Fatalf("partial evidence admitted before complete verification: %v", err)
				default:
				}
				if err := os.Remove(marker); err != nil {
					close(release)
					t.Fatal(err)
				}
				close(release)
				if err := <-done; err != nil {
					t.Fatalf("later complete owned evidence did not recover: %v", err)
				}
				select {
				case <-completeVerification:
				default:
					t.Fatal("recovery admitted without a later successful complete verification")
				}
				if query != "list-panes" || !partial {
					return
				}
				// Recovery is still tied to the current server, labels and pane.
				for _, change := range []string{"server", "label", "pane", "provenance", "gone", "zero panes", "duplicate pane"} {
					original, _, readErr := faketmuxfix.ReadTable(faketmuxfix.Tables{}.Path(row.Identity.Socket))
					if readErr != nil {
						t.Fatal(readErr)
					}
					faketmuxfix.Tables{}.Update(t, row.Identity.Socket, func(table *faketmuxfix.Table) {
						switch change {
						case "server":
							table.Server.PID++
						case "label":
							table.Sessions[0].Label = "ad1 ffffffffffffffff $0 other " + e.s.StoreID()
						case "pane":
							table.Sessions[0].Panes[0].AdPane = "ffffffffffffffff %0"
						case "provenance":
							table.Scope.Global = table.Sessions[0].Label
						case "gone":
							table.Sessions = nil
						case "zero panes":
							table.Sessions[0].Panes = nil
						case "duplicate pane":
							table.Sessions[0].Panes = append(table.Sessions[0].Panes, table.Sessions[0].Panes[0])
						}
					})
					calls := 0
					if err := VerifyGuidanceOwnership(context.Background(), time.Now().Add(time.Second), func() error { calls++; return VerifyPane(e.s, row, cfg) }); err == nil || calls != 1 {
						t.Errorf("changed %s admitted or retried: %v calls=%d", change, err, calls)
					}
					faketmuxfix.Tables{}.Write(t, row.Identity.Socket, original)
				}
			})
		}
	}
}

func TestGuidanceOwnershipActualPersistentCutRespectsOriginalBound(t *testing.T) {
	// Serial: requestEnv and the executable pipe holder set HOME/PATH.
	e := newRequestEnv(t, "off", "synthetic-session")
	row, err := e.s.GetSpawn(e.binding.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	heldGuidanceQuery(t, "list-panes", false)
	started := time.Now()
	until := started.Add(240 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), until)
	defer cancel()
	var worker sync.Mutex
	err = VerifyGuidanceOwnership(ctx, until, func() error {
		worker.Lock()
		defer worker.Unlock()
		if err := ctx.Err(); err != nil {
			return err
		}
		return VerifyPane(e.s, row, config.Default())
	})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 600*time.Millisecond {
		t.Fatalf("persistent cut exceeded original bound: %v after %v", err, time.Since(started))
	}
	// Drain a started reader; a worker scheduled after expiry touches no fixture.
	worker.Lock()
	worker.Unlock()
}

func TestGuidanceOwnershipLatePositiveAndCanceledWorkCannotAdmit(t *testing.T) {
	t.Parallel()
	for _, cancellation := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%v", cancellation), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			until := time.Now().Add(100 * time.Millisecond)
			go func() {
				done <- VerifyGuidanceOwnership(ctx, until, func() error { close(entered); <-release; close(finished); return nil })
			}()
			<-entered
			if cancellation {
				cancel()
			}
			err := <-done
			if cancellation && !errors.Is(err, context.Canceled) || !cancellation && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("late work admitted: %v", err)
			}
			close(release)
			<-finished
		})
	}
}

func TestGuidanceOwnershipRefusalAndExpiredReplacementAreOneShot(t *testing.T) {
	t.Parallel()
	for _, cause := range []error{
		errors.New("Cursor pane ownership changed"),
		&paneUncertain{},
		&paneUncertain{cause: errors.New("unavailable")},
		interruptedPane(&tmux.CallError{Call: tmux.CallLookup, Failure: tmux.FailUnrecognized, FirstLine: "output cut short: its pipes stayed open past the pipe-close wait"}),
		interruptedPane(&tmux.CallError{Call: tmux.CallListPanes, Failure: tmux.FailUnavailable}),
	} {
		calls := 0
		got := VerifyGuidanceOwnership(context.Background(), time.Now().Add(time.Second), func() error { calls++; return cause })
		if got != cause || calls != 1 {
			t.Fatalf("unproven interruption retried: %v calls=%d", got, calls)
		}
	}
	calls := 0
	if err := VerifyGuidanceOwnership(context.Background(), time.Time{}, func() error { calls++; return nil }); err != nil || calls != 1 {
		t.Fatalf("published replacement proof: %v calls=%d", err, calls)
	}
	transient := interruptedPane(&tmux.CallError{Call: tmux.CallLookup, Failure: tmux.FailTimeout})
	calls = 0
	if err := VerifyGuidanceOwnership(context.Background(), time.Time{}, func() error { calls++; return transient }); err != transient || calls != 1 {
		t.Fatalf("replacement gained new retry budget: %v calls=%d", err, calls)
	}
}

func TestCursorGuidanceCutDuringInitializedRejectsRetirementAndKeepsControls(t *testing.T) {
	// Serial: actual Run, configured MCP and executable holder own HOME/PATH.
	// b.ftd: notification cannot acknowledge or publish from uncertain evidence.
	if testing.Short() {
		t.Skip("actual configured MCP admission")
	}
	for _, control := range []string{"exit", "retire runtime", "retire token", "retire life"} {
		t.Run(control, func(t *testing.T) {
			scenario := guidanceScenario(t)
			scenario.MCPWaitAttempt = 1
			input := &completeInput{ready: make(chan struct{})}
			var marker string
			c := beginConfiguredCursorSession(t, Options{InitialPrompt: "never dispatch", mcpExecutable: guidanceCLI(t)}, scenario, openTranscript, input.start, func(e *requestEnv) {
				enableGuidance(t, e)
				marker = heldGuidanceQuery(t, "list-panes", false)
				if err := os.Remove(marker); err != nil {
					t.Fatal(err)
				}
			})
			<-input.ready
			c.wait(t, "MCP initialize response", func() bool { return guidanceEvent(t, c, "response", 1) })
			if err := os.WriteFile(marker, nil, 0600); err != nil {
				t.Fatal(err)
			}
			rotationRelease(t, c.e.runtime.MCPRelease)
			c.wait(t, "actual initialized query held", func() bool { _, err := os.Stat(filepath.Join(filepath.Dir(marker), "pids")); return err == nil })
			switch control {
			case "exit":
				input.emit(terminalInput{text: "/exit"})
			case "retire runtime":
				b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := c.e.s.MarkCursorRuntimeGone(b); err != nil || !ok {
					t.Fatalf("retire: %v %v", ok, err)
				}
			case "retire token":
				c.e.exec(t, "UPDATE spawns SET launch_token='ffffffffffffffff' WHERE instance_id=?", c.e.binding.InstanceID)
			case "retire life":
				c.e.exec(t, "UPDATE spawns SET life_number=life_number+1 WHERE instance_id=?", c.e.binding.InstanceID)
			}
			if control != "exit" {
				c.wait(t, "retired MCP child refused", func() bool {
					for _, event := range mcpEvents(t, c) {
						if strings.Contains(string(event["text"]), "Cursor MCP ownership retired") {
							return guidanceEvent(t, c, "exit", 1)
						}
					}
					return false
				})
				if len(rotationPrompts(c, t)) != 0 || rotationRecordCount(c, t, "guidance_initialized") != 0 {
					t.Fatal("retired initialized evidence admitted work")
				}
				input.emit(terminalInput{text: "/exit"})
			}
			select {
			case err := <-c.done:
				c.ended = true
				c.endErr = err
			case <-time.After(cancelWait + time.Second):
				t.Fatal("cut verification concealed control or retired authority")
			}
			if err := os.Remove(marker); err != nil {
				t.Fatal(err)
			}
			if len(rotationPrompts(c, t)) != 0 || rotationRecordCount(c, t, "guidance_initialized") != 0 {
				t.Fatal("uncertain or retired initialized evidence admitted work")
			}
			b, err := c.e.s.ReadCursorRuntime(c.e.binding.InstanceID)
			if err != nil || b.SessionID != "" {
				t.Fatalf("late publication: %+v %v", b, err)
			}
		})
	}
}
