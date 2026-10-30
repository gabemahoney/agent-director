package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/contextsource"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
)

// ContextReading is experimental: agent internals and this output shape have
// no stable-format guarantee. Unknown readings retain all five null fields.
// A known fraction estimates the last response's occupancy, not billing usage.
type ContextReading struct {
	// UsedTokens estimates the last response's context occupancy.
	UsedTokens   *int64     `json:"used_tokens"`
	// WindowTokens is the context capacity supported by the current evidence.
	WindowTokens *int64     `json:"window_tokens"`
	// Fraction is UsedTokens divided by WindowTokens.
	Fraction     *float64   `json:"fraction"`
	// Source names the evidence used to derive this reading.
	Source       *string    `json:"source"`
	// AsOf is the usage evidence's timestamp, in UTC.
	AsOf         *time.Time `json:"as_of"`
}

func contextOf(e store.SpawnContext) ContextReading {
	return contextForSubject(e, "")
}

func contextForSubject(e store.SpawnContext, child string) ContextReading {
	if !e.FreshCodexWriter() && e.CurrentClaudeCapacity() == nil {
		return ContextReading{}
	}
	procs := probe.NewProcChecker()
	live := func() bool {
		start, alive, known := procs.StartTime(e.PID)
		return known && alive && start == e.ProcStarttime
	}
	if !live() {
		return ContextReading{}
	}
	var reading ContextReading
	if e.Agent == store.AgentClaude {
		reading = readClaudeContext(e, child)
	} else if child == "" {
		reading = readCodexContext(e.SessionID, e.JSONLPath)
	}
	if !live() {
		return ContextReading{}
	}
	return reading
}

func readClaudeContext(e store.SpawnContext, child string) ContextReading {
	p := e.CurrentClaudeCapacity()
	if p == nil {
		return ContextReading{}
	}
	w, path, source := p.Main, e.JSONLPath, "claude_jsonl_usage_statusline_capacity"
	if child != "" {
		own, ok := p.Children[child]
		if !ok {
			return ContextReading{}
		}
		w = &own
		path = store.ClaudeChildPath(e.JSONLPath, e.SessionID, child)
		source = "claude_jsonl_usage_subagent_statusline_capacity"
	}
	if w == nil || w.Invalidated {
		return ContextReading{}
	}
	u, ok := contextsource.Claude(e.SessionID, child, path)
	if !ok || u.UUID != w.ResponseUUID || u.Model != w.Model || u.At.After(w.ObservedAt) {
		return ContextReading{}
	}
	used, window, at := u.Tokens, w.Window, u.At.UTC()
	fraction := float64(used) / float64(window)
	return ContextReading{&used, &window, &fraction, &source, &at}
}
