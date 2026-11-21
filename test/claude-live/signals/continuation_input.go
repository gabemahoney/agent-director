package signals

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

// The sole production caller has already bound the Stop, one-shot block and
// causal continuation request. Do not offer this system form to generic intake.
// Capture 6 contributes exactly the queued attachment's rendered text with
// only its outer reminder removed, on the first request after wake sighting.
func (c capture) continuationWakeIntake(continued Request) (Intake, string, error) {
	in, err := c.intake(ContinuationWakeOrder, "wait_turn")
	form := ""
	if err == nil {
		form = "user-system-reminder"
	}
	wake, _, promptErr := c.prompt(ContinuationWakeOrder)
	hook := c.m.ContinuationHook
	if promptErr != nil || c.transcriptErr != nil || hook == nil || continued.ID == "" || in.Time.IsZero() || !beforeMillis(continued.Time, in.Time) || runner.Text(wake, "admission_kind") != "prompt_sighting" {
		return in, form, err
	}
	after := runner.Number(wake, "admitted_at_ms")
	if !beforeMillis(time.UnixMilli(after), in.Time) {
		return in, form, err
	}
	// The passed request must be the already verified preceding continuation,
	// not an arbitrary earlier clock value used to authorize a new input form.
	bound := false
	for _, request := range c.t.Requests {
		if request.ID == continued.ID && request.Time.Equal(continued.Time) {
			bound = true
		}
	}
	if !bound {
		return in, form, err
	}
	prompt := Render(ContinuationWakeOrder, "wait_turn")
	var queued []TranscriptRow
	for _, row := range c.t.Rows {
		if row.Type == "attachment" && row.Attachment.Type == "queued_command" && row.Attachment.Prompt == prompt {
			queued = append(queued, row)
		}
	}
	if len(queued) != 1 {
		return in, form, err
	}
	row, a := queued[0], queued[0].Attachment
	var rendered []struct {
		Content string `json:"content"`
	}
	if a.CommandMode != "task-notification" || a.Origin.Kind != "task-notification" || a.Origin.Producer != "session-task" || json.Unmarshal(row.Rendered, &rendered) != nil || len(rendered) != 1 || rendered[0].Content != NativeWake(prompt) || row.Timestamp.UnixMilli() < runner.Number(wake, "transport_at_ms") || row.Timestamp.After(in.Time) {
		return in, form, err
	}
	// Validate the complete canonical rendering first: changing the label,
	// escaping or wrapper in both the attachment and blob must still refuse.
	want := strings.TrimSuffix(strings.TrimPrefix(rendered[0].Content, "<system-reminder>\n"), "\n</system-reminder>")
	in, err = c.t.nextInput(after, NativeWake(prompt), "user", []inputForm{{role: "system", text: want}})
	if err != nil {
		return in, "", err
	}
	if c.t.Requests[in.Request].Messages[in.Message].Role == "system" {
		return in, "system-stop-continuation", nil
	}
	return in, "user-system-reminder", nil
}
