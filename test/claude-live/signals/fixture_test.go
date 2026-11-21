package signals

// t3.xis.w3.ci.ai: exact captured A2 form and explicitly synthetic variants.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

func diagCopy(s runner.Snapshot) runner.Snapshot {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var out runner.Snapshot
	if err = json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}
func diagSave(t *testing.T, c *capture, name string, s runner.Snapshot) {
	t.Helper()
	b, e := json.Marshal(s)
	if e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(c.root, name)
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	c.m.Artifacts[name] = runner.Artifact{SHA256: runner.Digest(b), Bytes: int64(len(b))}
}

// Mutate explicit captured blobs and update their references. Conversation
// records remain independent evidence; this never alters the known sent corpus.
func mutateBlobs(t *testing.T, f *signalsfix.Fixture, owner int, change func(runner.Row)) {
	t.Helper()
	name := f.Transcript(owner)
	rows := f.Rows(name)
	hashes := map[string]string{}
	for _, r := range rows {
		if r["type"] != "api-request-blob" {
			continue
		}
		old := runner.Text(r, "hash")
		change(r)
		h, e := journalHash(r["message"])
		if e != nil {
			t.Fatal(e)
		}
		hashes[old] = h
		r["hash"] = h
	}
	for _, r := range rows {
		if r["type"] != "api-request" {
			continue
		}
		for i, h := range r["tail"].([]any) {
			if replacement, ok := hashes[h.(string)]; ok {
				r["tail"].([]any)[i] = replacement
			}
		}
	}
	f.WriteRows(name, rows)
}
func replaceText(value any, old, new string) any {
	switch v := value.(type) {
	case string:
		return strings.ReplaceAll(v, old, new)
	case []any:
		for i, x := range v {
			v[i] = replaceText(x, old, new)
		}
	case map[string]any:
		for k, x := range v {
			v[k] = replaceText(x, old, new)
		}
	}
	return value
}
func mutatePrompt(f *signalsfix.Fixture, owner int, text string, change func(runner.Row)) {
	f.Snapshots(owner, func(s *runner.Snapshot) {
		for _, m := range s.Tables["messages"] {
			if m["text"] != text {
				continue
			}
			for _, member := range s.Tables["members"] {
				if member["message_id"] != m["message_id"] {
					continue
				}
				for _, p := range s.Tables["prompts"] {
					if p["handover_id"] == member["handover_id"] && p["prompt_index"] == member["prompt_index"] {
						change(p)
					}
				}
			}
		}
	})
}

// Only synthetic variants materialize a changed request. The captured baseline
// keeps its original explicit/implicit blob journal without this transformation.
func rewriteRequest(t *testing.T, f *signalsfix.Fixture, owner int, id string, at time.Time, messages []Message) {
	t.Helper()
	name := f.Transcript(owner)
	rows := f.Rows(name)
	var changed []runner.Row
	seen := map[string]bool{}
	for _, r := range rows {
		if r["type"] == "api-request-blob" {
			h := runner.Text(r, "hash")
			if seen[h] {
				continue
			}
			seen[h] = true
		}
		if r["type"] == "api-request" && r["id"] == id {
			var tail []string
			for _, m := range messages {
				h, e := journalHash(m)
				if e != nil {
					t.Fatal(e)
				}
				tail = append(tail, h)
				if !seen[h] {
					changed = append(changed, runner.Row{"type": "api-request-blob", "sessionId": f.Manifest.Owned[owner].SessionID, "hash": h, "message": m})
					seen[h] = true
				}
			}
			r["keep"] = 0
			delete(r, "base")
			r["tail"] = tail
			r["timestamp"] = at
		}
		changed = append(changed, r)
	}
	f.WriteRows(name, changed)
}

func diag14Capture(t *testing.T) (capture, Request) {
	t.Helper()
	c, err := loadOwner(signalsfix.Passing(t).Root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.continuationCalls(); err != nil {
		t.Fatal(err)
	}
	p, _, err := c.prompt(ContinuationWakeOrder)
	if err != nil {
		t.Fatal(err)
	}
	complete := runner.Number(p, "transport_at_ms")
	stop := c.nextStop(complete).UnixMilli()
	active, terminal, err := c.textWindow(complete, stop)
	if err != nil {
		t.Fatal(err)
	}
	x, ok := c.caseRecord(Continuation)
	if !ok {
		t.Fatal("missing continuation case")
	}
	continued, _, err := c.continuationRequest(active.ID, terminal, stop, x.ObservedUntil)
	if err != nil {
		t.Fatal(err)
	}
	return c, continued
}

func TestClaudeLiveContinuationInputForms(t *testing.T) {
	for _, kind := range []string{"system-green", "user-green", "partial-block", "duplicate-block", "earlier-request", "later-request", "changed-request-wrapper", "changed-attachment-wrapper", "changed-label", "missing-attachment", "duplicate-attachment", "no-continuation", "foreign-continuation", "without-stop", "without-continuation-feedback", "generic-not-enabled"} {
		t.Run(kind, func(t *testing.T) {
			c, continued := diag14Capture(t)
			in, form, err := c.continuationWakeIntake(continued)
			if err != nil || form != "system-stop-continuation" {
				t.Fatalf("green setup: %+v %s %v", in, form, err)
			}
			marshal := func(v any) json.RawMessage {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			m := &c.t.Requests[in.Request].Messages[in.Message]
			original := *m
			var blocks []Block
			if err := json.Unmarshal(m.Content, &blocks); err != nil {
				t.Fatal(err)
			}
			attachment := -1
			for i, row := range c.t.Rows {
				if row.Type == "attachment" && row.Attachment.Type == "queued_command" && row.Attachment.Prompt == Render(ContinuationWakeOrder, "wait_turn") {
					attachment = i
				}
			}
			if attachment < 0 {
				t.Fatal("missing green attachment")
			}
			switch kind {
			case "system-green":
			case "user-green":
				m.Role = "user"
				blocks[in.Block].Text = NativeWake(Render(ContinuationWakeOrder, "wait_turn"))
				m.Content = marshal(blocks)
			case "partial-block":
				blocks[in.Block].Text = blocks[in.Block].Text[1:]
				m.Content = marshal(blocks)
			case "duplicate-block":
				blocks = append(blocks, blocks[in.Block])
				m.Content = marshal(blocks)
			case "earlier-request", "later-request":
				blocks = append(blocks[:in.Block], blocks[in.Block+1:]...)
				m.Content = marshal(blocks)
				index := in.Request - 1
				if kind == "later-request" {
					index = in.Request + 1
				}
				c.t.Requests[index].Messages = append(c.t.Requests[index].Messages, original)
			case "changed-request-wrapper":
				blocks[in.Block].Text = "<system-reminder>\n" + blocks[in.Block].Text + "\n</system-reminder>"
				m.Content = marshal(blocks)
			case "changed-attachment-wrapper":
				var rendered []map[string]string
				if err := json.Unmarshal(c.t.Rows[attachment].Rendered, &rendered); err != nil {
					t.Fatal(err)
				}
				rendered[0]["content"] = strings.Replace(rendered[0]["content"], "<system-reminder>", "<system-reminder changed>", 1)
				c.t.Rows[attachment].Rendered = marshal(rendered)
			case "changed-label":
				blocks[in.Block].Text = strings.ReplaceAll(blocks[in.Block].Text, "FileChanged", "OtherChanged")
				m.Content = marshal(blocks)
				row := &c.t.Rows[attachment]
				row.Attachment.Prompt = strings.ReplaceAll(row.Attachment.Prompt, "FileChanged", "OtherChanged")
				row.Rendered = json.RawMessage(strings.ReplaceAll(string(row.Rendered), "FileChanged", "OtherChanged"))
			case "missing-attachment":
				c.t.Rows = append(c.t.Rows[:attachment], c.t.Rows[attachment+1:]...)
			case "duplicate-attachment":
				c.t.Rows = append(c.t.Rows, c.t.Rows[attachment])
			case "no-continuation":
				continued = Request{}
			case "foreign-continuation":
				continued.ID = "not-the-verified-request"
			case "without-stop":
				c.hooks["Stop"] = nil
				if _, err := c.continuationEvidence(); err == nil || !strings.Contains(err.Error(), "Stop linkage") {
					t.Fatalf("unverified Stop path accepted: %v", err)
				}
				return
			case "without-continuation-feedback":
				for i := range c.t.Requests {
					if c.t.Requests[i].ID == continued.ID {
						c.t.Requests[i].Messages = nil
					}
				}
				if _, err := c.continuationEvidence(); err == nil {
					t.Fatal("missing Stop continuation accepted")
				}
				return
			case "generic-not-enabled":
				if _, err := c.intake(ContinuationWakeOrder, "wait_turn"); err == nil {
					t.Fatal("generic intake accepted system continuation form")
				}
				return
			}
			in, form, err = c.continuationWakeIntake(continued)
			if kind == "system-green" || kind == "user-green" {
				want := "system-stop-continuation"
				if kind == "user-green" {
					want = "user-system-reminder"
				}
				if err != nil || form != want {
					t.Fatalf("green: %+v %s %v", in, form, err)
				}
			} else if err == nil || form != "" {
				t.Fatalf("%s qualified: %+v %s %v", kind, in, form, err)
			}
		})
	}
}
