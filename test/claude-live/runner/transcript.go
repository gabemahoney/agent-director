package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/internal/spawn"
)

// TranscriptPath uses the same pure path resolver as the product's provisional
// transcript active check (pkg/api/active_checks.go). A NULL jsonl_path at
// SessionStart does not mean Claude never creates the file. No HOME discovery,
// store write, directory search or provider execution is needed here.
func TranscriptPath(home, cwd, session, recorded string) (string, error) {
	if session == "" || filepath.Base(session) != session || strings.ContainsAny(session, "/\\") {
		return "", errors.New("missing or invalid transcript session")
	}
	path := recorded
	if path == "" {
		if !filepath.IsAbs(cwd) || filepath.Clean(cwd) != cwd {
			return "", errors.New("missing or invalid owned cwd for transcript fallback")
		}
		var err error
		path, err = spawn.JsonlPathIn(filepath.Join(home, ".claude"), cwd, session)
		if err != nil {
			return "", err
		}
	}
	base := filepath.Join(home, ".claude", "projects")
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || rel == ".." || strings.HasPrefix(rel, "../") || filepath.Base(path) != session+".jsonl" {
		return "", errors.New("transcript path is outside the owned session")
	}
	return path, nil
}

// CheckTranscriptRecordings cross-checks the resolved path with completed
// parent recorder inputs. In-flight invocations may lack a receipt during the
// exercise; final inventory validation still rejects every incomplete entry.
func CheckTranscriptRecordings(root, runID string, o Owned, others ...Owned) error {
	p, err := Confined(root, "raw/hooks")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		ref := "raw/hooks/" + entry.Name()
		b, err := Read(root, ref+"/receipt.json")
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		var receipt Receipt
		if json.Unmarshal(b, &receipt) != nil || receipt.Schema != Schema || receipt.RunID != runID || !receipt.Complete || receipt.Input != "input.json" {
			return errors.New("invalid transcript recorder receipt")
		}
		b, err = Read(root, ref+"/input.json")
		if err != nil {
			return err
		}
		var payload Row
		if Digest(b) != receipt.SHA256 || len(b) != receipt.Bytes || json.Unmarshal(b, &payload) != nil || Text(payload, "hook_event_name") != receipt.Event {
			return errors.New("invalid transcript recorder input")
		}
		if Text(payload, "agent_id") != "" {
			continue
		}
		if Text(payload, "session_id") != o.SessionID || Text(payload, "transcript_path") != o.Transcript || Text(payload, "cwd") != o.CWD {
			matched := false
			for _, other := range others {
				path, e := TranscriptPath(TestHome, other.CWD, other.SessionID, other.Transcript)
				if e == nil && Text(payload, "session_id") == other.SessionID && Text(payload, "transcript_path") == path && Text(payload, "cwd") == other.CWD {
					matched = true
				}
			}
			if !matched {
				return errors.New("recorder input belongs to another parent")
			}
			continue
		}
		found = true
	}
	if !found {
		return errors.New("no completed parent recording for transcript binding")
	}
	return nil
}
