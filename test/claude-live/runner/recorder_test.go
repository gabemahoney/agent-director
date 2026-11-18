package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestClaudeLiveRecorderConcurrentPrivateExactInputs(t *testing.T) {
	r, _ := runnerFixture(t)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "live-"+r.Manifest.RunID+"-owned")
	dir := filepath.Join(r.Evidence.Root, "raw/hooks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	fixtures := map[string]string{"SessionStart": "session-start-startup", "FileChanged": "file-changed", "PostToolBatch": "post-tool-batch", "UserPromptSubmit": "user-prompt-submit", "Stop": "stop"}
	inputs := map[string][]byte{}
	for event, name := range fixtures {
		// Structural recorder inputs, not live qualification: FileChanged and
		// PostToolBatch are synthetic in the shared fixture catalogue.
		inputs[event] = readFixture(t, filepath.Join("../../../internal/hook/testdata/hook-payloads", name+".json"))
	}
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	for event, b := range inputs {
		for i := 0; i < 2; i++ {
			ready.Add(1)
			done.Add(1)
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				RecordHook(r.Evidence.Root, r.Manifest.RunID, event, bytes.NewReader(b))
			}()
		}
	}
	ready.Wait()
	close(start)
	done.Wait()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2*len(inputs) {
		t.Fatalf("invocations = %d, %v", len(entries), err)
	}
	counts := map[string]int{}
	for _, entry := range entries {
		base := filepath.Join(dir, entry.Name())
		receipt := readJSON[Receipt](t, filepath.Join(base, "receipt.json"))
		b := readFixture(t, filepath.Join(base, receipt.Input))
		if !bytes.Equal(b, inputs[receipt.Event]) || !receipt.Complete || receipt.RunID != r.Manifest.RunID || receipt.Schema != Schema || receipt.Bytes != len(b) || receipt.SHA256 != Digest(b) || receipt.Finished.Before(receipt.Received) || receipt.Received.IsZero() {
			t.Fatalf("invalid invocation receipt: %+v", receipt)
		}
		counts[receipt.Event]++
		for _, name := range []string{base, filepath.Join(base, "input.json"), filepath.Join(base, "receipt.json")} {
			st, err := os.Stat(name)
			if err != nil || st.Mode().Perm()&0077 != 0 {
				t.Fatalf("record not private: %s %v", name, err)
			}
		}
	}
	for event := range inputs {
		if counts[event] != 2 {
			t.Fatalf("lost/duplicated %s: %v", event, counts)
		}
	}
	var events []string
	for event := range inputs {
		events = append(events, event)
	}
	inventory, err := InspectRecordings(r.Evidence.Root, r.Manifest.RunID, events)
	if err != nil || len(inventory) != len(entries) {
		t.Fatalf("complete invocation inventory: %+v %v", inventory, err)
	}
	for _, invocation := range inventory {
		if !invocation.Complete || invocation.Reason != "" {
			t.Fatalf("valid recording refused: %+v", invocation)
		}
	}
}

type brokenHookReader struct{ panicRead bool }

func (r brokenHookReader) Read([]byte) (int, error) {
	if r.panicRead {
		panic("synthetic reader panic")
	}
	return 0, errors.New("synthetic input failure")
}

func TestClaudeLiveRecorderFailureIsSilent(t *testing.T) {
	r, _ := runnerFixture(t)
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "live-"+r.Manifest.RunID+"-owned")
	// Redirect only these two process globals; this test is deliberately serial.
	out, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	errout, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = out, errout
	t.Cleanup(func() { os.Stdout, os.Stderr = oldOut, oldErr; _ = out.Close(); _ = errout.Close() })
	RecordHook(r.Evidence.Root, r.Manifest.RunID, "Stop", bytes.NewReader([]byte("missing hook directory")))
	if err := os.MkdirAll(filepath.Join(r.Evidence.Root, "raw/hooks"), 0700); err != nil {
		t.Fatal(err)
	}
	RecordHook(r.Evidence.Root, r.Manifest.RunID, "Stop", brokenHookReader{})
	RecordHook(r.Evidence.Root, r.Manifest.RunID, "Stop", brokenHookReader{panicRead: true})
	entries, err := os.ReadDir(filepath.Join(r.Evidence.Root, "raw/hooks"))
	if err != nil || len(entries) != 2 {
		t.Fatalf("failed invocation records: %d, %v", len(entries), err)
	}
	complete := 0
	for _, e := range entries {
		_, err := os.Stat(filepath.Join(r.Evidence.Root, "raw/hooks", e.Name(), "receipt.json"))
		if os.IsNotExist(err) {
			continue // Panic left an incomplete invocation, never a success receipt.
		}
		if err != nil {
			t.Fatal(err)
		}
		receipt := readJSON[Receipt](t, filepath.Join(r.Evidence.Root, "raw/hooks", e.Name(), "receipt.json"))
		if receipt.Complete {
			complete++
		}
	}
	if complete != 0 || len(readFixture(t, out.Name())) != 0 || len(readFixture(t, errout.Name())) != 0 {
		t.Fatal("recorder emitted output or certified incomplete input")
	}
	t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "unrelated")
	RecordHook(r.Evidence.Root, r.Manifest.RunID, "Stop", io.LimitReader(bytes.NewReader([]byte("foreign")), 7))
	after, err := os.ReadDir(filepath.Join(r.Evidence.Root, "raw/hooks"))
	if err != nil || len(after) != len(entries) {
		t.Fatal("unrelated hook was recorded")
	}
}

func TestClaudeLiveRecordingInventoryRetainsIncompleteInvocations(t *testing.T) {
	for _, failure := range []string{"complete", "missing-directory", "empty", "input-only", "receipt-only", "extra-file", "foreign-run", "unregistered-event", "digest", "size", "incomplete", "event-mismatch", "symlink", "unexpected-entry"} {
		t.Run(failure, func(t *testing.T) {
			r, _ := runnerFixture(t)
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "live-"+r.Manifest.RunID+"-owned")
			hooks := filepath.Join(r.Evidence.Root, "raw/hooks")
			if err := os.MkdirAll(hooks, 0700); err != nil {
				t.Fatal(err)
			}
			input := readFixture(t, "../../../internal/hook/testdata/hook-payloads/stop.json")
			RecordHook(r.Evidence.Root, r.Manifest.RunID, "Stop", bytes.NewReader(input))
			entries, err := os.ReadDir(hooks)
			if err != nil || len(entries) != 1 {
				t.Fatalf("recorder setup: %d %v", len(entries), err)
			}
			dir := filepath.Join(hooks, entries[0].Name())
			receiptPath := filepath.Join(dir, "receipt.json")
			receipt := readJSON[Receipt](t, receiptPath)
			remove := func(path string) {
				t.Helper()
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			switch failure {
			case "missing-directory":
				if err := os.RemoveAll(hooks); err != nil {
					t.Fatal(err)
				}
			case "empty":
				remove(receiptPath)
				remove(filepath.Join(dir, "input.json"))
			case "input-only":
				remove(receiptPath)
			case "receipt-only":
				remove(filepath.Join(dir, "input.json"))
			case "extra-file":
				putFixture(t, filepath.Join(dir, "extra"), []byte("unrecorded"))
			case "symlink":
				target := filepath.Join(t.TempDir(), "saved-input")
				putFixture(t, target, input)
				remove(filepath.Join(dir, "input.json"))
				if err := os.Symlink(target, filepath.Join(dir, "input.json")); err != nil {
					t.Fatal(err)
				}
			case "unexpected-entry":
				remove(receiptPath)
				remove(filepath.Join(dir, "input.json"))
				remove(dir)
				putFixture(t, dir, []byte("not an invocation directory"))
			default:
				switch failure {
				case "foreign-run":
					receipt.RunID = "another-run"
				case "unregistered-event":
					receipt.Event = "FileChanged"
				case "digest":
					receipt.SHA256 = Digest([]byte("different"))
				case "size":
					receipt.Bytes++
				case "incomplete":
					receipt.Complete = false
				case "event-mismatch":
					input = readFixture(t, "../../../internal/hook/testdata/hook-payloads/user-prompt-submit.json")
					putFixture(t, filepath.Join(dir, "input.json"), input)
					receipt.SHA256, receipt.Bytes = Digest(input), len(input)
				}
				encoded, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				putFixture(t, receiptPath, encoded)
			}
			r.Manifest.RecordingHook = RecordingHook{Installed: true, Events: []string{"Stop"}, Directory: "raw/hooks"}
			inventory, inspectErr := InspectRecordings(r.Evidence.Root, r.Manifest.RunID, r.Manifest.RecordingHook.Events)
			if failure == "missing-directory" {
				if inspectErr == nil {
					t.Fatal("missing recorder directory accepted")
				}
			} else {
				if inspectErr != nil || len(inventory) != 1 {
					t.Fatalf("invocation vanished from inventory: %+v %v", inventory, inspectErr)
				}
				if inventory[0].Directory != "raw/hooks/"+entries[0].Name() || inventory[0].Complete != (failure == "complete") || (inventory[0].Reason == "") != (failure == "complete") {
					t.Fatalf("incorrect invocation outcome: %+v", inventory[0])
				}
			}
			v, err := r.ExecutePhase(context.Background(), func(context.Context, *Run) error { return nil }, nil)
			want := "fail"
			if failure == "complete" {
				want = "pass"
			}
			if failure == "missing-directory" {
				want = "not_completed"
			}
			if err != nil || v.Status != want {
				t.Fatalf("recording %s final verdict: %+v %v; want %s", failure, v, err, want)
			}
			if inspectErr == nil {
				indexed := readJSON[[]HookInvocation](t, filepath.Join(r.Evidence.Root, "raw/hook-inventory.json"))
				if !reflect.DeepEqual(indexed, inventory) {
					t.Fatal("finalization discarded incomplete facts")
				}
				m := readJSON[Manifest](t, filepath.Join(r.Evidence.Root, "manifest.json"))
				b := readFixture(t, filepath.Join(r.Evidence.Root, "raw/hook-inventory.json"))
				if m.Artifacts["raw/hook-inventory.json"] != (Artifact{SHA256: Digest(b), Bytes: int64(len(b))}) {
					t.Fatal("hook inventory is not indexed")
				}
			}
		})
	}
}
