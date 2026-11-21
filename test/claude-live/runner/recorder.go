package runner

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Receipt struct {
	Schema   int       `json:"schema"`
	RunID    string    `json:"run_id"`
	Event    string    `json:"event"`
	Received time.Time `json:"received"`
	Finished time.Time `json:"finished"`
	Input    string    `json:"input"`
	SHA256   string    `json:"sha256"`
	Bytes    int       `json:"bytes"`
	Complete bool      `json:"complete"`
}

// RecordHook never emits output or an exit decision. The CLI supplies a hard
// process deadline too: even a blocked filesystem operation must not hold a
// provider hook open. Receipt times are NOT AD output completion times.
func RecordHook(root, runID, event string, input io.Reader) {
	RecordOwnedHook(root, runID, event, input, os.Getenv("AGENT_DIRECTOR_INSTANCE_ID"))
}

// RecordOwnedHook shares the passive sink with identity-bound direct native probes.
func RecordOwnedHook(root, runID, event string, input io.Reader, instance string) {
	defer func() { _ = recover() }()
	if runID == "" || event == "" || !strings.HasPrefix(instance, "live-"+runID+"-") {
		return
	}
	p, err := Confined(root, "raw/hooks")
	if err != nil {
		return
	}
	dir, err := os.MkdirTemp(p, "invocation-")
	if err != nil {
		return
	}
	r := Receipt{Schema: Schema, RunID: runID, Event: event, Received: time.Now().UTC(), Input: "input.json"}
	b, readErr := io.ReadAll(io.LimitReader(input, 1<<20+1))
	if len(b) > 1<<20 {
		b = b[:1<<20]
		readErr = io.ErrShortBuffer
	}
	err = os.WriteFile(filepath.Join(dir, r.Input), b, 0600)
	r.Bytes, r.SHA256, r.Complete = len(b), Digest(b), readErr == nil && err == nil
	r.Finished = time.Now().UTC()
	meta, err := json.Marshal(r)
	if err != nil {
		return
	}
	temporary := filepath.Join(dir, "receipt.tmp")
	if err = os.WriteFile(temporary, append(meta, '\n'), 0600); err != nil {
		return
	}
	_ = os.Rename(temporary, filepath.Join(dir, "receipt.json"))
}
