//go:build linux

package cursorhost

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

func TestTranscriptResumePreservesPrefixAndContinuesSequence(t *testing.T) {
	tr := newTestTranscript(t)
	if err := tr.record("turn_outcome", map[string]any{"stopReason": "end_turn"}); err != nil {
		t.Fatal(err)
	}
	prefix, err := os.ReadFile(tr.path)
	if err != nil {
		t.Fatal(err)
	}
	b := tr.binding
	b.Token = "resumed-token"
	b.Connection = "resumed-connection"
	resumed, err := appendTranscript(tr.path, b)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.file.Close()
	if err := resumed.record("session_loaded", map[string]any{"origin": "main"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(tr.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, prefix) {
		t.Fatal("resume rewrote historical prefix")
	}
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte("\n")), []byte("\n"))
	for i, line := range lines[2:] {
		var r submissionRecord
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		if r.Sequence != uint64(i+3) || r.Connection != b.Connection || r.Token != b.Token || r.Session != b.SessionID || r.Instance != b.InstanceID || r.Life != b.Life {
			t.Fatalf("continued attribution: %s", line)
		}
	}
}

func TestTranscriptResumeRefusesBrokenContinuityWithoutCreation(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "truncated", "malformed", "wrong instance", "wrong life", "overflow", "zero sequence", "symlink replacement"} {
		t.Run(kind, func(t *testing.T) {
			tr := newTestTranscript(t)
			b := tr.binding
			original, err := os.ReadFile(tr.path)
			if err != nil {
				t.Fatal(err)
			}
			raw := original
			switch kind {
			case "missing":
				if err := os.Remove(tr.path); err != nil {
					t.Fatal(err)
				}
			case "empty":
				raw = nil
			case "truncated":
				raw = original[:len(original)-1]
			case "malformed":
				raw = []byte("not-json\n")
			case "wrong instance":
				b.InstanceID = "foreign"
			case "wrong life":
				b.Life++
			case "overflow":
				raw = bytes.Replace(original, []byte(`"sequence":1`), []byte(`"sequence":18446744073709551615`), 1)
			case "zero sequence":
				raw = bytes.Replace(original, []byte(`"sequence":1`), []byte(`"sequence":0`), 1)
			case "symlink replacement":
				target := tr.path + ".preserved"
				if err := os.Rename(tr.path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, tr.path); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "missing" && kind != "symlink replacement" {
				if err := os.WriteFile(tr.path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := appendTranscript(tr.path, b)
			if err == nil {
				got.file.Close()
				t.Fatal("invalid continuation accepted")
			}
			if kind == "missing" {
				if _, err := os.Lstat(tr.path); !os.IsNotExist(err) {
					t.Fatal("missing transcript recreated")
				}
				return
			}
			after, err := os.ReadFile(tr.path)
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatalf("refusal altered artifact: %q %v", after, err)
			}
		})
	}
}
