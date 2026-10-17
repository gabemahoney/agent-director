// Package cursorenv owns Cursor's explicit launch selection boundary.
package cursorenv

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Selectors lists the evidenced configuration and credential selectors. A
// launch must remove ambient copies before applying its protected selection.
func Selectors() []string {
	return []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR", "CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"}
}

// Validate checks only explicit Cursor selection, without filesystem or native
// configuration access. Directory selection does not establish account isolation.
func Validate(selected map[string]string) error {
	for _, key := range []string{"CURSOR_API_KEY", "AGENT_CLI_CREDENTIAL_STORE"} {
		if _, present := selected[key]; present {
			return fmt.Errorf("extra_env key %s selects an unreleased Cursor credential profile; this account-selection intent cannot be launched", key)
		}
	}
	for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR"} {
		value, present := selected[key]
		if !present {
			continue
		}
		invalid := value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value
		for _, b := range []byte(value) {
			invalid = invalid || b < 0x20 || b == 0x7f
		}
		if invalid {
			return fmt.Errorf("extra_env key %s requires a nonempty absolute, lexically clean directory path without control bytes; supply its literal path without expansion", key)
		}
	}
	return nil
}

// WithoutSelection returns a non-nil copy, preventing exec's nil-environment
// inheritance and removing all copies of each selector, including empty values.
func WithoutSelection(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		key, _, _ := strings.Cut(entry, "=")
		selected := false
		for _, selector := range Selectors() {
			selected = selected || key == selector
		}
		if !selected {
			out = append(out, entry)
		}
	}
	return out
}

// Runtime restores only the protected row's explicit directory selection.
// Call Validate first: credential-profile selections are never activated.
func Runtime(environ []string, selected map[string]string) []string {
	out := WithoutSelection(environ)
	for _, key := range []string{"CURSOR_CONFIG_DIR", "CURSOR_DATA_DIR"} {
		if value, present := selected[key]; present {
			out = append(out, key+"="+value)
		}
	}
	return out
}
