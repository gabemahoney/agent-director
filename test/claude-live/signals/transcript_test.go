package signals

import (
	"bytes"
	"encoding/json"
	"github.com/gabemahoney/agent-director/test/claude-live/internal/signalsfix"
	"github.com/gabemahoney/agent-director/test/claude-live/runner"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func historical(t *testing.T, n int) ([]byte, string) {
	t.Helper()
	f := signalsfix.Passing(t)
	root := filepath.Dir(f.Root)
	var sources []struct{ File, Session string }
	b, e := os.ReadFile(filepath.Join(root, "historical-provenance.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &sources); e != nil {
		t.Fatal(e)
	}
	index := n - 3
	b, e = os.ReadFile(filepath.Join(root, sources[index].File))
	if e != nil {
		t.Fatal(e)
	}
	return b, sources[index].Session
}
func rawRows(t *testing.T, b []byte) []runner.Row {
	t.Helper()
	var out []runner.Row
	for _, line := range bytes.Split(bytes.TrimSpace(b), []byte("\n")) {
		var r runner.Row
		if e := json.Unmarshal(line, &r); e != nil {
			t.Fatal(e)
		}
		out = append(out, r)
	}
	return out
}
func encodeRows(t *testing.T, rows []runner.Row) []byte {
	t.Helper()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	for _, r := range rows {
		if e := enc.Encode(r); e != nil {
			t.Fatal(e)
		}
	}
	return out.Bytes()
}

func TestClaudeLiveCapturedJournal(t *testing.T) {
	b, session := historical(t, 3)
	parsed, e := ParseTranscript(b, session)
	if e != nil {
		t.Fatal(e)
	}
	if len(parsed.Requests) != 2 || len(parsed.Requests[1].Messages) != 4 {
		t.Fatal("capture 3 request reconstruction changed")
	}
	texts, e := Texts(parsed.Requests[1].Messages[2])
	if e != nil || len(texts) != 1 || texts[0] != "LIVE-READY" {
		t.Fatalf("implicit terminal reconstruction: %q %v", texts, e)
	}
	in, e := parsed.NextInput(parsed.Requests[1].Time.UnixMilli(), NativeWake(Render(Plain, "wait_turn")), "user")
	if e != nil || in.Line != 41 || in.Message != 3 || in.Count != 1 {
		t.Fatalf("measured exact input: %+v %v", in, e)
	}
	for _, kind := range []string{"missing-response", "bad-tail", "bad-base", "bad-request-ref", "bad-block-index", "duplicate-request", "changed-content-old-hash", "duplicate-blob", "output-config-old-hash", "explicit-blob-bad-journal"} {
		t.Run(kind, func(t *testing.T) {
			rows := rawRows(t, b)
			switch kind {
			case "missing-response":
				rows = append(rows[:28], rows[29:]...)
			case "bad-tail":
				rows[40]["tail"].([]any)[0] = strings.Repeat("0", 64)
			case "bad-base":
				rows[40]["base"] = "absent"
			case "bad-request-ref":
				rows[28]["requestRef"] = "absent"
			case "bad-block-index":
				rows[28]["apiBlockIndex"] = 1
			case "duplicate-request":
				rows = append(rows, rows[40])
			case "changed-content-old-hash":
				rows[39]["message"] = replaceText(rows[39]["message"], "日本語", "日本")
			case "duplicate-blob":
				rows = append(rows, rows[39])
			case "output-config-old-hash":
				rows[16]["message"].(map[string]any)["output_config"] = map[string]any{}
			case "explicit-blob-bad-journal":
				// Even a valid explicit replacement cannot hide an inconsistent assistant
				// requestRef/apiBlockIndex relation in the original conversation evidence.
				m := parsed.Requests[1].Messages[2]
				hash, e := journalHash(m)
				if e != nil {
					t.Fatal(e)
				}
				rows[28]["apiBlockIndex"] = 1
				row := runner.Row{"type": "api-request-blob", "sessionId": session, "message": m, "hash": hash}
				rows = append(rows[:40], append([]runner.Row{row}, rows[40:]...)...)
			}
			if _, e := ParseTranscript(encodeRows(t, rows), session); e == nil {
				t.Fatal("invalid journal accepted")
			}
		})
	}
}

func TestClaudeLiveNextInputExactness(t *testing.T) {
	for _, kind := range []string{"exact", "suffix", "prefix", "unicode", "separator", "wrong-role", "duplicate", "later-echo"} {
		t.Run(kind, func(t *testing.T) {
			b, session := historical(t, 3)
			tr, e := ParseTranscript(b, session)
			if e != nil {
				t.Fatal(e)
			}
			req := &tr.Requests[1]
			m := &req.Messages[3]
			var blocks []Block
			if e = json.Unmarshal(m.Content, &blocks); e != nil {
				t.Fatal(e)
			}
			want := NativeWake(Render(Plain, "wait_turn"))
			if len(blocks) != 1 || blocks[0].Text != want {
				t.Fatal("measured baseline changed")
			}
			switch kind {
			case "suffix":
				blocks[0].Text += " "
			case "prefix":
				blocks[0].Text = " " + blocks[0].Text
			case "unicode":
				blocks[0].Text = strings.ReplaceAll(blocks[0].Text, "café", "cafe\u0301")
			case "separator":
				blocks[0].Text = strings.ReplaceAll(blocks[0].Text, "[agent-director: message 1 of 1]", "[agent-director: message 1/1]")
			case "wrong-role":
				m.Role = "assistant"
			case "duplicate":
				blocks = append(blocks, blocks[0])
			case "later-echo":
				later := *req
				later.Messages = append([]Message(nil), req.Messages...)
				later.ID = "later"
				tr.Requests = append(tr.Requests, later)
				blocks = nil
			}
			// Appending a request may reallocate; mutate the original request by index.
			content, _ := json.Marshal(blocks)
			tr.Requests[1].Messages[3].Content = content
			in, e := tr.NextInput(req.Time.UnixMilli(), want, "user")
			if kind == "exact" {
				if e != nil || in.Count != 1 {
					t.Fatalf("%+v %v", in, e)
				}
			} else if e == nil {
				t.Fatalf("changed next input passed: %+v", in)
			}
		})
	}
}

func TestClaudeLiveHistoricalMergedReversal(t *testing.T) {
	b, session := historical(t, 4)
	tr, e := ParseTranscript(b, session)
	if e != nil {
		t.Fatal(e)
	}
	var after int64
	var sf, wf inputForm
	for _, r := range tr.Requests {
		s, ok := tr.mergedForm(r.Time.UnixMilli(), Steer)
		if !ok {
			continue
		}
		w, ok := tr.mergedForm(r.Time.UnixMilli(), OverlapWake)
		if ok {
			after = r.Time.UnixMilli()
			sf, wf = s, w
			break
		}
	}
	if after == 0 {
		t.Fatal("capture 4 merged input missing")
	}
	si, e := tr.nextInput(after, NativeContext(Steer), "system", []inputForm{sf})
	if e != nil {
		t.Fatal(e)
	}
	wi, e := tr.nextInput(after, NativeWake(Render(OverlapWake, "wait_turn")), "user", []inputForm{wf})
	if e != nil {
		t.Fatal(e)
	}
	if si.Offset != 0 || wi.Offset != 160 || !earlier(si, wi) {
		t.Fatalf("real reversal changed: steer=%+v wake=%+v", si, wi)
	}
	for _, kind := range []string{"one-byte", "separator", "wrong-role", "duplicate-block", "attachment-missing"} {
		t.Run(kind, func(t *testing.T) {
			c, e := ParseTranscript(b, session)
			if e != nil {
				t.Fatal(e)
			}
			m := &c.Requests[si.Request].Messages[si.Message]
			var blocks []Block
			if e = json.Unmarshal(m.Content, &blocks); e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "one-byte":
				blocks[si.Block].Text += "X"
			case "separator":
				blocks[si.Block].Text = strings.ReplaceAll(blocks[si.Block].Text, "[agent-director: message 1 of 1]", "[agent-director: message 2 of 2]")
			case "wrong-role":
				m.Role = "user"
			case "duplicate-block":
				blocks = append(blocks, blocks[si.Block])
			case "attachment-missing":
				for i := range c.Rows {
					c.Rows[i].Attachment = TranscriptAttachment{}
				}
			}
			m.Content, _ = json.Marshal(blocks)
			form, ok := c.mergedForm(after, Steer)
			var forms []inputForm
			if ok {
				forms = append(forms, form)
			}
			if _, e := c.nextInput(after, NativeContext(Steer), "system", forms); e == nil {
				t.Fatal("changed merged whole input accepted")
			}
		})
	}
}
