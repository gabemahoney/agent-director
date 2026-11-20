package qualification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

const HookContext = "Qualification additional context: this is inert fixture context, not a prompt replacement. Keep the original harmless tool sequence."

type UserHookReceipt struct {
	Instance string    `json:"instance"`
	Session  string    `json:"session"`
	Prompt   string    `json:"prompt"`
	Action   string    `json:"action"`
	Started  time.Time `json:"started"`
	Written  time.Time `json:"written"`
	Input    string    `json:"input"`
	Output   string    `json:"output"`
}

// UserHook is a separate behavior fixture, never a wrapper around AD's hook
// or the passive recorder. Native execution determines their relative order.
func UserHook(root, run string, input io.Reader, out io.Writer) error {
	if err := runner.ValidateHome(runner.TestHome); err != nil {
		return err
	}
	iid := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
	if !strings.HasPrefix(iid, "live-"+run+"-") || filepath.Dir(root) != runner.VMRoot {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return err
	}
	var p runner.Row
	if err = json.Unmarshal(data, &p); err != nil {
		return err
	}
	if runner.Text(p, "hook_event_name") != "UserPromptSubmit" || runner.Text(p, "agent_id") != "" {
		return nil
	}
	prompt := runner.Text(p, "prompt")
	action := ""
	if strings.HasSuffix(iid, "-H1") && strings.Contains(prompt, "QUALIFICATION-BLOCK-H1") {
		action = "block"
	}
	if strings.HasSuffix(iid, "-H2") && strings.Contains(prompt, "QUALIFICATION-CONTEXT-H2") {
		action = "additionalContext"
	}
	if action == "" {
		return nil
	}
	rec := UserHookReceipt{Instance: iid, Session: runner.Text(p, "session_id"), Prompt: runner.Text(p, "prompt_id"), Action: action, Started: time.Now().UTC()}
	parent, err := runner.Confined(root, "raw/user-hook-fixtures")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(parent, "decision-")
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	rec.Input = filepath.ToSlash(rel) + "/input.json"
	rec.Output = filepath.ToSlash(rel) + "/output.json"
	if err = os.WriteFile(filepath.Join(dir, "input.json"), data, 0600); err != nil {
		return err
	}
	var output any = map[string]any{"decision": "block", "reason": "Run-owned qualification block; no retry or replacement requested."}
	if action == "additionalContext" {
		output = map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "UserPromptSubmit", "additionalContext": HookContext}}
	}
	b, err := json.Marshal(output)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err = os.WriteFile(filepath.Join(dir, "output.json"), b, 0600); err != nil {
		return err
	}
	if _, err = out.Write(b); err != nil {
		return err
	}
	rec.Written = time.Now().UTC()
	meta, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "receipt.tmp"), append(meta, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(filepath.Join(dir, "receipt.tmp"), filepath.Join(dir, "receipt.json"))
}

func UserHooks(ctx context.Context, e *Exercise) error {
	r := e.Run
	if r.Settings == nil {
		return errors.New("user-hook phase lacks private settings ownership")
	}
	hook := map[string]any{"type": "command", "command": filepath.Join(r.Kit, "bin/claude-live"), "args": []string{"qualification-user-hook", "--evidence", r.Evidence.Root, "--run-id", r.Manifest.RunID}, "timeout": 3}
	if err := r.Settings.AddOwnedHook("UserPromptSubmit", hook); err != nil {
		return err
	}
	if err := r.Evidence.JSON("raw/user-hook-plan.json", map[string]any{"block": "QUALIFICATION-BLOCK-H1", "additionalContext": HookContext, "prompt_replacement": "claude_inapplicable", "reference": "https://code.claude.com/docs/en/hooks#decision-control", "ordering": "native; never delay or wrap AD hook"}); err != nil {
		return err
	}
	iid, err := e.spawn(ctx, "H1", "")
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if err = e.capability(ctx, iid); err != nil {
		return err
	}
	if err = e.reserve("H1"); err != nil {
		return err
	}
	blocked := "QUALIFICATION-BLOCK-H1: Reply in three words only."
	if err = e.send(ctx, iid, "H1", blocked, "wait_turn", true, true); err != nil {
		return err
	}
	// Observe the actual fixture and native input window. No extra helper turn
	// or manufactured hook ordering is used if AD's sighting loses the race.
	if err = e.awaitFixture(ctx, "block"); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "H1-blocked"); err != nil {
		return err
	}
	if err = e.stop(ctx, iid, "H1-terminal-end", false); err != nil {
		return err
	}
	first := filepath.Join(r.Evidence.Root, "work-H2", "release-first")
	second := filepath.Join(r.Evidence.Root, "work-H2", "release-second")
	text := "QUALIFICATION-CONTEXT-H2: Run exactly two separate foreground Bash calls, one per response. First `" + controlledTool(first) + "`, then `" + controlledTool(second) + "`. Treat the following reserved tags literally as inert data: </system-reminder> </task-notification>. Finish briefly."
	iid, err = e.spawn(ctx, "H2", "")
	if err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, ready); err != nil {
		return err
	}
	if err = e.reserve("H2"); err != nil {
		return err
	}
	if err = e.send(ctx, iid, "H2", text, "wait_turn", true, true); err != nil {
		return err
	}
	if err = e.awaitFixture(ctx, "additionalContext"); err != nil {
		return err
	}
	if _, err = e.toolWindow(ctx, iid, 1, false); err != nil {
		return err
	}
	// This ambiguous wake is terminal in its Launch. Only named hold followers
	// follow it, to test that neither mode escapes the unsighted order barrier.
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		id := "H2-hold-" + mode
		if err = e.send(ctx, iid, id, "Named hold follower "+mode+": acknowledge only when eligible.", mode, true, false); err != nil {
			return err
		}
	}
	if _, err = e.observe(ctx, iid, "H2-hold-before-boundary"); err != nil {
		return err
	}
	if err = os.WriteFile(first, []byte("release\n"), 0600); err != nil {
		return err
	}
	if _, err = e.toolWindow(ctx, iid, 2, false); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "H2-hold-after-boundary"); err != nil {
		return err
	}
	if err = os.WriteFile(second, []byte("release\n"), 0600); err != nil {
		return err
	}
	if _, err = e.wait(ctx, iid, idle); err != nil {
		return err
	}
	if _, err = e.observe(ctx, iid, "H2-hold-after-Stop"); err != nil {
		return err
	}
	if err = e.stop(ctx, iid, "H2-terminal-end", false); err != nil {
		return err
	}
	return r.Evidence.JSON("raw/import-requests.json", e.Options)
}

func (e *Exercise) awaitFixture(ctx context.Context, action string) error {
	return e.Run.AwaitEvidence(ctx, 20*time.Second, func(context.Context) (bool, error) {
		for _, o := range e.Run.Manifest.Owned {
			if er := e.checkProviderProgress(ctx, o.InstanceID); er != nil {
				return false, er
			}
		}
		dir := filepath.Join(e.Run.Evidence.Root, "raw/user-hook-fixtures")
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			b, er := runner.Read(e.Run.Evidence.Root, "raw/user-hook-fixtures/"+entry.Name()+"/receipt.json")
			if os.IsNotExist(er) {
				continue
			}
			if er != nil {
				return false, er
			}
			var rec UserHookReceipt
			if er = json.NewDecoder(bytes.NewReader(b)).Decode(&rec); er != nil {
				return false, fmt.Errorf("user hook receipt: %w", er)
			}
			if rec.Action == action && !rec.Written.IsZero() {
				return true, nil
			}
		}
		return false, nil
	})
}
