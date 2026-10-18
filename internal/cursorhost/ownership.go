package cursorhost

import (
	"fmt"
	"os"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/probe"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/tmux"
)

// VerifyPane checks the same current-label/server/pane facts as the public
// pane surfaces. It never adopts a wrapper or interprets terminal content.
func VerifyPane(s *store.Store, row store.Spawn, cfg config.Config) error {
	id := row.Identity
	if row.Agent != store.AgentCursor || id.Token == "" || id.PanePID <= 0 || id.PaneStarttime == "" || id.Socket == "" {
		return fmt.Errorf("Cursor pane ownership unavailable")
	}
	t := tmux.New("", tmux.Timeouts{Query: cfg.Tmux.EffectiveQueryTimeout(), Action: cfg.Tmux.EffectiveActionTimeout(), Create: cfg.Tmux.EffectiveCreateTimeout(), WaitDelay: cfg.Tmux.EffectivePipeCloseWait()})
	result := tmux.Lookup(t, probe.NewProcChecker(), tmux.Launch{InstanceID: row.InstanceID, Token: id.Token, StoreID: s.StoreID(), Socket: id.Socket, ServerPID: id.ServerPID, ServerStart: id.ServerStart, ServerStarttime: id.ServerStarttime}, row.TmuxSessionName)
	if result.Verdict == tmux.CantTell {
		if result.CantTell == tmux.CantTellUnreadable {
			return interruptedPane(result.Cause)
		}
		return &paneUncertain{cause: result.Cause}
	}
	if result.Verdict != tmux.Ours {
		return fmt.Errorf("Cursor pane ownership changed")
	}
	panes, err := t.ListPanes(id.Socket)
	if err != nil {
		return interruptedPane(err)
	}
	matches := 0
	for _, p := range panes {
		if p.ID == id.PaneID && p.PID == id.PanePID && p.AdPane == id.Token && p.SessionID == result.Session.ID {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("Cursor pane binding changed")
	}
	return nil
}

func waitForOwnedPane(s *store.Store, cfg config.Config) (store.Spawn, error) {
	id := os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")
	if id == "" || os.Getenv("AGENT_DIRECTOR_AGENT") != "cursor" {
		return store.Spawn{}, fmt.Errorf("cursor-host requires an owned Cursor launch")
	}
	start, alive, known := probe.NewProcChecker().StartTime(os.Getpid())
	if !known || !alive {
		return store.Spawn{}, fmt.Errorf("gateway identity unavailable")
	}
	deadline := time.Now().Add(cfg.Tmux.EffectivePendingGrace())
	if cap := time.Now().Add(45 * time.Second); deadline.After(cap) {
		deadline = cap
	}
	for {
		row, err := s.GetSpawn(id)
		if err != nil {
			return store.Spawn{}, fmt.Errorf("owned Cursor row unavailable")
		}
		if row.Agent != store.AgentCursor || row.State != store.StatePending {
			return store.Spawn{}, fmt.Errorf("cursor-host requires a fresh pending Cursor launch")
		}
		if row.Identity.PanePID > 0 {
			if row.Identity.PanePID != os.Getpid() || row.Identity.PaneStarttime != start {
				return store.Spawn{}, fmt.Errorf("cursor-host is not the recorded pane owner")
			}
			if err = VerifyPane(s, row, cfg); err != nil {
				return store.Spawn{}, err
			}
			return row, nil
		}
		if !time.Now().Before(deadline) {
			return store.Spawn{}, fmt.Errorf("Cursor launch identity was not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
