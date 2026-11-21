package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// This is test input, not a product hook or a delivery marker. The next
// request must retain this exact instruction to prove a Stop continuation.
const ContinuationReason = "Continue this bounded Stop probe once. Use no tools. Reply with exactly CONTINUATION-DONE, then stop."
const ContinuationHeldSteer = "held-steer-v1"
const ContinuationToolProgram = "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do sleep 1; done"
const ContinuationToolReason = "Continue this bounded Stop probe once. Run exactly one foreground Bash call with this program:\n```bash\n" + ContinuationToolProgram + "\n```\nDo not background it. Then reply exactly CONTINUATION-DONE and finish."

func ContinuationInstruction(variant string) (string, error) {
	switch variant {
	case "":
		return ContinuationReason, nil
	case ContinuationHeldSteer:
		return ContinuationToolReason, nil
	default:
		return "", errors.New("unknown continuation variant")
	}
}

type ContinuationHook struct {
	InstanceID string `json:"instance_id"`
	CWD        string `json:"cwd"`
	Command    string `json:"command"`
	SHA256     string `json:"sha256"`
	Installed  bool   `json:"installed"`
	Removed    bool   `json:"removed"`
	Variant    string `json:"variant,omitempty"`
}

type ContinuationReceipt struct {
	Receipt
	Decision string `json:"decision"`
}

func (r *Run) StartContinuation(id, cwd string) error {
	return r.StartContinuationVariant(id, cwd, "")
}

func (r *Run) StartContinuationVariant(id, cwd, variant string) error {
	if _, err := ContinuationInstruction(variant); err != nil {
		return err
	}
	if err := validateHome(r.Home, filepath.Join(r.scopeRoot(), "home")); err != nil {
		return err
	}
	if cwd != filepath.Join(r.Evidence.Root, "work-continuation") || id != "live-"+r.Manifest.RunID+"-continuation" || !r.Manifest.RecordingHook.Installed {
		return errors.New("continuation requires its isolated owned launch")
	}
	h := ContinuationHook{InstanceID: id, CWD: cwd, Command: r.Manifest.RecordingHook.Command, SHA256: r.Manifest.RecordingHook.SHA256, Variant: variant}
	args := []string{"continue-stop", "--evidence", r.Evidence.Root, "--run-id", r.Manifest.RunID}
	if variant != "" {
		args = append(args, "--variant", variant)
	}
	settings := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": h.Command, "args": args, "timeout": 3}}}}}}
	if err := r.Evidence.JSON("raw/continuation-settings.json", settings); err != nil {
		return err
	}
	b, err := Read(r.Evidence.Root, "raw/continuation-settings.json")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(r.Evidence.Root, "raw/continuation/events"), 0700); err != nil {
		return err
	}
	if err = r.Evidence.Put("work-continuation/.claude/settings.local.json", b); err != nil {
		return err
	}
	h.Installed = true
	r.Manifest.ContinuationHook = &h
	return nil
}

// Arm after SessionStart has bound the launch, while the initial tool runs.
func (r *Run) ArmContinuation(o Owned) error {
	h := r.Manifest.ContinuationHook
	if h == nil || !h.Installed || h.InstanceID != o.InstanceID || h.CWD != o.CWD || o.SessionID == "" || o.LaunchToken == "" || o.LaunchID == "" {
		return errors.New("unbound continuation owner")
	}
	path, err := TranscriptPath(r.Home, o.CWD, o.SessionID, o.Transcript)
	if err != nil {
		return err
	}
	o.Transcript = path
	return r.Evidence.JSON("raw/continuation-owner.json", o)
}

func (r *Run) RemoveContinuation() error {
	h := r.Manifest.ContinuationHook
	if h == nil || !h.Installed {
		return nil
	}
	const name = "work-continuation/.claude/settings.local.json"
	b, err := Read(r.Evidence.Root, name)
	if err != nil {
		return err
	}
	want, err := Read(r.Evidence.Root, "raw/continuation-settings.json")
	if err != nil {
		return err
	}
	if !bytes.Equal(b, want) {
		return errors.New("continuation settings changed; retained for inspection")
	}
	p, err := Confined(r.Evidence.Root, name)
	if err != nil {
		return err
	}
	if err = os.Remove(p); err != nil {
		return err
	}
	h.Removed = true
	return nil
}

// ContinueStop consumes an exclusive one-shot before emitting any decision.
// Subsequent Stops (including stop_hook_active) are silent. The CLI also has
// a hard two-second deadline, so this sibling cannot delay AD indefinitely.
func ContinueStop(root, runID string, input io.Reader, output io.Writer, variants ...string) {
	variant := ""
	if len(variants) > 1 {
		return
	}
	if len(variants) == 1 {
		variant = variants[0]
	}
	reason, err := ContinuationInstruction(variant)
	if err != nil {
		return
	}
	if runID == "" {
		return
	}
	b, err := Read(root, "raw/continuation-owner.json")
	var o Owned
	if err != nil || json.Unmarshal(b, &o) != nil || o.InstanceID != "live-"+runID+"-continuation" || os.Getenv("AGENT_DIRECTOR_INSTANCE_ID") != o.InstanceID || o.CWD != filepath.Join(root, "work-continuation") {
		return
	}
	b, err = io.ReadAll(io.LimitReader(input, 1<<20+1))
	var p Row
	if err != nil || len(b) > 1<<20 || json.Unmarshal(b, &p) != nil || Text(p, "hook_event_name") != "Stop" || Text(p, "session_id") != o.SessionID || Text(p, "cwd") != o.CWD || Text(p, "transcript_path") != o.Transcript || Text(p, "agent_id") != "" || Text(p, "prompt_id") == "" {
		return
	}
	active, ok := p["stop_hook_active"].(bool)
	if !ok {
		return
	}
	base, err := Confined(root, "raw/continuation/events")
	if err != nil {
		return
	}
	dir, err := os.MkdirTemp(base, "invocation-")
	if err != nil {
		return
	}
	rec := ContinuationReceipt{Receipt: Receipt{Schema: Schema, RunID: runID, Event: "Stop", Received: time.Now().UTC(), Input: "input.json", SHA256: Digest(b), Bytes: len(b)}}
	if err = os.WriteFile(filepath.Join(dir, "input.json"), b, 0600); err != nil {
		return
	}
	decision := []byte{}
	if !active {
		used, e := Confined(root, "raw/continuation/used")
		if e != nil {
			return
		}
		f, e := os.OpenFile(used, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e == nil {
			if f.Close() != nil {
				return
			}
			rec.Decision = "block"
			decision, _ = json.Marshal(map[string]string{"decision": "block", "reason": reason})
		} else if !os.IsExist(e) {
			return
		}
	}
	if err = os.WriteFile(filepath.Join(dir, "output.json"), decision, 0600); err != nil {
		return
	}
	rec.Finished, rec.Complete = time.Now().UTC(), true
	meta, err := json.Marshal(rec)
	if err != nil || os.WriteFile(filepath.Join(dir, "receipt.json"), meta, 0600) != nil {
		return
	}
	_, _ = output.Write(decision)
}
