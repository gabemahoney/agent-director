package apitest

import (
	"fmt"

	"github.com/gabemahoney/agent-director/internal/store"
)

func familySeedState(state string) string {
	if state == store.StateWaiting || state == store.StateEnded || state == store.StateMissing {
		return store.StateWaiting
	}
	return store.StateWorking
}

// InitializeFamilyHistory makes a synthetic tree initially settled in its raw aggregate.
func InitializeFamilyHistory(dbPath string) error {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return err
	}
	defer raw.Close()
	_, err = raw.Exec(`WITH RECURSIVE family(root,id) AS (
 SELECT instance_id,instance_id FROM spawns UNION ALL
 SELECT family.root,child.instance_id FROM family JOIN spawns child ON child.parent_id=family.id
) UPDATE spawns SET family_state=CASE WHEN EXISTS (
 SELECT 1 FROM family JOIN spawns member ON member.instance_id=family.id
 WHERE family.root=spawns.instance_id AND member.state NOT IN ('waiting','ended','missing')
) THEN 'working' ELSE 'waiting' END`)
	if err != nil {
		return fmt.Errorf("InitializeFamilyHistory: %w", err)
	}
	_, err = raw.Exec(`UPDATE spawns SET family_settled_state=family_state,family_changed_ms=0`)
	return err
}

// SetFamilyHistory sets only family history; own-state settling is independent.
func SetFamilyHistory(dbPath, id, rawState, settled string, stampMs int64) error {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return err
	}
	defer raw.Close()
	res, err := raw.Exec(`UPDATE spawns SET family_state=?,family_settled_state=?,family_changed_ms=? WHERE instance_id=?`, rawState, settled, stampMs, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return fmt.Errorf("SetFamilyHistory: %w: %s", store.ErrSpawnNotFound, id)
	}
	return nil
}

// ReadFamilyHistory returns the raw aggregate, retained value and change time.
func ReadFamilyHistory(dbPath, id string) (state, settled string, stampMs int64, err error) {
	raw, err := openRawStore(dbPath)
	if err != nil {
		return "", "", 0, err
	}
	defer raw.Close()
	err = raw.QueryRow(`SELECT family_state,family_settled_state,family_changed_ms FROM spawns WHERE instance_id=?`, id).Scan(&state, &settled, &stampMs)
	return
}
