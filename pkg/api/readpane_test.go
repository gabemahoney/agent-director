package api_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// fakeCaptureTmux is a recording fake for ReadPane. It returns the
// scripted body for every CapturePane call and records the
// (name, nLines, ansi) argv. failErr is the error to return when non-nil.
type fakeCaptureTmux struct {
	wantName  string
	wantLines int
	body      string
	failErr   error

	gotName  string
	gotLines int
	gotANSI  bool
	calls    int
}

func (f *fakeCaptureTmux) CapturePane(name string, nLines int, ansi bool) (string, error) {
	f.calls++
	f.gotName = name
	f.gotLines = nLines
	f.gotANSI = ansi
	if f.failErr != nil {
		return "", f.failErr
	}
	return f.body, nil
}

func TestReadPaneDefaultStripsANSIPreservesGlyphs(t *testing.T) {
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-1", "cd-rp-1", store.StateWaiting, "off")
	tmux := &fakeCaptureTmux{
		body: "\x1b[31m❯\x1b[0m what is 2+2?\n\x1b[1m4\x1b[0m\n🐝 Brewed for 1s\n",
	}
	res, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-1",
	})
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	want := "❯ what is 2+2?\n4\n🐝 Brewed for 1s\n"
	if res.Pane != want {
		t.Fatalf("Pane = %q; want %q (ANSI stripped, glyphs preserved)", res.Pane, want)
	}
}

func TestReadPaneANSITrueReturnsRawBytes(t *testing.T) {
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-2", "cd-rp-2", store.StateWaiting, "off")
	raw := "\x1b[31m❯\x1b[0m question"
	tmux := &fakeCaptureTmux{body: raw}
	res, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-2",
		ANSI:             true,
	})
	if err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if res.Pane != raw {
		t.Fatalf("Pane = %q; want raw passthrough %q", res.Pane, raw)
	}
	if !strings.Contains(res.Pane, "\x1b[") {
		t.Fatalf("raw mode must preserve escape codes; got %q", res.Pane)
	}
}

func TestReadPaneForwardsANSIFlagToTmux(t *testing.T) {
	// Per b.s12: ANSI=true must reach the tmux layer so the underlying
	// `capture-pane` call adds `-e`. Without that, tmux strips escapes
	// before agent-director ever sees them and the "raw bytes" promise
	// of `--ansi=true` collapses.
	t.Run("ansi=true forwards true", func(t *testing.T) {
		s, _ := apitest.OpenStoreWithRow(t, "id-rp-ansi-on", "cd-x", store.StateWaiting, "off")
		tmux := &fakeCaptureTmux{body: "x"}
		if _, err := api.ReadPane(s, tmux, api.ReadPaneParams{
			ClaudeInstanceID: "id-rp-ansi-on",
			ANSI:             true,
		}); err != nil {
			t.Fatalf("ReadPane: %v", err)
		}
		if !tmux.gotANSI {
			t.Fatalf("CapturePane(ansi=%v); want true", tmux.gotANSI)
		}
	})
	t.Run("ansi=false (default) forwards false", func(t *testing.T) {
		s, _ := apitest.OpenStoreWithRow(t, "id-rp-ansi-off", "cd-x", store.StateWaiting, "off")
		tmux := &fakeCaptureTmux{body: "x"}
		if _, err := api.ReadPane(s, tmux, api.ReadPaneParams{
			ClaudeInstanceID: "id-rp-ansi-off",
		}); err != nil {
			t.Fatalf("ReadPane: %v", err)
		}
		if tmux.gotANSI {
			t.Fatalf("CapturePane(ansi=%v); want false", tmux.gotANSI)
		}
	})
}

func TestReadPaneDefaultLineCountIsTwentyFive(t *testing.T) {
	// NLines=0 (the zero value) must fall back to DefaultReadPaneLines.
	// The fake records whatever the verb asked for; pinning 25 here is
	// the public contract.
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-3", "cd-rp-3", store.StateWaiting, "off")
	tmux := &fakeCaptureTmux{body: "x\n"}
	if _, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-3",
	}); err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if tmux.gotLines != api.DefaultReadPaneLines {
		t.Fatalf("CapturePane(nLines=%d); want %d", tmux.gotLines, api.DefaultReadPaneLines)
	}
	if api.DefaultReadPaneLines != 25 {
		t.Fatalf("DefaultReadPaneLines = %d; SRD §12 pins 25", api.DefaultReadPaneLines)
	}
}

func TestReadPaneCustomLineCountPassesThrough(t *testing.T) {
	// SRD §12: no upper cap. The verb must pass any positive number
	// through to tmux verbatim.
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-4", "cd-rp-4", store.StateWaiting, "off")
	tmux := &fakeCaptureTmux{body: "x\n"}
	if _, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-4",
		NLines:           1000,
	}); err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if tmux.gotLines != 1000 {
		t.Fatalf("CapturePane(nLines=%d); want 1000 passthrough", tmux.gotLines)
	}
}

func TestReadPaneUsesTmuxSessionNameFromRow(t *testing.T) {
	// The verb must read the row's tmux_session_name, not the caller's
	// instance id. Different rows produce different sessions; mixing
	// them up would let one Spawn read another's pane.
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-5", "cd-magic-session", store.StateWaiting, "off")
	tmux := &fakeCaptureTmux{body: "x\n"}
	if _, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-5",
	}); err != nil {
		t.Fatalf("ReadPane: %v", err)
	}
	if tmux.gotName != "cd-magic-session" {
		t.Fatalf("CapturePane name = %q; want %q", tmux.gotName, "cd-magic-session")
	}
}

func TestReadPaneSpawnNotFound(t *testing.T) {
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-6", "cd-rp-6", store.StateWaiting, "off")
	tmux := &fakeCaptureTmux{body: "x\n"}
	_, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "absent",
	})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
	if tmux.calls != 0 {
		t.Fatalf("CapturePane was called for absent row")
	}
}

func TestReadPaneTmuxCaptureFailed(t *testing.T) {
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-7", "cd-rp-7", store.StateWaiting, "off")
	tmux := &fakeCaptureTmux{failErr: errReadPaneSentinel}
	_, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-7",
	})
	if !errors.Is(err, errReadPaneSentinel) {
		t.Fatalf("err = %v; want errReadPaneSentinel chain", err)
	}
}

func TestReadPaneWorksOnEndedSpawn(t *testing.T) {
	// Unlike send-keys, read-pane has NO state precondition — a caller
	// inspecting an ended Spawn's final pane bytes is a valid use case
	// (post-mortem capture). The row lookup is the only gate.
	s, _ := apitest.OpenStoreWithRow(t, "id-rp-8", "cd-rp-8", store.StateEnded, "off")
	tmux := &fakeCaptureTmux{body: "final state"}
	res, err := api.ReadPane(s, tmux, api.ReadPaneParams{
		ClaudeInstanceID: "id-rp-8",
	})
	if err != nil {
		t.Fatalf("ReadPane on ended Spawn: %v", err)
	}
	if res.Pane != "final state" {
		t.Fatalf("Pane = %q; want %q", res.Pane, "final state")
	}
}

// TestReadPaneAllowPendingIsNoOp confirms ReadPane has no state guard:
// both allow_pending=true and allow_pending=false succeed on a pending Spawn.
func TestReadPaneAllowPendingIsNoOp(t *testing.T) {
	for _, allowPending := range []bool{true, false} {
		allowPending := allowPending
		t.Run(fmt.Sprintf("allow_pending=%v", allowPending), func(t *testing.T) {
			s, _ := apitest.OpenStoreWithRow(t, "id-rp-ap-"+fmt.Sprint(allowPending), "cd-rp-ap", store.StatePending, "off")
			tmux := &fakeCaptureTmux{body: "pane content"}
			res, err := api.ReadPane(s, tmux, api.ReadPaneParams{
				ClaudeInstanceID: "id-rp-ap-" + fmt.Sprint(allowPending),
				AllowPending:     allowPending,
			})
			if err != nil {
				t.Fatalf("ReadPane(allow_pending=%v, state=pending): %v", allowPending, err)
			}
			if res.Pane != "pane content" {
				t.Fatalf("Pane = %q; want %q", res.Pane, "pane content")
			}
		})
	}
}

// errReadPaneSentinel is a stand-in for tmux.ErrTmuxCaptureFailed in this
// test file; the verb wraps whatever the tmux layer returns, so any
// sentinel proves the chain is preserved.
var errReadPaneSentinel = errors.New("readpane test sentinel")
