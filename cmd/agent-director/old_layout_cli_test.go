package main_test

// old_layout_cli_test.go covers the built binary on a home that holds only the
// old ~/.agent-director (SR-11.7, bug b.nx4): every store-opening verb refuses
// with ErrOldLayout and creates nothing, the hook fails open and creates
// nothing, and the DB-free verbs, trail-emit and the no-verb run create
// nothing either. The presence rules themselves (links, both directories, a
// store already in the new one) are pkg/api's old-layout
// tests and internal/layout's; this file keeps to a plain old directory. The DB-free
// shapes are TestDBFreeVerbsProduceNoStore's (side_effects_test.go).

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// dbFreeVerbs are the manifest verbs that open no store: help, version,
// trail-emit and the hook, which run() and runHook dispatch before
// setupClient.
var dbFreeVerbs = []string{"help", "version", "trail-emit", "hook"}

// storeOpeningVerbs returns every verb of the manifest that opens the store,
// each as a bare argv (serve with --stdio): the refusal comes before any flag
// is read. Deriving them from the manifest means a verb added later is
// covered without an edit here.
func storeOpeningVerbs(t *testing.T) [][]string {
	t.Helper()
	var argvs [][]string
	for _, v := range manifest.Verbs {
		// Internal gateway plumbing parses and opens an existing store itself,
		// before public setupClient and its ErrOldLayout envelope.
		if slices.Contains(dbFreeVerbs, v.Name) || v.Name == "cursor-host" {
			continue
		}
		argv := []string{v.Name}
		if v.Name == "serve" {
			argv = append(argv, "--stdio")
		}
		argvs = append(argvs, argv)
	}
	if !slices.ContainsFunc(argvs, func(a []string) bool { return a[0] == "list" }) ||
		!slices.ContainsFunc(argvs, func(a []string) bool { return a[0] == "serve" }) {
		t.Fatalf("store-opening verbs = %v; want the manifest's, list and serve among them", argvs)
	}
	return argvs
}

func TestOldLayoutCursorHostRefusesWithoutCreatingFiles(t *testing.T) {
	for _, args := range [][]string{{"cursor-host"}, {"cursor-host", "--runtime", "must-not-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			o := newOldLayout(t)
			stdout, _, code := runCLIWithHome(t, o.home, args...)
			if code != 1 || stdout != "" {
				t.Fatalf("internal gateway exit=%d stdout=%q; want refusal", code, stdout)
			}
			o.assertUntouched(t)
		})
	}
}

// assertOldLayoutRefusal checks one refusal: exit 1 with only the ErrOldLayout
// envelope, whose description names this release's install.sh and the new
// directory, and the home exactly as seeded.
func assertOldLayoutRefusal(t *testing.T, o oldLayout, stdout, stderr string, code int) {
	t.Helper()
	env := assertOnlyEnvelope(t, stdout, stderr, code, "ErrOldLayout")
	for _, want := range []string{"install.sh", "~/.config/agent-director"} {
		if !strings.Contains(env.ErrDescription, want) {
			t.Errorf("err_description = %q; want it to name %q", env.ErrDescription, want)
		}
	}
	o.assertUntouched(t)
}

// TestOldLayoutRefusesEveryStoreOpeningVerb: with only the old directory, a
// store has been kept there and no new directory exists, every store-opening
// verb, serve included, exits 1 with only the ErrOldLayout envelope and the
// home tree unchanged: no new directory, no ~/.config, no errors-log line from
// clisetup.Open and no trail line.
func TestOldLayoutRefusesEveryStoreOpeningVerb(t *testing.T) {
	for _, argv := range storeOpeningVerbs(t) {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			o := newOldLayout(t)

			stdout, stderr, code := runCLIWithHome(t, o.home, argv...)

			assertOldLayoutRefusal(t, o, stdout, stderr, code)
		})
	}
}

// TestOldLayoutNotRefusedWithStorePath: --store-path names the store, so the
// old directory is not looked at: the verb succeeds on that store, and the home
// is still as seeded (the trail writes nothing while only the old directory
// exists).
func TestOldLayoutNotRefusedWithStorePath(t *testing.T) {
	o := newOldLayout(t)
	dir := t.TempDir()

	stdout, stderr, code := runCLIWithHome(t, o.home, "--store-path", filepath.Join(dir, "s.db"), "list")

	if code != 0 || stdout != "{\"spawns\":[]}\n" || stderr != "" {
		t.Fatalf("list --store-path: exit=%d stdout=%q stderr=%q; want 0 and an empty list", code, stdout, stderr)
	}
	o.assertUntouched(t)
	if _, err := os.Stat(filepath.Join(dir, "s.db")); err != nil {
		t.Errorf("no store at --store-path: %v", err)
	}
}

// TestOldLayoutHookFailsOpenAndCreatesNothing: in that state the hook exits 0
// with empty stdout and logs one line, creating nothing (the old-layout check
// comes before the hook's logger, which would create errors.log); a relayed
// PermissionRequest still gets its deny.
func TestOldLayoutHookFailsOpenAndCreatesNothing(t *testing.T) {
	const (
		permission   = `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`
		sessionStart = `{"hook_event_name":"SessionStart","source":"startup","transcript_path":"/x/old.jsonl"}`
	)
	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	relayOn := map[string]string{hook.EnvRelayMode: hook.RelayModeOn, "AGENT_DIRECTOR_INSTANCE_ID": "id-old-layout"}
	relayOff := map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-old-layout"}
	for _, tc := range []struct {
		name    string
		env     map[string]string
		payload string
		want    string // the whole stdout
	}{
		{"SessionStart", relayOff, sessionStart, ""},
		{"SessionStart relay on", relayOn, sessionStart, ""},
		{"PermissionRequest relay off", relayOff, permission, ""},
		{"PermissionRequest relay on", relayOn, permission, deny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := newOldLayout(t)

			stdout, stderr, code, timedOut := runBounded(t, o.home, tc.env, tc.payload, false, surfaceDeadline, "hook")

			if timedOut || code != 0 || stdout != tc.want {
				t.Errorf("hook exit=%d stdout=%q timedOut=%v; want 0 and %q (stderr=%q)", code, stdout, timedOut, tc.want, stderr)
			}
			if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "install.sh") {
				t.Errorf("stderr = %q; want exactly one line naming install.sh", stderr)
			}
			o.assertUntouched(t)
		})
	}
}

// TestOldLayoutNoVerbHookPayloadDropsItsRecord: a no-verb run with a hook
// payload on stdin still prints nothing and exits 0, but its ad.hook.ignored
// record is dropped (the trail writes nothing while only the old directory
// exists), so the home is unchanged.
func TestOldLayoutNoVerbHookPayloadDropsItsRecord(t *testing.T) {
	o := newOldLayout(t)
	payload := `{"hook_event_name":"SessionStart","transcript_path":"` + noExecFormTranscript + `"}` + "\n"

	stdout, stderr, code := runNoVerb(t, o.home, map[string]string{"AGENT_DIRECTOR_INSTANCE_ID": "id-old-noverb"},
		cliOpts{stdin: payload})

	if code != 0 || stdout != "" {
		t.Errorf("exit=%d stdout=%q stderr=%q; want 0 and empty stdout", code, stdout, stderr)
	}
	o.assertUntouched(t)
}
