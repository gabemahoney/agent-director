package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// BusySendCoverage is a Launch's or conversation's busy-send coverage
// (SR-9.1): whether busy send's keyless store-and-forward route serves it.
// Its values are permanent stored codes in spawns.busy_send and
// busy_conversations.busy_send, never ordinals.
type BusySendCoverage string

const (
	BusySendNotCovered BusySendCoverage = "not_covered"
	BusySendCovered    BusySendCoverage = "covered"
)

// busySendCoveredAgents is this version's covered set (SR-9.1), the one fixed
// list every coverage decision reads: the v20→v21 backfill (busy_send_schema.go)
// and every later launch-time recording. A release that covers an agent kind
// appends it here; each release's set must include every earlier release's
// (SR-7.3). Claude Code is covered, so the same migration records existing
// Claude Launches covered without fabricating a route or wake endpoint.
var busySendCoveredAgents = [...]string{AgentClaude}

// BusySendCoveredAgents returns a copy of this version's covered set, so no
// caller can change the list every coverage decision reads.
func BusySendCoveredAgents() []string {
	return append([]string(nil), busySendCoveredAgents[:]...)
}

// BusySendCoverageForAgent is this version's coverage for a Launch of agent,
// from busySendCoveredAgents. It serves only the migration and the creation of
// a Launch or conversation; every runtime delivery, refusal or typing decision
// reads the coverage the target's current Launch recorded instead, because a
// Launch keeps the coverage it started with whatever binary handles the call.
func BusySendCoverageForAgent(agent string) BusySendCoverage {
	if agent == "" {
		agent = AgentClaude
	}
	for _, covered := range busySendCoveredAgents {
		if agent == covered {
			return BusySendCovered
		}
	}
	return BusySendNotCovered
}

// ResumeLaunchCoverage is the new-Launch preflight rule: a resume can add
// coverage but cannot remove its conversation's coverage. Current-Launch
// decisions instead use GetBusySendCoverage or the already-read Spawn.BusySend.
func ResumeLaunchCoverage(conversation BusySendCoverage, agent string) BusySendCoverage {
	if conversation == BusySendCovered {
		return BusySendCovered
	}
	return BusySendCoverageForAgent(agent)
}

func decodeBusySendCoverage(raw any) (BusySendCoverage, error) {
	s, ok := raw.(string)
	if !ok || (s != string(BusySendCovered) && s != string(BusySendNotCovered)) {
		return "", fmt.Errorf("store: invalid busy-send coverage")
	}
	return BusySendCoverage(s), nil
}

// GetBusySendCoverage reads the published current Launch's persisted fact.
// It never observes the provider or consults this binary's covered set. A
// pending resume/reuse continues to expose the published Launch until commit.
func (s *Store) GetBusySendCoverage(instanceID string) (BusySendCoverage, error) {
	var raw any
	err := s.db.QueryRow(`SELECT busy_send FROM spawns WHERE instance_id = ?`, instanceID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return "", fmt.Errorf("store: read busy-send coverage: %w", err)
	}
	return decodeBusySendCoverage(raw)
}

// GetBusySendConversationCoverage reads the published binding for new-Launch
// resume preflight, before any write, with ResumeLaunchCoverage. Missing or
// corrupt bindings are errors, never an uncovered answer. Fresh spawn/reuse
// preflight uses BusySendCoverageForAgent with the new agent instead.
func (s *Store) GetBusySendConversationCoverage(instanceID string) (BusySendCoverage, error) {
	fact, err := s.GetBusySendConversationBinding(instanceID)
	return fact.Coverage, err
}

// BusySendConversationBinding is the validated published conversation fact,
// independent of the current Launch's coverage and this binary's provider set.
type BusySendConversationBinding struct {
	ConversationID string
	Provider       string
	Coverage       BusySendCoverage
}

// GetBusySendConversationBinding reads one binding snapshot without writing.
// A missing or corrupt core binding is never interpreted as uncovered.
func (s *Store) GetBusySendConversationBinding(instanceID string) (BusySendConversationBinding, error) {
	var fact BusySendConversationBinding
	var binding, conversation, publication, owner, identity, raw, provider any
	err := s.db.QueryRow(`SELECT s.busy_conversation_id, c.conversation_id,
	        c.publication, c.instance_id, c.store_id, c.busy_send, c.provider
	    FROM spawns s LEFT JOIN busy_conversations c ON c.conversation_id = s.busy_conversation_id
	    WHERE s.instance_id = ?`, instanceID).Scan(&binding, &conversation, &publication, &owner, &identity, &raw, &provider)
	if errors.Is(err, sql.ErrNoRows) {
		return fact, fmt.Errorf("%w: %s", ErrSpawnNotFound, instanceID)
	}
	if err != nil {
		return fact, fmt.Errorf("store: read busy-send conversation coverage: %w", err)
	}
	id, ok := binding.(string)
	if !ok || id == "" || conversation != id || publication != string(BusySendPublished) || owner != instanceID || identity != s.StoreID() {
		return fact, fmt.Errorf("store: invalid busy-send conversation binding")
	}
	agent, ok := provider.(string)
	if !ok || agent == "" {
		return fact, fmt.Errorf("store: invalid busy-send conversation provider")
	}
	coverage, err := decodeBusySendCoverage(raw)
	if err != nil {
		return fact, err
	}
	return BusySendConversationBinding{ConversationID: id, Provider: agent, Coverage: coverage}, nil
}
