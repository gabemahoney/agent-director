//go:build linux

// Package dryrun_test runs the measure-exit offline dry run
// (tools/measure-exit/dryrun.sh) end to end in the sandbox: the v-next
// binary and the driver against the stub claude on private tmux servers,
// the guard over a fixture home, and the runner's print-only mode. Run it
// with -count=1: it builds the binaries in a subprocess.
package dryrun_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/testsupport/sandboxguard"
)

// TestMain refuses to run outside the sandbox.
func TestMain(m *testing.M) {
	sandboxguard.Require()
	os.Exit(m.Run())
}

// sentinels are credential values in the dry run's environment; dry mode
// must forward and write none of them.
var sentinels = map[string]string{
	"ANTHROPIC_API_KEY":       "sk-ant-dryrun-sentinel-api-key-0123456789abcdef",
	"ANTHROPIC_AUTH_TOKEN":    "dryrun-sentinel-gateway-token-0123456789",
	"CLAUDE_CODE_OAUTH_TOKEN": "dryrun-sentinel-oauth-token-0123456789",
	"ANTHROPIC_BASE_URL":      "https://dryrun-sentinel-gateway.invalid",
	"AWS_SECRET_ACCESS_KEY":   "dryrun-sentinel-aws-secret-0123456789",
}

// dryResults is the part of results.json the test reads.
type dryResults struct {
	DryRun    bool   `json:"dry_run"`
	Banner    string `json:"banner"`
	Isolation struct {
		Home           string `json:"home"`
		TmuxTmpdir     string `json:"tmux_tmpdir"`
		RecordedSocket string `json:"recorded_socket"`
	} `json:"isolation"`
	Seed struct {
		Path           string `json:"path"`
		CredentialMode string `json:"credential_mode"`
	} `json:"seed"`
	Cases []struct {
		ID        string `json:"id"`
		Attempted int    `json:"attempted"`
		Completed int    `json:"completed"`
		Samples   []struct {
			InstanceID string `json:"claude_instance_id"`
		} `json:"samples"`
	} `json:"cases"`
	RN9 struct {
		Scenarios []struct {
			ID      string `json:"id"`
			Verdict string `json:"verdict"`
			Hooks   []struct {
				Event    string `json:"event"`
				Source   string `json:"source"`
				AgentID  string `json:"agent_id"`
				PIDMatch bool   `json:"pid_match"`
				Outcome  string `json:"outcome"`
				Reason   string `json:"reason"`
			} `json:"hooks"`
		} `json:"scenarios"`
	} `json:"rn9"`
	RN7 struct {
		Rows []struct {
			Event string `json:"event"`
		} `json:"rows"`
	} `json:"rn7_record"`
	Probe struct {
		Versions []struct {
			Result string `json:"result"`
			Note   string `json:"note"`
		} `json:"versions"`
	} `json:"probe"`
	Notes []string `json:"notes"`
}

// load reads one run's results.json.
func load(t *testing.T, path string) dryResults {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r dryResults
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return r
}

// defaultSocketPresent reports whether the default tmux socket under the
// TMUX_TMPDIR dir exists.
func defaultSocketPresent(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()), "default"))
	return err == nil && fi.Mode()&fs.ModeSocket != 0
}

func TestDryRun(t *testing.T) {
	for _, tool := range []string{"tmux", "jq", "go", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	// The dry run gets its own TMUX_TMPDIR: other packages of the suite run
	// tmux on the container's shared default socket at the same time.
	tmuxTmpdir := t.TempDir()
	socketBefore := defaultSocketPresent(tmuxTmpdir)
	out := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "../dryrun.sh", "--out", out, "--keep")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") && !strings.HasPrefix(kv, "TMUX_TMPDIR=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	for k, v := range sentinels {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Env = append(cmd.Env, "TMUX_TMPDIR="+tmuxTmpdir)
	start := time.Now()
	printed, err := cmd.CombinedOutput()
	t.Logf("dry run took %s", time.Since(start).Round(time.Second))
	// --keep leaves the throwaway tree (/tmp/mx-dry.*) for the seed check;
	// its "built ... into <tmp>/bin" line names it.
	for _, line := range strings.Split(string(printed), "\n") {
		if bin, ok := strings.CutPrefix(line, "ok: built agent-director and measure-exit into "); ok && strings.HasPrefix(bin, "/tmp/mx-dry.") {
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(bin)) })
		}
	}
	if err != nil || !strings.Contains(string(printed), "DRY RUN PASSED") {
		t.Fatalf("dry run failed (%v):\n%s", err, printed)
	}
	full := load(t, filepath.Join(out, "full", "results.json"))

	t.Run("banner", func(t *testing.T) {
		table := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(out, "full", "results-table.txt"))), "\n")
		if !full.DryRun || !strings.Contains(full.Banner, "DRY RUN") || !strings.Contains(table[0], "DRY RUN") || !strings.Contains(table[len(table)-1], "DRY RUN") {
			t.Errorf("dry_run %t banner %q; table starts %q", full.DryRun, full.Banner, table[0])
		}
	})

	t.Run("every case completed every sample", func(t *testing.T) {
		got := map[string]bool{}
		for _, c := range full.Cases {
			got[c.ID] = c.Attempted > 0 && c.Completed == c.Attempted
			for _, s := range c.Samples {
				if s.InstanceID == "" {
					t.Errorf("%s: a sample's agent never reported in", c.ID)
				}
			}
		}
		for _, id := range []string{"rn6.idle", "rn6.midturn", "rn6.mcp", "rn6.idle.raised-hook", "rn6.midturn.raised-hook", "rn6.mcp.raised-hook",
			"rn6.idle.raised-env", "rn6.midturn.raised-env", "rn6.mcp.raised-env", "rn2.natural", "rn2.pause", "rn2.mcp"} {
			if !got[id] {
				t.Errorf("case %s missing or not every sample completed", id)
			}
		}
	})

	t.Run("RN-9 and RN-7", func(t *testing.T) {
		found := map[string]bool{}
		for _, s := range full.RN9.Scenarios {
			if s.Verdict != "pass" {
				t.Errorf("%s: %s", s.ID, s.Verdict)
			}
			for _, h := range s.Hooks {
				switch {
				case s.ID == "rn9.resume" && h.Event == "SessionStart" && h.Source == "resume" && h.PIDMatch && h.Outcome == "applied":
					found["resume applied"] = true
				case s.ID == "rn9.team-inprocess" && h.AgentID != "" && h.Outcome == "ignored" && h.Reason == "subagent_event":
					found["in-process teammate ignored as subagent_event"] = true
				case s.ID == "rn9.team-splitpane" && !h.PIDMatch && h.Outcome == "ignored" && h.Reason == "pid_mismatch":
					found["split-pane teammate ignored as pid_mismatch"] = true
				}
			}
		}
		for _, want := range []string{"resume applied", "in-process teammate ignored as subagent_event", "split-pane teammate ignored as pid_mismatch"} {
			if !found[want] {
				t.Errorf("no hook shows %s", want)
			}
		}
		if len(full.RN9.Scenarios) != 4 || len(full.RN7.Rows) == 0 {
			t.Errorf("%d scenarios, %d RN-7 rows", len(full.RN9.Scenarios), len(full.RN7.Rows))
		}
		assertAbsent(t, "recorder file", readFile(t, filepath.Join(out, "full", "rn9.drive-hooks.jsonl")), "Use the Bash tool", "Reply with the single word")
	})

	t.Run("probe with both stubs", func(t *testing.T) {
		for dir, want := range map[string]string{"full": "args_received", "probe-kept": "args_received", "probe-dropped": "args_not_received"} {
			r := load(t, filepath.Join(out, dir, "results.json"))
			if len(r.Probe.Versions) != 1 || r.Probe.Versions[0].Result != want {
				t.Errorf("%s: probe %+v, want %s", dir, r.Probe.Versions, want)
			}
		}
		if note := load(t, filepath.Join(out, "probe-dropped", "results.json")).Probe.Versions[0].Note; !strings.Contains(note, "no_exec_form") {
			t.Errorf("the args-dropping stub's note %q lacks no_exec_form", note)
		}
	})

	t.Run("a non-stub claude is refused before any spawn", func(t *testing.T) {
		refused := load(t, filepath.Join(out, "refused", "results.json"))
		if !strings.Contains(strings.Join(refused.Notes, "\n"), "dry-stub-only") {
			t.Errorf("notes %q", refused.Notes)
		}
		if strings.Contains(readFile(t, filepath.Join(out, "refused", "run-log.jsonl")), `"kind":"spawn"`) {
			t.Error("the refused run spawned an agent")
		}
	})

	t.Run("isolation", func(t *testing.T) {
		iso := full.Isolation
		tmp := filepath.Dir(filepath.Dir(iso.Home))
		if iso.Home == u.HomeDir || !strings.HasPrefix(iso.TmuxTmpdir, tmp+"/") {
			t.Errorf("isolation %+v", iso)
		}
		if !strings.HasPrefix(iso.RecordedSocket, iso.TmuxTmpdir+"/") {
			t.Errorf("results.json recorded socket %q is outside the private TMUX_TMPDIR %s", iso.RecordedSocket, iso.TmuxTmpdir)
		}
		var sockets []string
		for _, line := range strings.Split(readFile(t, filepath.Join(out, "full", "harness-ids.txt")), "\n") {
			if s, ok := strings.CutPrefix(line, "socket "); ok {
				sockets = append(sockets, s)
			}
		}
		if len(sockets) != 1 || sockets[0] != iso.RecordedSocket {
			t.Errorf("identifiers file sockets %q, want only results.json's %q", sockets, iso.RecordedSocket)
		}
		if _, err := os.Stat(filepath.Join(u.HomeDir, ".agent-director")); err == nil {
			t.Errorf("%s/.agent-director exists after the dry run", u.HomeDir)
		}
		if defaultSocketPresent(tmuxTmpdir) != socketBefore {
			t.Errorf("the default tmux socket changed (present before: %t)", socketBefore)
		}
		assertRunLog(t, filepath.Join(out, "full", "run-log.jsonl"), iso.TmuxTmpdir)
	})

	t.Run("no credential reached any output", func(t *testing.T) {
		seed := full.Seed
		if seed.CredentialMode != "none" || filepath.Dir(seed.Path) != full.Isolation.Home {
			t.Errorf("seed %+v", seed)
		}
		if fi, err := os.Stat(seed.Path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("seeded state file %s: %v", seed.Path, err)
		}
		key := sentinels["ANTHROPIC_API_KEY"]
		texts := []string{key[len(key)-20:]}
		for _, v := range sentinels {
			texts = append(texts, v)
		}
		assertAbsent(t, seed.Path, readFile(t, seed.Path), texts...)
		_ = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				assertAbsent(t, p, readFile(t, p), texts...)
			}
			return nil
		})
		assertAbsent(t, "dry run output", string(printed), texts...)
	})
}

// assertRunLog checks every logged call is an allowed action and every tmux
// call names a socket under the private TMUX_TMPDIR.
func assertRunLog(t *testing.T, path, tmuxTmpdir string) {
	t.Helper()
	allowed := map[string]bool{"version": true, "read": true, "spawn": true, "measured": true, "drive": true, "teardown": true}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e struct {
			Case string   `json:"case"`
			Kind string   `json:"kind"`
			Argv []string `json:"argv"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil || len(e.Argv) < 2 {
			t.Fatalf("run log line %q: %v", sc.Text(), err)
		}
		verb := e.Argv[1]
		if filepath.Base(e.Argv[0]) == "tmux" {
			if e.Argv[1] != "-S" || !strings.HasPrefix(e.Argv[2], tmuxTmpdir+"/") {
				t.Errorf("tmux call off the private socket: %q", e.Argv)
			}
			verb = e.Argv[3]
		}
		ok := allowed[e.Kind]
		switch e.Kind {
		case "read":
			ok = verb == "get" || verb == "list" || verb == "list-sessions" || verb == "list-panes"
		case "measured":
			ok = verb == "kill-pane" || verb == "send-keys" || verb == "pause"
		case "drive":
			ok = strings.HasPrefix(e.Case, "rn9.") && (verb == "send-keys" || verb == "decide" || verb == "pause")
		case "teardown":
			ok = e.Case == "probe.exec-form" && verb == "kill-pane"
		}
		if !ok {
			t.Errorf("unexpected %s action in %s: %q", e.Kind, e.Case, e.Argv)
		}
	}
}

// readFile returns path's content.
func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertAbsent fails when text contains any of secrets.
func assertAbsent(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s contains the sentinel %q", what, s)
		}
	}
}
