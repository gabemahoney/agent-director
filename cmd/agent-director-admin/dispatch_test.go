package main_test

// dispatch_test.go covers agent-director-admin's own refusals (b.vqr): an
// unknown verb (plain kill included) is ErrUnknownVerb, a bad flag, a global
// flag with no value or a positional argument ErrInvalidFlags, each with one
// envelope and nothing done; the old --claude-instance-id flag's refusal naming
// --instance-id (SR-4.3); and the literal follows of its advice (b.fji H1, H3, H8).

import (
	"encoding/json"
	"strings"
	"testing"
)

// oldIDRefusal is the description of the old instance-id flag's refusal.
const oldIDRefusal = "--claude-instance-id is no longer accepted; use --instance-id"

// oldIDForms are the spellings of the old flag with a value that the flag
// package accepts, with the spelling of the flag it is replaced by.
var oldIDForms = []struct {
	name     string
	old, new func(id string) []string
}{
	{"double dash", func(id string) []string { return []string{"--claude-instance-id", id} },
		func(id string) []string { return []string{"--instance-id", id} }},
	{"double dash equals", func(id string) []string { return []string{"--claude-instance-id=" + id} },
		func(id string) []string { return []string{"--instance-id=" + id} }},
	{"single dash", func(id string) []string { return []string{"-claude-instance-id", id} },
		func(id string) []string { return []string{"-instance-id", id} }},
	{"single dash equals", func(id string) []string { return []string{"-claude-instance-id=" + id} },
		func(id string) []string { return []string{"-instance-id=" + id} }},
}

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
		{"plain kill", func(id string) []string { return []string{"kill", "--instance-id", id} }, "ErrUnknownVerb"},
		// b.pu2: `--store-path ""` once opened HOME's default store, this row's.
		{"global flag with an empty two-token value", func(id string) []string {
			return []string{"--store-path", "", "kill-finished", "--instance-id", id}
		}, "ErrInvalidFlags"},
		{"kill-finished opt-in flag", func(id string) []string {
			return []string{"kill-finished", "--instance-id", id, "--include-finished"}
		}, "ErrInvalidFlags"},
		{"kill-finished positional", func(id string) []string { return []string{"kill-finished", id} }, "ErrInvalidFlags"},
		{"delete unknown flag", func(id string) []string {
			return []string{"delete", "--instance-id", id, "--bogus"}
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
		{"kill-finished", "--instance-id is required", func(t *testing.T, r finishedRow, stdout string) {
			assertKillSent(t, stdout, true)
		}},
		{"delete", "--instance-id is required (≥1)", func(t *testing.T, r finishedRow, stdout string) {
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

// TestAdminRefusesOldInstanceIDFlag: kill-finished and delete refuse
// --claude-instance-id, in each form the flag package accepts, with one
// ErrInvalidFlags envelope naming --instance-id; nothing runs: no tmux call, no
// kill trail line, and the row and its session stay as they were (SR-4.3).
func TestAdminRefusesOldInstanceIDFlag(t *testing.T) {
	for _, verb := range []string{"kill-finished", "delete"} {
		for _, form := range oldIDForms {
			t.Run(verb+"/"+form.name, func(t *testing.T) {
				r := seedFinishedWithSession(t)

				stdout, stderr, code := runAdmin(t, r.home, append([]string{verb}, form.old(r.id)...)...)

				env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags")
				if env.ErrDescription != oldIDRefusal {
					t.Errorf("err_description = %q; want %q", env.ErrDescription, oldIDRefusal)
				}
				assertInvocationKinds(t, r.home)
				if got := killCalled(t, r.home); len(got) != 0 {
					t.Errorf("ad.kill.called lines = %v; want none", got)
				}
				if left := sessionsLeft(t, r.socket); len(left) != 1 {
					t.Errorf("sessions = %+v; want the row's session untouched", left)
				}
				assertRowUnchanged(t, r.home, r.id, r.before)
			})
		}
	}
}

// TestAdviceFollow_H8_AdminOldInstanceIDFlag: H8 "--claude-instance-id is no
// longer accepted; use --instance-id". Re-run with the flag it names, in the
// spelling the caller used, the verb runs and succeeds.
func TestAdviceFollow_H8_AdminOldInstanceIDFlag(t *testing.T) {
	for _, verb := range []string{"kill-finished", "delete"} {
		for _, form := range oldIDForms {
			t.Run(verb+"/"+form.name, func(t *testing.T) {
				r := seedFinishedWithSession(t)
				stdout, stderr, code := runAdmin(t, r.home, append([]string{verb}, form.old(r.id)...)...)
				if desc := assertOnlyEnvelope(t, stdout, stderr, code, "ErrInvalidFlags").ErrDescription; desc != oldIDRefusal {
					t.Fatalf("description = %q; want %q", desc, oldIDRefusal)
				}

				stdout, stderr, code = runAdmin(t, r.home, append([]string{verb}, form.new(r.id)...)...)

				if code != 0 || stderr != "" {
					t.Fatalf("%s with --instance-id: exit = %d, stderr = %q; want 0 and empty", verb, code, stderr)
				}
				if verb == "delete" {
					assertRowGone(t, r.home, r.id)
				} else {
					assertKillSent(t, stdout, true)
				}
			})
		}
	}
}
