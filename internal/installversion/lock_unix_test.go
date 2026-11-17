//go:build linux || darwin

package installversion

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicationAdministratorLock(t *testing.T) {
	for _, hold := range []string{"after:lock", "before:bootstrap.runtime-entry.rename", "before:bootstrap.completion.rename"} {
		t.Run(hold, func(t *testing.T) {
			f := newPublicationFixture(t)
			if hold == "after:lock" {
				publishOK(t, f.root, f.a)
			}
			cmd := childCommand(t, childRequest{Root: f.root, Candidate: f.b, HoldPoint: hold})
			in, err := cmd.StdinPipe()
			must(t, err)
			t.Cleanup(func() { _ = in.Close() })
			out, err := cmd.StdoutPipe()
			must(t, err)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			must(t, cmd.Start())
			waited := false
			t.Cleanup(func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			line, err := bufio.NewReader(out).ReadString('\n')
			must(t, err)
			if line != "held\n" {
				t.Fatalf("holder barrier %q, %s", line, stderr.String())
			}
			lockPath := filepath.Join(f.root, "install.lock")
			inode, err := os.Stat(lockPath)
			must(t, err)
			before := snapshot(t, f.root)
			// The holder is a different process. A deadline only guards a
			// blocking flock regression; no timing grace or sleep releases it.
			done := make(chan error, 1)
			go func() { _, err := publishCandidate(f.root, f.a, Hooks{}); done <- err }()
			select {
			case err := <-done:
				expectRefusal(t, err, "lock_busy")
			case <-time.After(5 * time.Second):
				t.Fatal("second administrator waited for live holder")
			}
			readDone := make(chan error, 1)
			go func() { _, err := ListInstalled(f.root); readDone <- err }()
			select {
			case err := <-readDone:
				must(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("reader waited for administrator lock")
			}
			assertSame(t, snapshot(t, f.root), before)
			must(t, cmd.Process.Kill())
			_ = cmd.Wait()
			waited = true
			publishOK(t, f.root, f.b)
			after, err := os.Stat(lockPath)
			must(t, err)
			if !os.SameFile(inode, after) {
				t.Fatal("lock inode replaced across refusal/death/recovery")
			}
			assertEstablished(t, f.root, "new")
		})
	}
}
