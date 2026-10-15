package cursorfix

import (
	"bytes"
	"embed"
	"encoding/json"
	"testing"
)

//go:embed testdata/authorizations.json testdata/questions.json
var authorizationData embed.FS

type Authorization struct {
	Name, Provenance, Source, Method string
	ID, Params                       json.RawMessage
}

// Authorizations returns sanitized captured envelopes with supplied fields intact.
func Authorizations(t testing.TB) []Authorization {
	return authorizationFixtures(t, "testdata/authorizations.json")
}

func authorizationFixtures(t testing.TB, path string) []Authorization {
	t.Helper()
	raw, err := authorizationData.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []Authorization
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// Permission is synthetic; its approval kinds and opaque input are not live evidence.
func Permission(session, tool, kind string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"sessionId": session, "toolCall": map[string]any{"toolCallId": tool, "title": "Synthetic action", "kind": kind, "status": "pending", "rawInput": nil}, "options": []map[string]string{
		{"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"},
		{"optionId": "allow-always", "name": "Allow always", "kind": "allow_always"},
		{"optionId": "reject-once", "name": "Reject", "kind": "reject_once"},
	}})
	return raw
}

// SourceDerivedQuestions uses installed-source signatures and synthetic values, never live captures.
func SourceDerivedQuestions(t testing.TB, session string) []Authorization {
	t.Helper()
	cases := authorizationFixtures(t, "testdata/questions.json")
	encoded, _ := json.Marshal(session)
	for i := range cases {
		cases[i].Params = bytes.ReplaceAll(cases[i].Params, []byte(`"SOURCE_DERIVED_SESSION"`), encoded)
	}
	return cases
}
