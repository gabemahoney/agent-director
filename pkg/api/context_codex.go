package api

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const (
	maxContextFile = 64 << 20
	maxContextLine = 4 << 20
)

// readCodexContext is called only with current-launch startup authority.
// cli_version is creation metadata, and cannot establish that authority.
func readCodexContext(session, path string) ContextReading {
	if session == "" || !filepath.IsAbs(path) {
		return ContextReading{}
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > maxContextFile {
		return ContextReading{}
	}
	// A file replaced by a FIFO between Lstat and open must not block reads.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ContextReading{}
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return ContextReading{}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxContextFile+1))
	if err != nil || len(data) == 0 || len(data) > maxContextFile || data[len(data)-1] != '\n' {
		return ContextReading{}
	}
	after, err := f.Stat()
	if err != nil || int64(len(data)) != after.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return ContextReading{}
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(after, current) || current.Size() != after.Size() || !current.ModTime().Equal(after.ModTime()) {
		return ContextReading{}
	}
	var state codexContextState
	for i, line := range bytes.Split(data[:len(data)-1], []byte{'\n'}) {
		if len(line) == 0 || len(line) > maxContextLine || !state.record(line, session, i == 0) {
			return ContextReading{}
		}
	}
	return state.reading
}

type codexContextRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexContextPayload struct {
	Type        string               `json:"type"`
	ID          string               `json:"id"`
	Version     string               `json:"cli_version"`
	Provider    string               `json:"model_provider"`
	ProviderID  string               `json:"model_provider_id"`
	HistoryMode string               `json:"history_mode"`
	Fork        json.RawMessage      `json:"forked_from_id"`
	HistoryBase json.RawMessage      `json:"history_base"`
	TurnID      string               `json:"turn_id"`
	ThreadID    string               `json:"thread_id"`
	Model       string               `json:"model"`
	Window      *int64               `json:"model_context_window"`
	Settings    *codexContextPayload `json:"thread_settings"`
	Info        *struct {
		Window *int64 `json:"model_context_window"`
		Last   *struct {
			Total *int64 `json:"total_tokens"`
		} `json:"last_token_usage"`
	} `json:"info"`
}

type codexContextState struct {
	previous      time.Time
	turn          string
	model         string
	settingsModel string
	provider      string
	window        int64
	reading       ContextReading
}

func presentJSON(raw json.RawMessage) bool {
	return len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (s *codexContextState) record(line []byte, session string, first bool) bool {
	var r codexContextRecord
	var p codexContextPayload
	if json.Unmarshal(line, &r) != nil || !presentJSON(r.Payload) || json.Unmarshal(r.Payload, &p) != nil ||
		presentJSON(p.Fork) || presentJSON(p.HistoryBase) {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, r.Timestamp)
	if err != nil || at.IsZero() || at.Before(s.previous) || at.Year() < 0 || at.Year() > 9999 {
		return false
	}
	s.previous = at
	if first {
		if r.Type != "session_meta" || p.ID != session || p.Version != "0.159.2" || p.Provider != "openai" || p.HistoryMode != "paginated" {
			return false
		}
		s.provider = p.Provider
		return true
	}
	if p.ThreadID != "" && p.ThreadID != session {
		return false
	}
	switch r.Type {
	case "turn_context":
		if p.TurnID == "" || p.TurnID != s.turn || p.Model == "" ||
			(p.Provider != "" && p.Provider != s.provider) || (p.ProviderID != "" && p.ProviderID != s.provider) ||
			(s.settingsModel != "" && p.Model != s.settingsModel) || (p.Window != nil && *p.Window != s.window) {
			return false
		}
		if s.model != p.Model {
			s.reading = ContextReading{}
			if s.model != "" {
				// A model change inside a turn has no new applicable start capacity.
				s.window = 0
			}
		}
		s.model = p.Model
	case "compacted":
		s.reading = ContextReading{}
	case "response_item":
		s.reading = ContextReading{}
		switch p.Type {
		case "message", "agent_message", "reasoning", "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output":
		default:
			return false
		}
	case "token_usage_record":
		s.reading = ContextReading{}
		if p.TurnID == "" || p.TurnID != s.turn || p.ThreadID != session {
			return false
		}
	case "world_state", "inter_agent_communication_metadata":
		s.reading = ContextReading{}
	case "event_msg":
		return s.event(p, session, at)
	default:
		// A new structural event might rewind or replace active history.
		return false
	}
	return true
}

func (s *codexContextState) event(p codexContextPayload, session string, at time.Time) bool {
	switch p.Type {
	case "task_started":
		s.reading = ContextReading{}
		s.turn, s.model, s.window = p.TurnID, "", 0
		if p.TurnID == "" || p.Window == nil || *p.Window <= 0 {
			return false
		}
		s.window = *p.Window
	case "thread_settings_applied":
		s.reading = ContextReading{}
		if p.ThreadID != session || p.Settings == nil || p.Settings.Model == "" || p.Settings.ProviderID != "openai" {
			return false
		}
		s.model, s.provider, s.window = "", p.Settings.ProviderID, 0
		s.settingsModel = p.Settings.Model
	case "token_count":
		s.reading = ContextReading{}
		if s.turn == "" || s.model == "" || s.provider != "openai" || s.window <= 0 ||
			p.Info == nil || p.Info.Last == nil || p.Info.Last.Total == nil || *p.Info.Last.Total < 0 ||
			p.Info.Window == nil || *p.Info.Window != s.window || (p.TurnID != "" && p.TurnID != s.turn) {
			return true
		}
		used, window, source, measured := *p.Info.Last.Total, s.window, "codex_jsonl_last_token_usage", at.UTC()
		fraction := float64(used) / float64(window)
		s.reading = ContextReading{&used, &window, &fraction, &source, &measured}
	case "task_complete", "item_completed":
		if p.TurnID == "" || p.TurnID != s.turn {
			return false
		}
	default:
		return false
	}
	return true
}
