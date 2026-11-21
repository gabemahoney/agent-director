package main_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/layout"
	"github.com/gabemahoney/agent-director/internal/resumedelegate"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/faketmuxfix"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// holdRuntimeInstallationLock holds the real advisory inode in a test process,
// with pipe readiness/release barriers, while runtime openers execute. No
// publication/recovery work is needed to hold the administrator's lock.
func holdRuntimeInstallationLock(t *testing.T, home string) {
	t.Helper()
	root := directorDir(home)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "AD_IP_LOCK_FILE="+filepath.Join(root, layout.InstallLock))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Errorf("lock holder: %v", err)
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "held\n" {
		t.Fatalf("lock holder readiness %q, %v", line, err)
	}
}

func privateResumeRequest(t *testing.T) resumedelegate.Request {
	t.Helper()
	home, dir := t.TempDir(), t.TempDir()
	path := filepath.Join(dir, "custom.db")
	apitest.SeedStore(t, path)
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := st.StoreID()
	_ = st.Close()
	return resumedelegate.Request{RequestFormat: 1, ResponseFormat: 1, Params: json.RawMessage(`{"instance_id":"recipient-` + id + `"}`), Home: home, StorePath: path, StoreID: id,
		ConfigPath: filepath.Join(dir, "config.toml"), TmuxCommand: filepath.Join(buildFakeTmux(t), "tmux"),
		Caller: resumedelegate.Caller{Process: "original-caller", PID: 4242, Hostname: "caller-host", User: "caller-user", ParentInstanceID: "parent-" + id}, Deadline: time.Now().Add(10 * time.Second)}
}

func runPrivateResume(t *testing.T, request resumedelegate.Request, env []string) (resumedelegate.Response, string, int) {
	t.Helper()
	var input bytes.Buffer
	if err := resumedelegate.Encode(&input, request); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := mustRun(t, cliOpts{env: env, stdin: input.String(), deadline: 12 * time.Second}, resumedelegate.Argument)
	if code != 0 {
		return resumedelegate.Response{}, stderr, code
	}
	response, err := resumedelegate.DecodeResponse(strings.NewReader(out), 1)
	if err != nil || stderr != "" {
		t.Fatalf("private stdout must contain one protocol value: %q; stderr %q; %v", out, stderr, err)
	}
	return response, stderr, code
}

// b.kii: private dispatch must precede default setup. Config is reloaded from
// the reference, exact store wins over db_path, and caller identity survives
// into the resumed row/trail instead of becoming the delegate's identity.
func TestPrivateResumeRecipient(t *testing.T) {
	for _, useDefault := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit config", true: "default marker"}[useDefault], func(t *testing.T) {
			r := privateResumeRequest(t)
			recipientID, parentID := "recipient-"+r.StoreID, r.Caller.ParentInstanceID
			poison := newOldLayout(t)
			if useDefault {
				r.ConfigPath, r.DefaultConfig = "", true
			}
			cfg := r.ConfigPath
			if useDefault {
				cfg = filepath.Join(directorDir(r.Home), "config.toml")
			}
			if err := os.MkdirAll(filepath.Dir(cfg), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cfg, []byte("[store]\ndb_path = '"+filepath.Join(r.Home, "must-not-create.db")+"'\n[relay]\ntimeout_seconds = 37\n"), 0600); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(transcript, nil, 0600); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{parentID, recipientID} {
				opts := []apitest.SpawnOption{apitest.WithAgent("codex"), apitest.WithNoPreTrust(), apitest.WithJsonlPath(transcript), apitest.WithTmuxSocket(spawnSocket(t, r.Home))}
				if _, err := apitest.SeedSpawn(r.StorePath, id, store.StateEnded, t.TempDir(), "off", "saved-session", false,
					opts...); err != nil {
					t.Fatal(err)
				}
			}
			holdRuntimeInstallationLock(t, r.Home)
			env := homeEnv(poison.home, map[string]string{"FAKE_TMUX_LOG": filepath.Join(r.Home, "fake-tmux.log"), "TMUX_TMPDIR": spawnTmuxTmpdir(t, r.Home), "AGENT_DIRECTOR_INSTANCE_ID": "wrong-parent"})
			response, stderr, code := runPrivateResume(t, r, env)
			if code != 0 || response.Error != nil {
				t.Fatalf("recipient: %+v, exit %d, %s", response, code, stderr)
			}
			if string(response.Result) != `{"instance_id":"`+recipientID+`","pre_trust":"skipped"}` {
				t.Fatalf("result %s", response.Result)
			}
			cols, err := apitest.ReadSpawnColumns(r.StorePath, recipientID)
			if err != nil || cols.State != store.StatePending || cols.ParentID != parentID {
				t.Fatalf("resumed row %+v, %v", cols, err)
			}
			creates := 0
			for _, argv := range faketmuxfix.ReadLog(t, filepath.Join(r.Home, "fake-tmux.log")) {
				joined := strings.Join(argv, " ")
				if strings.Contains(joined, "new-session") {
					creates++
					if !strings.Contains(joined, "--timeout 37") {
						t.Errorf("referenced relay config not used in %q", argv)
					}
				}
			}
			if creates != 1 {
				t.Errorf("created %d sessions; want exactly one", creates)
			}
			if _, err := os.Stat(filepath.Join(r.Home, "must-not-create.db")); !os.IsNotExist(err) {
				t.Fatalf("config store path was used: %v", err)
			}
			// A conflicting ownership scope emits the original caller identity.
			// It also proves request.Caller, not the inherited environment or the
			// recipient's PID, is used for a later one-shot call.
			tables := faketmuxfix.Tables{}
			tables.Update(t, spawnSocket(t, r.Home), func(table *faketmuxfix.Table) {
				table.Scope.Global = "unexpected-owner"
			})
			r.Params = json.RawMessage(`{"instance_id":"` + parentID + `"}`)
			response, stderr, code = runPrivateResume(t, r, env)
			if code != 0 || response.Error == nil {
				t.Fatalf("conflicting scope was resumed: %+v exit %d %s", response, code, stderr)
			}
			disagreements := eventsOf(readTrailLines(t, r.Home), "ad.provenance.disagree")
			if len(disagreements) != 1 || disagreements[0]["reason"] != "scope_value" {
				t.Fatalf("scope disagreement trail: %+v", disagreements)
			}
			for _, event := range disagreements {
				if event["caller_process"] != "original-caller" || event["caller_pid"] != float64(4242) || event["caller_hostname"] != "caller-host" || event["caller_user"] != "caller-user" {
					t.Fatalf("caller replaced: %+v", event)
				}
			}
			poison.assertUntouched(t)
		})
	}
}

func TestPrivateResumeRecipientOriginalDeadline(t *testing.T) {
	r := privateResumeRequest(t)
	// A FIFO config blocks the recipient during referenced setup. The original
	// deadline must still terminate it; a sender timeout is not doing the work.
	if err := syscall.Mkfifo(r.ConfigPath, 0600); err != nil {
		t.Fatal(err)
	}
	r.Deadline = time.Now().Add(500 * time.Millisecond)
	var input bytes.Buffer
	if err := resumedelegate.Encode(&input, r); err != nil {
		t.Fatal(err)
	}
	out, stderr, code, timedOut := execCLI(t, cliOpts{env: homeEnv(t.TempDir(), nil), stdin: input.String(), deadline: 3 * time.Second}, resumedelegate.Argument)
	if timedOut || code != 1 || out != "" || stderr != "" || time.Now().Before(r.Deadline) {
		t.Fatalf("blocked setup deadline: exit=%d timedOut=%v out=%q stderr=%q", code, timedOut, out, stderr)
	}
}

func TestPrivateResumeEntryStaysPrivate(t *testing.T) {
	help, stderr, code := runCLI(t, "help")
	if code != 0 || stderr != "" {
		t.Fatalf("help: %d %s", code, stderr)
	}
	surface, err := json.Marshal(manifest.Verbs)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"resume-delegation-job", "request_format", "response_format", "config-path", "delegation_unconfirmed"} {
		if strings.Contains(help, private) || strings.Contains(string(surface), private) {
			t.Errorf("private transport leaked %q", private)
		}
	}
}

func TestPrivateResumeRecipientRefusesBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"wrong store", "missing store", "malformed params", "unsupported format", "expired"} {
		t.Run(scenario, func(t *testing.T) {
			r := privateResumeRequest(t)
			recipientID := "recipient-" + r.StoreID
			if _, err := apitest.SeedSpawn(r.StorePath, recipientID, store.StateEnded, t.TempDir(), "off", "", false); err != nil {
				t.Fatal(err)
			}
			before, err := apitest.ReadSpawnColumns(r.StorePath, recipientID)
			if err != nil {
				t.Fatal(err)
			}
			originalStore := r.StorePath
			switch scenario {
			case "wrong store":
				r.StoreID = "different-store"
			case "missing store":
				r.StorePath = filepath.Join(t.TempDir(), "absent.db")
			case "malformed params":
				r.Params = json.RawMessage(`{"instance_id":null}`)
			case "unsupported format":
				r.RequestFormat = 2
			case "expired":
				r.Deadline = time.Now().Add(-time.Second)
			}
			response, _, code := runPrivateResume(t, r, homeEnv(t.TempDir(), map[string]string{"FAKE_TMUX_LOG": filepath.Join(r.Home, "fake-tmux.log")}))
			if scenario == "unsupported format" || scenario == "expired" {
				if code == 0 {
					t.Fatal("accepted invalid outer request")
				}
			} else if code != 0 || response.Error == nil {
				t.Fatalf("want structured refusal, got %+v exit %d", response, code)
			}
			if scenario == "wrong store" && (response.Error.Name != "ErrInternal" || response.Error.Description != "resume delegation: store identity mismatch; nothing was written or launched") {
				t.Fatalf("wrong-store guard order: %+v", response.Error)
			}
			after, err := apitest.ReadSpawnColumns(originalStore, recipientID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("refusal mutated row: %+v, %v", after, err)
			}
			if scenario == "missing store" {
				if _, err := os.Stat(r.StorePath); !os.IsNotExist(err) {
					t.Fatalf("missing store created: %v", err)
				}
			}
			if calls := faketmuxfix.ReadLog(t, filepath.Join(r.Home, "fake-tmux.log")); len(calls) != 0 {
				t.Fatalf("refusal called tmux: %v", calls)
			}
		})
	}
}

func TestRuntimeOpenersIndependentOfInstall(t *testing.T) {
	home := t.TempDir()
	apitest.SeedStore(t, stateDB(home))
	holdRuntimeInstallationLock(t, home)
	for _, tc := range []struct {
		name  string
		args  []string
		input string
	}{
		{"CLI", []string{"list"}, ""},
		{"MCP", []string{"serve", "--stdio"}, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list","arguments":{}}}` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, code := mustRun(t, cliOpts{env: homeEnv(home, nil), stdin: tc.input, deadline: 5 * time.Second}, tc.args...)
			if code != 0 || stderr != "" || !strings.Contains(out, `"spawns"`) && !strings.Contains(out, `\"spawns\"`) {
				t.Fatalf("runtime opener: exit %d out=%s stderr=%s", code, out, stderr)
			}
		})
	}
	for _, name := range []string{layout.InstallContract, layout.InstallInitialization, layout.VersionsDir, layout.PublicationsDir} {
		if _, err := os.Lstat(filepath.Join(directorDir(home), name)); !os.IsNotExist(err) {
			t.Errorf("runtime created install resource %s: %v", name, err)
		}
	}
}
