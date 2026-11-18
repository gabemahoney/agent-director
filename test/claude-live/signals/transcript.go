package signals

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type Message struct {
	ID         string          `json:"id,omitempty"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason,omitempty"`
}
type TranscriptRow struct {
	Type           string               `json:"type"`
	SessionID      string               `json:"sessionId"`
	OtherSessionID string               `json:"session_id"`
	Timestamp      time.Time            `json:"timestamp"`
	Hash           string               `json:"hash"`
	ID             string               `json:"id"`
	Base           string               `json:"base"`
	RequestRef     string               `json:"requestRef"`
	APIBlockIndex  *int                 `json:"apiBlockIndex"`
	QuerySource    string               `json:"querySource"`
	Keep           int                  `json:"keep"`
	Tail           []string             `json:"tail"`
	Message        Message              `json:"message"`
	Sidechain      bool                 `json:"isSidechain"`
	Attachment     TranscriptAttachment `json:"attachment"`
	Rendered       json.RawMessage      `json:"rendered"`
	Line           int                  `json:"-"`
}

type Request struct {
	Time     time.Time
	ID       string
	Line     int
	Messages []Message
}

type Transcript struct {
	Rows     []TranscriptRow
	Requests []Request
}

// ParseTranscript replays Claude's keep/tail journal, selecting the prefix by
// base when present (older journals use the preceding request). Claude omits
// blobs already recoverable from conversation rows. Those rows supply content
// only when their canonical hash occurs in an actual request's tail; a displayed
// user row, hook attachment or assistant reply alone never proves model input.
func ParseTranscript(b []byte, session string) (Transcript, error) {
	var t Transcript
	blobs := map[string]Message{}
	requests := map[string][]Message{}
	ignored := map[string]bool{}
	journal := newJournalMessages()
	var history []Message
	scan := bufio.NewScanner(bytes.NewReader(b))
	scan.Buffer(make([]byte, 4096), 64<<20)
	for line := 1; scan.Scan(); line++ {
		var row TranscriptRow
		if err := json.Unmarshal(scan.Bytes(), &row); err != nil {
			return t, fmt.Errorf("transcript line %d: %w", line, err)
		}
		row.Line = line
		if row.Sidechain {
			continue
		}
		if row.SessionID != "" && row.SessionID != session {
			return t, errors.New("transcript belongs to another session")
		}
		if row.OtherSessionID != "" && row.OtherSessionID != session {
			return t, errors.New("transcript parent identity changed")
		}
		switch row.Type {
		case "user", "assistant":
			if row.Type == "assistant" {
				if row.RequestRef == "" || ignored[row.RequestRef] {
					break // Legacy display rows and non-main responses are not journal sources.
				}
				if _, ok := requests[row.RequestRef]; !ok {
					return t, fmt.Errorf("transcript line %d: unknown assistant requestRef", line)
				}
			}
			if row.SessionID != session {
				return t, fmt.Errorf("transcript line %d: missing conversation session", line)
			}
			if err := journal.add(row); err != nil {
				return t, fmt.Errorf("transcript line %d: %w", line, err)
			}
		case "api-request-blob":
			if row.Hash == "" || row.SessionID != session || row.Message.Role == "" || len(row.Message.Content) == 0 {
				return t, errors.New("invalid request blob")
			}
			// Preserve fields outside the intake projection when verifying the
			// explicit blob's identity; they are part of its canonical message.
			var blob struct {
				Message map[string]any `json:"message"`
			}
			dec := json.NewDecoder(bytes.NewReader(scan.Bytes()))
			dec.UseNumber()
			if err := dec.Decode(&blob); err != nil {
				return t, fmt.Errorf("transcript line %d: %w", line, err)
			}
			// E4 transcript A line 47 stores string content but hashes its
			// single text-block form. Preserve the string in the intake view.
			if text, ok := blob.Message["content"].(string); ok {
				blob.Message["content"] = []any{map[string]any{"type": "text", "text": text}}
			}
			hash, err := journalHash(blob.Message)
			if err != nil {
				return t, fmt.Errorf("transcript line %d: invalid request blob hash: %w", line, err)
			}
			if hash != row.Hash {
				return t, fmt.Errorf("transcript line %d: request blob hash mismatch", line)
			}
			if _, ok := blobs[row.Hash]; ok {
				return t, errors.New("duplicate request blob identity")
			}
			blobs[row.Hash] = row.Message
		case "api-request":
			if _, ok := requests[row.ID]; ok || ignored[row.ID] {
				return t, errors.New("duplicate request identity")
			}
			if row.QuerySource != "repl_main_thread" {
				ignored[row.ID] = true
				continue
			}
			prefix := history
			if row.Base != "" {
				var ok bool
				prefix, ok = requests[row.Base]
				if !ok {
					return t, fmt.Errorf("request line %d references absent base", line)
				}
			}
			if row.SessionID != session || row.ID == "" || row.Timestamp.IsZero() || row.Keep < 0 || row.Keep > len(prefix) || row.Tail == nil {
				return t, errors.New("changed or incomplete request journal")
			}
			history = append([]Message(nil), prefix[:row.Keep]...)
			for _, hash := range row.Tail {
				m, ok := blobs[hash]
				if !ok {
					m, ok = journal.messages[hash]
				}
				if !ok {
					return t, fmt.Errorf("request references absent blob %s", hash)
				}
				history = append(history, m)
			}
			requests[row.ID] = history
			t.Requests = append(t.Requests, Request{row.Timestamp, row.ID, line, append([]Message(nil), history...)})
		}
		t.Rows = append(t.Rows, row)
	}
	if err := scan.Err(); err != nil {
		return t, err
	}
	if len(t.Requests) == 0 {
		return t, errors.New("no main-thread model request journal")
	}
	return t, nil
}

func Texts(m Message) ([]string, error) {
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return []string{text}, nil
	}
	var blocks []Block
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil, err
	}
	var texts []string
	for _, b := range blocks {
		if b.Type == "text" {
			texts = append(texts, b.Text)
		}
	}
	return texts, nil
}

type Intake struct {
	Request int       `json:"request"`
	Line    int       `json:"line"`
	Message int       `json:"message"`
	Block   int       `json:"block"`
	Offset  int       `json:"offset"` // Byte offset of a whole input within a measured merged block.
	Time    time.Time `json:"time"`
	Count   int       `json:"count"`
}

func (t Transcript) NextInput(after int64, want, role string) (Intake, error) {
	return t.nextInput(after, want, role, nil)
}

type inputForm struct {
	role, text string
	offset     int
}

func (t Transcript) nextInput(after int64, want, role string, additional []inputForm) (Intake, error) {
	forms := append([]inputForm{{role: role, text: want}}, additional...)
	for i, request := range t.Requests {
		if request.Time.UnixMilli() < after {
			continue
		}
		in := Intake{Request: i, Line: request.Line, Time: request.Time}
		for j, m := range request.Messages {
			eligible := false
			for _, form := range forms {
				eligible = eligible || m.Role == form.role
			}
			if !eligible {
				continue
			}
			texts, err := Texts(m)
			if err != nil {
				return in, err
			}
			for k, text := range texts {
				for _, form := range forms {
					if m.Role == form.role && text == form.text {
						in.Count++
						in.Message = j
						in.Block = k
						in.Offset = form.offset
					}
				}
			}
		}
		if in.Count != 1 {
			return in, fmt.Errorf("next eligible request contains %d whole inputs; expected exactly one", in.Count)
		}
		return in, nil
	}
	return Intake{}, errors.New("no next eligible model request")
}

func earlier(a, b Intake) bool {
	if a.Request != b.Request {
		return a.Request < b.Request
	}
	if a.Message != b.Message {
		return a.Message < b.Message
	}
	if a.Block != b.Block {
		return a.Block < b.Block
	}
	return a.Offset < b.Offset
}
