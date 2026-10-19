package hook

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gabemahoney/agent-director/internal/contextsource"
	"github.com/gabemahoney/agent-director/internal/store"
)

// These callbacks have capacity authority only; they never enter the lifecycle
// classifier, session binder, permission relay, or subagent discovery path.
func handleClaudeCapacity(raw []byte, st HookStore, hc HandleConfig, parent parentIdentity, at time.Time) error {
	var launch string
	for attempt := 0; attempt < 8; attempt++ {
		err := recordClaudeCapacity(raw, st, hc, parent, at, &launch)
		if err != errClaudeCapacityContention {
			return err
		}
	}
	return fmt.Errorf("Claude capacity changed during observation")
}

var errClaudeCapacityContention = fmt.Errorf("Claude capacity snapshot changed")

func recordClaudeCapacity(raw []byte, st HookStore, hc HandleConfig, parent parentIdentity, at time.Time, launch *string) error {
	if hc.Agent != "" && hc.Agent != store.AgentClaude {
		return nil
	}
	writer, ok := st.(interface {
		RecordClaudeCapacity(string, store.HookGate, store.ClaudeCapacity) (bool, error)
		ListSubagentRecords(string) (bool, []store.SubagentRow, error)
	})
	if !ok || hc.Env == nil {
		return nil
	}
	id, err := ResolveInstanceID(hc.Env)
	if err != nil {
		return nil
	}
	row, err := st.GetSpawn(id)
	if err != nil {
		return nil
	}
	e := row.ContextEvidence()
	if e.Agent != store.AgentClaude || !store.IsLiveState(e.State) || e.State == store.StatePending || e.SessionID == "" || e.JSONLPath == "" ||
		e.PID != parent.pid || e.PanePID != parent.pid || e.ProcStarttime != parent.start || e.PaneStarttime != parent.start || parent.start == "" {
		return nil
	}
	if *launch != "" && *launch != e.LaunchToken {
		return nil
	}
	*launch = e.LaunchToken
	snapshot, err := json.Marshal(e.ClaudeCapacity)
	if err != nil {
		return err
	}
	expected := string(snapshot)
	gate := store.HookGate{ParentPID: parent.pid, ParentStart: parent.start, SessionID: e.SessionID, ClaudeCapacitySnapshot: &expected}
	var p struct {
		Session string `json:"session_id"`
		Version string `json:"version"`
		Model   struct {
			ID string `json:"id"`
		} `json:"model"`
		Window struct {
			Size  int64                `json:"context_window_size"`
			Usage contextsource.Counts `json:"current_usage"`
		} `json:"context_window"`
		Tasks []struct {
			ID, Type, Model string
			Window          int64 `json:"contextWindowSize"`
		} `json:"tasks"`
	}
	if json.Unmarshal(raw, &p) != nil || p.Session != e.SessionID {
		return nil
	}
	cap := store.ClaudeCapacity{LaunchToken: e.LaunchToken, SessionID: e.SessionID, JSONLPath: e.JSONLPath, PID: e.PID, Starttime: e.ProcStarttime, Version: contextsource.ClaudeVersion, Provider: "anthropic", Children: map[string]store.ClaudeWindow{}}
	if current := e.BoundClaudeCapacity(); current != nil {
		cap = *current
	}
	qualified := claudeFirstParty(hc.Env)
	switch hc.ContextSource {
	case "statusLine":
		prior := cap.Main
		cap.Main = nil
		if prior != nil {
			// Keep the rejected response bound until a genuinely new response
			// qualifies; replaying an older valid callback cannot restore it.
			rejected := *prior
			rejected.Invalidated = true
			cap.Main = &rejected
		}
		if !qualified || p.Version != contextsource.ClaudeVersion {
			return clearClaudeCapacity(writer, id, gate, e)
		}
		usage, valid := contextsource.Claude(e.SessionID, "", e.JSONLPath)
		cap.Version, cap.Provider = contextsource.ClaudeVersion, "anthropic"
		if valid && p.Window.Size > 0 && usage.Model == p.Model.ID && !usage.At.After(at) && p.Window.Usage.Equal(usage.Counts) {
			cap.Main = &store.ClaudeWindow{Model: usage.Model, Window: p.Window.Size, ResponseUUID: usage.UUID, ObservedAt: at.UTC()}
			cap.Main.Invalidated = sameResponseInvalidated(prior, *cap.Main)
		}
	case "subagentStatusLine":
		if !qualified {
			return clearClaudeCapacity(writer, id, gate, e)
		}
		if e.CurrentClaudeCapacity() == nil {
			return nil
		}
		_, children, err := writer.ListSubagentRecords(id)
		if err != nil {
			return err
		}
		visible := map[string]bool{}
		for _, child := range children {
			visible[child.SubagentID] = child.ContextEvidence.LaunchToken == e.LaunchToken && child.SessionID != nil && *child.SessionID == e.SessionID
		}
		priorChildren := cap.Children
		cap.Children = map[string]store.ClaudeWindow{}
		for id, w := range priorChildren {
			if visible[id] {
				w.Invalidated = true
				cap.Children[id] = w
			}
		}
		seen := map[string]bool{}
		for _, task := range p.Tasks {
			if seen[task.ID] {
				return clearClaudeCapacity(writer, id, gate, e)
			}
			seen[task.ID] = true
			if !visible[task.ID] || task.Type != "local_agent" || task.Model == "" || task.Window <= 0 {
				continue
			}
			if _, exists := cap.Children[task.ID]; !exists && len(cap.Children) >= 128 {
				continue
			}
			path := store.ClaudeChildPath(e.JSONLPath, e.SessionID, task.ID)
			usage, valid := contextsource.Claude(e.SessionID, task.ID, path)
			if valid && usage.Model == task.Model && !usage.At.After(at) {
				w := store.ClaudeWindow{Model: task.Model, Window: task.Window, ResponseUUID: usage.UUID, ObservedAt: at.UTC()}
				prior, exists := priorChildren[task.ID]
				if exists {
					w.Invalidated = sameResponseInvalidated(&prior, w)
				}
				cap.Children[task.ID] = w
			}
		}
	default:
		return nil
	}
	return writeClaudeCapacity(writer, id, gate, cap)
}

// Reject unqualified provider/window overrides using the native callback's
// actual environment, including settings.env applied by the running agent.
func claudeFirstParty(env func(string) string) bool {
	for _, key := range []string{"ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_GATEWAY", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST", "CLAUDE_CODE_PROFILE", "ANTHROPIC_CUSTOM_MODEL_OPTION", "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "DISABLE_COMPACT", "ANTHROPIC_DEFAULT_HAIKU_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_DEFAULT_SONNET_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_DEFAULT_OPUS_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_DEFAULT_FABLE_MODEL_SUPPORTED_CAPABILITIES", "ANTHROPIC_CUSTOM_MODEL_OPTION_SUPPORTED_CAPABILITIES"} {
		if env(key) != "" {
			return false
		}
	}
	return true
}

func clearClaudeCapacity(writer interface {
	RecordClaudeCapacity(string, store.HookGate, store.ClaudeCapacity) (bool, error)
}, id string, gate store.HookGate, e store.SpawnContext) error {
	// Retain invalidation provenance so the same response cannot be restored.
	p := store.ClaudeCapacity{LaunchToken: e.LaunchToken, SessionID: e.SessionID, JSONLPath: e.JSONLPath, PID: e.PID, Starttime: e.ProcStarttime, Version: "unqualified", Provider: "unknown", Children: map[string]store.ClaudeWindow{}}
	if prior := e.BoundClaudeCapacity(); prior != nil {
		p = *prior
		p.Version, p.Provider = "unqualified", "unknown"
		if p.Main != nil {
			p.Main.Invalidated = true
		}
		for id, w := range p.Children {
			w.Invalidated = true
			p.Children[id] = w
		}
	}
	return writeClaudeCapacity(writer, id, gate, p)
}

func writeClaudeCapacity(writer interface {
	RecordClaudeCapacity(string, store.HookGate, store.ClaudeCapacity) (bool, error)
}, id string, gate store.HookGate, p store.ClaudeCapacity) error {
	applied, err := writer.RecordClaudeCapacity(id, gate, p)
	if err == nil && !applied {
		return errClaudeCapacityContention
	}
	return err
}

func sameResponseInvalidated(prior *store.ClaudeWindow, next store.ClaudeWindow) bool {
	return prior != nil && prior.ResponseUUID == next.ResponseUUID && (prior.Invalidated || prior.Model != next.Model || prior.Window != next.Window)
}
