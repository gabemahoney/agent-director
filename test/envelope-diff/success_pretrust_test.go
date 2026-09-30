package envelope_diff

import "testing"

// TestPreTrustMismatch checks the success driver's pre_trust check fails an
// envelope whose pre_trust is missing, mistyped or another value, so a field
// that silently disappears from both sides of the diff still fails.
func TestPreTrustMismatch(t *testing.T) {
	cases := []struct {
		name     string
		envelope string
		wantErr  bool
	}{
		{"ok", `{"claude_instance_id":"x","pre_trust":"ok"}`, false},
		{"missing", `{"claude_instance_id":"x"}`, true},
		{"failed", `{"claude_instance_id":"x","pre_trust":"failed"}`, true},
		{"skipped", `{"claude_instance_id":"x","pre_trust":"skipped"}`, true},
		{"null", `{"claude_instance_id":"x","pre_trust":null}`, true},
		{"not json", `not json`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := preTrustMismatch([]byte(tc.envelope), "ok")
			if (err != nil) != tc.wantErr {
				t.Errorf("preTrustMismatch(%s) = %v; wantErr %v", tc.envelope, err, tc.wantErr)
			}
		})
	}
}
