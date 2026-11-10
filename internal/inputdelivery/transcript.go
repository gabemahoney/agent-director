package inputdelivery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/gabemahoney/agent-director/internal/inputjournal"
	"os"
	"time"

	"github.com/google/uuid"
)

const maxTranscriptBytes = 32 << 20

func readBounded(path string, limit int64) ([]byte, error) {
	return inputjournal.ReadEvidence(path, limit)
}

func (s *nativeSession) paths(id Identity) ([]string, error) {
	if id.Agent == "claude" {
		if s.manifest.Transcript == "" {
			return nil, fmt.Errorf("no launch-bound transcript")
		}
		return []string{s.manifest.Transcript}, nil
	}
	paths, err := ownedTranscripts(id)
	previous, loadErr := s.Load()
	if loadErr != nil {
		return nil, loadErr
	}
	if previous != nil && previous.Identity == id {
		seen := map[string]bool{}
		for _, p := range paths {
			seen[p] = true
		}
		// Carry validated ownership through successive durable attempts, even
		// when the agent no longer has the rollout descriptor open.
		for p, mark := range previous.Watermark {
			if mark.SessionID != "" && mark.MetadataSHA256 != "" && !seen[p] {
				paths = append(paths, p)
			}
		}
	}
	if len(paths) > 0 {
		return paths, nil
	}
	return paths, err
}

func (s *nativeSession) Watermark(id Identity) (map[string]Watermark, error) {
	paths, err := s.paths(id)
	if err != nil {
		return nil, err
	}
	marks := map[string]Watermark{}
	var nativeSessionID string
	previous, err := s.Load()
	if err != nil {
		return nil, err
	}
	if previous != nil && previous.Identity == id {
		nativeSessionID = previous.NativeSessionID
	}
	for _, p := range paths {
		if s.ctx != nil {
			if err := s.ctx.Err(); err != nil {
				return nil, err
			}
		}
		data, err := readBounded(p, maxTranscriptBytes)
		if os.IsNotExist(err) {
			if id.Agent == "codex" {
				return nil, fmt.Errorf("owned rollout unavailable at watermark")
			}
			marks[p] = Watermark{PrefixSHA256: Hash(nil)}
			continue
		}
		if err != nil {
			return nil, err
		}
		mark := Watermark{Size: int64(len(data)), PrefixSHA256: Hash(data)}
		if id.Agent == "codex" {
			session, valid := s.codexSession(data, id)
			if !valid {
				if previous != nil && previous.Identity == id && previous.Watermark[p].SessionID != "" {
					return nil, fmt.Errorf("owned transcript metadata unavailable")
				}
				continue
			}
			if nativeSessionID != "" && nativeSessionID != session {
				return nil, fmt.Errorf("multiple owned native sessions at watermark")
			}
			nativeSessionID = session
			mark.SessionID, mark.MetadataSHA256 = session, Hash(data[:bytes.IndexByte(data, '\n')])
			if previous != nil && previous.Identity == id {
				old := previous.Watermark[p]
				if old.SessionID != "" && (old.SessionID != session || old.MetadataSHA256 != mark.MetadataSHA256) {
					return nil, fmt.Errorf("owned transcript metadata changed")
				}
			}
		}
		marks[p] = mark
	}
	return marks, nil
}

func (s *nativeSession) Confirm(a *Attempt) (*Proof, error) {
	if a.PayloadSHA256 != Hash([]byte(a.Payload)) || !a.EnterAttempted {
		return nil, nil
	}
	paths, err := s.paths(a.Identity)
	// A previously observed owned descriptor is durable evidence even after
	// the native process exits. An unobserved path is never guessed by time.
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
	}
	for p, m := range a.Watermark {
		if m.SessionID != "" && m.MetadataSHA256 != "" && !seen[p] {
			paths = append(paths, p)
			seen[p] = true
		}
	}
	if err != nil && len(paths) == 0 {
		return nil, err
	}
	var proof *Proof
	for _, p := range paths {
		if s.ctx != nil {
			if err := s.ctx.Err(); err != nil {
				return nil, err
			}
		}
		data, err := readBounded(p, maxTranscriptBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		mark, known := a.Watermark[p]
		if mark.Size < 0 || mark.Size > int64(len(data)) {
			return nil, fmt.Errorf("transcript truncated after watermark")
		}
		if known && Hash(data[:mark.Size]) != mark.PrefixSHA256 {
			return nil, fmt.Errorf("transcript changed before watermark")
		}
		session := s.manifest.SessionID
		if a.Identity.Agent == "codex" {
			var valid bool
			var created time.Time
			session, created, valid = s.codexMetadata(data, a.Identity)
			if !valid {
				continue
			}
			headerEnd := bytes.IndexByte(data, '\n')
			metadataHash := Hash(data[:headerEnd])
			if a.NativeSessionID != "" && a.NativeSessionID != session {
				continue
			}
			// An FD first observed after the snapshot proves process ownership,
			// not that its existing records are new. Only a native session
			// created after this request's durable snapshot can start at zero.
			if !known && (a.SnapshotAt.IsZero() || !created.After(a.SnapshotAt)) {
				continue
			}
			if mark.SessionID != "" && (mark.SessionID != session || mark.MetadataSHA256 != metadataHash) {
				return nil, fmt.Errorf("owned transcript metadata changed")
			}
			if mark.SessionID == "" || a.NativeSessionID == "" {
				if !known {
					mark = Watermark{PrefixSHA256: Hash(nil)}
				}
				mark.SessionID, mark.MetadataSHA256 = session, metadataHash
				a.NativeSessionID = session
				if a.Watermark == nil {
					a.Watermark = map[string]Watermark{}
				}
				a.Watermark[p] = mark
				if err = s.save(a, true); err != nil {
					return nil, err
				}
			}
		}
		if a.NativeSessionID != "" && a.NativeSessionID != session {
			continue
		}
		// If the snapshot ended in an unfinished record, that record belongs
		// to the pre-request prefix and cannot confirm this request.
		offset := mark.Size
		if offset > 0 && data[offset-1] != '\n' {
			n := bytes.IndexByte(data[offset:], '\n')
			if n < 0 {
				continue
			}
			offset += int64(n) + 1
		}
		for offset < int64(len(data)) {
			n := bytes.IndexByte(data[offset:], '\n')
			if n < 0 {
				break
			}
			line := data[offset : offset+int64(n)]
			recordID, match := s.matchRecord(line, a, session)
			if match {
				if proof != nil {
					return nil, fmt.Errorf("multiple matching user records after watermark")
				}
				proof = &Proof{Path: p, SessionID: session, RecordID: recordID, RawSHA256: Hash(line), Offset: offset, Raw: append([]byte(nil), line...)}
			}
			offset += int64(n) + 1
		}
	}
	return proof, nil
}

func (s *nativeSession) codexSession(data []byte, id Identity) (string, bool) {
	session, _, valid := s.codexMetadata(data, id)
	return session, valid
}

func (s *nativeSession) codexMetadata(data []byte, id Identity) (string, time.Time, bool) {
	n := bytes.IndexByte(data, '\n')
	if n < 0 {
		return "", time.Time{}, false
	}
	var meta struct {
		Type    string
		Payload struct {
			ID, CWD   string
			Version   string `json:"cli_version"`
			Timestamp string
		}
	}
	if json.Unmarshal(data[:n], &meta) != nil || meta.Type != "session_meta" || meta.Payload.CWD != id.CWD || meta.Payload.Version != "0.159.2" {
		return "", time.Time{}, false
	}
	if _, err := uuid.Parse(meta.Payload.ID); err != nil {
		return "", time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, meta.Payload.Timestamp)
	// A process descriptor can briefly reference an older rollout. Its own
	// session metadata must also belong to this fresh diagnostic launch.
	if err != nil || t.Before(s.manifest.Created) {
		return "", time.Time{}, false
	}
	return meta.Payload.ID, t, true
}

func (s *nativeSession) matchRecord(line []byte, a *Attempt, session string) (string, bool) {
	return inputjournal.MatchRecord(line, a, session)
}

// ClaudeExact matches the complete payload through the canonical pinned parser.
func ClaudeExact(text, payload string) bool { return inputjournal.ClaudeExact(text, payload) }
