package qualification_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func TestQualificationDialogFallbackResolution(t *testing.T) {
	for _, kind := range []string{"exact", "wrong-candidate", "missing-reason", "other-combination"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "dialogs")
			c := f.Capture()
			key := q.DialogKey{Mode: "wait_turn", Origin: "background", State: "open"}
			log := []byte(`{"Action":"pass","Package":"unit/dialog","Test":"TestDialog/relay-off-background"}` + "\n")
			hash := runner.Digest(log)
			f.PutBytes("raw/scheduler/"+hash, log)
			test := q.SchedulerTest{Coverage: key.ID(), Package: "unit/dialog", Name: "TestDialog", Case: "relay-off-background", SHA256: hash, Format: "go-test-json", Reason: "native unavailable", Kind: "native_unavailable", Source: "unit/dialog_test.go", Subject: &key}
			job := q.SchedulerJob{ID: "unit-job", Candidate: c.Before.Identity.Candidate, Product: c.Before.Identity.Product, Platform: "linux/amd64", Status: "succeeded", Environment: json.RawMessage(`{"dpkg_architecture":"amd64"}`)}
			switch kind {
			case "wrong-candidate":
				job.Candidate = "stale"
			case "missing-reason":
				test.Reason = ""
			case "other-combination":
				other := key
				other.Relay = true
				test.Subject = &other
			}
			job.Tests = []q.SchedulerTest{test}
			f.Put("raw/scheduler-evidence.json", q.SchedulerEvidence{Schema: q.Schema, Jobs: []q.SchedulerJob{job}})
			checks := q.CheckDialogMatrix(c, nil)
			rows := q.ResolveCoverageEvidence([]q.Capture{c}, map[string][]runner.Result{"dialogs": checks})
			want := "fail"
			if kind == "exact" {
				want = "pass"
			}
			if got := coverageRow(t, rows, key.ID()); got.Status != want {
				t.Fatalf("%+v want %s", got, want)
			}
			opposite := key
			opposite.Relay = true
			if got := coverageRow(t, rows, opposite.ID()); got.Status != "not_completed" {
				t.Fatalf("fallback leaked to another relay: %+v", got)
			}
		})
	}
}

func TestQualificationRelayOffSafety(t *testing.T) {
	for _, kind := range []string{"keyless-end", "without-keyless-end", "cleanup-only-end", "decide-answer", "pane-answer", "tmux-pane-answer", "harmless-effort-line"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "dialogs")
			c := f.Capture()
			c.Before.DialogCases = []string{"D4"}
			c.Commands = map[string]runner.Command{}
			c.Hooks = nil
			start := time.Unix(1800000000, 0).UTC()
			iid := "live-" + c.Manifest.RunID + "-D4"
			sid := "unit-session"
			token := "unit-launch"
			c.Owners = []q.OwnerEvidence{{Owner: runner.Owned{InstanceID: iid, SessionID: sid, LaunchToken: token, Socket: "/unit/dialog/tmux.sock", CWD: "/unit/dialog", Exited: true, StoppedAt: start.Add(10 * time.Second)}}}
			before := runner.Snapshot{InstanceID: iid, Observed: start, ReadCompleted: start, Tables: map[string][]runner.Row{"spawns": {{"session_id": sid, "launch_token": token}}}}
			after := runner.Snapshot{InstanceID: iid, Observed: start.Add(time.Second), ReadCompleted: start.Add(time.Second), Tables: map[string][]runner.Row{"spawns": {{"session_id": sid, "launch_token": token}}, "messages": {
				{"message_id": "a", "text": "a", "text_sha256": runner.Digest([]byte("a")), "place": "held"}, {"message_id": "b", "text": "b", "text_sha256": runner.Digest([]byte("b")), "place": "held"}, {"message_id": "c", "text": "c", "text_sha256": runner.Digest([]byte("c")), "place": "held"}, {"message_id": "d", "text": "d", "text_sha256": runner.Digest([]byte("d")), "place": "held"}}}}
			c.Snapshots = map[string]runner.Snapshot{"unit/before": before, "unit/after": after}
			c.Commands["spawn"] = runner.Command{Name: "spawn-D4", Argv: []string{"ad", "spawn", "--relay-mode", "off"}}
			gate := "raw/unit-gate"
			f.Put(gate+"/input.json", runner.Row{"session_id": sid, "tool_name": "Bash", "tool_input": runner.Row{"command": "printf qualification > /unit/dialog/qd-main.txt"}})
			key := q.DialogKey{Mode: "wait_turn", Origin: "main", State: "about_to_open"}
			ref := putCommand(f, &c, "unit-send", api.DeliveryResult{Class: "held"})
			cmd := c.Commands[ref]
			cmd.Argv = []string{"ad", "send-keys", "--instance-id", iid, "--send-mode", "wait_turn", "--confirmed-delivery", "false"}
			cmd.Started = start.Add(500 * time.Millisecond)
			cmd.Ended = cmd.Started
			c.Commands[ref] = cmd
			end := runner.Command{Name: "relay-off-end-D4", Argv: []string{"ad", "kill", "--instance-id", iid}, Started: start.Add(2 * time.Second), Ended: start.Add(3 * time.Second)}
			c.Commands["end"] = end
			switch kind {
			case "without-keyless-end":
				delete(c.Commands, "end")
			case "cleanup-only-end":
				delete(c.Commands, "end")
				end.Name = "cleanup-kill"
				c.Commands["cleanup"] = end
			case "decide-answer":
				c.Commands["answer"] = runner.Command{Name: "answer", Argv: []string{"ad", "decide", "--instance-id", iid}, Started: start.Add(500 * time.Millisecond), Ended: start.Add(600 * time.Millisecond)}
			case "pane-answer":
				c.Commands["answer"] = runner.Command{Name: "answer", Argv: []string{"ad", "send-keys", "--instance-id", iid, "--key", "Enter"}, Started: start.Add(500 * time.Millisecond), Ended: start.Add(600 * time.Millisecond)}
			case "tmux-pane-answer":
				c.Commands["answer"] = runner.Command{Name: "dialog-direct-enter", Argv: []string{"tmux", "-S", "/unit/dialog/tmux.sock", "send-keys", "-t", iid, "Enter"}, Started: start.Add(500 * time.Millisecond), Ended: start.Add(600 * time.Millisecond)}
			case "harmless-effort-line":
				f.PutBytes("raw/harmless-pane", []byte("effort: high -> medium\n"))
			}
			rows := q.CheckDialogMatrix(c, []q.DialogObservation{{ID: "unit", Key: key, Instance: iid, Before: "unit/before", After: "unit/after", Gate: gate, Send: ref}})
			want := "pass"
			if kind == "without-keyless-end" || kind == "cleanup-only-end" {
				want = "not_completed"
			}
			if kind == "decide-answer" || kind == "pane-answer" || kind == "tmux-pane-answer" {
				want = "fail"
			}
			if got := namedResult(t, rows, key.ID()); got.Status != want {
				t.Fatalf("%+v; want %s", got, want)
			}
		})
	}
}
