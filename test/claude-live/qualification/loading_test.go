package qualification_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// Synthetic native fact, using the existing recorder format and validator.
func unitHook(t *testing.T, f *qualificationfix.Fixture, owner runner.Owned, event string, at time.Time, extra runner.Row) q.HookFact {
	t.Helper()
	payload := runner.Row{"session_id": owner.SessionID, "cwd": owner.CWD, "hook_event_name": event}
	for k, v := range extra {
		payload[k] = v
	}
	dir := "raw/hooks/invocation-unit-" + event + "-" + strconv.FormatInt(at.UnixNano(), 10)
	f.Put(dir+"/input.json", payload)
	b := f.Read(dir + "/input.json")
	rec := runner.Receipt{Schema: runner.Schema, RunID: f.Manifest.RunID, Event: event, Received: at, Finished: at.Add(time.Millisecond), Input: "input.json", SHA256: runner.Digest(b), Bytes: len(b), Complete: true}
	f.Put(dir+"/receipt.json", rec)
	return q.HookFact{Receipt: rec, Payload: payload, Reference: dir + "/input.json"}
}

func TestQualificationOwnedTranscriptAbsence(t *testing.T) {
	for _, kind := range []string{"fresh-reuse", "blocked-H1", "reuse-without-operation", "block-wrong-prompt", "misbound-absence", "missing-SessionStart"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "user-hooks")
			at := f.Manifest.Finished.Add(time.Second)
			blocked := kind == "blocked-H1" || kind == "block-wrong-prompt"
			suffix := "unit-fresh"
			if blocked {
				suffix = "H1"
			}
			o := runner.Owned{Agent: "claude", InstanceID: "live-" + f.Manifest.RunID + "-" + suffix, SessionID: "unit-absence-session", LaunchToken: "unit-absence-launch", LaunchID: "unit-launch", CWD: "/unit/absence", PID: 1, ProcessStart: "unit-process", Started: at, StoppedAt: at.Add(3 * time.Second), Exited: true}
			prefix := o.InstanceID + "-" + o.LaunchToken
			f.Put("raw/final-"+prefix+"-store.json", runner.Snapshot{InstanceID: o.InstanceID, Tables: map[string][]runner.Row{"spawns": {{"session_id": o.SessionID, "launch_token": o.LaunchToken}}}})
			f.PutBytes("raw/"+prefix+"-trail.jsonl", nil)
			f.Manifest.Owned = append(f.Manifest.Owned, o)
			f.Manifest.Finished = at.Add(4 * time.Second)
			if kind != "missing-SessionStart" {
				unitHook(t, f, o, "SessionStart", at.Add(300*time.Millisecond), nil)
			}
			if blocked {
				unitHook(t, f, o, "UserPromptSubmit", at.Add(time.Second), runner.Row{"prompt_id": "unit-prompt", "prompt": "QUALIFICATION-BLOCK-H1 unit"})
				input, output := "raw/unit-block-input.json", "raw/unit-block-output.json"
				f.Put(input, runner.Row{"session_id": o.SessionID, "cwd": o.CWD, "prompt_id": "unit-prompt"})
				f.Put(output, runner.Row{"decision": "block"})
				rec := q.UserHookReceipt{Instance: o.InstanceID, Session: o.SessionID, Prompt: "unit-prompt", Action: "block", Started: at.Add(time.Second), Written: at.Add(1100 * time.Millisecond), Input: input, Output: output}
				if kind == "block-wrong-prompt" {
					rec.Prompt = "another-prompt"
				}
				f.Put("raw/user-hook-fixtures/unit-block/receipt.json", rec)
			} else if kind != "reuse-without-operation" {
				ref := "raw/unit-fresh-reuse.json"
				f.Put(ref, runner.Row{"instance_id": o.InstanceID})
				cmd := runner.Command{Name: "unit-fresh-reuse", Stdout: ref, Argv: []string{"ad", "spawn", "--instance-id", o.InstanceID, "--reuse-finished"}, Started: at, Ended: at.Add(200 * time.Millisecond)}
				b, _ := json.Marshal(cmd)
				f.PutBytes("commands.jsonl", append(f.Read("commands.jsonl"), append(b, '\n')...))
				f.Put("raw/qualification-observations.json", []q.Observation{{Instance: o.InstanceID, Kind: "resume", Command: ref}})
			}
			absence := runner.Row{"Instance": o.InstanceID, "Launch": o.LaunchToken, "Session": o.SessionID, "Reason": "not_exist", "Observed": at.Add(2 * time.Second)}
			if kind == "misbound-absence" {
				absence["Session"] = "another-session"
			}
			f.Put("raw/"+o.InstanceID+"-"+o.LaunchToken+"-transcript-absence.json", absence)
			inventory, err := runner.InspectRecordings(f.Root, f.Manifest.RunID, f.Manifest.RecordingHook.Events)
			if err != nil {
				t.Fatal(err)
			}
			f.Put("raw/hook-inventory.json", inventory)
			// Put already indexes exact bytes; user-hook receipts have their own
			// format, so do not run ci's passive-receipt rewriting helper here.
			f.JSON("manifest.json", f.Manifest)
			c, err := q.LoadCapture(f.Root)
			valid := kind == "fresh-reuse" || kind == "blocked-H1"
			if !valid {
				if err == nil {
					t.Fatal("absence loaded without its required owned fact")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			loaded := c.Owners[len(c.Owners)-1]
			if loaded.Owner.InstanceID != o.InstanceID || loaded.TranscriptRef != "raw/"+o.InstanceID+"-"+o.LaunchToken+"-transcript-absence.json" {
				t.Fatalf("wrong absence owner: %+v", loaded)
			}
			if got := q.CheckWholeInput(q.WholeInput{Body: "unit known input", Mode: "wait_turn", After: at.UnixMilli(), Transcript: loaded.Transcript}); got.Status != "not_completed" {
				t.Fatalf("absence proved intake: %+v", got)
			}
		})
	}
}

func TestQualificationProductBuildGraph(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(map[bool]string{false: "pinned-dependency", true: "in-tree-local-replacement"}[local], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "src")
			files := map[string]string{"go.mod": "module unit\n", "go.sum": "unit pinned sums\n", "main.go": "package main\n", "local/dep.go": "package dep\n"}
			hashes := map[string]string{}
			for name, text := range files {
				path := filepath.Join(source, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
				hashes[name] = runner.Digest([]byte(text))
			}
			write := func(name string, v any) string {
				t.Helper()
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				p := filepath.Join(root, name)
				if err = os.WriteFile(p, b, 0600); err != nil {
					t.Fatal(err)
				}
				return p
			}
			inv := write("inventory.json", runner.Row{"tree": strings.Repeat("1", 40), "files": hashes})
			dep := runner.Row{"Dir": filepath.Join(root, "module-cache/dep"), "GoFiles": []string{"dep.go"}, "Module": runner.Row{"Path": "unit/dep", "Version": "v1.2.3"}}
			if local {
				dep["Dir"] = filepath.Join(source, "local")
				dep["Module"] = runner.Row{"Path": "unit/dep", "Version": "v1.2.3", "Replace": runner.Row{"Path": "./local"}}
			}
			graph := filepath.Join(root, "graph.jsonl")
			main, _ := json.Marshal(runner.Row{"Dir": source, "GoFiles": []string{"main.go"}, "Module": runner.Row{"Path": "unit", "Main": true}})
			dependency, _ := json.Marshal(dep)
			if err := os.WriteFile(graph, append(append(main, '\n'), dependency...), 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(root, "product.json")
			err := q.BuildProductSource(source, inv, graph, strings.Repeat("2", 64), "linux/amd64", "unit-toolchain", out)
			if local {
				if err == nil || err.Error() != "unpinned/local replacement in product build graph" {
					t.Fatalf("hashed local replacement: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p, err := q.LoadProductSource(root, "product.json")
			if err != nil || len(p.Files) != 3 || p.Files["main.go"] != hashes["main.go"] {
				t.Fatalf("pinned graph: %+v %v", p, err)
			}
		})
	}
}
