package main_test

// dispatch_test.go covers agent-director-admin's own refusals (b.vqr): an
// unknown verb (plain kill included) is ErrUnknownVerb, a bad flag, a global
// flag with no value or a positional argument ErrInvalidFlags, each with one
// envelope and nothing done; and the literal follows of its advice (b.fji H1,
// H3).

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestAdminRefusesUnknownVerbsAndFlags: each refusal exits 1 with exactly one
// envelope, makes no tmux call and leaves a finished row's own reported-in
// session, which a kill-finished that ran would end, and the row as they were.
func TestAdminRefusesUnknownVerbsAndFlags(t *testing.T) {
	cases := []struct {
		name string
		args func(id string) []string
		want string
	}{
		{"unknown verb", func(string) []string { return []string{"frob"} }, "ErrUnknownVerb"},
		{"plain kill", func(id string) []string { return []string{"kill", "--claude-instance-id", id} }, "ErrUnknownVerb"},
		{"global flag with no value", func(id string) []string {
			return []string{"kill-finished", "--claude-instance-id", id, "--store-path"}
		}, "ErrInvalidFlags"},
		{"global flag with an empty value", func(id string) []string {
			return []string{"--home=", "kill-finished", "--claude-instance-id", id}
		}, "ErrInvalidFlags"},
		// b.pu2: `--store-path ""` once opened HOME's default store, this row's.
		{"global flag with an empty two-token value", func(id string) []string {
			return []string{"--store-path", "", "kill-finished", "--claude-instance-id", id}
		}, "ErrInvalidFlags"},
		{"kill-finished unknown flag", func(id string) []string {
			return []string{"kill-finished", "--claude-instance-id", id, "--bogus"}
		}, "ErrInvalidFlags"},
		{"kill-finished opt-in flag", func(id string) []string {
			return []string{"kill-finished", "--claude-instance-id", id, "--include-finished"}
		}, "ErrInvalidFlags"},
		{"kill-finished positional", func(id string) []string { return []string{"kill-finished", id} }, "ErrInvalidFlags"},
		{"delete unknown flag", func(id string) []string {
			return []string{"delete", "--claude-instance-id", id, "--bogus"}
		}, "ErrInvalidFlags"},
		{"delete positional", func(id string) []string {
			return []string{"delete", "--claude-instance-id", id, id}
		}, "ErrInvalidFlags"},
		{"version flag", func(string) []string { return []string{"version", "--bogus"} }, "ErrInvalidFlags"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := seedFinishedWithSession(t)

			stdout, stderr, code := runAdmin(t, r.home, tc.args(r.id)...)

			assertOnlyEnvelope(t, stdout, stderr, code, tc.want)
			assertInvocationKinds(t, r.home)
			if left := sessionsLeft(t, r.socket); len(left) != 1 {
				t.Errorf("sessions = %+v; want the row's session untouched", left)
			}
			assertRowUnchanged(t, r.home, r.id, r.before)
		})
	}
}

// TestAdviceFollow_H1_AdminUnknownVerbTryHelp: H1 "unknown verb %q; try 'agent-director-admin help'".
func TestAdviceFollow_H1_AdminUnknownVerbTryHelp(t *testing.T) {
	home := t.TempDir()
	stdout, stderr, code := runAdmin(t, home, "frob")
	desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrUnknownVerb").ErrDescription
	const want = `unknown verb "frob"; try 'agent-director-admin help'`
	if !strings.Contains(desc, want) {
		t.Fatalf("description %q lacks %q", desc, want)
	}

	_, quoted, _ := strings.Cut(desc, "try '")
	command, _, _ := strings.Cut(quoted, "'")
	argv := strings.Fields(command)
	if len(argv) < 1 || argv[0] != "agent-director-admin" {
		t.Fatalf("advised command %q is not an agent-director-admin invocation", command)
	}
	stdout, stderr, code = runAdmin(t, home, argv[1:]...)
	if code != 0 || stderr != "" {
		t.Fatalf("advised %q: exit = %d, stderr = %q; want 0 and empty", command, code, stderr)
	}
	if first, _, _ := strings.Cut(stdout, "\n"); first != approvalStatement {
		t.Errorf("advised %q: first line = %q; want the human-approval statement", command, first)
	}
	if !strings.Contains(stdout, usageLines["kill-finished"]) || !strings.Contains(stdout, usageLines["delete"]) {
		t.Errorf("advised %q lists no verb usage:\n%s", command, stdout)
	}
}

// TestAdviceFollow_H3_AdminFlagIsRequired: H3 "--X is required" family. Dropping
// the flag gives the message; adding the flag it names runs the verb.
func TestAdviceFollow_H3_AdminFlagIsRequired(t *testing.T) {
	cases := []struct {
		verb, want string
		check      func(t *testing.T, r finishedRow, stdout string)
	}{
		{"kill-finished", "--claude-instance-id is required", func(t *testing.T, r finishedRow, stdout string) {
			assertKillSent(t, stdout, true)
		}},
		{"delete", "--claude-instance-id is required (≥1)", func(t *testing.T, r finishedRow, stdout string) {
			var res struct {
				Results map[string]string `json:"results"`
			}
			if err := json.Unmarshal([]byte(stdout), &res); err != nil || len(res.Results) != 1 || res.Results[r.id] != "ok" {
				t.Errorf("stdout = %q; want results {%s: ok}", stdout, r.id)
			}
			assertRowGone(t, r.home, r.id)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.verb, func(t *testing.T) {
			r := seedFinishedWithSession(t)
			stdout, stderr, code := runAdmin(t, r.home, tc.verb)
			desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription
			if desc != tc.want {
				t.Fatalf("description = %q; want %q", desc, tc.want)
			}
			assertInvocationKinds(t, r.home)
			assertRowUnchanged(t, r.home, r.id, r.before)

			flag, _, _ := strings.Cut(desc, " ")
			stdout, stderr, code = runAdmin(t, r.home, tc.verb, flag, r.id)
			if code != 0 || stderr != "" {
				t.Fatalf("%s %s %s: exit = %d, stderr = %q; want 0 and empty", tc.verb, flag, r.id, code, stderr)
			}
			tc.check(t, r, stdout)
		})
	}
}
