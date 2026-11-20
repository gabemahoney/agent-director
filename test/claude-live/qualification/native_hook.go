package qualification

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

type NativePlan struct {
	Schema   int               `json:"schema"`
	Run      string            `json:"run"`
	Root     string            `json:"root"`
	CWD      string            `json:"cwd"`
	Instance string            `json:"instance"`
	Session  string            `json:"session"`
	Route    string            `json:"route"`
	Pointer  string            `json:"pointer"`
	Watch    string            `json:"watch"`
	Marks    map[string]string `json:"marks"`
	Rungs    []Rung            `json:"rungs"`
}
type NativeEmission struct {
	Case    string    `json:"case"`
	Route   string    `json:"route"`
	Session string    `json:"session"`
	Event   string    `json:"event"`
	Started time.Time `json:"started"`
	Written time.Time `json:"written"`
	BodySHA string    `json:"body_sha"`
	Input   string    `json:"input"`
	Output  string    `json:"output"`
	Exit    int       `json:"exit"`
	Error   string    `json:"error,omitempty"`
}

// NativeOutput is an offline seam for the exact native output shapes. The
// provider performs its own transformation; the emitter never pre-escapes it.
func NativeOutput(event, watch, body string) ([]byte, []byte, int, error) {
	switch event {
	case "SessionStart":
		b, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "watchPaths": []string{watch}}})
		return append(b, '\n'), nil, 0, err
	case "FileChanged":
		return nil, []byte(body), 2, nil
	case "PostToolBatch":
		b, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": body}})
		return append(b, '\n'), nil, 0, err
	default:
		return nil, nil, 0, nil
	}
}

func NativeHook(planPath, event string, input io.Reader, out, errout io.Writer) (int, error) {
	if err := runner.ValidateHome(runner.TestHome); err != nil {
		return 0, err
	}
	data, err := runner.Read(filepath.Dir(planPath), filepath.Base(planPath))
	if err != nil {
		return 0, err
	}
	var plan NativePlan
	if json.Unmarshal(data, &plan) != nil || plan.Schema != Schema || plan.Run == "" || !strings.HasPrefix(plan.Instance, "live-"+plan.Run+"-") || filepath.Dir(plan.Root) != runner.VMRoot {
		return 0, errors.New("invalid native plan")
	}
	if filepath.Dir(plan.CWD) != plan.Root || filepath.Dir(planPath) != filepath.Join(plan.Root, "raw") || (plan.Route != "wake" && plan.Route != "post_tool_batch") {
		return 0, errors.New("native plan location/route mismatch")
	}
	if err = nativeRuntime(plan); err != nil {
		return 0, err
	}
	if _, err = runner.Confined(plan.Root, "raw/hooks"); err != nil {
		return 0, err
	}
	data, err = io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return 0, err
	}
	var payload runner.Row
	if json.Unmarshal(data, &payload) != nil || runner.Text(payload, "session_id") != plan.Session || runner.Text(payload, "agent_id") != "" || runner.Text(payload, "hook_event_name") != event || runner.Text(payload, "cwd") != plan.CWD {
		return 0, errors.New("native hook parent/session mismatch")
	}
	runner.RecordOwnedHook(plan.Root, plan.Run, event, bytes.NewReader(data), plan.Instance)
	if event == "SessionStart" && plan.Route == "wake" {
		stdout, _, code, err := NativeOutput(event, plan.Watch, "")
		if err == nil {
			_, err = out.Write(stdout)
		}
		return code, err
	}
	if event != "FileChanged" && event != "PostToolBatch" {
		return 0, nil
	}
	// Serialize output selection, including repeated PostToolBatch callbacks.
	lockPath := plan.Pointer + "-lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return 0, errors.New("concurrent native output callback; no second output emitted")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	var boundary string
	if event == "PostToolBatch" {
		calls, ok := payload["tool_calls"].([]any)
		if !ok || len(calls) == 0 {
			return 0, errors.New("native batch has no tool identities")
		}
		var ids []string
		for _, call := range calls {
			row, ok := call.(map[string]any)
			if !ok {
				return 0, errors.New("invalid native tool identity")
			}
			id, _ := row["tool_use_id"].(string)
			if id == "" {
				return 0, errors.New("missing native tool identity")
			}
			ids = append(ids, id)
		}
		b, er := json.Marshal(ids)
		if er != nil {
			return 0, er
		}
		boundary = runner.Digest(b)
		for _, path := range plan.Marks {
			b, er := os.ReadFile(path)
			if os.IsNotExist(er) {
				continue
			}
			if er != nil {
				return 0, er
			}
			if string(b) == boundary {
				return 0, nil
			}
		}
	}
	var chosen *Rung
	if plan.Route == "wake" {
		if event != "FileChanged" || runner.Text(payload, "file_path") != plan.Watch {
			return 0, nil
		}
		b, err := os.ReadFile(plan.Pointer)
		if err != nil {
			return 0, err
		}
		for i := range plan.Rungs {
			if plan.Rungs[i].ID == strings.TrimSpace(string(b)) {
				chosen = &plan.Rungs[i]
				break
			}
		}
	} else if event == "PostToolBatch" {
		for i := range plan.Rungs {
			if _, err := os.Stat(plan.Marks[plan.Rungs[i].ID]); os.IsNotExist(err) {
				chosen = &plan.Rungs[i]
				break
			}
		}
	}
	if chosen == nil {
		return 0, nil
	}
	mark := plan.Marks[chosen.ID]
	if filepath.Dir(mark) != filepath.Dir(plan.Pointer) {
		return 0, errors.New("native state path outside owned runtime")
	}
	f, err := os.OpenFile(mark, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if _, err = f.WriteString(boundary); err != nil {
		_ = f.Close()
		return 0, err
	}
	if err = f.Close(); err != nil {
		return 0, err
	}
	dir, err := runner.Confined(plan.Root, "raw/native-emissions")
	if err != nil {
		return 0, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return 0, err
	}
	dir, err = os.MkdirTemp(dir, "output-")
	if err != nil {
		return 0, err
	}
	rel, err := filepath.Rel(plan.Root, dir)
	if err != nil {
		return 0, err
	}
	record := NativeEmission{Case: chosen.ID, Route: plan.Route, Session: plan.Session, Event: event, Started: time.Now().UTC(), BodySHA: runner.Digest([]byte(chosen.Body)), Input: rel + "/input.json", Output: rel + "/output.bin"}
	if err = os.WriteFile(filepath.Join(dir, "input.json"), data, 0600); err != nil {
		return 0, err
	}
	stdout, stderr, code, err := NativeOutput(event, plan.Watch, chosen.Body)
	if err != nil {
		return 0, err
	}
	output := stdout
	if len(stderr) > 0 {
		output = stderr
	}
	if err = os.WriteFile(filepath.Join(dir, "output.bin"), output, 0600); err != nil {
		return 0, err
	}
	if len(stdout) > 0 {
		_, err = out.Write(stdout)
	}
	if err == nil && len(stderr) > 0 {
		_, err = errout.Write(stderr)
	}
	record.Written = time.Now().UTC()
	record.Exit = code
	if err != nil {
		record.Error = err.Error()
	}
	meta, marshalErr := json.Marshal(record)
	if marshalErr == nil {
		temporary := filepath.Join(dir, "receipt.tmp")
		marshalErr = os.WriteFile(temporary, append(meta, '\n'), 0600)
		if marshalErr == nil {
			marshalErr = os.Rename(temporary, filepath.Join(dir, "receipt.json"))
		}
	}
	return code, errors.Join(err, marshalErr)
}
