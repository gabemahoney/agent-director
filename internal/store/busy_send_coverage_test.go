package store

import (
	"reflect"
	"testing"
)

// E7: the published conversation, not the uncovered Launch's provider or
// coverage, supplies delegation identity. Existing reader-corruption tests
// cover bad bindings; these are the new provider-validation edges.
func TestBusySendConversationBindingProvider(t *testing.T) {
	for _, provider := range []string{"codex", "", "unknown_future_provider"} {
		t.Run(provider, func(t *testing.T) {
			s, _ := openTempStore(t)
			id := busyLaunchInsert(t, s, AgentClaude)
			if provider == "" {
				s.db.SetMaxOpenConns(1)
				mustExec(t, s.db, `PRAGMA ignore_check_constraints=ON`)
			}
			mustExec(t, s.db, `UPDATE busy_conversations SET provider=?,busy_send='covered' WHERE instance_id=?`, provider, id)
			mustExec(t, s.db, `UPDATE spawns SET busy_send='not_covered' WHERE instance_id=?`, id)
			before := dbDump(t, s.filePath)
			binding, err := s.GetBusySendConversationBinding(id)
			if provider == "" {
				if err == nil || binding != (BusySendConversationBinding{}) {
					t.Fatalf("empty provider accepted: %+v, %v", binding, err)
				}
			} else if err != nil || binding.Provider != provider || binding.Coverage != BusySendCovered || binding.ConversationID == "" {
				t.Fatalf("binding = %+v, %v", binding, err)
			}
			if !reflect.DeepEqual(before, dbDump(t, s.filePath)) {
				t.Fatal("binding reader mutated the store")
			}
		})
	}
}
