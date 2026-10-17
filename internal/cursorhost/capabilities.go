package cursorhost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// initializeCapabilities validates the ACP 1 shape without requiring optional
// features. Text prompts and session/new are baseline operations; session/load
// alone needs an advertised capability. Unknown extensions remain opaque.
// Schema: https://agentclientprotocol.com/protocol/v1/schema#agentcapabilities
func initializeCapabilities(raw json.RawMessage) (bool, error) {
	// Decode exact protocol keys: case variants are opaque extensions and must
	// neither supply a required field nor overwrite its actual value.
	var init map[string]json.RawMessage
	var version *int
	if json.Unmarshal(raw, &init) != nil || json.Unmarshal(init["protocolVersion"], &version) != nil || version == nil {
		return false, fmt.Errorf("Cursor ACP initialize returned a malformed response")
	}
	if *version != 1 {
		return false, fmt.Errorf("Cursor runtime does not support ACP protocol 1")
	}
	caps, ok := capabilityObject(init["agentCapabilities"])
	if !ok || !capabilityBooleans(caps, "loadSession") {
		return false, fmt.Errorf("Cursor ACP initialize returned malformed agent capabilities")
	}
	for _, group := range []struct {
		name     string
		booleans []string
		markers  []string
	}{
		{"promptCapabilities", []string{"image", "audio", "embeddedContext"}, nil},
		{"mcpCapabilities", []string{"http", "sse"}, nil},
		{"sessionCapabilities", nil, []string{"list", "resume", "close", "delete", "additionalDirectories"}},
		{"auth", nil, []string{"logout"}},
	} {
		value, present := caps[group.name]
		if !present {
			continue
		}
		fields, valid := capabilityObject(value)
		if !valid || !capabilityBooleans(fields, group.booleans...) {
			return false, fmt.Errorf("Cursor ACP initialize returned malformed agent capabilities")
		}
		for _, name := range group.markers {
			if marker, present := fields[name]; present && !bytes.Equal(bytes.TrimSpace(marker), []byte("null")) {
				if _, valid := capabilityObject(marker); !valid {
					return false, fmt.Errorf("Cursor ACP initialize returned malformed agent capabilities")
				}
			}
		}
	}
	return bytes.Equal(bytes.TrimSpace(caps["loadSession"]), []byte("true")), nil
}

// capabilityObject checks only the reserved metadata container, never its
// extension values. Null is allowed for _meta, but not for a capability group.
func capabilityObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	if meta, present := object["_meta"]; present {
		var fields map[string]json.RawMessage
		if json.Unmarshal(meta, &fields) != nil {
			return nil, false
		}
	}
	return object, true
}

func capabilityBooleans(fields map[string]json.RawMessage, names ...string) bool {
	for _, name := range names {
		if value, present := fields[name]; present {
			value = bytes.TrimSpace(value)
			if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
				return false
			}
		}
	}
	return true
}
