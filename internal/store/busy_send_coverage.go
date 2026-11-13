package store

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
// (SR-7.3). Adding claude, as E2 does, makes the same migration record
// existing Claude Launches covered with no change to its code.
var busySendCoveredAgents = [...]string{}

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
	for _, covered := range busySendCoveredAgents {
		if agent == covered {
			return BusySendCovered
		}
	}
	return BusySendNotCovered
}
