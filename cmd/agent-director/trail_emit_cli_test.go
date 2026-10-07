package main_test

import (
	"testing"
)

// TestTrailEmitRelayAttemptCLI: trail-emit relay-attempt, DB-free (SR-A-2.3),
// prints {} and appends one ad.relay_attempt.completed line carrying its
// flags, the outcome a JSON number for an HTTP status and a string for a named
// class, the byte counts 0 when omitted, and source relay_hook (SR-A-1.4).
// Its flag refusals are advice_follow_cli_test.go's H2 and H3; the outcome
// parse is trail_emit_cmd_test.go's.
func TestTrailEmitRelayAttemptCLI(t *testing.T) {
	for _, tc := range []struct {
		outcome     string
		bytes       []string
		wantOutcome any
		wantBytes   float64
	}{
		{"404", []string{"--bytes-sent", "1024", "--bytes-received", "1024"}, float64(404), 1024},
		{"connection_refused", nil, "connection_refused", 0},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			home := t.TempDir()
			stdout, stderr, code := runCLIWithHome(t, home, append([]string{"trail-emit", "relay-attempt",
				"--token", "tok-abc", "--endpoint", "http://localhost:9999/relay", "--outcome", tc.outcome,
				"--instance-id", "inst-test-1"}, tc.bytes...)...)
			if code != 0 || stdout != "{}\n" {
				t.Fatalf("exit = %d, stdout = %q; want 0 and {}\nstderr=%s", code, stdout, stderr)
			}
			ra := relayAttemptLines(readTrailLines(t, home))
			if len(ra) != 1 {
				t.Fatalf("ad.relay_attempt.completed lines = %v; want exactly one", ra)
			}
			want := map[string]any{"outcome": tc.wantOutcome, "source": "relay_hook", "claude_instance_id": "inst-test-1",
				"request_token": "tok-abc", "target_endpoint": "http://localhost:9999/relay",
				"bytes_sent": tc.wantBytes, "bytes_received": tc.wantBytes}
			for k, v := range want {
				if ra[0][k] != v {
					t.Errorf("%s = %v (%T); want %v (%T)", k, ra[0][k], ra[0][k], v, v)
				}
			}
			if ts, _ := ra[0]["ts"].(string); !tsRe.MatchString(ts) {
				t.Errorf("ts %v does not match SR-A-7.9", ra[0]["ts"])
			}
		})
	}
}

// TestTrailNoHomeWritesNothing: with HOME="" trail-emit fails ErrTrailWrite and
// a no-verb hook payload exits 0 silently, neither writing under the cwd (b.iin).
func TestTrailNoHomeWritesNothing(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		wantErr string // "" wants a silent exit 0
	}{
		{"trail-emit", []string{"trail-emit", "relay-attempt", "--token", "tok-iin",
			"--endpoint", "http://127.0.0.1:9/r", "--outcome", "200", "--instance-id", "iin-x"}, "ErrTrailWrite"},
		{"no verb hook payload", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stdout, stderr, code := runInDir(t, dir, "", tc.argv...)
			if tc.wantErr != "" {
				assertOnlyEnvelope(t, stdout, stderr, code, tc.wantErr)
			} else if code != 0 || stdout != "" || stderr != "" {
				t.Errorf("exit = %d, stdout = %q, stderr = %q; want 0 and both empty", code, stdout, stderr)
			}
			assertHomeTree(t, dir)
		})
	}
}
