package store_test

// AC-RES-20 (SR-5.4, SR-22.6; PO 2026-09-26 UPG): a row from before the
// install carries the no_pre_trust column default, pre-trust allowed, so its
// resume pre-trusts the row's working directory before the move to pending.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
	"github.com/gabemahoney/agent-director/pkg/api"
)

// seedHomeClaudeJSON writes body to $HOME/.claude.json, where pre-trust looks
// for a row whose extra env sets no CLAUDE_CONFIG_DIR, and puts back whatever
// was there at cleanup. HOME is the throwaway directory this package's
// TestMain sets (never the real one), so callers must not run in parallel.
func seedHomeClaudeJSON(t *testing.T, body string) string {
	t.Helper()
	home := os.Getenv("HOME")
	if !strings.HasPrefix(filepath.Base(home), "ad-store-home-") {
		t.Fatalf("HOME = %q; want TestMain's throwaway ad-store-home-* directory", home)
	}
	p := filepath.Join(home, ".claude.json")
	prev, err := os.ReadFile(p)
	switch {
	case err == nil:
		t.Cleanup(func() { _ = os.WriteFile(p, prev, 0o600) })
	case errors.Is(err, os.ErrNotExist):
		t.Cleanup(func() { _ = os.Remove(p) })
	default:
		t.Fatalf("read %s: %v", p, err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// trustAccepted reports projects[cwd].hasTrustDialogAccepted in the .claude.json at p.
func trustAccepted(t *testing.T, p, cwd string) bool {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var doc struct {
		Projects map[string]struct {
			HasTrustDialogAccepted bool `json:"hasTrustDialogAccepted"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v\n%s", p, err, raw)
	}
	return doc.Projects[cwd].HasTrustDialogAccepted
}

// TestPreTrustMigratedV4RowResumePreTrusts: the fixture's resumable finished
// row, migrated from v4, reads back with pre-trust allowed, and its resume
// reports pre_trust ok, writes the trust entry for its cwd and moves it to
// pending.
func TestPreTrustMigratedV4RowResumePreTrusts(t *testing.T) {
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	m := newMigratedClient(t)
	id := neverWrittenRow(m.f)
	cwd := m.f.CWD[id]
	plantTranscript(t, cwd, "sess-nw-prior")

	s, err := store.Open(m.f.Path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	before, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s): %v", id, err)
	}
	if before.NoPreTrust {
		t.Fatalf("migrated row %s NoPreTrust = true; want the column default, pre-trust allowed", id)
	}
	if before.State != "ended" {
		t.Fatalf("migrated row %s state = %q; want ended (resumable)", id, before.State)
	}

	p := seedHomeClaudeJSON(t, `{"numStartups": 3, "projects": {"/elsewhere": {"hasTrustDialogAccepted": false}}}`)
	if trustAccepted(t, p, cwd) {
		t.Fatalf("seeded %s already trusts %s; the check would be vacuous", p, cwd)
	}

	res, err := m.c.Resume(api.ResumeParams{ClaudeInstanceID: id})
	if err != nil {
		t.Fatalf("Resume(%s): %v", id, err)
	}
	if res.PreTrust != "ok" {
		t.Errorf("Resume(%s) pre_trust = %q; want ok (migrated rows carry the default, pre-trust allowed)", id, res.PreTrust)
	}
	if launches := m.rec.SocketCallsOf(tmux.CallCreate); len(launches) != 1 {
		t.Fatalf("Resume(%s) made %d relaunches; want 1", id, len(launches))
	}
	if !trustAccepted(t, p, cwd) {
		t.Errorf("after Resume(%s), %s has no trust entry for %s; want pre-trust to have written it", id, p, cwd)
	}
	after, err := s.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn(%s) after resume: %v", id, err)
	}
	if after.State != "pending" {
		t.Errorf("state after resume = %q; want pending", after.State)
	}
	if after.NoPreTrust {
		t.Errorf("NoPreTrust after resume = true; want the recorded choice unchanged (allowed)")
	}
}
