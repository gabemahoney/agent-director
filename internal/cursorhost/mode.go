package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// requestedSessionMode verifies the session's own advertised mode state before
// issuing set_mode. It never substitutes another available mode.
func requestedSessionMode(raw json.RawMessage, requested string) error {
	var result struct {
		Modes *struct {
			Current   string `json:"currentModeId"`
			Available []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"availableModes"`
		} `json:"modes"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Modes == nil || result.Modes.Current == "" || len(result.Modes.Available) == 0 {
		return fmt.Errorf("Cursor session mode state unavailable; remove --plan, or --mode and its value, then retry")
	}
	seen := make(map[string]bool)
	for _, mode := range result.Modes.Available {
		if mode.ID == "" || mode.Name == "" || seen[mode.ID] {
			return fmt.Errorf("invalid Cursor session mode state")
		}
		seen[mode.ID] = true
	}
	if !seen[result.Modes.Current] || !seen[requested] {
		return fmt.Errorf("requested Cursor mode unavailable; remove --plan, or --mode and its value, then retry")
	}
	return nil
}

// ACP v1 SetSessionModeResponse is an object with no required fields. An empty
// object is authoritative when correlated to the outstanding request. Optional
// echoed fields must agree; notifications alone cannot complete the request.
func confirmSessionMode(raw json.RawMessage, session, mode string) error {
	var result map[string]json.RawMessage
	if json.Unmarshal(raw, &result) != nil || result == nil {
		return fmt.Errorf("invalid Cursor mode acknowledgement")
	}
	for key, want := range map[string]string{"sessionId": session, "modeId": mode, "currentModeId": mode} {
		if value, ok := result[key]; ok {
			var got string
			if json.Unmarshal(value, &got) != nil || got != want {
				return fmt.Errorf("Cursor mode acknowledgement mismatch")
			}
		}
	}
	return nil
}

func modeUpdate(raw json.RawMessage, session, requested string) error {
	var params struct {
		Session string `json:"sessionId"`
		Update  struct {
			Kind  string          `json:"sessionUpdate"`
			Mode  string          `json:"currentModeId"`
			Agent json.RawMessage `json:"agentId"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &params) != nil {
		return fmt.Errorf("invalid Cursor mode update")
	}
	if params.Update.Kind != "current_mode_update" {
		return nil
	}
	// Child/plan metadata supplies no main-session configuration authority.
	if len(params.Update.Agent) > 0 && !bytes.Equal(params.Update.Agent, []byte(`""`)) && !bytes.Equal(params.Update.Agent, []byte("null")) {
		return nil
	}
	if params.Session != session || params.Update.Mode != requested {
		return fmt.Errorf("Cursor mode update contradicts requested session mode")
	}
	return nil
}
