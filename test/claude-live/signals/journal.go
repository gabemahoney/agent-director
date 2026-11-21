package signals

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"unicode/utf16"
)

// Request-blob hashes cover canonical JSON, with recursively sorted object
// keys. Conversation sources contribute only role/content, not response usage,
// IDs or stop metadata. apiBlockIndex joins split assistant content in API order.
// This is supported by capture 3 lines 16/17/29/40/41 and E4's between transcript
// lines 24/25/28 and 39-43. Unknown transformations still leave an unresolved hash.
type journalMessages struct {
	messages  map[string]Message
	responses map[string]*journalResponse
}

type journalResponse struct {
	id     string
	blocks []json.RawMessage
	hash   string
}

func newJournalMessages() journalMessages {
	return journalMessages{map[string]Message{}, map[string]*journalResponse{}}
}

func (j journalMessages) add(row TranscriptRow) error {
	m := Message{Role: row.Message.Role, Content: row.Message.Content}
	if m.Role != row.Type || len(m.Content) == 0 || bytes.Equal(m.Content, []byte("null")) {
		return errors.New("invalid conversation message")
	}
	if row.Type == "assistant" {
		var blocks []json.RawMessage
		if row.Message.ID == "" || row.APIBlockIndex == nil || json.Unmarshal(m.Content, &blocks) != nil || len(blocks) == 0 {
			return errors.New("incomplete assistant journal blocks")
		}
		r := j.responses[row.RequestRef]
		if r == nil {
			r = &journalResponse{id: row.Message.ID}
			j.responses[row.RequestRef] = r
		}
		if r.id != row.Message.ID || *row.APIBlockIndex != len(r.blocks) {
			return errors.New("inconsistent assistant journal block identity/order")
		}
		r.blocks = append(r.blocks, blocks...)
		m.Content, _ = json.Marshal(r.blocks)
		// A partial response is not a second message. Rebuild the pool below so
		// another complete response with identical content retains its identity.
		hash, err := journalHash(m)
		if err != nil {
			return err
		}
		old := r.hash
		r.hash = hash
		if old != "" {
			delete(j.messages, old)
			for _, other := range j.responses {
				if other.hash == old {
					content, _ := json.Marshal(other.blocks)
					j.messages[old] = Message{Role: "assistant", Content: content}
					break
				}
			}
		}
		j.messages[hash] = m
		return nil
	}
	hash, err := journalHash(m)
	if err == nil {
		j.messages[hash] = m
	}
	return err
}

// Explicit blobs hash their complete message (including fields such as the
// system output_config). Conversation sources pass a normalized Message above.
func journalHash(message any) (string, error) {
	raw, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // Journal JSON already has the provider's numeric spelling.
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	b, err := journalJSON(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}

func journalJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		// JavaScript sorts strings as UTF-16 code units, including non-BMP keys.
		sort.Slice(keys, func(i, k int) bool {
			a, z := utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[k]))
			for n := 0; n < len(a) && n < len(z); n++ {
				if a[n] != z[n] {
					return a[n] < z[n]
				}
			}
			return len(a) < len(z)
		})
		b.WriteByte('{')
		for i, key := range keys {
			if i != 0 {
				b.WriteByte(',')
			}
			name, _ := journalJSON(key)
			value, err := journalJSON(v[key])
			if err != nil {
				return nil, err
			}
			b.Write(name)
			b.WriteByte(':')
			b.Write(value)
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i != 0 {
				b.WriteByte(',')
			}
			value, err := journalJSON(item)
			if err != nil {
				return nil, err
			}
			b.Write(value)
		}
		b.WriteByte(']')
	default:
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(v); err != nil {
			return nil, err
		}
		raw := bytes.TrimSuffix(b.Bytes(), []byte("\n"))
		// encoding/json escapes these two characters even with HTML escaping
		// disabled; JSON.stringify emits them literally. Unescape only actual
		// escapes, never the bytes of a literal backslash-u in message text.
		var out bytes.Buffer
		for i := 0; i < len(raw); i++ {
			if raw[i] == '\\' && i+1 < len(raw) {
				if i+6 <= len(raw) && (string(raw[i:i+6]) == `\u2028` || string(raw[i:i+6]) == `\u2029`) {
					if raw[i+5] == '8' {
						out.WriteRune('\u2028')
					} else {
						out.WriteRune('\u2029')
					}
					i += 5
					continue
				}
				out.WriteByte(raw[i])
				i++
			}
			out.WriteByte(raw[i])
		}
		return out.Bytes(), nil
	}
	return b.Bytes(), nil
}
