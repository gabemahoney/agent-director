package runner

import (
	"encoding/json"
	"errors"
	"fmt"
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

// ErrTranscriptBindingPending means an operational wait must observe again.
// It never grants transcript ownership or makes sealed evidence complete.
var ErrTranscriptBindingPending = errors.New("transcript binding pending")

// CheckTranscriptRecordings checks independently supplied owner paths. Without
// a Run, its Home comes from an already recorded owner path, never a hook.
// Runs use their explicit scoped Home with the same matcher via ReadTranscript.
func CheckTranscriptRecordings(root, runID string, o Owned, others ...Owned) error {
	for _, candidate := range append([]Owned{o}, others...) {
		if candidate.Agent != "" && candidate.Agent != "claude" {
			continue
		}
		if offset := strings.LastIndex(candidate.Transcript, "/.claude/projects/"); offset >= 0 {
			home := candidate.Transcript[:offset]
			if home == "" {
				home = string(filepath.Separator)
			}
			return checkTranscriptRecordings(home, root, runID, o, others...)
		}
	}
	return fmt.Errorf("%w: independently recorded owner path not yet available", ErrTranscriptBindingPending)
}

type transcriptOwner struct {
	owner Owned
	path  string
}

// One resolver supplies the checked and sibling tuples; no hook can establish
// an owner's session, cwd or transcript path by itself.
func transcriptOwners(home string, o Owned, others []Owned) ([]transcriptOwner, bool, error) {
	var owners []transcriptOwner
	pending := false
	for _, candidate := range append([]Owned{o}, others...) {
		if candidate.Agent != "" && candidate.Agent != "claude" {
			continue
		}
		if candidate.SessionID == "" {
			pending = true
			continue
		}
		path, err := TranscriptPath(home, candidate.CWD, candidate.SessionID, candidate.Transcript)
		if err != nil {
			return nil, false, fmt.Errorf("transcript owner instance=%q session_id=%q: %w", candidate.InstanceID, candidate.SessionID, err)
		}
		owners = append(owners, transcriptOwner{candidate, path})
	}
	return owners, pending, nil
}

func checkTranscriptRecordings(home, root, runID string, o Owned, others ...Owned) error {
	owners, pending, err := transcriptOwners(home, o, others)
	if err != nil {
		return err
	}
	p, err := Confined(root, "raw/hooks")
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(p)
	if os.IsNotExist(err) {
		return fmt.Errorf("%w: own recording not yet available for instance=%q session_id=%q", ErrTranscriptBindingPending, o.InstanceID, o.SessionID)
	}
	if err != nil {
		return err
	}
	found := false
	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("invalid transcript recorder entry %q", entry.Name())
		}
		ref := "raw/hooks/" + entry.Name()
		b, err := Read(root, ref+"/receipt.json")
		if os.IsNotExist(err) {
			continue // Publication is in flight; final inventory remains strict.
		}
		if err != nil {
			return err
		}
		var receipt Receipt
		if err = json.Unmarshal(b, &receipt); err != nil {
			// Legacy/in-flight writers can expose an empty or truncated receipt.
			// Atomic publication below prevents this in new recorder invocations.
			if strings.Contains(err.Error(), "unexpected end of JSON input") {
				continue
			}
			return fmt.Errorf("invalid transcript recorder receipt invocation=%q: %w", ref, err)
		}
		if receipt.Schema != Schema || receipt.RunID != runID || !receipt.Complete || receipt.Input != "input.json" {
			return fmt.Errorf("invalid transcript recorder receipt invocation=%q", ref)
		}
		b, err = Read(root, ref+"/input.json")
		if err != nil {
			return err
		}
		var payload Row
		if Digest(b) != receipt.SHA256 || len(b) != receipt.Bytes || json.Unmarshal(b, &payload) != nil || Text(payload, "hook_event_name") != receipt.Event {
			return fmt.Errorf("invalid transcript recorder input invocation=%q", ref)
		}
		if Text(payload, "agent_id") != "" {
			continue
		}
		matched := false
		for _, candidate := range owners {
			if Text(payload, "session_id") == candidate.owner.SessionID && Text(payload, "transcript_path") == candidate.path && Text(payload, "cwd") == candidate.owner.CWD {
				matched = true
				if candidate.owner.InstanceID == o.InstanceID && candidate.owner.LaunchToken == o.LaunchToken && candidate.owner.SessionID == o.SessionID {
					found = true
				}
			}
		}
		if !matched {
			context := fmt.Sprintf("session_id=%q event=%q invocation=%q", Text(payload, "session_id"), receipt.Event, ref)
			if pending {
				return fmt.Errorf("%w: %s has no independently bound owned parent yet", ErrTranscriptBindingPending, context)
			}
			return fmt.Errorf("recorder input %s does not match any bound owned parent", context)
		}
	}
	if !found || pending {
		return fmt.Errorf("%w: own recording or registered parent identity not yet available for instance=%q session_id=%q", ErrTranscriptBindingPending, o.InstanceID, o.SessionID)
	}
	return nil
}
