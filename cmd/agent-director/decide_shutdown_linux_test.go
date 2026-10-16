//go:build linux

package main_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"golang.org/x/sys/unix"
)

func TestDecideProcessShutdownBoundsBlockedAudit(t *testing.T) {
	home := t.TempDir()
	id, err := apitest.SeedSpawn(stateDB(home), "", store.StateCheckPermission, "", "on", "", true)
	if err != nil {
		t.Fatal(err)
	}
	req, err := apitest.SeedPermissionRequest(stateDB(home), id, "Bash")
	if err != nil {
		t.Fatal(err)
	}
	apitest.HoldWriteLock(t, stateDB(home), time.Minute)
	path := filepath.Join(directorDir(home), "ad-trail.jsonl")
	if err = unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	// A real full kernel pipe blocks the child's writer without blocking its open.
	padding := bytes.Repeat([]byte("x"), 4096)
	filled := 0
	for {
		n, e := unix.Write(fd, padding)
		if n > 0 {
			filled += n
		}
		if errors.Is(e, unix.EAGAIN) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	start := time.Now()
	stdout, stderr, code, timedOut := runBounded(t, home, nil, "", false, 300*time.Millisecond+time.Second+cliStartAllowance, append(decideArgv(id, req.RequestToken), "--max-wait-ms", "300")...)
	if timedOut {
		t.Fatalf("child failed finite shutdown after %v: %s", time.Since(start), stderr)
	}
	var envelope struct {
		ErrName string `json:"err_name"`
	}
	if code != 1 || stdout != "" || json.Unmarshal([]byte(strings.Split(stderr, "\n")[0]), &envelope) != nil || envelope.ErrName != "ErrStoreBusy" {
		t.Fatalf("audit changed verdict: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "agent-director: audit shutdown:") || !strings.Contains(stderr, "pending") || !strings.Contains(stderr, "context deadline exceeded") {
		t.Errorf("missing truthful pending shutdown diagnostic: %q", stderr)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond+500*time.Millisecond || elapsed > 300*time.Millisecond+time.Second+cliStartAllowance {
		t.Errorf("child drain elapsed=%v; want fixed 500ms teardown within original CLI allowance", elapsed)
	}
	pr, err := openDBForRead(t, home).GetPermissionRequest(id, req.RequestToken)
	if err != nil || pr.Decision != "" || pr.AttemptedDecision != "" {
		t.Errorf("blocked audit changed durable verdict: %+v %v", pr, err)
	}
	// Drain only after the child exits: it cannot fabricate or duplicate an audit.
	var drained []byte
	for {
		n, e := unix.Read(fd, padding)
		if n > 0 {
			drained = append(drained, padding[:n]...)
		}
		if errors.Is(e, unix.EAGAIN) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
	}
	if len(drained) != filled || bytes.Count(drained, []byte("ad.decide.called")) != 0 || bytes.Count(drained, []byte("ad.row_mutation.committed")) != 0 {
		t.Errorf("blocked process claimed a durable or duplicate audit: filled=%d drained=%d", filled, len(drained))
	}
}
