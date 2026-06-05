package main_test

import (
	"strings"
	"testing"
)

// relayAttemptLines filters trail lines for ad.relay_attempt.completed events.
func relayAttemptLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.relay_attempt.completed" {
			out = append(out, l)
		}
	}
	return out
}

// runTrailEmitRelayAttempt is a thin helper that runs
// `agent-director trail-emit relay-attempt <extraArgs>` with a dedicated
// stateDir and a throwaway HOME (no state.db bootstrapping needed).
func runTrailEmitRelayAttempt(t *testing.T, stateDir string, extraArgs ...string) (string, string, int) {
	t.Helper()
	home := t.TempDir()
	args := append([]string{"trail-emit", "relay-attempt"}, extraArgs...)
	return runCLIWithEnv(t, home,
		map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
		"", args...)
}

// trailEmitOutcomeCase parameterizes TestTrailEmitRelayAttemptOutcomes.
type trailEmitOutcomeCase struct {
	outcome     string
	wantOutcome any // float64 for HTTP status codes; string for named classes
}

// TestTrailEmitRelayAttemptOutcomes is the primary table-driven test covering
// all six outcome variants. For each it asserts:
//   - Exactly one ad.relay_attempt.completed line is appended.
//   - outcome encodes as a JSON number (float64 after unmarshal) for HTTP
//     status codes and as a JSON string for named-class error classes.
//   - All required top-level fields are present with the expected values:
//     claude_instance_id, request_token, target_endpoint, bytes_sent,
//     bytes_received, source="relay_hook", ts (SR-A-7.9).
//   - Exit code is 0 and stdout is the strictly structured "{}" envelope.
func TestTrailEmitRelayAttemptOutcomes(t *testing.T) {
	cases := []trailEmitOutcomeCase{
		{"200", float64(200)},
		{"404", float64(404)},
		{"500", float64(500)},
		{"connection_refused", "connection_refused"},
		{"timeout", "timeout"},
		{"dns_failure", "dns_failure"},
	}

	for _, tc := range cases {
		t.Run("outcome_"+tc.outcome, func(t *testing.T) {
			stateDir := t.TempDir()

			stdout, stderr, code := runTrailEmitRelayAttempt(t, stateDir,
				"--token", "tok-abc",
				"--endpoint", "http://localhost:9999/relay",
				"--outcome", tc.outcome,
				"--bytes-sent", "1024",
				"--bytes-received", "512",
				"--instance-id", "inst-test-1",
			)
			if code != 0 {
				t.Fatalf("exit = %d; want 0\nstderr=%s", code, stderr)
			}

			// stdout must be the strictly structured "{}" envelope (not empty).
			if strings.TrimRight(stdout, "\n") != "{}" {
				t.Errorf("stdout = %q; want \"{}\"", stdout)
			}

			// Exactly one relay-attempt trail line.
			lines := readTrailLines(t, stateDir)
			ra := relayAttemptLines(lines)
			if len(ra) != 1 {
				t.Fatalf("ad.relay_attempt.completed line count = %d; want exactly 1", len(ra))
			}
			row := ra[0]

			// outcome: type AND value must match.
			if row["outcome"] != tc.wantOutcome {
				t.Errorf("outcome = %v (%T); want %v (%T)",
					row["outcome"], row["outcome"],
					tc.wantOutcome, tc.wantOutcome)
			}

			// ts: SR-A-7.9 format.
			if ts, ok := row["ts"].(string); !ok || !tsRe.MatchString(ts) {
				t.Errorf("ts %v does not match SR-A-7.9 regex", row["ts"])
			}
			// source is always "relay_hook" (SR-A-1.4).
			if row["source"] != "relay_hook" {
				t.Errorf("source = %v; want relay_hook", row["source"])
			}
			// identity and routing fields.
			if row["claude_instance_id"] != "inst-test-1" {
				t.Errorf("claude_instance_id = %v; want inst-test-1", row["claude_instance_id"])
			}
			if row["request_token"] != "tok-abc" {
				t.Errorf("request_token = %v; want tok-abc", row["request_token"])
			}
			if row["target_endpoint"] != "http://localhost:9999/relay" {
				t.Errorf("target_endpoint = %v; want http://localhost:9999/relay", row["target_endpoint"])
			}
			// byte counters (JSON numbers → float64).
			if bs, _ := row["bytes_sent"].(float64); bs != 1024 {
				t.Errorf("bytes_sent = %v; want 1024", row["bytes_sent"])
			}
			if br, _ := row["bytes_received"].(float64); br != 512 {
				t.Errorf("bytes_received = %v; want 512", row["bytes_received"])
			}
		})
	}
}

// TestTrailEmitRelayAttemptConnectionFailureZeroBytes verifies the acceptance
// criterion: bytes_sent and bytes_received default to 0 when the flags are
// omitted on a connection-failure outcome.
func TestTrailEmitRelayAttemptConnectionFailureZeroBytes(t *testing.T) {
	stateDir := t.TempDir()

	stdout, stderr, code := runTrailEmitRelayAttempt(t, stateDir,
		"--token", "tok-conn",
		"--endpoint", "http://host/gone",
		"--outcome", "connection_refused",
		"--instance-id", "inst-conn-1",
		// intentionally omit --bytes-sent / --bytes-received
	)
	if code != 0 {
		t.Fatalf("exit = %d; want 0\nstderr=%s", code, stderr)
	}
	if strings.TrimRight(stdout, "\n") != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}

	lines := readTrailLines(t, stateDir)
	ra := relayAttemptLines(lines)
	if len(ra) != 1 {
		t.Fatalf("ad.relay_attempt.completed count = %d; want 1", len(ra))
	}
	row := ra[0]

	if bs, _ := row["bytes_sent"].(float64); bs != 0 {
		t.Errorf("bytes_sent = %v; want 0 (connection failure, flag omitted)", row["bytes_sent"])
	}
	if br, _ := row["bytes_received"].(float64); br != 0 {
		t.Errorf("bytes_received = %v; want 0 (connection failure, flag omitted)", row["bytes_received"])
	}
	if row["outcome"] != "connection_refused" {
		t.Errorf("outcome = %v; want connection_refused", row["outcome"])
	}
}

// TestTrailEmitRelayAttemptNoDBRequired verifies that the verb succeeds even
// when state.db does not exist. trail-emit is special-cased in main.go before
// setupClient (SR-A-2.3, t3.4uk.nz.j9.2k).
func TestTrailEmitRelayAttemptNoDBRequired(t *testing.T) {
	stateDir := t.TempDir()
	// home with no .agent-director directory — no DB exists.
	home := t.TempDir()

	stdout, stderr, code := runCLIWithEnv(t, home,
		map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
		"",
		"trail-emit", "relay-attempt",
		"--token", "tok-nodb",
		"--endpoint", "http://example.com",
		"--outcome", "200",
		"--instance-id", "inst-nodb-1",
	)
	if code != 0 {
		t.Fatalf("exit = %d; want 0 (no state.db should be needed)\nstderr=%s", code, stderr)
	}
	if strings.TrimRight(stdout, "\n") != "{}" {
		t.Errorf("stdout = %q; want \"{}\"", stdout)
	}

	lines := readTrailLines(t, stateDir)
	ra := relayAttemptLines(lines)
	if len(ra) != 1 {
		t.Fatalf("ad.relay_attempt.completed count = %d; want 1", len(ra))
	}
}

// trailEmitNegativeCase parameterizes TestTrailEmitRelayAttemptFlagErrors.
type trailEmitNegativeCase struct {
	name string
	args []string
}

// TestTrailEmitRelayAttemptFlagErrors covers the not-fail-open contract:
// malformed or missing required flags must produce a non-zero exit code,
// an ErrInvalidFlags envelope on stderr, and zero ad.relay_attempt.completed
// lines in the trail (no partial emit on bad input).
func TestTrailEmitRelayAttemptFlagErrors(t *testing.T) {
	cases := []trailEmitNegativeCase{
		{
			name: "bad_outcome_banana",
			args: []string{
				"--token", "tok", "--endpoint", "http://x",
				"--outcome", "banana", "--instance-id", "inst-1",
			},
		},
		{
			name: "bad_outcome_out_of_range",
			args: []string{
				"--token", "tok", "--endpoint", "http://x",
				"--outcome", "99", "--instance-id", "inst-1",
			},
		},
		{
			name: "missing_token",
			args: []string{
				"--endpoint", "http://x", "--outcome", "200", "--instance-id", "inst-1",
			},
		},
		{
			name: "missing_instance_id",
			args: []string{
				"--token", "tok", "--endpoint", "http://x", "--outcome", "200",
			},
		},
		{
			name: "missing_outcome",
			args: []string{
				"--token", "tok", "--endpoint", "http://x", "--instance-id", "inst-1",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			home := t.TempDir()

			args := append([]string{"trail-emit", "relay-attempt"}, tc.args...)
			_, stderr, code := runCLIWithEnv(t, home,
				map[string]string{"AGENT_DIRECTOR_STATE_DIR": stateDir},
				"", args...)

			// Must exit non-zero.
			if code == 0 {
				t.Fatalf("exit = 0; want non-zero")
			}

			// stderr must be a parseable ErrInvalidFlags envelope.
			env := parseEnvelope(t, stderr)
			if env.ErrName != "ErrInvalidFlags" {
				t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
			}

			// Trail: zero ad.relay_attempt.completed lines — no partial emit.
			ra := relayAttemptLines(trailLinesOrNil(t, stateDir))
			if len(ra) != 0 {
				t.Errorf("expected 0 trail lines; got %d: %v", len(ra), ra)
			}
		})
	}
}
