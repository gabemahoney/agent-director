package qualification_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/qualificationfix"
	q "github.com/gabemahoney/agent-director/test/claude-live/qualification"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"github.com/gabemahoney/agent-director/test/claude-live/signals"
)

func TestQualificationHistoricalInputFraming(t *testing.T) {
	data, err := os.ReadFile("testdata/recorded/idle-input.json")
	if err != nil {
		t.Fatal(err)
	}
	var clip struct {
		Journal []json.RawMessage
		Body    string
	}
	if err := json.Unmarshal(data, &clip); err != nil {
		t.Fatal(err)
	}
	var journal bytes.Buffer
	for _, row := range clip.Journal {
		if err := json.Compact(&journal, row); err != nil {
			t.Fatal(err)
		}
		journal.WriteByte('\n')
	}
	var request struct {
		Session string `json:"sessionId"`
	}
	_ = json.Unmarshal(clip.Journal[1], &request)
	tr, err := signals.ParseTranscript(journal.Bytes(), request.Session)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"recorded-framing", "altered-newline", "shortened-body"} {
		t.Run(kind, func(t *testing.T) {
			body := clip.Body
			want := "pass"
			if kind == "altered-newline" {
				body = strings.Replace(body, "\n", "\r\n", 1)
				want = "fail"
			}
			if kind == "shortened-body" {
				body = body[:len(body)-1]
				want = "fail"
			}
			got := q.CheckWholeInput(q.WholeInput{Body: body, Mode: "wait_turn", After: tr.Requests[0].Time.UnixMilli(), Transcript: tr, Evidence: []string{"testdata/recorded/idle-input.json"}})
			if got.Status != want {
				t.Fatalf("%+v want %s", got, want)
			}
		})
	}
}

func TestQualificationNoRequestCapture(t *testing.T) {
	for _, kind := range []string{"valid-zero-request", "malformed-journal", "unexplained-absence"} {
		t.Run(kind, func(t *testing.T) {
			f := qualificationfix.New(t, "user-hooks")
			owner := f.Manifest.Owned[0]
			ref := "raw/" + owner.InstanceID + "-" + owner.LaunchToken + "-transcript.jsonl"
			switch kind {
			case "valid-zero-request":
				f.Put(ref, runner.Row{"type": "system", "sessionId": owner.SessionID})
			case "malformed-journal":
				f.PutBytes(ref, []byte("{broken\n"))
			case "unexplained-absence":
				delete(f.Manifest.Artifacts, ref)
				if err := os.Remove(f.Path(ref)); err != nil {
					t.Fatal(err)
				}
			}
			f.Reindex()
			c, err := q.LoadCapture(f.Root)
			if kind != "valid-zero-request" {
				if err == nil {
					t.Fatalf("%s journal accepted", kind)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := q.CheckWholeInput(q.WholeInput{Body: "known", Mode: "wait_turn", After: 1, Transcript: c.Owners[0].Transcript})
			if got.Status != "not_completed" {
				t.Fatalf("zero requests proved model input: %+v", got)
			}
		})
	}
}

func TestQualificationDecimalPackingBoundary(t *testing.T) {
	// With the candidate's 169-byte wrapper, ten 907-byte bodies plus
	// decimal-width separators and blank lines total 9598 bytes. At 908,
	// ten total 9608 and only nine fit the unchanged 9600-byte prompt cap.
	for _, tc := range []struct{ size, first int }{{907, 10}, {908, 9}} {
		t.Run(fmt.Sprint(tc.size), func(t *testing.T) {
			owner := runner.Owned{InstanceID: "unit", SessionID: "session", LaunchToken: "launch"}
			s := runner.Snapshot{InstanceID: "unit", Tables: map[string][]runner.Row{"spawns": {{"session_id": "session", "launch_token": "launch"}}, "handovers": {{"handover_id": "h", "instance_id": "unit", "launch_token": "launch", "session_id": "session", "boundary": "tool_boundary"}}, "prompts": {{"handover_id": "h", "prompt_index": int64(0), "message_count": int64(tc.first - 1)}}}}
			for i := 1; i <= 11; i++ {
				id := fmt.Sprint(i)
				text := strings.Repeat("x", tc.size)
				pi := 0
				if i > tc.first {
					pi = 1
				}
				s.Tables["messages"] = append(s.Tables["messages"], runner.Row{"message_id": id, "instance_id": "unit", "accepted_seq": int64(i), "text": text, "text_sha256": runner.Digest([]byte(text))})
				s.Tables["members"] = append(s.Tables["members"], runner.Row{"handover_id": "h", "member_index": int64(i - 1), "prompt_index": int64(pi), "message_id": id})
			}
			got := q.CheckPackingAndOrder(q.PackingEvidence{Owner: owner, Snapshot: s})
			if got.Status != "fail" || got.Reason != "prompt does not use the candidate's longest fitting prefix" {
				t.Fatalf("decimal/wrapper boundary not judged: %+v", got)
			}
		})
	}
}
