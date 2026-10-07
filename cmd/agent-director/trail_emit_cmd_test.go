package main

import "testing"

// TestParseRelayOutcome: --outcome is an HTTP status in 100-599 (an int) or
// one of the three named classes (a string); anything else is refused, so
// trail-emit writes nothing (not fail-open).
func TestParseRelayOutcome(t *testing.T) {
	for in, want := range map[string]any{
		"100": 100, "200": 200, "599": 599,
		"connection_refused": "connection_refused", "timeout": "timeout", "dns_failure": "dns_failure",
		"99": nil, "600": nil, "banana": nil, "": nil, "Timeout": nil,
	} {
		got, err := parseRelayOutcome(in)
		if want == nil {
			if err == nil {
				t.Errorf("parseRelayOutcome(%q) = %v; want an error", in, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("parseRelayOutcome(%q) = %v (%T), %v; want %v (%T)", in, got, got, err, want, want)
		}
	}
}
