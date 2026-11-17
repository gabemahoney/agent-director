//go:build linux

package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"golang.org/x/sys/unix"
)

// TR2: real sender deadlines and pipe cleanup, independently of the injected
// classification table. An isolated subreaper owns the orphaned pipe holder.
func TestResumeDelegationRealTransport(t *testing.T) {
	// Serial: re-exec isolation for subreaper state, scratch HOME and trail.
	if os.Getenv("AD_DJ_TRANSPORT_HARNESS") != "1" {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, self, "-test.run=^TestResumeDelegationRealTransport$", "-test.v")
		cmd.Env = append(os.Environ(), "AD_DJ_TRANSPORT_HARNESS=1", "GORACE=atexit_sleep_ms=0")
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		t.Logf("isolated real-sender cases:\n%s", out)
		if err != nil {
			t.Fatalf("real-sender qualification: %v", err)
		}
		return
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"resume-stalled", "resume-empty-pipes", "resume-valid-pipes"} {
		t.Run(kind, func(t *testing.T) {
			e := newDelegateEnv(t)
			cfgPath := filepath.Join(filepath.Dir(e.dbPath), "transport.toml")
			if err := os.WriteFile(cfgPath, []byte("[tmux]\npipe_close_wait_ms = 80\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := e.c.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			e.c, err = api.New(api.Options{StorePath: e.dbPath, ConfigPath: cfgPath, TmuxClient: e.rec})
			if err != nil {
				t.Fatal(err)
			}
			// Short socket path, outside HOME so control traffic is not a trust mutation.
			dir, err := os.MkdirTemp("", "resume-transport-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			path := filepath.Join(dir, "control")
			listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			script := fmt.Sprintf("#!/bin/sh\n[ \"$1\" = %s ] || exit 90\nAD_DJ_HOLDER_KIND=%s AD_DJ_HOLDER_PATH=%s GORACE=atexit_sleep_ms=0 exec %s -test.run='^TestBusySendResourceHolderProcess$'\n",
				quote(resumedelegate.Argument), quote(kind), quote(path), quote(self))
			e.ready(t, "real-sender", 2, []string{"claude", "codex"}, "", true, []byte(script))
			e.ready(t, "unused-fallback", 1, []string{"claude", "codex"}, "", true)
			check := e.untouched(t)
			budget := 10 * time.Second // pipe cleanup must finish far before this deadline
			if kind == "resume-stalled" {
				budget = 2 * time.Second
			}
			deadline := time.Now().Add(budget)
			resume := api.CapturedResumeDelegateForTest(e.c, deadline) // nil runner: production transport
			type outcome struct {
				result api.ResumeResult
				err    error
			}
			done := make(chan outcome, 1)
			var delegate, holder *net.UnixConn
			holderPID, holderFD, returned := 0, -1, false
			defer func() {
				// Release only our capability-held connections. The sender reaps its
				// child; this isolated subreaper waits for the identified descendant.
				for _, conn := range []*net.UnixConn{delegate, holder} {
					if conn != nil {
						_, _ = conn.Write([]byte{'r'})
						_ = conn.Close()
					}
				}
				if !returned {
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("sender failed cleanup after causal release")
					}
				}
				if holderPID != 0 {
					var status unix.WaitStatus
					reaped := make(chan error, 1)
					go func() { _, err := unix.Wait4(holderPID, &status, 0, nil); reaped <- err }()
					select {
					case err := <-reaped:
						if err != nil || !status.Exited() || status.ExitStatus() != 0 {
							t.Errorf("owned pipe holder: %v / %v", status, err)
						}
					case <-time.After(3 * time.Second):
						// A pidfd cannot signal a recycled PID if Wait4 just completed.
						if holderFD >= 0 {
							_ = unix.PidfdSendSignal(holderFD, unix.SIGKILL, nil, 0)
						}
						<-reaped
						t.Error("owned pipe holder ignored causal release")
					}
					if holderFD >= 0 {
						_ = unix.Close(holderFD)
					}
				}
			}()
			go func() {
				result, err := resume(api.ResumeParams{InstanceID: e.row.ID}, "codex")
				done <- outcome{result, err}
			}()
			accept := func() (*net.UnixConn, resumeTransportEvent) {
				t.Helper()
				_ = listener.SetDeadline(time.Now().Add(4 * time.Second))
				conn, err := listener.AcceptUnix()
				if err != nil {
					t.Fatal(err)
				}
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				var event resumeTransportEvent
				if err := json.NewDecoder(conn).Decode(&event); err != nil {
					_ = conn.Close()
					t.Fatal(err)
				}
				return conn, event
			}
			var received resumeTransportEvent
			delegate, received = accept()
			if received.Kind != kind || !received.Request.Deadline.Equal(deadline) || !time.Now().Before(deadline) {
				t.Fatalf("child did not receive the remaining original allowance: %+v; want %v", received, deadline)
			}
			limit := time.Until(deadline) + time.Second
			if kind != "resume-stalled" {
				if _, err := delegate.Write([]byte{'g'}); err != nil {
					t.Fatal(err)
				}
				var held resumeTransportEvent
				holder, held = accept()
				holderPID = held.PID
				if held.Kind != "resume-pipe-holder" || held.ParentPID != received.PID {
					t.Fatalf("not the delegate's descendant: %+v / %+v", held, received)
				}
				holderFD, err = unix.PidfdOpen(holderPID, 0)
				if err != nil {
					t.Fatal(err)
				}
				// The child's exit path closes this socket after the descendant's
				// readiness acknowledgement. Its inherited stdout/stderr stay open.
				if _, err := io.ReadAll(delegate); err != nil {
					t.Fatalf("delegate did not exit: %v", err)
				}
				limit = time.Second // configured 80ms wait, not the remaining 10s budget
			}
			var got outcome
			select {
			case got = <-done:
				returned = true
			case <-time.After(limit):
				t.Fatal("real sender exceeded its original deadline or pipe-close allowance")
			}
			if kind == "resume-stalled" && time.Now().Before(deadline) {
				t.Fatal("stalled child returned before its original deadline")
			}
			if kind == "resume-valid-pipes" {
				if got.err != nil || got.result.InstanceID != e.row.ID || got.result.PreTrust != "skipped" {
					t.Fatalf("valid response lost during pipe cleanup: %+v / %v", got.result, got.err)
				}
			} else {
				assertOneName(t, got.err, "ErrInternal")
				if got.err.Error() != "delegated resume outcome is unknown; a Launch may have been created; check the agent with get before taking further action" || api.ErrDetails(got.err) != nil {
					t.Fatalf("not plain unknown outcome: %v / %+v", got.err, api.ErrDetails(got.err))
				}
			}
			if holder != nil {
				// Prove the call returned while its descendant still held the pipes.
				if _, err := holder.Write([]byte{'p'}); err != nil {
					t.Fatal(err)
				}
				var answer [1]byte
				if _, err := io.ReadFull(holder, answer[:]); err != nil || answer[0] != 'h' {
					t.Fatalf("pipe holder already gone: %q / %v", answer, err)
				}
			}
			if b, err := os.ReadFile(filepath.Join(dir, "invocations")); err != nil || string(b) != "delegate\n" {
				t.Fatalf("sender retried: %q / %v", b, err)
			}
			check()
		})
	}
}

func TestBusySendOldRuntimeAndHostFault(t *testing.T) {
	// Serial: separately compiled runtime; fixture processes and real filesystem
	// observations. One build is reused for both requirements (SR-7.2/T7).
	bin := buildBusyOldRuntime(t)
	t.Run("OldVersionWakeOnlyUnknownProvider", func(t *testing.T) {
		for _, force := range []bool{false, true} {
			t.Run(map[bool]string{false: "ordinary", true: "force-notification"}[force], func(t *testing.T) {
				e := newKillEnv(t)
				e.rowAgent = "codex"
				r := e.seedRow(t, killRowSpec{})
				seedBusyCovered(t, e, r.ID)
				wake, touched := busyWake(t, e, r.ID)
				if err := apitest.SeedBusySendRoute(e.dbPath, r.ID, "codex_app_server", wake); err != nil {
					t.Fatal(err)
				}
				home, trap, marker := busyRuntimeHome(t)
				before := delegateFiles(t, home)
				args := []string{"send-keys", "--instance-id", r.ID, "--text", "from old runtime", "--confirmed-delivery", "--caller-uuid", busyCaller, "--delivery-request-id", "old-request"}
				var ended chan error
				if force {
					args = append(args, "--force")
					// End only this fixture Launch after observing the real content-free
					// wake. This causal event ends force without waiting out its 12s budget.
					fd, err := unix.InotifyInit1(unix.IN_CLOEXEC)
					if err != nil {
						t.Fatal(err)
					}
					defer unix.Close(fd)
					if _, err := unix.InotifyAddWatch(fd, wake, unix.IN_ATTRIB); err != nil {
						t.Fatal(err)
					}
					ended = make(chan error, 1)
					go func() {
						fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
						if n, err := unix.Poll(fds, 3000); err != nil || n == 0 {
							if err == nil {
								err = os.ErrDeadlineExceeded
							}
							ended <- err
							return
						}
						_, err := e.st.EndBusySendLaunch(r.ID, r.Token)
						ended <- err
					}()
				}
				reply := runBusyRuntime(t, bin, home, e.dbPath, trap, args...)
				if ended != nil {
					if err := <-ended; err != nil {
						t.Fatalf("force wake/Launch End barrier: %v", err)
					}
				}
				// The covered wire value is DeliveryResult itself, not the legacy wrapper.
				var wire api.SendKeysResult
				if reply.code != 0 || json.Unmarshal([]byte(reply.stdout), &wire) != nil || wire.Delivery == nil {
					t.Fatalf("old admission: %+v", reply)
				}
				result := *wire.Delivery
				if result.Wait == nil {
					t.Fatalf("no wait: %+v", result)
				}
				// The send returns its admission snapshot; the force carries the
				// later Result after our causally observed Launch End.
				want := busyHeld(r.ID, busyCaller, "old-request", "readiness_unknown", result.Wait.Since)
				if force {
					if wire.Force == nil || wire.Force.ForceID == "" || wire.Force.EndedAt == nil {
						t.Fatalf("force result %+v", wire.Force)
					}
					carried := busyHeld(r.ID, busyCaller, "old-request", "launch_ended", *wire.Force.EndedAt)
					wantForce := api.ForceResult{ForceID: wire.Force.ForceID, Outcome: "refused", Reason: "not_issued", StartedAt: wire.Force.StartedAt, EndedAt: wire.Force.EndedAt,
						CarriedPairs: []api.DeliveryPair{{CallerUUID: busyCaller, RequestID: "old-request"}}, CarriedResults: []api.DeliveryResult{carried}}
					if !reflect.DeepEqual(*wire.Force, wantForce) {
						t.Fatalf("force result %+v; want %+v", wire.Force, wantForce)
					}
				}
				if !force && wire.Force != nil {
					t.Fatalf("ordinary send created force: %+v", wire.Force)
				}
				if !reflect.DeepEqual(result, want) {
					t.Fatalf("Result %+v; want %+v", result, want)
				}
				if !touched() {
					t.Fatal("old runtime did not notify its recorded wake")
				}
				if b, err := os.ReadFile(wake); err != nil || len(b) != 0 {
					t.Fatalf("wake acquired content %q / %v", b, err)
				}
				if !reflect.DeepEqual(before, delegateFiles(t, home)) {
					t.Fatal("old runtime changed files outside the recorded wake/store/trail")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("old runtime reached provider/tmux/delegation: %v", err)
				}
				// Readback and literal repeat of this pair do not wake or admit again.
				read := runBusyRuntime(t, bin, home, e.dbPath, trap, "get-delivery", "--caller-uuid", busyCaller, "--delivery-request-id", "old-request")
				if read.code != 0 {
					t.Fatalf("readback: %+v", read)
				}
				var found api.GetDeliveryResult
				if err := json.Unmarshal([]byte(read.stdout), &found); err != nil || found.Result == nil {
					t.Fatalf("readback %s / %v", read.stdout, err)
				}
				repeatArgs := args
				if force {
					repeatArgs = args[:len(args)-1]
				}
				repeat := runBusyRuntime(t, bin, home, e.dbPath, trap, repeatArgs...)
				var repeated api.DeliveryResult
				if repeat.code != 0 || json.Unmarshal([]byte(repeat.stdout), &repeated) != nil || !reflect.DeepEqual(repeated, *found.Result) {
					t.Fatalf("repeat %s; want %+v", repeat.stdout, found.Result)
				}
				conflictArgs := append([]string{}, repeatArgs...)
				for i := range conflictArgs {
					if conflictArgs[i] == "from old runtime" {
						conflictArgs[i] = "different text"
					}
				}
				runBusyRuntime(t, bin, home, e.dbPath, trap, conflictArgs...).errorName(t, "ErrCantSend")
				if touched() {
					t.Fatal("repeat/conflict/readback notified the route")
				}
				counts := storefix.ReadBusySendCounts(t, e.dbPath)
				if counts.Messages != 1 || counts.Pairs != 1 || counts.InputAttempts != 0 {
					t.Fatalf("old runtime counts: %+v", counts)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("repeat/readback reached provider/tmux/delegation: %v", err)
				}
			})
		}
	})
	t.Run("HostStalledSQLiteWriterAndRetry", func(t *testing.T) {
		e := newKillEnv(t)
		r := e.seedRow(t, killRowSpec{})
		seedBusyCovered(t, e, r.ID)
		_, touched := busyWake(t, e, r.ID)
		home, trap, marker := busyRuntimeHome(t)
		permissionID, err := apitest.SeedSpawn(e.dbPath, "", store.StateCheckPermission, t.TempDir(), "on", "", false)
		if err != nil {
			t.Fatal(err)
		}
		req, err := apitest.SeedPermissionRequest(e.dbPath, permissionID, "Bash")
		if err != nil {
			t.Fatal(err)
		}
		before := busyFacts(t, e.dbPath, r.ID)
		beforeCounts := storefix.ReadBusySendCounts(t, e.dbPath)
		permission, err := e.st.GetPermissionRequest(permissionID, req.RequestToken)
		if err != nil {
			t.Fatal(err)
		}
		release := holdBusyResource(t, "sqlite", e.dbPath)
		decide := []string{"decide", "--instance-id", permissionID, "--request-token", req.RequestToken, "--decision", "allow", "--max-wait-ms", "0"}
		send := []string{"send-keys", "--instance-id", r.ID, "--text", "same pair retry", "--confirmed-delivery", "--caller-uuid", busyCaller, "--delivery-request-id", "host-stall"}
		for _, args := range [][]string{decide, send} {
			reply := runBusyRuntime(t, bin, home, e.dbPath, trap, args...)
			reply.errorName(t, "ErrStoreBusy")
			// The process startup allowance is separate from the unchanged 0/40ms
			// store waits. A renewed/default 10s wait cannot fit this bound.
			if reply.elapsed > 2*time.Second {
				t.Fatalf("%s renewed its finite allowance: %v", args[0], reply.elapsed)
			}
		}
		if after := busyFacts(t, e.dbPath, r.ID); !reflect.DeepEqual(after, before) {
			t.Fatalf("busy refusal admitted/defaulted: %+v -> %+v", before, after)
		}
		if after := storefix.ReadBusySendCounts(t, e.dbPath); after != beforeCounts {
			t.Fatalf("busy refusal claimed a pair/reservation/input: %+v", after)
		}
		if after, err := e.st.GetPermissionRequest(permissionID, req.RequestToken); err != nil || !reflect.DeepEqual(after, permission) {
			t.Fatalf("busy refusal changed verdict: %+v / %v", after, err)
		}
		if touched() {
			t.Fatal("busy refusal touched wake")
		}
		release() // resume and release only the positively owned holder
		// Caller contract: "Retry only as a new, deliberate call with a fresh
		// budget." A confirmed send reuses the identical Caller UUID/Message ID.
		accepted := runBusyRuntime(t, bin, home, e.dbPath, trap, send...)
		if accepted.code != 0 || !touched() {
			t.Fatalf("same-pair retry: %+v", accepted)
		}
		repeat := runBusyRuntime(t, bin, home, e.dbPath, trap, send...)
		if repeat.code != 0 || repeat.stdout != accepted.stdout || touched() {
			t.Fatalf("repeat accepted twice: first %+v, repeat %+v", accepted, repeat)
		}
		verdict := runBusyRuntime(t, bin, home, e.dbPath, trap, decide...)
		if verdict.code != 0 {
			t.Fatalf("literal decide retry: %+v", verdict)
		}
		if row, err := e.st.GetPermissionRequest(permissionID, req.RequestToken); err != nil || row.Decision != "allow" {
			t.Fatalf("verdict %+v / %v", row, err)
		}
		if n := storefix.ReadBusySendCounts(t, e.dbPath); n.Messages != 1 || n.Pairs != 1 || n.InputAttempts != 0 {
			t.Fatalf("retry counts %+v", n)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("input during lock/retry: %v", err)
		}
		// No advice is synthesized into a transport error: the exact published
		// retry instruction above is executable through the same CLI requests.
		doc, err := os.ReadFile("../../docs/caller-contract.md")
		if err != nil || !strings.Contains(strings.Join(strings.Fields(string(doc)), " "), "Retry only as a new, deliberate call with a fresh budget.") {
			t.Fatalf("retry advice changed: %v", err)
		}
	})
}
