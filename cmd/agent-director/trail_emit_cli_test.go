package main_test

import (
	"fmt"
	"testing"
)

// TestTrailEmitRelayAttemptCLI: trail-emit relay-attempt, DB-free (SR-A-2.3),
// prints {} and appends one ad.relay_attempt.completed line carrying its
// flags, the outcome a JSON number for an HTTP status and a string for a named
// class, the byte counts 0 when omitted and read as decimal (b.c4n), and source
// relay_hook (SR-A-1.4). Its flag refusals are advice_follow_cli_test.go's H2,
// H3 and H7 and int_flags_cli_test.go's; the outcome parse is trail_emit_cmd_test.go's.
func TestTrailEmitRelayAttemptCLI(t *testing.T) {
	for _, tc := range []struct {
		outcome     string
		bytes       []string
		wantOutcome any
		wantBytes   float64
	}{
		{"404", []string{"--bytes-sent", "1024", "--bytes-received", "1024"}, float64(404), 1024},
		{"200", []string{"--bytes-sent", "010", "--bytes-received", "010"}, float64(200), 10},
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
			want := map[string]any{"outcome": tc.wantOutcome, "source": "relay_hook", "instance_id": "inst-test-1",
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

// b.t1b: globals around relay-attempt select its trail only at flag positions;
// token values and words after an operand must never redirect HOME.
func TestTrailEmitGlobalFlagPositions(t *testing.T) {
	for _, position := range []string{"before subverb", "in relay flags", "token value", "after operand"} {
		t.Run(position, func(t *testing.T) {
			home, selected, decoy := t.TempDir(), t.TempDir(), t.TempDir()
			token := "tok-global"
			args := []string{"trail-emit"}
			if position == "before subverb" {
				args = append(args, "--home", selected)
			}
			args = append(args, "relay-attempt")
			if position == "token value" {
				token = "--home=" + decoy
			}
			args = append(args, "--token", token, "--endpoint", "http://127.0.0.1:9/r", "--outcome", "200", "--instance-id", "trail-global")
			if position == "in relay flags" {
				args = append(args, "--home", selected)
			}
			unexpected := []string{"stray", "--home=" + decoy}
			if position == "after operand" {
				args = append(args, unexpected...)
			}
			stdout, stderr, code := runCLIWithHome(t, home, args...)
			assertHomeTree(t, decoy)
			if position == "after operand" {
				env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
				if want := fmt.Sprintf("unexpected arguments: %q", unexpected); env.ErrDescription != want {
					t.Errorf("err_description=%q; want %q", env.ErrDescription, want)
				}
				assertHomeTree(t, home)
				assertHomeTree(t, selected)
				return
			}
			if code != 0 || stdout != "{}\n" || stderr != "" {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want 0, {} and empty", code, stdout, stderr)
			}
			trailHome := selected
			if position == "token value" {
				trailHome = home
				assertHomeTree(t, selected)
			} else {
				assertHomeTree(t, home)
			}
			assertHomeTree(t, trailHome, ".config", ".config/agent-director", ".config/agent-director/ad-trail.jsonl")
			lines := readTrailLines(t, trailHome)
			attempts := relayAttemptLines(lines)
			if len(lines) != 1 || len(attempts) != 1 {
				t.Fatalf("trail=%v; want one relay-attempt record", lines)
			}
			for k, want := range map[string]any{"request_token": token, "instance_id": "trail-global", "source": "relay_hook", "target_endpoint": "http://127.0.0.1:9/r", "outcome": float64(200)} {
				if got := attempts[0][k]; got != want {
					t.Errorf("record[%q]=%v; want %v", k, got, want)
				}
			}
		})
	}
}

// TestTrailEmitOldLayoutReportsTrailWrite: with only the old directory present
// trail-emit writes nothing and creates nothing: it fails ErrTrailWrite, as for
// any trail it cannot write, and the home keeps every path and byte (SR-11.7).
func TestTrailEmitOldLayoutReportsTrailWrite(t *testing.T) {
	o := newOldLayout(t)

	stdout, stderr, code := runCLIWithHome(t, o.home, "trail-emit", "relay-attempt", "--token", "tok-old",
		"--endpoint", "http://127.0.0.1:9/r", "--outcome", "200", "--instance-id", "old-x")

	assertOnlyEnvelope(t, stdout, stderr, code, "ErrTrailWrite")
	o.assertUntouched(t)
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
