package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// CodexStartup records origin, not usage. Only an admitted main Codex
// SessionStart with source=startup can establish it. Codex 0.159.2 emits
// startup only for InitialHistory::New; resume/supplied history and fork
// cannot qualify the rollout's creation-only cli_version as the producer.
type CodexStartup struct {
	LaunchToken string `json:"launch_token"`
	SessionID   string `json:"session_id"`
	JSONLPath   string `json:"jsonl_path"`
	PID         int    `json:"pid"`
	Starttime   string `json:"starttime"`
}

const maxCodexStartupBytes = 8192

func (p CodexStartup) valid() bool {
	return decodeLaunchToken(p.LaunchToken) != "" && p.SessionID != "" && len(p.SessionID) <= 256 &&
		filepath.IsAbs(p.JSONLPath) && len(p.JSONLPath) <= 4096 && p.PID > 0 && p.Starttime != "" && len(p.Starttime) <= 128
}

func decodeCodexStartup(raw any) *CodexStartup {
	s, ok := raw.(string)
	if !ok || len(s) > maxCodexStartupBytes {
		return nil
	}
	var p CodexStartup
	d := json.NewDecoder(strings.NewReader(s))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || !p.valid() || d.Decode(new(any)) != io.EOF {
		return nil
	}
	return &p
}

func codexStartupArg(prior sessionStartPrior, gate HookGate, path string) any {
	p := CodexStartup{prior.launchToken, gate.SessionID, path, gate.ParentPID, gate.ParentStart}
	// Only the first report into an unbound session can establish origin.
	// In particular, a delayed startup must never restore provenance after a
	// resume/fork/clear (including a failed resume restoring the old pane).
	if prior.agent != AgentCodex || gate.Event != "SessionStart" || gate.Examined.SessionID != "" || !gate.CodexStartup || !p.valid() {
		return nil
	}
	// All fields have bounded primitive types; Marshal cannot fail.
	b, _ := json.Marshal(p)
	if len(b) > maxCodexStartupBytes {
		return nil
	}
	return string(b)
}

// ContextEvidence selects only this row's current evidence and authority.
func (sp Spawn) ContextEvidence() SpawnContext {
	return SpawnContext{FamilyState: sp.FamilyState, Agent: sp.Agent, State: sp.State, SessionID: sp.SessionID, JSONLPath: sp.JSONLPath,
		LaunchToken: sp.Identity.Token, PID: sp.PID, ProcStarttime: sp.ProcStarttime,
		PanePID: sp.Identity.PanePID, PaneStarttime: sp.Identity.PaneStarttime,
		EndReportedAtMillis: sp.EndReportedAtMillis, CodexStartup: sp.CodexStartup, ClaudeCapacity: sp.ClaudeCapacity}
}

// FreshCodexWriter verifies that no launch, session, path or writer changed
// since the admitted startup. The API additionally checks this process live.
func (e SpawnContext) FreshCodexWriter() bool {
	p := e.CodexStartup
	return e.Agent == AgentCodex && IsLiveState(e.State) && e.State != StatePending && e.EndReportedAtMillis == 0 &&
		p != nil && p.valid() && e.LaunchToken == p.LaunchToken && e.SessionID == p.SessionID && e.JSONLPath == p.JSONLPath &&
		e.PID == p.PID && e.PanePID == p.PID && e.ProcStarttime == p.Starttime && e.PaneStarttime == p.Starttime
}

// migrateV12toV13 adds bounded startup provenance, without inferring origin
// for existing sessions. Compatible reentry preserves valid explicit markers.
func migrateV12toV13(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(spawns)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def sql.NullString
		if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		if !strings.EqualFold(name, "codex_startup") {
			continue
		}
		if name != "codex_startup" || kind != "TEXT" || notnull != 0 || pk != 0 || def.Valid {
			rows.Close()
			return fmt.Errorf("%w: store: v12→v13 incompatible Codex startup column", ErrSchemaMismatch)
		}
		found = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if found {
		rows, err = tx.Query(`SELECT codex_startup FROM spawns WHERE codex_startup IS NOT NULL`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw any
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if decodeCodexStartup(raw) == nil {
				rows.Close()
				return fmt.Errorf("%w: store: v12→v13 invalid Codex startup value", ErrSchemaMismatch)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	} else if _, err = tx.Exec(`ALTER TABLE spawns ADD COLUMN codex_startup TEXT`); err != nil {
		return err
	}
	if err = migrateClaudeCapacity(tx); err != nil {
		return err
	}
	if _, err = tx.Exec(`PRAGMA user_version = 13`); err != nil {
		return err
	}
	return tx.Commit()
}
