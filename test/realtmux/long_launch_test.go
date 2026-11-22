package realtmux_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// b.xhh: exercise all three public launch paths with real tmux and the
// existing recording stand-in. A create receipt alone cannot prove exec ran.
func TestLongLaunchProcessDelivery(t *testing.T) {
	for _, shape := range []string{"claude-arg", "codex-arg", "claude-env"} {
		for _, verb := range []string{"spawn", "reuse", "resume"} {
			t.Run(shape+"/"+verb, func(t *testing.T) {
				root := t.TempDir()
				t.Setenv(codexRecordDirEnv, root)
				unsetEnv(t, codexHookEnv, "CODEX_HOME")
				stub := program(t, "recording-agent", codexStubScript)
				f := newSpawnFix(t, "[launch]\nclaude_path = "+strconv.Quote(stub)+"\ncodex_path = "+strconv.Quote(stub))
				payload := strings.Repeat("x", 30000)
				p := api.SpawnParams{InstanceID: newInstanceID("long"), Agent: "claude", AgentArgs: []string{"--append-system-prompt", payload}}
				if shape == "codex-arg" {
					p.Agent, p.AgentArgs = "codex", []string{"--config=developer_instructions=" + payload}
				}
				if shape == "claude-env" {
					p.AgentArgs, p.ExtraEnv = nil, map[string]string{"LONG_LAUNCH_VALUE": payload}
				}
				if verb != "spawn" {
					args, err := json.Marshal(p.AgentArgs)
					if err != nil {
						t.Fatal(err)
					}
					sid := newInstanceID("session")
					_, err = apitest.SeedSpawn(f.DBPath, p.InstanceID, store.StateEnded, f.CWD, "off", sid, false,
						apitest.WithAgent(p.Agent), apitest.WithRawAgentArgs(string(args)), apitest.WithExtraEnv(p.ExtraEnv),
						apitest.WithTmuxSocket(f.Socket), apitest.WithNoPreTrust(),
						apitest.WithJsonlPath(apitest.SeedJsonl(t, f.CWD, sid)))
					if err != nil {
						t.Fatal(err)
					}
				}
				if verb == "resume" {
					if _, err := f.API.Resume(api.ResumeParams{InstanceID: p.InstanceID}); err != nil {
						t.Fatalf("long resume must succeed: %s", describe(err))
					}
					f.launched(t, p.InstanceID)
				} else {
					p.ReuseFinished = verb == "reuse"
					f.mustLaunchWith(t, p)
				}
				r := codexRecordOf(root, p.InstanceID)
				r.waitRecorded(t)
				if shape == "claude-env" {
					if got, ok := r.env(t, "LONG_LAUNCH_VALUE"); !ok || got != payload {
						t.Fatalf("launched process env: present=%v bytes=%d; want exact 30000 bytes", ok, len(got))
					}
				} else if args := r.nulList(t, "argv"); !slices.Contains(args, p.AgentArgs[len(p.AgentArgs)-1]) {
					t.Fatal("launched process did not receive the exact long argument")
				}
				if p.Agent == "codex" {
					// Split checks the normal composed settings and hook block too.
					sessionStartCommand(t, r.launch(t))
				}
			})
		}
	}
}

// b.xhh: the transport must carry a single raw argument right up to Linux's
// per-string exec limit, including shell/config punctuation and a newline.
func TestLongLaunchClientArgumentBoundary(t *testing.T) {
	for _, size := range []int{30000, 120000, 131071, 131072} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			rt := newRealTmux(t)
			root, id := t.TempDir(), newInstanceID("client-long")
			stub := program(t, "recording-agent", codexStubScript)
			payload := strings.Repeat("x", size-8) + "'\";#\n\\$!"
			c, err := rt.create(t, createSpec{InstanceID: id,
				Envs: map[string]string{codexRecordDirEnv: root}, Command: []string{stub, payload}})
			if size == 131072 {
				if err == nil {
					t.Fatal("131072-byte argument must be refused before tmux create")
				}
				if got := rt.run(t, "list-sessions"); got.Exit == 0 {
					t.Fatal("oversized argument created a session")
				}
				return
			}
			if err != nil {
				t.Fatalf("%d-byte raw argument must launch: %s", size, describe(err))
			}
			r := codexRecordOf(root, id)
			r.waitRecorded(t)
			if got := r.nulList(t, "argv"); len(got) != 1 || got[0] != payload {
				t.Fatalf("process argv differs from exact %d-byte payload", size)
			}
			assertLabel(t, c.Reply.SessionID, rt.label(t, c.Reply.SessionID),
				"ad1 "+c.Token+" "+c.Reply.SessionID+" "+id+" "+c.StoreID)
		})
	}
}

// b.xhh: API input bypasses the outer CLI's own exec limit. Refusal must
// happen before a row or tmux session exists, not as a silent dead pane.
func TestLongLaunchRefusedBeforeRow(t *testing.T) {
	f := newSpawnFix(t)
	id := newInstanceID("oversized")
	_, err := f.spawnWith(api.SpawnParams{InstanceID: id, AgentArgs: []string{strings.Repeat("x", 131072)}})
	if !errors.Is(err, api.ErrInvalidFlags) {
		t.Errorf("131072-byte API argument: got %s, want ErrInvalidFlags", describe(err))
	}
	if _, err := f.API.Get(id); !errors.Is(err, api.ErrSpawnNotFound) {
		t.Errorf("refused launch wrote a row: Get error %s", describe(err))
	}
	if got := f.run(t, "list-sessions"); got.Exit == 0 {
		t.Fatal("refused launch created a tmux session")
	}
	if _, err := os.Stat(f.Socket); err == nil {
		t.Errorf("refused launch started a tmux server at %s", f.Socket)
	}
}
