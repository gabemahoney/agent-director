package signals

import (
	"encoding/json"
	"strings"
)

type TranscriptAttachment struct {
	Type        string          `json:"type"`
	Event       string          `json:"hookEvent"`
	Stdout      string          `json:"stdout"`
	Content     json.RawMessage `json:"content"`
	Prompt      string          `json:"prompt"`
	CommandMode string          `json:"commandMode"`
	HumanTurn   bool            `json:"humanTurn"`
	Origin      struct {
		Kind     string `json:"kind"`
		Producer string `json:"producer"`
	} `json:"origin"`
	AddedNames        []string          `json:"addedNames"`
	AddedLines        []string          `json:"addedLines"`
	SurfacedNames     []string          `json:"surfacedNames"`
	AddedBlocks       []string          `json:"addedBlocks"`
	RemovedNames      []string          `json:"removedNames"`
	WireHiddenNames   []string          `json:"wireHiddenNames"`
	ReaddedNames      []string          `json:"readdedNames"`
	PendingMCPServers []json.RawMessage `json:"pendingMcpServers"`
	FailedMCPServers  []json.RawMessage `json:"failedMcpServers"`
}

// mergedForm reconstructs capture 4's tool-boundary system text from independent
// attachment records and the original probe inputs. It never extracts a prompt
// from a substring of the model request. NextInput still requires equality of
// the entire text block, including the unrelated tool/MCP context and separators.
// Attachments alone cannot establish intake: the request must contain this form.
func (t Transcript) mergedForm(after int64, text string) (inputForm, bool) {
	index := -1
	for i, req := range t.Requests {
		if req.Time.UnixMilli() >= after {
			index = i
			break
		}
	}
	if index <= 0 || (text != Steer && text != OverlapWake) {
		return inputForm{}, false
	}
	previous, request := t.Requests[index-1], t.Requests[index]
	endLine := 0
	for _, row := range t.Rows {
		if row.Line > request.Line && (row.Type == "api-request" || (row.Type == "assistant" && row.RequestRef == request.ID)) {
			endLine = row.Line
			break
		}
	}
	if endLine == 0 {
		return inputForm{}, false
	}
	var counts [4]int
	var tools, instructions TranscriptAttachment
	for _, row := range t.Rows {
		// The provider writes some attachments after their request row. Use
		// their native timestamp within this request interval, not disk order.
		if row.Type != "attachment" || row.Line <= previous.Line || row.Line >= endLine || !row.Timestamp.After(previous.Time) || row.Timestamp.After(request.Time) {
			continue
		}
		a := row.Attachment
		switch a.Type {
		case "hook_additional_context":
			var content []string
			if a.Event != "PostToolBatch" || json.Unmarshal(a.Content, &content) != nil || len(content) != 1 || content[0] != Render(Steer, "steer_turn") {
				return inputForm{}, false
			}
			counts[0]++
		case "queued_command":
			if a.Prompt != Render(OverlapWake, "wait_turn") || a.CommandMode != "task-notification" || a.Origin.Kind != "task-notification" || a.Origin.Producer != "session-task" {
				return inputForm{}, false
			}
			counts[1]++
		case "deferred_tools_delta":
			tools = a
			counts[2]++
		case "mcp_instructions_delta":
			instructions = a
			counts[3]++
		}
	}
	if counts != [4]int{1, 1, 1, 1} || len(instructions.RemovedNames) != 0 || len(instructions.AddedBlocks) == 0 || len(instructions.AddedBlocks) != len(instructions.AddedNames) {
		return inputForm{}, false
	}
	toolText, ok := addedToolsText(tools)
	if !ok {
		return inputForm{}, false
	}
	steer := NativeContext(Steer)
	wake := nativeQueuedWake(Render(OverlapWake, "wait_turn"))
	parts := []string{steer, wake, toolText,
		"# MCP Server Instructions\n\nThe following MCP servers have provided instructions for how to use their tools and resources:\n\n" + strings.Join(instructions.AddedBlocks, "\n\n")}
	form := inputForm{role: "system", text: strings.Join(parts, "\n\n")}
	if text == OverlapWake {
		form.offset = len(steer) + 2
	}
	return form, true
}

// Only the measured additions-only delta is supported. No arbitrary suffix of
// the request is ignored, and no account-specific tool names are compiled in.
func addedToolsText(a TranscriptAttachment) (string, bool) {
	if len(a.AddedNames) == 0 || len(a.AddedNames) != len(a.AddedLines) || len(a.RemovedNames)+len(a.WireHiddenNames)+len(a.ReaddedNames)+len(a.PendingMCPServers)+len(a.FailedMCPServers) != 0 {
		return "", false
	}
	surfaced := map[string]bool{}
	for _, name := range a.SurfacedNames {
		if surfaced[name] {
			return "", false
		}
		surfaced[name] = true
	}
	seen := map[string]bool{}
	var ready, deferred []string
	for i, name := range a.AddedNames {
		if name == "" || seen[name] || a.AddedLines[i] != name || strings.ContainsAny(name, "\r\n") {
			return "", false
		}
		seen[name] = true
		if surfaced[name] {
			ready = append(ready, name)
		} else {
			deferred = append(deferred, name)
		}
	}
	if len(ready) == 0 || len(ready) != len(surfaced) || len(deferred) == 0 {
		return "", false
	}
	return "The following tools just became available and are ready to use:\n" + strings.Join(ready, "\n") +
		"\n\nThe following deferred tools are now available via ToolSearch. Their schemas are NOT loaded — calling them directly will fail with InputValidationError. Use ToolSearch with query \"select:<name>[,<name>...]\" to load tool schemas before calling them:\n" + strings.Join(deferred, "\n"), true
}

// mergedBody reconstructs the measured additions-only tool/MCP frame for a
// single known steer body. It never learns a successful suffix from a request.
func (t Transcript) mergedBody(after int64, body string) (inputForm, bool) {
	index := -1
	for i, q := range t.Requests {
		if q.Time.UnixMilli() >= after {
			index = i
			break
		}
	}
	if index <= 0 {
		return inputForm{}, false
	}
	previous, request := t.Requests[index-1], t.Requests[index]
	end := 0
	for _, row := range t.Rows {
		if row.Line > request.Line && (row.Type == "api-request" || (row.Type == "assistant" && row.RequestRef == request.ID)) {
			end = row.Line
			break
		}
	}
	if end == 0 {
		return inputForm{}, false
	}
	count := 0
	var tools, instructions *TranscriptAttachment
	for _, row := range t.Rows {
		if row.Type != "attachment" || row.Line <= previous.Line || row.Line >= end || !row.Timestamp.After(previous.Time) || row.Timestamp.After(request.Time) {
			continue
		}
		a := row.Attachment
		switch a.Type {
		case "hook_additional_context":
			var content []string
			if a.Event != "PostToolBatch" || json.Unmarshal(a.Content, &content) != nil || len(content) != 1 || content[0] != RenderBody(body, "steer_turn") {
				return inputForm{}, false
			}
			count++
		case "queued_command":
			return inputForm{}, false // needs all separately known inputs, as ci's existing mergedForm does
		case "deferred_tools_delta":
			if tools != nil {
				return inputForm{}, false
			}
			copy := a
			tools = &copy
		case "mcp_instructions_delta":
			if instructions != nil {
				return inputForm{}, false
			}
			copy := a
			instructions = &copy
		}
	}
	if count != 1 {
		return inputForm{}, false
	}
	parts := []string{NativeContextBody(body)}
	if tools != nil {
		text, ok := addedToolsText(*tools)
		if !ok {
			return inputForm{}, false
		}
		parts = append(parts, text)
	}
	if instructions != nil {
		a := *instructions
		if len(a.RemovedNames) != 0 || len(a.AddedBlocks) == 0 || len(a.AddedBlocks) != len(a.AddedNames) {
			return inputForm{}, false
		}
		parts = append(parts, "# MCP Server Instructions\n\nThe following MCP servers have provided instructions for how to use their tools and resources:\n\n"+strings.Join(a.AddedBlocks, "\n\n"))
	}
	return inputForm{role: "system", text: strings.Join(parts, "\n\n")}, true
}

// NextBodyInput uses the same request journal and measured forward forms for
// a frozen multi-Message body. No substring, normalization or echo proof.
func (t Transcript) NextBodyInput(after int64, body, mode string) (Intake, error) {
	want, role := NativeWake(RenderBody(body, "wait_turn")), "user"
	var additional []inputForm
	if mode == "steer_turn" {
		want, role = NativeContextBody(body), "system"
		if form, ok := t.mergedBody(after, body); ok && (form.role != role || form.text != want || form.offset != 0) {
			additional = append(additional, form)
		}
	}
	return t.nextInput(after, want, role, additional)
}
