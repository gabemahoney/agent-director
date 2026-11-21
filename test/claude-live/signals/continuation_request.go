package signals

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// A request can be journalled before the assistant row whose Stop starts it
// (capture 5, request 46 / terminal 47). Link their conversation content, not
// their file positions. Only the request's own tail can introduce feedback;
// a later request retaining it through keep/base is not another continuation.
func (c capture) continuationRequest(active string, terminal int, release int64, until time.Time) (Request, map[string]any, error) {
	observed := map[string]any{}
	journal := newJournalMessages()
	for _, row := range c.t.Rows {
		if row.Type == "assistant" && row.RequestRef == active && row.Line <= terminal {
			if err := journal.add(row); err != nil {
				return Request{}, observed, err
			}
		}
	}
	response := journal.responses[active]
	if response == nil || response.hash == "" {
		return Request{}, observed, errors.New("missing terminal conversation message")
	}
	h := c.m.ContinuationHook // continuationCalls already verified this registration.
	if h == nil {
		return Request{}, observed, errors.New("missing verified continuation hook")
	}
	reason, err := c.continuationInstruction()
	if err != nil {
		return Request{}, observed, err
	}
	feedback := "Stop hook feedback:\n" + reason
	command := h.Command + " continue-stop --evidence " + filepath.Dir(h.CWD) + " --run-id " + c.m.RunID
	if h.Variant != "" {
		command += " --variant " + h.Variant
	}
	blocking := fmt.Sprintf("Stop hook blocking error from command: %q: %s", command, reason)
	var found Request
	candidates := []map[string]any{}
	var invalid error
	for _, request := range c.t.Requests {
		if request.Time.UnixMilli() <= release || request.Time.After(until) {
			continue
		}
		keep, rows := -1, 0
		for _, row := range c.t.Rows {
			if row.Type == "api-request" && row.ID == request.ID {
				keep, rows = row.Keep, rows+1
			}
		}
		if rows != 1 || keep < 0 || keep > len(request.Messages) {
			return Request{}, observed, errors.New("missing request tail boundary")
		}
		terminalAt, terminals := -1, 0
		for i, message := range request.Messages {
			if message.Role != "assistant" {
				continue
			}
			hash, err := journalHash(Message{Role: message.Role, Content: message.Content})
			if err != nil {
				return Request{}, observed, err
			}
			if hash == response.hash {
				terminalAt, terminals = i, terminals+1
			}
		}
		feedbacks, errorsSeen, markers := 0, 0, 0
		feedbackAt, errorAt := -1, -1
		valid := true
		for i := keep; i < len(request.Messages); i++ {
			message := request.Messages[i]
			texts, err := Texts(message)
			if err != nil {
				return Request{}, observed, err
			}
			for _, text := range texts {
				if !strings.Contains(text, reason) && !strings.HasPrefix(text, "Stop hook feedback:") && !strings.HasPrefix(text, "Stop hook blocking error from command:") {
					continue
				}
				markers++
				switch {
				case message.Role == "user" && text == feedback:
					feedbacks++
					feedbackAt = i
				case message.Role == "system" && text == blocking:
					errorsSeen++
					errorAt = i
				default:
					valid = false
				}
			}
		}
		if markers == 0 {
			continue
		}
		candidate := map[string]any{"request_line": request.Line, "request_id": request.ID, "request_time": request.Time, "new_input_start": keep, "terminal_message": terminalAt, "terminal_matches": terminals, "feedback_message": feedbackAt, "blocking_error_message": errorAt, "feedback_blocks": feedbacks, "blocking_error_blocks": errorsSeen, "new_instruction_blocks": markers}
		candidates = append(candidates, candidate)
		if !valid || feedbacks != 1 || errorsSeen != 1 || markers != 2 {
			invalid = errors.New("continuation request has changed or duplicated hook feedback")
		} else if terminals != 1 || terminalAt >= feedbackAt || feedbackAt >= errorAt {
			invalid = errors.New("continuation feedback does not follow the unique terminal message in its conversation")
		}
		found = request
	}
	observed["candidates"] = candidates
	if len(candidates) != 1 {
		return Request{}, observed, errors.New("missing or ambiguous continuation request")
	}
	if invalid != nil {
		return Request{}, observed, invalid
	}
	return found, observed, nil
}
