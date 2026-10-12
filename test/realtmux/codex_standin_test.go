package realtmux_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/codexfix"
)

// The realtmux stand-in `codex` and its record (E4-T2 T7). The sandbox has no
// Codex, so a bash script named codex is the pane's process: it records the
// arguments and environment its launch gave it, then runs AD's own composed
// SessionStart hook command the way Codex runs a hook, the account's login
// shell with -c and the payload on stdin. test/driver/stand-in-codex/codex is
// the Docker copy; the block between their "shared" markers is the same text
// (TestCodexStandInsShareTheirBlock).
//
// The stand-in reads its record directory from AD_CODEX_RECORD_DIR (the tmux
// server's global environment gives it to the pane) and its files from
// <dir>/<AGENT_DIRECTOR_INSTANCE_ID>/: argv and env (NUL-terminated entries),
// cwd and pid (written last) are what it records; hookcmd, payload.json and
// go are what the test hands it; shell, hook.out, hook.err and hook.exit
// (written last) are what it reports back.

const (
	codexRecordDirEnv = "AD_CODEX_RECORD_DIR"
	codexHookEnv      = "AD_CODEX_HOOK"

	codexSharedBegin = "# >>> shared with test/realtmux/codex_standin_test.go"
	codexSharedEnd   = "# <<< shared with test/realtmux/codex_standin_test.go"

	// codexDriverStandIn is the Docker driver's copy, relative to this package.
	codexDriverStandIn = "../driver/stand-in-codex/codex"
	// codexSessionStartFixture is E4-T1's Codex SessionStart payload.
	codexSessionStartFixture = "../../internal/hook/testdata/codex-hook-payloads/session-start-startup.json"
)

// codexSharedBlock is the part both stand-ins run: record the launch, then
// the opt-in hook run.
const codexSharedBlock = codexSharedBegin + `
id="${AGENT_DIRECTOR_INSTANCE_ID:-}"
dir=""
if [[ "$id" =~ ^[A-Za-z0-9._-]+$ ]]; then
    dir="${AD_CODEX_RECORD_DIR:-/tmp/stand-in-codex}/$id"
    mkdir -p "$dir"
    : >"$dir/argv"
    for a in "$@"; do
        printf '%s\0' "$a" >>"$dir/argv"
    done
    env -0 >"$dir/env"
    pwd -P >"$dir/cwd"
    printf '%s\n' "$$" >"$dir/pid"
fi
if [[ -n "$dir" && -n "${AD_CODEX_HOOK:-}" ]]; then
    for _ in {1..1500}; do
        [[ -e "$dir/go" ]] && break
        sleep 0.02
    done
    [[ -e "$dir/go" ]] || exit 1
    shell="$(getent passwd "$(id -u)" | cut -d: -f7)"
    case "${shell##*/}" in
        bash | zsh) ;;
        *) shell=bash ;;
    esac
    printf '%s\n' "$shell" >"$dir/shell"
    "$shell" -c "$(cat "$dir/hookcmd")" <"$dir/payload.json" >"$dir/hook.out" 2>"$dir/hook.err"
    printf '%s\n' "$?" >"$dir/hook.exit.tmp"
    mv "$dir/hook.exit.tmp" "$dir/hook.exit"
fi
` + codexSharedEnd + "\n"

// codexStubScript is the stand-in `codex`. After the shared block it stays the
// pane's process with a stable argv (a loop, never an exec), like the Claude
// stand-in of hook_parent_test.go.
const codexStubScript = "#!/bin/bash\n# Stand-in codex for test/realtmux/codex_launch_test.go.\n" +
	codexSharedBlock + "while :; do sleep 3600; done\n"

// codexRecord is one launch's directory of the stand-in.
type codexRecord struct{ dir string }

// codexRecordOf is the record directory of instance id under root.
func codexRecordOf(root, id string) codexRecord { return codexRecord{dir: filepath.Join(root, id)} }

// path is a file of the record.
func (r codexRecord) path(name string) string { return filepath.Join(r.dir, name) }

// has reports whether the record file name exists.
func (r codexRecord) has(name string) bool {
	_, err := os.Stat(r.path(name))
	return err == nil
}

// nulList reads a NUL-terminated record file (argv, env).
func (r codexRecord) nulList(t testing.TB, name string) []string {
	t.Helper()
	data, err := os.ReadFile(r.path(name))
	if err != nil {
		t.Fatalf("read the stand-in's %s: %v", name, err)
	}
	s := strings.TrimSuffix(string(data), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

// waitRecorded waits until the stand-in finished recording its launch (its pid
// file is written last).
func (r codexRecord) waitRecorded(t testing.TB) {
	t.Helper()
	waitFor(t, "the stand-in codex records its launch in "+r.dir,
		func() bool {
			data, err := os.ReadFile(r.path("pid"))
			return err == nil && strings.TrimSpace(string(data)) != ""
		},
		func() string { return "no pid file in " + r.dir })
}

// pid is the stand-in's recorded pid.
func (r codexRecord) pid(t testing.TB) int {
	t.Helper()
	n, err := strconv.Atoi(readFileTrim(t, r.path("pid")))
	if err != nil {
		t.Fatalf("the stand-in's pid file: %v", err)
	}
	return n
}

// launch is the recorded launch split by codexfix: the stand-in does not
// record the program, so the splitter is given "codex" as argv[0].
func (r codexRecord) launch(t testing.TB) codexfix.Launch {
	t.Helper()
	return codexfix.Split(t, append([]string{"codex"}, r.nulList(t, "argv")...))
}

// env is the recorded environment's value of key, and whether it was set.
func (r codexRecord) env(t testing.TB, key string) (string, bool) {
	t.Helper()
	return envValue(r.nulList(t, "env"), key)
}

// sessionStartCommand returns AD's SessionStart hook command from the recorded
// launch (the one `exec <path> hook --agent codex` handler, the help entry not
// being it) and the program path it names.
func sessionStartCommand(t testing.TB, l codexfix.Launch) (command, exe string) {
	t.Helper()
	var found []codexfix.Hook
	for _, h := range l.HooksFor("SessionStart") {
		if h.Kind == codexfix.HookAD {
			found = append(found, h)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the launch carries %d `hook --agent` handlers for SessionStart, want 1 (events %v)", len(found), l.Events())
	}
	h := found[0]
	if h.Agent != "codex" || h.Exec == "" {
		t.Fatalf("SessionStart handler %q: agent %q, program %q; want an exec form of --agent codex", h.Command, h.Agent, h.Exec)
	}
	return h.Command, h.Exec
}

// writePayload writes E4-T1's Codex SessionStart payload, its cwd the
// launch's and its transcript a rollout in a temporary Codex folder (the real
// one is never touched), to the record's payload.json; it returns the payload's
// session id.
func (r codexRecord) writePayload(t *testing.T, cwd string) string {
	t.Helper()
	raw, err := os.ReadFile(codexSessionStartFixture)
	if err != nil {
		t.Fatalf("read the Codex SessionStart fixture: %v", err)
	}
	var p map[string]any
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("Codex SessionStart fixture: %v", err)
	}
	sid, _ := p["session_id"].(string)
	if sid == "" {
		t.Fatalf("Codex SessionStart fixture has no session_id")
	}
	p["cwd"] = cwd
	p["transcript_path"] = codexfix.RolloutPath(t, t.TempDir(), sid, true)
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := os.WriteFile(r.path("payload.json"), out, 0o600); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	return sid
}
