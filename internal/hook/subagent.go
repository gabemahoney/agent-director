package hook

import (
	"encoding/json"
	"log"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
)

// Subagent capture (b.xjn): the Claude Code half of the SRD's "Agent scope".
// This file reads the payload fields only this feature reads and maps Claude
// Code's SubagentStart, SubagentStop and Agent tool hooks onto the store's
// agent-neutral events. The store applies them under the hook gate; nothing here
// writes a row itself.

// The subagent hook events and the tool whose hooks carry a launch.
const (
	EventNameSubagentStart = "SubagentStart"
	EventNameSubagentStop  = "SubagentStop"
	EventNamePreToolUse    = "PreToolUse"
	// ToolAgent is the tool_name of a subagent launch, matched exactly (Claude
	// Code 2.1.280 names the tool Agent).
	ToolAgent = "Agent"
)

// Launch-result statuses of tool_response.status.
const (
	launchStatusAsync     = "async_launched"
	launchStatusCompleted = "completed"
)

// subagentStore optional extensions of HookStore. *store.Store satisfies them; a
// test double that does not gets today's write.

// subagentLifecycleWriter is the combined gated row write and entry
// transaction of SubagentStart and SubagentStop (store ApplySubagentLifecycle).
type subagentLifecycleWriter interface {
	ApplySubagentLifecycle(instanceID string, gate store.HookGate, ev store.SubagentLifecycleEvent, triggeringEventName, jsonlPath string, jsonlPresent bool) (store.UpsertOutcome, store.HookApplied, error)
}

// subagentAfterWriter is the entry and claimable-launch transaction that follows
// a hook's own applied row write (store ApplySubagentAfterWrite).
type subagentAfterWriter interface {
	ApplySubagentAfterWrite(instanceID string, gate store.HookGate, w store.SubagentAfterWrite) error
}

// subagentTurnEndReader is the plain read behind a main-agent Stop's skip
// decision (store SubagentTurnEndHasWork): it takes the whole turn end, snapshot
// included.
type subagentTurnEndReader interface {
	SubagentTurnEndHasWork(instanceID string, te store.SubagentTurnEnd) (bool, error)
}

// subagentFields are the payload fields only the subagent feature reads. A field
// is supplied when it is present, not null, of the type it is read as and, for a
// string, not empty; one that is not is the zero value, and reading never fails
// (SR-5.1). tool_input and tool_response count only when they are objects. The
// value of tool_input.name, the content fields, an error, counters, outputFile,
// tool_response.agent_id and session_crons are never read.
type subagentFields struct {
	// SessionID is session_id (SubagentStart, PostToolUse(Agent)).
	SessionID string
	// AgentType is agent_type (SubagentStart).
	AgentType string
	// StopHookActive is stop_hook_active (SubagentStop).
	StopHookActive bool
	// InputType is tool_input.subagent_type.
	InputType string
	// InputDescription is tool_input.description.
	InputDescription string
	// InputNamed reports whether tool_input.name is supplied; its value is never
	// kept.
	InputNamed bool
	// ResponseAgentID is tool_response.agentId.
	ResponseAgentID string
	// ResponseStatus is tool_response.status.
	ResponseStatus string
	// ResponseModel is tool_response.resolvedModel.
	ResponseModel string
	// ResponseAgentType is tool_response.agentType, read from a completed result
	// only.
	ResponseAgentType string
}

// readSubagentFields reads the fields of subagentFields from raw. A payload that
// is not a JSON object, or a field of the wrong type, reads as not supplied.
func readSubagentFields(raw json.RawMessage) subagentFields {
	var f subagentFields
	top := rawObject(raw)
	f.SessionID = rawString(top["session_id"])
	f.AgentType = rawString(top["agent_type"])
	f.StopHookActive = rawBool(top["stop_hook_active"])
	if in := rawObject(top["tool_input"]); in != nil {
		f.InputType = rawString(in["subagent_type"])
		f.InputDescription = rawString(in["description"])
		f.InputNamed = rawString(in["name"]) != ""
	}
	if resp := rawObject(top["tool_response"]); resp != nil {
		f.ResponseAgentID = rawString(resp["agentId"])
		f.ResponseStatus = rawString(resp["status"])
		f.ResponseModel = rawString(resp["resolvedModel"])
		if f.ResponseStatus == launchStatusCompleted {
			f.ResponseAgentType = rawString(resp["agentType"])
		}
	}
	return f
}

// rawObject decodes raw as a JSON object, or returns nil when it is not one.
// Values stay raw so a wrongly typed one never fails the decode.
func rawObject(raw json.RawMessage) map[string]json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

// rawString decodes raw as a non-empty JSON string; anything else is "".
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// rawBool decodes raw as a JSON boolean; anything else is false.
func rawBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false
	}
	return b
}

// backgroundTaskType is the `type` of a background_tasks entry that is a
// subagent; backgroundTaskRunning is the `status` of one that is running.
const (
	backgroundTaskSubagent = "subagent"
	backgroundTaskRunning  = "running"
)

// backgroundTaskHead is the only part of any background_tasks entry that is
// decoded first: its type. A non-subagent (teammate) entry is decoded no
// further (SR-5.1). It has no field for a description or any other field
// (SR-5.2, N13). Values stay raw so a wrongly typed one reads as not supplied
// and never fails the decode. (encoding/json matches the key names without
// regard to case.)
type backgroundTaskHead struct {
	Type json.RawMessage `json:"type"`
}

// backgroundTaskSubagentFields is what is decoded, in a second pass, of an entry
// whose type is subagent: its status, id and agent_type. Like
// backgroundTaskHead it has no field for a description or any other field
// (SR-5.2, N13), so none is ever held in memory by this reader.
type backgroundTaskSubagentFields struct {
	Status    json.RawMessage `json:"status"`
	ID        json.RawMessage `json:"id"`
	AgentType json.RawMessage `json:"agent_type"`
}

// readBackgroundTasks reads a Stop's or a SubagentStop's background_tasks
// leniently (SR-5.1, SR-5.2). present is false when the field is absent, null or
// not an array: no snapshot. An array, even an empty one, is present. An element
// that is not an object lists nothing. Each object's `type` is read; only when it
// is `subagent` are its `status` and `id` read too, and it lists a subagent when
// the status is `running` and the id is a non-empty string, and only then is its
// `agent_type` read (empty when not supplied or not a string). No entry's
// description, and no field but `type` of a non-subagent entry, is read. It never
// fails.
func readBackgroundTasks(raw json.RawMessage) (listed []store.SubagentListed, present bool) {
	var entries []json.RawMessage
	if err := json.Unmarshal(rawObject(raw)["background_tasks"], &entries); err != nil || entries == nil {
		return nil, false
	}
	for _, el := range entries {
		var head backgroundTaskHead
		if err := json.Unmarshal(el, &head); err != nil || rawString(head.Type) != backgroundTaskSubagent {
			continue
		}
		var e backgroundTaskSubagentFields
		if err := json.Unmarshal(el, &e); err != nil || rawString(e.Status) != backgroundTaskRunning {
			continue
		}
		id := rawString(e.ID)
		if id == "" {
			continue
		}
		listed = append(listed, store.SubagentListed{ID: id, Type: rawString(e.AgentType)})
	}
	return listed, true
}

// isSubagentLifecycle reports whether res is a SubagentStart or SubagentStop
// that feeds subagent capture (res.SubagentCapture: Claude's hooks only; a
// Codex one is a plain soft refresh with no entry write).
func isSubagentLifecycle(res ClassifyResult) bool {
	return res.SubagentCapture && (res.EventName == EventNameSubagentStart || res.EventName == EventNameSubagentStop)
}

// applySubagentLifecycle is the gated write of a SubagentStart or SubagentStop
// (ApplySubagentLifecycle), with the hook's receipt time. Without an agent_id it
// is the same soft refresh with no entry writes. A start without a type never
// claims; a stop carries stop_hook_active and its background_tasks listing
// (SR-7.6; none when the field is absent or not an array). ok is false when st has no such
// method, and the caller makes the ordinary write.
func applySubagentLifecycle(st HookStore, instanceID string, gate store.HookGate, res ClassifyResult, raw json.RawMessage, receivedMs int64, jsonlPath string, jsonlPresent bool) (outcome store.UpsertOutcome, applied store.HookApplied, ok bool, err error) {
	w, has := st.(subagentLifecycleWriter)
	if !has {
		return "", store.HookApplied{}, false, nil
	}
	ev := store.SubagentLifecycleEvent{ReceivedMs: receivedMs}
	if res.AgentID != "" {
		f := readSubagentFields(raw)
		ev.SubagentID = res.AgentID
		if res.EventName == EventNameSubagentStart {
			ev.Kind = store.SubagentLifecycleStart
			ev.SessionID = f.SessionID
			ev.Type = f.AgentType
		} else {
			ev.Kind = store.SubagentLifecycleStop
			ev.SupersedesBlockedStop = f.StopHookActive
			ev.Listing, _ = readBackgroundTasks(raw)
		}
	}
	outcome, applied, err = w.ApplySubagentLifecycle(instanceID, gate, ev, res.EventName, jsonlPath, jsonlPresent)
	return outcome, applied, true, err
}

// isAgentToolHook reports whether res is a PreToolUse, PostToolUse or
// PostToolUseFailure of the Agent tool that feeds subagent capture.
func isAgentToolHook(res ClassifyResult) bool {
	if !res.SubagentCapture || res.ToolName != ToolAgent {
		return false
	}
	switch res.EventName {
	case EventNamePreToolUse, EventNamePostToolUse, EventNamePostToolUseFailure:
		return true
	}
	return false
}

// isMainAgentStop reports whether res is a Stop with no agent_id that feeds
// subagent capture (a Codex Stop runs no turn-end transaction).
func isMainAgentStop(res ClassifyResult) bool {
	return res.SubagentCapture && res.EventName == EventNameStop && res.AgentID == ""
}

// subagentAfterWrite builds the entry transaction that follows a hook's applied
// row write: the Agent tool's launch request, launch result or launch failure,
// or a main-agent Stop's turn end with its background_tasks snapshot (none when
// the field is absent or not an array). ok is false when the hook has none (any other
// event, a non-Agent tool, a Stop with an agent_id, a request that opens no
// claimable launch, or a result that carries nothing to apply).
func subagentAfterWrite(res ClassifyResult, raw json.RawMessage, receivedMs int64) (w store.SubagentAfterWrite, ok bool) {
	switch {
	case isMainAgentStop(res):
		te := store.SubagentTurnEnd{ReceivedMs: receivedMs}
		if listed, present := readBackgroundTasks(raw); present {
			te.Snapshot = &store.SubagentSnapshot{SessionID: readSubagentFields(raw).SessionID, Listed: listed}
		}
		return store.SubagentAfterWrite{TurnEnd: &te}, true
	case !isAgentToolHook(res):
		return store.SubagentAfterWrite{}, false
	}
	f := readSubagentFields(raw)
	switch res.EventName {
	case EventNamePreToolUse:
		if res.ToolUseID == "" || f.InputType == "" || f.InputNamed {
			return store.SubagentAfterWrite{}, false
		}
		return store.SubagentAfterWrite{LaunchRequest: &store.SubagentLaunchRequest{
			ToolUseID: res.ToolUseID, Type: f.InputType, ReceivedMs: receivedMs,
		}}, true
	case EventNamePostToolUseFailure:
		if res.ToolUseID == "" {
			return store.SubagentAfterWrite{}, false
		}
		return store.SubagentAfterWrite{LaunchFailure: &store.SubagentLaunchFailure{ToolUseID: res.ToolUseID}}, true
	}
	// PostToolUse: the launch result. With no tool_use_id and no agentId (a
	// teammate's) there is nothing to close or record.
	if res.ToolUseID == "" && f.ResponseAgentID == "" {
		return store.SubagentAfterWrite{}, false
	}
	typ := f.InputType
	if typ == "" {
		typ = f.ResponseAgentType
	}
	outcome := store.SubagentLaunchOther
	switch f.ResponseStatus {
	case launchStatusAsync:
		outcome = store.SubagentLaunchBackgroundStarted
	case launchStatusCompleted:
		outcome = store.SubagentLaunchForegroundFinished
	}
	return store.SubagentAfterWrite{LaunchDetails: &store.SubagentLaunchDetails{
		ToolUseID:   res.ToolUseID,
		SubagentID:  f.ResponseAgentID,
		LauncherID:  res.AgentID,
		SessionID:   f.SessionID,
		Type:        typ,
		Description: f.InputDescription,
		Model:       f.ResponseModel,
		Outcome:     outcome,
		ReceivedMs:  receivedMs,
	}}, true
}

// runSubagentAfterWrite is Handle's entry transaction after an applied row write
// (SR-3.3): the Agent tool's launch request, launch result or launch failure,
// and a main-agent Stop's turn end with its snapshot, the latter only when the
// skip read finds something to change (SR-12.2). It is fail-open: a store without the
// methods does nothing, and a failure logs one line carrying the instance id and
// event name and changes nothing else, so the hook's upsert_outcome stays the
// row write's. It adds no sleep, poll or timeout.
func runSubagentAfterWrite(st HookStore, instanceID string, gate store.HookGate, res ClassifyResult, raw json.RawMessage, start time.Time, logger *log.Logger) {
	aw, ok := st.(subagentAfterWriter)
	if !ok {
		return
	}
	receivedMs := start.UnixMilli()
	w, ok := subagentAfterWrite(res, raw, receivedMs)
	if !ok {
		return
	}
	if w.TurnEnd != nil {
		if r, has := st.(subagentTurnEndReader); has {
			work, err := r.SubagentTurnEndHasWork(instanceID, *w.TurnEnd)
			if err != nil {
				logf(logger, "hook: subagent turn-end read (instance=%s, event=%s): %v", instanceID, res.EventName, err)
				return
			}
			if !work {
				return
			}
		}
	}
	if err := aw.ApplySubagentAfterWrite(instanceID, gate, w); err != nil {
		logf(logger, "hook: subagent entry transaction (instance=%s, event=%s): %v", instanceID, res.EventName, err)
	}
}

var _ subagentLifecycleWriter = (*store.Store)(nil)
var _ subagentAfterWriter = (*store.Store)(nil)
var _ subagentTurnEndReader = (*store.Store)(nil)
