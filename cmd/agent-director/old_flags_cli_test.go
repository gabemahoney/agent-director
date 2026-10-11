package main_test

// old_flags_cli_test.go: the CLI refuses the flags it had before it lost their
// Claude names, naming the flag that replaced each (SR-4.3): in every form the
// flag package accepts, on every verb that now has --instance-id and on
// make-template's --agent-args, and never for arguments after spawn's `--`.
// Each refusal runs nothing; H8 and H9 follow the advice. The admin binary's
// rows are dispatch_test.go's.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

const (
	oldIDFlag      = "claude-instance-id"
	oldIDRefusal   = "--claude-instance-id is no longer accepted; use --instance-id"
	oldArgsFlag    = "claude-args"
	oldArgsRefusal = "--claude-args is no longer accepted; use --agent-args"
)

// oldFlagForms are the spellings of a flag with a value that the flag package accepts.
var oldFlagForms = []struct {
	name   string
	render func(flag, value string) []string
}{
	{"double dash", func(f, v string) []string { return []string{"--" + f, v} }},
	{"double dash equals", func(f, v string) []string { return []string{"--" + f + "=" + v} }},
	{"single dash", func(f, v string) []string { return []string{"-" + f, v} }},
	{"single dash equals", func(f, v string) []string { return []string{"-" + f + "=" + v} }},
}

// oldFlagWorld is a HOME holding one seeded row (and a pending permission
// request when the verb decides one), with what it held before any run.
type oldFlagWorld struct {
	home, fakeDir, id, token, socket string
	before                           apitest.SpawnColumns
	trailLines                       int
}

// newOldFlagWorld seeds the row in state on the socket the CLI resolves, with
// its own live session when pane is set and a permission request when
// request is set (the row then relays).
func newOldFlagWorld(t *testing.T, state string, pane, request bool) oldFlagWorld {
	t.Helper()
	home := t.TempDir()
	socket := spawnSocket(t, home)
	relay := ""
	if request {
		relay = "on"
	}
	opts := []apitest.SpawnOption{apitest.WithTmuxSocket(socket)}
	if state == store.StateEnded { // a finished row resumes from its transcript
		transcript := filepath.Join(home, "transcript.jsonl")
		if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
		opts = append(opts, apitest.WithJsonlPath(transcript))
	}
	id, err := apitest.SeedSpawn(stateDB(home), "", state, "", relay, uuid.NewString(), true, opts...)
	if err != nil {
		t.Fatalf("SeedSpawn: %v", err)
	}
	w := oldFlagWorld{home: home, fakeDir: buildFakeTmux(t), id: id, socket: socket}
	if request {
		req, err := apitest.SeedPermissionRequest(stateDB(home), id, "Bash")
		if err != nil {
			t.Fatalf("SeedPermissionRequest: %v", err)
		}
		w.token = req.RequestToken
	}
	if pane {
		faketmuxfix.Tables{}.Write(t, socket, fakeTable(ownSession(t, home, id, "")))
	}
	w.before = rowColumns(t, home, id)
	w.trailLines = len(trailOrNil(t, home))
	return w
}

// run runs the CLI under the world's HOME with the fake tmux first on PATH.
func (w oldFlagWorld) run(t *testing.T, argv ...string) (stdout, stderr string, code int) {
	t.Helper()
	return runSpawnCLI(t, w.home, w.fakeDir, argv...)
}

// assertNothingRan fails unless nothing the verb would have done happened: the
// seeded row is unchanged and still the store's only one, no permission
// request was decided, no template file exists, the fake tmux was never run
// and the trail holds wantTrail more lines than before.
func (w oldFlagWorld) assertNothingRan(t *testing.T, wantTrail int) {
	t.Helper()
	if after := rowColumns(t, w.home, w.id); jsonOf(t, after) != jsonOf(t, w.before) {
		t.Errorf("row %s = %+v; want unchanged %+v", w.id, after, w.before)
	}
	s := openDBForRead(t, w.home)
	if spawns, err := s.ListSpawns(store.ListFilters{}); err != nil || len(spawns) != 1 {
		t.Errorf("rows = %+v (err %v); want only the seeded row", spawns, err)
	}
	reqs, err := s.PermissionRequestsForSpawn(w.id)
	if err != nil {
		t.Fatalf("PermissionRequestsForSpawn: %v", err)
	}
	for _, r := range reqs {
		if r.Decision != "" {
			t.Errorf("request %s decision = %q; want none recorded", r.RequestToken, r.Decision)
		}
	}
	if _, err := os.Stat(filepath.Join(directorDir(w.home), "templates")); !os.IsNotExist(err) {
		t.Errorf("templates dir: stat err = %v; want it absent", err)
	}
	assertInvocationKinds(t, w.home)
	if got := len(trailOrNil(t, w.home)) - w.trailLines; got != wantTrail {
		t.Errorf("trail grew by %d lines; want %d", got, wantTrail)
	}
}

// oldFlagFollow returns argv with the old flag desc refuses (whatever its
// dashes or "=" form) replaced by the flag it says to use, as a caller does.
func oldFlagFollow(t *testing.T, argv []string, desc string) []string {
	t.Helper()
	m := regexp.MustCompile(`^--([a-z-]+) is no longer accepted; use (--[a-z-]+)$`).FindStringSubmatch(desc)
	if m == nil {
		t.Fatalf("description %q is not an old-flag refusal", desc)
	}
	out := append([]string{}, argv...)
	replaced := 0
	for i, a := range out {
		if name, value, ok := strings.Cut(strings.TrimLeft(a, "-"), "="); strings.HasPrefix(a, "-") && name == m[1] {
			out[i] = m[2]
			if ok {
				out[i] += "=" + value
			}
			replaced++
		}
	}
	if replaced != 1 {
		t.Fatalf("argv %q holds the old flag %d times; want once", argv, replaced)
	}
	return out
}

// oldIDVerb is a verb that takes --instance-id: the row it needs, its flags
// other than the id (verb first), what its refusal writes to the trail, and the
// value to give the old flag.
type oldIDVerb struct {
	verb          string
	state         string
	pane, request bool
	flags         func(w oldFlagWorld) []string
	wantTrail     int
	value         func(w oldFlagWorld) string
}

func oldIDVerbs() []oldIDVerb {
	rowID := func(w oldFlagWorld) string { return w.id }
	only := func(args ...string) func(oldFlagWorld) []string { return func(oldFlagWorld) []string { return args } }
	return []oldIDVerb{
		{verb: "status", state: store.StateWaiting, flags: only("status"), value: rowID},
		{verb: "get", state: store.StateWaiting, flags: only("get"), value: rowID},
		{verb: "read-pane", state: store.StateWaiting, pane: true, flags: only("read-pane"), value: rowID},
		{verb: "send-keys", state: store.StateWaiting, pane: true, flags: only("send-keys", "--text", "hi"), value: rowID},
		{verb: "kill", state: store.StateWaiting, pane: true, flags: only("kill"), value: rowID},
		{verb: "pause", state: store.StateEnded, flags: only("pause"), value: rowID},
		{verb: "resume", state: store.StateEnded, flags: only("resume"), value: rowID},
		// decide writes its own ad.decide.called line, ErrInvalidFlags, for any refused flag.
		{verb: "decide", state: store.StateCheckPermission, request: true, wantTrail: 1, value: rowID,
			flags: func(w oldFlagWorld) []string {
				return []string{"decide", "--request-token", w.token, "--decision", "allow"}
			}},
		{verb: "spawn", state: store.StateWaiting, value: func(oldFlagWorld) string { return uuid.NewString() },
			flags: func(w oldFlagWorld) []string {
				return withAgent("claude", "spawn", "--cwd", w.home, "--no-pre-trust")
			}},
		{verb: "trail-emit", state: store.StateWaiting, value: func(oldFlagWorld) string { return "inst-old-flag" },
			flags: only("trail-emit", "relay-attempt", "--token", "tok-old-flag", "--endpoint", "http://127.0.0.1:9/r", "--outcome", "200")},
	}
}

// TestCLIRefusesOldInstanceIDFlag: every verb with --instance-id refuses
// --claude-instance-id, in each form the flag package accepts, with one
// ErrInvalidFlags envelope naming --instance-id, and nothing runs (SR-4.3).
func TestCLIRefusesOldInstanceIDFlag(t *testing.T) {
	for _, v := range oldIDVerbs() {
		for _, form := range oldFlagForms {
			t.Run(v.verb+"/"+form.name, func(t *testing.T) {
				w := newOldFlagWorld(t, v.state, v.pane, v.request)
				argv := append(v.flags(w), form.render(oldIDFlag, v.value(w))...)

				stdout, stderr, code := w.run(t, argv...)

				env := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrInvalidFlags")
				if env.ErrDescription != oldIDRefusal {
					t.Errorf("err_description = %q; want %q", env.ErrDescription, oldIDRefusal)
				}
				w.assertNothingRan(t, v.wantTrail)
			})
		}
	}
}

// TestAdviceFollow_H8_OldInstanceIDFlag: H8 "--claude-instance-id is no longer
// accepted; use --instance-id" (both binaries). Re-run with the flag it names,
// everything else as it was, the verb runs and succeeds.
func TestAdviceFollow_H8_OldInstanceIDFlag(t *testing.T) {
	for _, v := range oldIDVerbs() {
		t.Run(v.verb, func(t *testing.T) {
			w := newOldFlagWorld(t, v.state, v.pane, v.request)
			argv := append(v.flags(w), "--"+oldIDFlag, v.value(w))
			stdout, stderr, code := w.run(t, argv...)
			desc := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrInvalidFlags").ErrDescription
			if desc != oldIDRefusal {
				t.Fatalf("description = %q; want %q", desc, oldIDRefusal)
			}

			stdout, stderr, code = w.run(t, oldFlagFollow(t, argv, desc)...)
			if code != 0 {
				t.Fatalf("%s with --instance-id: exit = %d, stdout = %q, stderr = %q; want 0", v.verb, code, stdout, stderr)
			}
		})
	}
}

// TestCLIRefusesOldAgentArgsFlag: make-template refuses --claude-args, in each
// form, naming --agent-args, and writes no template file (SR-4.3).
func TestCLIRefusesOldAgentArgsFlag(t *testing.T) {
	for _, form := range oldFlagForms {
		t.Run(form.name, func(t *testing.T) {
			w := newOldFlagWorld(t, store.StateWaiting, false, false)
			argv := withAgent("claude", "make-template", append([]string{"--name", "old-args"}, form.render(oldArgsFlag, "--model")...)...)

			stdout, stderr, code := w.run(t, argv...)

			env := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrInvalidFlags")
			if env.ErrDescription != oldArgsRefusal {
				t.Errorf("err_description = %q; want %q", env.ErrDescription, oldArgsRefusal)
			}
			w.assertNothingRan(t, 0)
		})
	}
}

// TestAdviceFollow_H9_OldAgentArgsFlag: H9 "--claude-args is no longer
// accepted; use --agent-args". Re-run with the flag it names, make-template
// writes the template, which holds the args as agent_args.
func TestAdviceFollow_H9_OldAgentArgsFlag(t *testing.T) {
	w := newOldFlagWorld(t, store.StateWaiting, false, false)
	argv := withAgent("claude", "make-template", "--name", "old-args", "--claude-args", "--model")
	stdout, stderr, code := w.run(t, argv...)
	desc := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrInvalidFlags").ErrDescription
	if desc != oldArgsRefusal {
		t.Fatalf("description = %q; want %q", desc, oldArgsRefusal)
	}

	stdout, stderr, code = w.run(t, oldFlagFollow(t, argv, desc)...)
	if code != 0 {
		t.Fatalf("make-template with --agent-args: exit = %d, stdout = %q, stderr = %q; want 0", code, stdout, stderr)
	}
	body, err := os.ReadFile(filepath.Join(directorDir(w.home), "templates", "old-args.toml"))
	if err != nil || !strings.Contains(string(body), "agent_args = ") || !strings.Contains(string(body), "--model") {
		t.Errorf("template = %q (err %v); want it to hold agent_args with --model", body, err)
	}
}

// TestCLIOldFlagUnknownWhereNewIsAbsent: a verb whose FlagSet lacks the new
// flag keeps the flag package's unknown-flag error for the old one (spawn has
// no --agent-args: its args follow `--`; list has no --instance-id), and
// nothing runs.
func TestCLIOldFlagUnknownWhereNewIsAbsent(t *testing.T) {
	cases := []struct {
		name string
		argv func(w oldFlagWorld) []string
		want string
	}{
		{"spawn --claude-args", func(w oldFlagWorld) []string {
			return withAgent("claude", "spawn", "--cwd", w.home, "--claude-args", "--model")
		}, "flag provided but not defined: -claude-args"},
		{"list --claude-instance-id", func(w oldFlagWorld) []string {
			return []string{"list", "--claude-instance-id", w.id}
		}, "flag provided but not defined: -claude-instance-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newOldFlagWorld(t, store.StateWaiting, false, false)

			stdout, stderr, code := w.run(t, tc.argv(w)...)

			env := assertOnlyEnvelope(t, stdout, lastJSONLine(stderr), code, "ErrInvalidFlags")
			if env.ErrDescription != tc.want {
				t.Errorf("err_description = %q; want %q", env.ErrDescription, tc.want)
			}
			w.assertNothingRan(t, 0)
		})
	}
}

// TestSpawnOldFlagsAfterDashDashPassThrough: after spawn's `--` an old flag is
// the agent's own argument, passed through unchanged, not refused (SR-4.3).
func TestSpawnOldFlagsAfterDashDashPassThrough(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	id := spawnOK(t, home, fakeDir, "--cwd", t.TempDir(), "--no-pre-trust", "--",
		"--claude-instance-id", "x", "--claude-args=y")

	got, err := json.Marshal(getRow(t, home, fakeDir, id)["agent_args"])
	if want := `["--claude-instance-id","x","--claude-args=y"]`; err != nil || string(got) != want {
		t.Errorf("agent_args = %s (err %v); want %s", got, err, want)
	}
}
