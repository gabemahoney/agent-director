package api_test

// starting_row_fixture_test.go is the shared finished-row fixture of the
// starting-session rule (SR-4.2) and kill's reported-in rule (SR-6.7) on the
// kill fixture: the default bound and window, and a finished row described
// in seconds relative to the instant the rule reads (ruleInstant), seeded
// with its own session. resume's tests (resume_starting_test.go,
// resume_client_test.go) and kill's finished-row opt-in tests use it. It
// holds no tests.

import (
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// The default starting-session bound and stopping window.
var (
	defBound  = secs(config.DefaultStartingSessionSeconds)
	defWindow = secs(config.DefaultStoppingWindowSeconds)
)

// startingRow is a finished row as the rule sees it at ruleInstant: its
// state, when it ended (endedAgo, negative in the future; noEndedAt for
// NULL), its agent, whether it records a pid (noPID: NULL pid and
// proc_starttime, the pane kept) and a session id (noSessionID: none), and
// its own session's age (noSession: Gone).
type startingRow struct {
	state       string
	endedAgo    time.Duration
	noEndedAt   bool
	agent       agentState
	noPID       bool
	noSessionID bool
	noSession   bool
	age         time.Duration
}

// seedStarting seeds s's row on its server (seedOnServer), with a session id
// and transcript unless noSessionID, and its own session unless noSession.
func (e *killEnv) seedStarting(t *testing.T, s startingRow) resumeRow {
	t.Helper()
	spec := killRowSpec{State: s.state, Agent: s.agent, NoSession: true,
		Opts: []apitest.SpawnOption{apitest.WithNoEndedAt()}}
	if !s.noEndedAt {
		spec = e.resumableSpec(s.endedAgo, s.agent)
		spec.State = s.state
	}
	if s.noPID {
		spec.Opts = append(spec.Opts, apitest.WithNoPID())
	}
	var r resumeRow
	if s.noSessionID {
		r = e.seedOnServer(t, spec, e.seedRow)
	} else {
		r = e.seedResumableRow(t, spec)
	}
	if !s.noSession {
		e.seedSession(t, &r.killRow, e.createdBefore(s.age))
	}
	return r
}

// recordsPID reports that s's row records a pid.
func (s startingRow) recordsPID() bool { return !s.noPID && s.agent != agentNotRecorded }

// startingCase is r's starting-session description parameters under bound
// and window: the window is checked when the row has an ended_at and records
// a pid or a session id (SR-4.2).
func (s startingRow) startingCase(r resumeRow, bound, window time.Duration) apitest.StartingSession {
	return apitest.StartingSession{InstanceID: r.ID, Name: r.Name, Window: window, Bound: bound,
		NoSession: s.noSession, WindowChecked: !s.noEndedAt && (s.recordsPID() || !s.noSessionID),
		SessionID: !s.noSessionID}
}
