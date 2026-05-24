package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// unmarshalObj parses b into a map[string]any and fails the test if b is not
// valid JSON or does not represent a JSON object.
func unmarshalObj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v — raw: %s", err, b)
	}
	return m
}

// TestSuccessEnvelopeFlatPayload verifies that successEnvelope serialises a
// tagged struct payload into a flat JSON object, with the field key preserved
// from the json struct tag, and no err_name key present.
func TestSuccessEnvelopeFlatPayload(t *testing.T) {
	payload := struct {
		Handle string `json:"handle"`
	}{"abc"}
	b := successEnvelope(payload)
	m := unmarshalObj(t, b)
	if got, ok := m["handle"]; !ok {
		t.Fatal("missing \"handle\" key in success envelope")
	} else if got != "abc" {
		t.Fatalf("handle = %v; want \"abc\"", got)
	}
	if _, has := m["err_name"]; has {
		t.Fatal("success envelope must not contain \"err_name\"")
	}
}

// TestSuccessEnvelopeNilPayload verifies that a nil payload produces the empty
// JSON object "{}".
func TestSuccessEnvelopeNilPayload(t *testing.T) {
	b := successEnvelope(nil)
	m := unmarshalObj(t, b)
	if len(m) != 0 {
		t.Fatalf("want empty object, got %v", m)
	}
}

// TestErrorEnvelope verifies the exact two-field shape of an error envelope:
// {"err_name":"ErrFoo","err_description":"bar"}.
func TestErrorEnvelope(t *testing.T) {
	b := errorEnvelope("ErrFoo", "bar")
	m := unmarshalObj(t, b)
	if got := m["err_name"]; got != "ErrFoo" {
		t.Fatalf("err_name = %v; want \"ErrFoo\"", got)
	}
	if got := m["err_description"]; got != "bar" {
		t.Fatalf("err_description = %v; want \"bar\"", got)
	}
	if len(m) != 2 {
		t.Fatalf("want exactly 2 keys, got %d: %v", len(m), m)
	}
}

// TestClassifyAndEnvelopeKnownSentinel verifies that a pkg/api sentinel error
// present in errnames.Catalog is returned with its documented err_name, not
// the fallback "ErrInternal".
func TestClassifyAndEnvelopeKnownSentinel(t *testing.T) {
	// Wrap the sentinel to confirm errors.Is matching works through wrapping.
	wrapped := fmt.Errorf("some context: %w", pkgapi.ErrSpawnNotInteractive)
	b := classifyAndEnvelope(wrapped)
	m := unmarshalObj(t, b)
	if got := m["err_name"]; got != "ErrSpawnNotInteractive" {
		t.Fatalf("err_name = %v; want \"ErrSpawnNotInteractive\"", got)
	}
}

// TestClassifyAndEnvelopeUnknownError verifies that an error not present in
// errnames.Catalog is classified as "ErrInternal".
func TestClassifyAndEnvelopeUnknownError(t *testing.T) {
	b := classifyAndEnvelope(errors.New("oops"))
	m := unmarshalObj(t, b)
	if got := m["err_name"]; got != "ErrInternal" {
		t.Fatalf("err_name = %v; want \"ErrInternal\"", got)
	}
}

// TestSanitizationStripsAbsolutePaths verifies that absolute filesystem paths
// in an ErrInternal description are replaced with "<path>" before the envelope
// is returned, so sensitive host paths never cross the C boundary.
func TestSanitizationStripsAbsolutePaths(t *testing.T) {
	b := classifyAndEnvelope(errors.New("read /home/horde/secret/foo.db: permission denied"))
	m := unmarshalObj(t, b)
	desc, _ := m["err_description"].(string)
	if strings.Contains(desc, "/home/horde/secret") {
		t.Fatalf("err_description contains absolute path segment: %q", desc)
	}
}

// TestSanitizationStripsStackFrames verifies that Go stack-frame location
// lines (tab-indented, ending in .go:<line>) are removed from ErrInternal
// descriptions.
func TestSanitizationStripsStackFrames(t *testing.T) {
	raw := "some error\n\tgithub.com/foo/bar.go:123 +0x1a0\n\tgithub.com/foo/baz.go:456 +0x200"
	b := classifyAndEnvelope(errors.New(raw))
	m := unmarshalObj(t, b)
	desc, _ := m["err_description"].(string)
	if strings.Contains(desc, ".go:") {
		t.Fatalf("err_description still contains a stack-frame location: %q", desc)
	}
}

// TestSanitizationLengthCap verifies that ErrInternal descriptions longer than
// maxSanitizedLen bytes are truncated to maxSanitizedLen bytes followed by
// a "..." suffix.
func TestSanitizationLengthCap(t *testing.T) {
	b := classifyAndEnvelope(errors.New(strings.Repeat("x", 10000)))
	m := unmarshalObj(t, b)
	desc, _ := m["err_description"].(string)
	// Cap is maxSanitizedLen bytes + 3-byte "..." truncation marker.
	if len(desc) > maxSanitizedLen+3 {
		t.Fatalf("err_description len=%d; want ≤ %d", len(desc), maxSanitizedLen+3)
	}
	if !strings.HasSuffix(desc, "...") {
		t.Fatalf("truncated description should end with \"...\"; got: %q", desc)
	}
}

// TestEnvelopeJSONShapeIsObject verifies that all three envelope helpers
// always produce valid JSON that parses as a JSON object (not an array, number,
// or scalar).
func TestEnvelopeJSONShapeIsObject(t *testing.T) {
	cases := []struct {
		name string
		b    []byte
	}{
		{"successEnvelope(nil)", successEnvelope(nil)},
		{"successEnvelope(payload)", successEnvelope(map[string]string{"k": "v"})},
		{"errorEnvelope", errorEnvelope("ErrX", "desc")},
		{"classifyAndEnvelope(unknown)", classifyAndEnvelope(errors.New("test"))},
		{"classifyAndEnvelope(sentinel)", classifyAndEnvelope(pkgapi.ErrSpawnNotInteractive)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unmarshalObj(t, tc.b) // fails on invalid JSON or non-object
		})
	}
}
