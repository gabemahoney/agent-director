package runner

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// HookInvocation retains even an empty directory left by the recorder's hard
// deadline. A missing receipt must never disappear from the artifact index.
type HookInvocation struct {
	Directory string `json:"directory"`
	Complete  bool   `json:"complete"`
	Reason    string `json:"reason,omitempty"`
}

// InspectRecordings is file-only and is shared by final collection and offline
// validation. Every invocation must contain exactly one receipt/input pair.
func InspectRecordings(root, runID string, events []string) ([]HookInvocation, error) {
	p, err := Confined(root, "raw/hooks")
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	out := []HookInvocation{}
	for _, entry := range entries {
		ref := "raw/hooks/" + entry.Name()
		inv := HookInvocation{Directory: ref}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "invocation-") {
			inv.Reason = "unexpected recorder entry"
		} else if err := inspectInvocation(root, ref, runID, events); err != nil {
			inv.Reason = err.Error()
		} else {
			inv.Complete = true
		}
		out = append(out, inv)
	}
	return out, nil
}

func inspectInvocation(root, ref, runID string, events []string) error {
	p, err := Confined(root, ref)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	if len(files) != 2 || files[0].Name() != "input.json" || files[1].Name() != "receipt.json" {
		return errors.New("recorder invocation lacks an exact input/receipt pair")
	}
	b, err := Read(root, filepath.ToSlash(filepath.Join(ref, "receipt.json")))
	if err != nil {
		return err
	}
	var receipt Receipt
	if json.Unmarshal(b, &receipt) != nil || receipt.Schema != Schema || receipt.RunID != runID || !slices.Contains(events, receipt.Event) || !receipt.Complete || receipt.Input != "input.json" || receipt.Received.IsZero() || receipt.Finished.Before(receipt.Received) {
		return errors.New("incomplete or mismatched recorder receipt")
	}
	b, err = Read(root, ref+"/input.json")
	if err != nil {
		return err
	}
	if Digest(b) != receipt.SHA256 || len(b) != receipt.Bytes {
		return errors.New("recorded input hash/size mismatch")
	}
	var payload Row
	if json.Unmarshal(b, &payload) != nil || Text(payload, "hook_event_name") != receipt.Event {
		return errors.New("recorded input event differs from receipt")
	}
	return nil
}
