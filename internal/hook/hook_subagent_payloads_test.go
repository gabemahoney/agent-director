package hook_test

// hook_subagent_payloads_test.go — the payload half of the shared helpers of the
// subagent-capture handler tests (b.xjn, SRD SR-15.1, SR-15.3):
// hook_subagent_records_test.go, hook_subagent_claims_test.go,
// hook_subagent_inputs_test.go and hook_subagent_safety_test.go. The row, store
// and script half is in hook_subagent_helpers_test.go.
//
//   - Fixture names and the ids they carry (the fx* and sub* constants).
//   - variant: a fixture with keys set or removed, nested ones included, so no
//     test writes a JSON literal. The builders after it (nestedRequest,
//     secondStart, ...) are the SR-15.1 variants every test file uses.
//   - sentinelPayload, oversized, longOf and longVariant: the no-content, 1 MiB
//     and 1 KiB tests' inputs.

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/hook"
)

// The subagent fixtures (SRD Appendix B), by their B number.
const (
	fxStart           = "subagent-start.json"                // B1
	fxStartTeammate   = "subagent-start-teammate.json"       // B2
	fxStop            = "subagent-stop.json"                 // B3
	fxStopInternal    = "subagent-stop-internal.json"        // B4
	fxResult          = "post-tool-use-agent.json"           // B5
	fxResultNested    = "post-tool-use-agent-nested.json"    // B6
	fxResultTeammate  = "post-tool-use-agent-teammate.json"  // B7
	fxMainStop        = "stop-background-tasks.json"         // B8
	fxRequest         = "pre-tool-use-agent.json"            // B9
	fxRequestTeammate = "pre-tool-use-agent-teammate.json"   // B10
	fxCompleted       = "post-tool-use-agent-completed.json" // B11
	fxFailureBase     = "post-tool-use-failure.json"         // the baseline's, made an Agent failure by agentFailure
)

// The ids and texts the subagent fixtures carry; TestSubagentFixtureShapes
// pins them against the files.
const (
	subSessionID       = "9b2e4c71-3f58-4d0a-8e6b-1a7c5d9f2e40"
	subAgentID         = "a3f9c2e81b7d4056" // B1's, B3's, B5's and B11's subagent
	subTeammateID      = "ahelper-5c1e9a7b3d2f8406"
	subChildID         = "a8d4e7b1c0f26a93" // B6's child
	subInternalID      = "a2b6d9f04e8c1735" // B4's
	subToolUseID       = "toolu_01BgLaunchAbc123Xyz"
	subNestedToolUseID = "toolu_01NestedLaunchDef456"
	subTeamToolUseID   = "toolu_01TeamSpawnGhi789Uvw"
	subModel           = "fixture-model-v1"
	subLaunchDesc      = "Background probe launch"
	subNestedDesc      = "Nested probe launch"
	subListingDesc     = "Claude Code label for the task"
	subType            = "general-purpose"
	subTeammateType    = "helper"

	// The second launch of the claim tests, which no fixture holds.
	subSecondAgentID   = "a5c7e9b1d3f20486"
	subSecondToolUseID = "toolu_01SecondLaunchJkl012"
	subSecondDesc      = "Second probe launch"
	subSecondModel     = "fixture-model-v2"

	// subSentinel is the text sentinelPayload puts in every content and never-read
	// field. No fixture holds it.
	subSentinel = "SENTINEL-CONTENT-4e8b1d7a"
)

// subEpoch is the receipt time of step 0; at(n) is n steps later. Steps are
// ten seconds apart, so each is past the 2 s grace of the one before.
const subEpoch int64 = 1_790_000_000_000

func at(step int64) int64 { return subEpoch + step*10_000 }

// ---------------------------------------------------------------------------
// Payload variants

// payloadEdit changes a decoded payload.
type payloadEdit func(m map[string]any)

// setKey sets the dotted path (such as "tool_response.status") to value,
// creating the objects on the way.
func setKey(path string, value any) payloadEdit {
	return func(m map[string]any) {
		parts := strings.Split(path, ".")
		for _, p := range parts[:len(parts)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[p] = next
			}
			m = next
		}
		m[parts[len(parts)-1]] = value
	}
}

// delKey removes the dotted path; a path that is not there is left as it is.
func delKey(path string) payloadEdit {
	return func(m map[string]any) {
		parts := strings.Split(path, ".")
		for _, p := range parts[:len(parts)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				return
			}
			m = next
		}
		delete(m, parts[len(parts)-1])
	}
}

// getKey reads the dotted path of m.
func getKey(m map[string]any, path string) (any, bool) {
	var v any = m
	for _, p := range strings.Split(path, ".") {
		obj, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok = obj[p]; !ok {
			return nil, false
		}
	}
	return v, true
}

// emptyListing is a SubagentStop's or Stop's empty background_tasks (F12).
func emptyListing() payloadEdit { return setKey("background_tasks", []any{}) }

// taskEntry is one running subagent of a background_tasks listing, with the
// fixture's description; overrides replace or add keys (a type, a status, a
// wrong-typed id).
func taskEntry(id, agentType string, overrides map[string]any) map[string]any {
	e := map[string]any{"id": id, "type": "subagent", "status": "running", "agent_type": agentType, "description": subListingDesc}
	for k, v := range overrides {
		e[k] = v
	}
	return e
}

// tasks sets background_tasks to entries (taskEntry maps, or any other element a
// test needs).
func tasks(entries ...any) payloadEdit { return setKey("background_tasks", entries) }

// decodeFixture reads fixture as a map.
func decodeFixture(t *testing.T, fixture string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readPayloadFixture(t, fixture), &m); err != nil {
		t.Fatalf("fixture %s: %v", fixture, err)
	}
	return m
}

// fixtureValue is the value of the dotted path in fixture.
func fixtureValue(t *testing.T, fixture, path string) any {
	t.Helper()
	v, ok := getKey(decodeFixture(t, fixture), path)
	if !ok {
		t.Fatalf("fixture %s has no %q", fixture, path)
	}
	return v
}

// variant returns fixture with edits applied, as a payload. Its transcript_path
// is moved to a file on disk for the (edited) session_id, as subPayload does,
// unless an edit set it; a session id too long for a file name gets a path
// that is not on disk.
func variant(t *testing.T, fixture string, edits ...payloadEdit) []byte {
	t.Helper()
	m := decodeFixture(t, fixture)
	original := m["transcript_path"]
	for _, e := range edits {
		e(m)
	}
	if m["transcript_path"] == original {
		sid, _ := m["session_id"].(string)
		switch {
		case sid == "":
		case len(sid) > 200:
			m["transcript_path"] = "/home/agent/.claude/projects/-work-demo/" + sid + ".jsonl"
		default:
			m["transcript_path"] = writeTranscript(t, sid)
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal variant of %s: %v", fixture, err)
	}
	return raw
}

// payloadTranscript is the transcript_path of payload.
func payloadTranscript(t *testing.T, payload []byte) string {
	t.Helper()
	var p struct {
		Path string `json:"transcript_path"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	return p.Path
}

// The nested launch: B1's subagent launches B6's child (type subType, as B6
// has it). nestedRequest is its launch request, made by the launcher;
// nestedStart and nestedStop are the child's own start and stop; nestedCompleted
// is the launcher's completed result for it.
func nestedRequest(t *testing.T) []byte {
	return variant(t, fxRequest, setKey("agent_id", subAgentID), setKey("agent_type", subType),
		setKey("tool_use_id", subNestedToolUseID), setKey("tool_input.description", subNestedDesc))
}

func nestedStart(t *testing.T) []byte {
	return variant(t, fxStart, setKey("agent_id", subChildID))
}

func nestedStop(t *testing.T) []byte {
	return variant(t, fxStop, setKey("agent_id", subChildID), emptyListing())
}

func nestedCompleted(t *testing.T) []byte {
	return variant(t, fxCompleted, setKey("agent_id", subAgentID), setKey("agent_type", subType),
		setKey("tool_use_id", subNestedToolUseID), setKey("tool_input.description", subNestedDesc),
		setKey("tool_response.agentId", subChildID))
}

// foregroundStop is B3 with the empty listing of a foreground subagent's stop.
func foregroundStop(t *testing.T) []byte { return variant(t, fxStop, emptyListing()) }

// mainStop is a main-agent Stop with an empty listing (B8 without its tasks).
func mainStop(t *testing.T) []byte { return variant(t, fxMainStop, emptyListing()) }

// secondRequest, secondStart and secondResult are the second launch of the
// claim tests: B9 with another tool_use_id, B1 with another agent_id, and B5
// with that tool_use_id and agentId and its own description and model.
func secondRequest(t *testing.T) []byte {
	return variant(t, fxRequest, setKey("tool_use_id", subSecondToolUseID))
}

func secondStart(t *testing.T) []byte {
	return variant(t, fxStart, setKey("agent_id", subSecondAgentID))
}

func secondResult(t *testing.T) []byte {
	return variant(t, fxResult, setKey("tool_use_id", subSecondToolUseID),
		setKey("tool_response.agentId", subSecondAgentID),
		setKey("tool_input.description", subSecondDesc), setKey("tool_response.description", subSecondDesc),
		setKey("tool_response.resolvedModel", subSecondModel))
}

// namedRequest is B9 with tool_input.name (a named launch, which opens no
// claimable launch).
func namedRequest(t *testing.T) []byte {
	return variant(t, fxRequest, setKey("tool_input.name", subTeammateType))
}

// typelessRequest is B9 without tool_input.subagent_type.
func typelessRequest(t *testing.T) []byte {
	return variant(t, fxRequest, delKey("tool_input.subagent_type"))
}

// teammateRequest is the teammate claim's request (N10): a launch request of
// B2's agent_type with no name.
func teammateRequest(t *testing.T) []byte {
	return variant(t, fxRequest, setKey("tool_input.subagent_type", subTeammateType))
}

// teammateStop is B3 for the teammate: its id and type, an empty listing.
func teammateStop(t *testing.T) []byte {
	return variant(t, fxStop, setKey("agent_id", subTeammateID), setKey("agent_type", subTeammateType), emptyListing())
}

// agentFailure is a PostToolUseFailure(Agent) for toolUseID: the baseline's
// post-tool-use-failure.json with tool_name Agent and B9's tool_input.
func agentFailure(t *testing.T, toolUseID string, edits ...payloadEdit) []byte {
	return variant(t, fxFailureBase, append([]payloadEdit{setKey("session_id", subSessionID), setKey("tool_name", "Agent"),
		setKey("tool_use_id", toolUseID), setKey("tool_input", fixtureValue(t, fxRequest, "tool_input"))}, edits...)...)
}

// ---------------------------------------------------------------------------
// Sentinel, oversized and long payloads

// sentinelEdits set every content field Claude Code sends for the subagent
// events, and every other never-read field of SR-5.2, to subSentinel: the
// prompts, tool_response.content, last_assistant_message, an error,
// agent_transcript_path, outputFile, tool_response.agent_id, the value of
// tool_input.name, every background_tasks description, usage and session_crons.
// A field the event does not carry is added, never one the rules read.
func sentinelEdits() payloadEdit {
	return func(m map[string]any) {
		for _, k := range []string{"prompt", "last_assistant_message", "agent_transcript_path", "error"} {
			m[k] = subSentinel
		}
		m["session_crons"] = []any{subSentinel}
		if in, ok := m["tool_input"].(map[string]any); ok {
			in["prompt"] = subSentinel
			if _, named := in["name"]; named {
				in["name"] = subSentinel
			}
		}
		if resp, ok := m["tool_response"].(map[string]any); ok {
			for _, k := range []string{"prompt", "content", "outputFile", "agent_id"} {
				resp[k] = subSentinel
			}
			resp["usage"] = map[string]any{"note": subSentinel}
		}
		if tasks, ok := m["background_tasks"].([]any); ok {
			for _, task := range tasks {
				if entry, ok := task.(map[string]any); ok {
					entry["description"] = subSentinel
				}
			}
		}
	}
}

// sentinelPayload is fixture with the sentinel in every field of sentinelEdits,
// then edits.
func sentinelPayload(t *testing.T, fixture string, edits ...payloadEdit) []byte {
	t.Helper()
	return variant(t, fixture, append([]payloadEdit{sentinelEdits()}, edits...)...)
}

// oversizeAt sets the dotted path to the sentinel repeated past the hook's
// 1 MiB limit.
func oversizeAt(path string) payloadEdit {
	return setKey(path, strings.Repeat(subSentinel, int(hook.MaxPayloadBytes)/len(subSentinel)+1))
}

// oversized is fixture with its content field filled past the limit: a
// SubagentStop's last_assistant_message, a completed result's
// tool_response.content.
func oversized(t *testing.T, fixture string, edits ...payloadEdit) []byte {
	t.Helper()
	path := "last_assistant_message"
	if fixture == fxCompleted {
		path = "tool_response.content"
	}
	raw := variant(t, fixture, append([]payloadEdit{oversizeAt(path)}, edits...)...)
	if int64(len(raw)) <= hook.MaxPayloadBytes {
		t.Fatalf("oversized(%s) is %d bytes; want over %d", fixture, len(raw), hook.MaxPayloadBytes)
	}
	return raw
}

// subagentStringMax is SR-5.3's bound.
const subagentStringMax = 1024

// longOf returns a value longer than 1 KiB built from base, with a two-byte
// character across the 1024-byte boundary (bytes 1023 and 1024, 0-based): base,
// padding up to 1023 bytes, the character, then a tail.
func longOf(base string) string {
	return base + strings.Repeat("-", subagentStringMax-1-len(base)) + "é" + "-tail"
}

// exactOf returns base padded to exactly 1024 bytes.
func exactOf(base string) string {
	return base + strings.Repeat("-", subagentStringMax-len(base))
}

// wantCut is the longest prefix of s of whole UTF-8 characters within 1024
// bytes (SR-5.3), computed here and not by the store's own cut.
func wantCut(s string) string {
	if len(s) <= subagentStringMax {
		return s
	}
	i := subagentStringMax
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// bounded are the dotted paths of the strings SR-5.3 cuts that a fixture may
// carry.
var bounded = []string{
	"tool_use_id", "tool_input.subagent_type", "tool_input.description", "agent_id", "agent_type", "session_id",
	"tool_response.agentId", "tool_response.agentType", "tool_response.resolvedModel",
}

// longVariant is fixture with every string of SR-5.3 it carries replaced by
// longOf its value (one value wherever two carry one id), and a SubagentStop's
// listing id and agent_type too, then edits. mapper turns each value into its
// long form (longOf or exactOf).
func longVariant(t *testing.T, fixture string, mapper func(string) string, edits ...payloadEdit) []byte {
	t.Helper()
	all := append([]payloadEdit{func(m map[string]any) {
		for _, path := range bounded {
			if v, ok := getKey(m, path); ok {
				if s, isStr := v.(string); isStr && s != "" {
					setKey(path, mapper(s))(m)
				}
			}
		}
		if tasks, ok := m["background_tasks"].([]any); ok {
			for _, task := range tasks {
				if entry, ok := task.(map[string]any); ok {
					for _, k := range []string{"id", "agent_type"} {
						if s, isStr := entry[k].(string); isStr && s != "" {
							entry[k] = mapper(s)
						}
					}
				}
			}
		}
	}}, edits...)
	return variant(t, fixture, all...)
}
