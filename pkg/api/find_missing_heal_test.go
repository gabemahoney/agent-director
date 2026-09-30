package api_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gabemahoney/agent-director/internal/spawn"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procfix"
)

// find-missing's provisional-transcript healing (b.v2c AC3); fakes and runFindMissing live in find_missing_test.go.
// TestFindMissingHealsProvisionalTranscript: a provisional row (NULL jsonl_path) whose transcript has since
// appeared is healed with the recomposed path (b.v2c AC3/AC4).
func TestFindMissingHealsProvisionalTranscript(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	const (
		id      = "prov-heal-1"
		session = "session-heal-uuid"
		cwd     = "/tmp/proj"
	)
	appeared, err := spawn.JsonlPath(cwd, session)
	if err != nil {
		t.Fatalf("compute path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(appeared), 0o700); err != nil {
		t.Fatalf("mkdir transcript parent: %v", err)
	}
	if err := os.WriteFile(appeared, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	pc := procfix.New()
	pc.Set(11, procfix.Alive(fmStart))
	st := &fakeFindMissingStore{
		rows:        []store.LiveSpawnIdentity{liveRow(id, withSessionStart(11, fmStart))},
		provisional: []store.ProvisionalTranscript{{ClaudeInstanceID: id, ClaudeSessionID: session, CWD: cwd}},
	}

	mustFindMissing(t, st, pc)
	want := []healCall{{id, session, appeared}}
	if !slices.Equal(st.healed, want) {
		t.Errorf("heal calls = %+v; want %+v", st.healed, want)
	}
}

// TestFindMissingSkipsProvisionalWhenTranscriptStillAbsent: a provisional row whose transcript has not appeared
// stays provisional.
func TestFindMissingSkipsProvisionalWhenTranscriptStillAbsent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := &fakeFindMissingStore{provisional: []store.ProvisionalTranscript{
		{ClaudeInstanceID: "prov-absent-1", ClaudeSessionID: "no-file-uuid", CWD: "/tmp/proj"},
	}}

	mustFindMissing(t, st, procfix.New())
	if len(st.healed) != 0 {
		t.Errorf("HealJsonlPath called %d times; want 0 (transcript still absent)", len(st.healed))
	}
}
