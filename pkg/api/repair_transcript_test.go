package api_test

// repair_transcript_test.go — b.v2c AC7 coverage for the repair-transcript verb:
// the one-shot operator recovery path that re-associates an orphaned transcript
// with a row. Drives the unexported impl (exported as api.RepairTranscript) with
// a fake store so the file-existence guard, flag validation, and store
// delegation are all pinned without a real DB.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api"
)

// fakeRepairStore records the RepairTranscript call and can inject an error.
type fakeRepairStore struct {
	err        error
	calledWith [3]string
	calls      int
}

func (f *fakeRepairStore) RepairTranscript(instanceID, sessionID, jsonlPath string) error {
	f.calls++
	f.calledWith = [3]string{instanceID, sessionID, jsonlPath}
	return f.err
}

// TestRepairTranscriptHappyPath is the AC7 REGRESSION test: given an existing
// transcript on disk, the verb verifies it, delegates to the store with the
// exact (instance, session, path) triple, and echoes them back.
func TestRepairTranscriptHappyPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovered.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	s := &fakeRepairStore{}

	got, err := api.RepairTranscript(s, api.RepairTranscriptParams{
		ClaudeInstanceID: "id-1", ClaudeSessionID: "sess-1", JSONLPath: path,
	})
	if err != nil {
		t.Fatalf("RepairTranscript: %v", err)
	}
	if s.calls != 1 || s.calledWith != [3]string{"id-1", "sess-1", path} {
		t.Errorf("store called with %v (%d times); want [id-1 sess-1 %s] once", s.calledWith, s.calls, path)
	}
	if got.ClaudeInstanceID != "id-1" || got.ClaudeSessionID != "sess-1" || got.JSONLPath != path {
		t.Errorf("result = %+v; want the repaired triple echoed", got)
	}
}

// TestRepairTranscriptMissingFileRefuses pins that a jsonl_path that does not
// exist on disk is rejected with ErrRepairTranscriptMissing BEFORE the store is
// touched — re-associating a row with an absent path would just recreate the
// b.v2c dead-pointer bug.
func TestRepairTranscriptMissingFileRefuses(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	s := &fakeRepairStore{}

	_, err := api.RepairTranscript(s, api.RepairTranscriptParams{
		ClaudeInstanceID: "id-1", ClaudeSessionID: "sess-1", JSONLPath: absent,
	})
	if !errors.Is(err, api.ErrRepairTranscriptMissing) {
		t.Fatalf("err = %v; want ErrRepairTranscriptMissing", err)
	}
	if s.calls != 0 {
		t.Errorf("store called %d times on a missing transcript; want 0 (refuse before delegating)", s.calls)
	}
}

// TestRepairTranscriptValidatesRequiredFlags pins the ErrInvalidFlags guards for
// each empty required parameter.
func TestRepairTranscriptValidatesRequiredFlags(t *testing.T) {
	present := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(present, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cases := []struct {
		name   string
		params api.RepairTranscriptParams
	}{
		{"missing instance", api.RepairTranscriptParams{ClaudeSessionID: "s", JSONLPath: present}},
		{"missing session", api.RepairTranscriptParams{ClaudeInstanceID: "i", JSONLPath: present}},
		{"missing path", api.RepairTranscriptParams{ClaudeInstanceID: "i", ClaudeSessionID: "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &fakeRepairStore{}
			_, err := api.RepairTranscript(s, tc.params)
			if !errors.Is(err, api.ErrInvalidFlags) {
				t.Fatalf("err = %v; want ErrInvalidFlags", err)
			}
			if s.calls != 0 {
				t.Errorf("store called %d times on invalid flags; want 0", s.calls)
			}
		})
	}
}

// TestRepairTranscriptPropagatesStoreError pins that a store-side error (e.g.
// ErrSpawnNotFound for an absent instance) propagates unchanged.
func TestRepairTranscriptPropagatesStoreError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	sentinel := errors.New("ErrSpawnNotFound")
	s := &fakeRepairStore{err: sentinel}

	_, err := api.RepairTranscript(s, api.RepairTranscriptParams{
		ClaudeInstanceID: "ghost", ClaudeSessionID: "s", JSONLPath: path,
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v; want the store error propagated", err)
	}
}
