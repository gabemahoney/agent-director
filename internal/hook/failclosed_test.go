package hook_test

// failclosed_test.go — Handle's error paths against a HookStore double: SRD
// §3.2 (state tracking is fail-open: log, exit 0, print nothing), SRD §6.4
// (with relay_mode on, a PermissionRequest hook that fails before its request
// is recorded ends in a deny envelope) and b.45p (a hook that is not, or may
// not be, a PermissionRequest prints nothing). Poll's exits are pinned in
// polling_test.go; the relay's failures after its request is recorded, which
// print nothing (b.146 rule 3), and its ack-before-answer order in
// relay_test.go and timeout_writes_test.go.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// flakyRelayStore is a HookStore and hook.RelayStore double with programmable
// errors and recorded calls. Its gated writes report applied unless they
// error (the hook is the row's own agent, SR-22.9); the gate itself is the
// store's and is tested against a real store. GetPermissionRequest runs
// onGet, then returns getRows/getErrs in turn, the last entry sticky.
// AckRelayDecision acks ackDecision when set, else the decision of the last
// row read; ackNone makes it match nothing. DenyRelayTimeout denies unless
// denyNone.
type flakyRelayStore struct {
	transitionErr, identityErr, insertErr error
	ackErr, denyErr                       error
	ackNone, denyNone                     bool
	ackDecision, ackReason                string
	getRows                               []store.PermissionRow
	getErrs                               []error
	onGet                                 func()
	idx                                   atomic.Int32
	identityN                             int
	inserts                               []store.RelayRequest
	acks, denies                          []relayWrite
	transitionArgs                        []transitionCall
}

// relayWrite is one recorded ack or timeout deny: its request and its wait
// for the write lock.
type relayWrite struct {
	InstanceID, RequestToken string
	MaxWait                  time.Duration
}

type transitionCall struct {
	InstanceID, NewState string
	SoftRefresh          bool
}

func (f *flakyRelayStore) GetSpawn(string) (store.Spawn, error) { return store.Spawn{}, nil }
func (f *flakyRelayStore) ApplyHookTransition(instanceID string, _ store.HookGate, newState string, softRefresh bool, _, _ string, _ bool) (store.HookApplied, error) {
	f.transitionArgs = append(f.transitionArgs, transitionCall{instanceID, newState, softRefresh})
	return store.HookApplied{Applied: f.transitionErr == nil}, f.transitionErr
}
func (f *flakyRelayStore) RecordSessionStartIdentity(string, store.HookGate, string, bool) (store.HookApplied, bool, error) {
	f.identityN++
	return store.HookApplied{Applied: f.identityErr == nil}, false, f.identityErr
}
func (f *flakyRelayStore) InsertRelayRequest(_ string, _ store.HookGate, req store.RelayRequest, _ int, _ time.Duration) (store.UpsertOutcome, store.HookApplied, error) {
	f.inserts = append(f.inserts, req)
	if f.insertErr != nil {
		return store.UpsertError, store.HookApplied{}, f.insertErr
	}
	return store.UpsertInserted, store.HookApplied{Applied: true}, nil
}
func (f *flakyRelayStore) AckRelayDecision(instanceID, requestToken string, _ time.Time, maxWait time.Duration, check func() error) (string, string, bool, error) {
	f.acks = append(f.acks, relayWrite{instanceID, requestToken, maxWait})
	if check != nil {
		if err := check(); err != nil {
			return "", "", false, err
		}
	}
	switch {
	case f.ackErr != nil:
		return "", "", false, f.ackErr
	case f.ackNone:
		return "", "", false, nil
	case f.ackDecision != "":
		return f.ackDecision, f.ackReason, true, nil
	}
	row := f.lastRow()
	return row.Decision, row.DecisionReason, row.Decision != "", nil
}
func (f *flakyRelayStore) DenyRelayTimeout(instanceID, requestToken string, _ time.Time, maxWait time.Duration, check func() error) (bool, error) {
	f.denies = append(f.denies, relayWrite{instanceID, requestToken, maxWait})
	if check != nil {
		if err := check(); err != nil {
			return false, err
		}
	}
	return f.denyErr == nil && !f.denyNone, f.denyErr
}
func (f *flakyRelayStore) GetPermissionRequest(_, _ string) (store.PermissionRow, error) {
	if f.onGet != nil {
		f.onGet()
	}
	if len(f.getRows) == 0 {
		return store.PermissionRow{}, sql.ErrNoRows
	}
	i := min(int(f.idx.Add(1)-1), len(f.getRows)-1)
	return f.getRows[i], f.getErrs[i]
}

// lastRow is the row the latest read returned.
func (f *flakyRelayStore) lastRow() store.PermissionRow {
	if len(f.getRows) == 0 {
		return store.PermissionRow{}
	}
	return f.getRows[max(min(int(f.idx.Load())-1, len(f.getRows)-1), 0)]
}

// relayDoubleConfig is hookConfig for a relayed hook of id against a double:
// a parent that stays the hook's starting Claude Code, hookConfig's virtual
// clock, and a 30 s relay timeout.
func relayDoubleConfig(id string) hook.HandleConfig {
	hc := hookConfig(envWith(id), hookParent{PID: 4242, Start: storefix.SeedPaneStarttime, Name: "claude"})
	hc.RelayTimeout = 30 * time.Second
	return hc
}

// envWith is a relay-on hook environment for instance id.
func envWith(id string) func(string) string { return envHook(id, hook.RelayModeOn) }

// assertDenyEnvelope confirms stdout is one SRD §6.3 deny envelope.
func assertDenyEnvelope(t *testing.T, stdout *bytes.Buffer) {
	t.Helper()
	var env struct {
		HookSpecificOutput struct {
			Decision struct {
				Behavior string `json:"behavior"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || env.HookSpecificOutput.Decision.Behavior != "deny" {
		t.Fatalf("stdout = %q (%v); want a deny envelope (SRD §6.4)", stdout.String(), err)
	}
}

func newSilentLogger() *log.Logger { return log.New(io.Discard, "", 0) }

// errReader is a stdin whose Read always fails.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("simulated stdin failure") }

// TestHandleErrorPaths: Handle returns nil on every path, makes only the store
// calls before a failure, logs the failure and prints the envelope the event
// and relay mode call for: nothing (SRD §3.2, b.45p), deny (SRD §6.4) or, for
// the relayed request that is decided, the decision. A relayed request makes
// no separate state write: its first write is the request with the move to
// check_permission (b.146 rule 1).
func TestHandleErrorPaths(t *testing.T) {
	const (
		id   = "id-1"
		pr   = `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`
		ptu  = `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`
		ss   = `{"hook_event_name":"SessionStart","transcript_path":"/x/abc.jsonl"}`
		stop = `{"hook_event_name":"Stop"}`
	)
	relayOff, relayOn := envHook(id, ""), envWith(id)
	relayOnNoID := envWith("")
	deny := hook.EncodeDecision(hook.EventNamePermissionRequest, "deny", "") + "\n"
	dbErr := errors.New("db unreachable")
	cases := []struct {
		name                    string
		env                     func(string) string
		payload                 string // stdin; "" = a read error
		st                      *flakyRelayStore
		want                    string // stdout
		log                     string // a phrase the log carries
		transitions, identities int    // store calls made
		outcome                 string // ad.hook.fired's upsert_outcome; "" = unchecked
		noRelayLines            bool   // no ad.relay_attempt.completed or ad.resume.observed
	}{
		// SRD §3.2: state tracking is fail-open.
		{name: "no instance id", env: envHook("", ""), payload: ss, log: "resolve instance id"},
		{name: "malformed payload", env: relayOff, payload: "not json", log: "classify"},
		{name: "payload over the cap", env: relayOff, payload: strings.Repeat("a", int(hook.MaxPayloadBytes)+1), log: "read payload"},
		{name: "transition error", env: relayOff, payload: stop, st: &flakyRelayStore{transitionErr: dbErr},
			transitions: 1, log: "apply transition", outcome: "error"},
		{name: "SessionStart identity error", env: relayOff, payload: ss, st: &flakyRelayStore{identityErr: dbErr},
			identities: 1, log: "record session start identity"},
		// SR-22.9: SessionStart's state and identity are one gated write, no separate transition.
		{name: "SessionStart is one write", env: relayOff, payload: ss, identities: 1},
		{name: "PermissionRequest, relay unset", env: relayOff, payload: pr, transitions: 1, noRelayLines: true},
		{name: "PermissionRequest, relay off", env: envHook(id, hook.RelayModeOff), payload: pr, transitions: 1, noRelayLines: true},
		// SRD §6.4: relay on, a PermissionRequest's failures are a deny envelope.
		{name: "PermissionRequest, no instance id", env: relayOnNoID, payload: pr, want: deny},
		{name: "PermissionRequest, invalid instance id", env: envWith("id/with/slash"), payload: pr, want: deny},
		{name: "PermissionRequest, first write error", env: relayOn, payload: pr, st: &flakyRelayStore{insertErr: dbErr},
			want: deny, log: "first write", outcome: "error", noRelayLines: true},
		{name: "PermissionRequest, allowed", env: relayOn, payload: pr,
			st:   &flakyRelayStore{getRows: []store.PermissionRow{{Decision: "allow", DecisionReason: "trusted"}}, getErrs: []error{nil}},
			want: hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "trusted") + "\n", outcome: "inserted"},
		// b.45p: relay on, a hook that is not (or may not be) a PermissionRequest prints nothing.
		{name: "relay on, malformed payload", env: relayOn, payload: "not json"},
		{name: "relay on, stdin read error", env: relayOn, log: "read payload"},
		{name: "PreToolUse, relay on, no instance id", env: relayOnNoID, payload: ptu},
		{name: "PreToolUse, relay on, transition error", env: relayOn, payload: ptu, st: &flakyRelayStore{transitionErr: errors.New("BUSY")}, transitions: 1},
		{name: "SessionStart, relay on", env: relayOn, payload: ss, identities: 1, noRelayLines: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := tc.st
			if st == nil {
				st = &flakyRelayStore{}
			}
			var stdin io.Reader = errReader{}
			if tc.payload != "" {
				stdin = strings.NewReader(tc.payload)
			}
			var stdout, logBuf bytes.Buffer
			before := len(readTrailLines(t, trailFile()))

			hc := relayDoubleConfig(id)
			hc.Env = tc.env
			err := hook.Handle(context.Background(), stdin, &stdout, st, hc, log.New(&logBuf, "", 0))

			if err != nil || stdout.String() != tc.want {
				t.Errorf("Handle = %v, stdout %q; want nil, %q", err, stdout.String(), tc.want)
			}
			if !strings.Contains(logBuf.String(), tc.log) {
				t.Errorf("log = %q; want it to carry %q", logBuf.String(), tc.log)
			}
			if len(st.transitionArgs) != tc.transitions || st.identityN != tc.identities {
				t.Errorf("transitions/identity writes = %d/%d; want %d/%d", len(st.transitionArgs), st.identityN, tc.transitions, tc.identities)
			}
			if tc.outcome != "" {
				row := hookFiredAt(t, before)
				assertStr(t, row, "upsert_outcome", tc.outcome)
				assertNoToolInput(t, row)
			}
			for _, ev := range []string{"ad.relay_attempt.completed", "ad.resume.observed"} {
				if n := len(linesAfter(t, before, ev, id)); tc.noRelayLines && n != 0 {
					t.Errorf("%s lines = %d; want none (the relay did not poll)", ev, n)
				}
			}
		})
	}
}

// TestReadPayloadAndInstanceID pins io.go's limits: a payload up to
// MaxPayloadBytes reads (empty included) and one byte more is
// ErrPayloadTooLarge; an instance id must be set, with no path separator, NUL
// or control byte.
func TestReadPayloadAndInstanceID(t *testing.T) {
	for _, n := range []int64{0, hook.MaxPayloadBytes} {
		if p, err := hook.ReadPayload(bytes.NewReader(make([]byte, n))); err != nil || int64(len(p)) != n {
			t.Errorf("ReadPayload(%d bytes) = %d bytes, %v; want all, nil", n, len(p), err)
		}
	}
	if _, err := hook.ReadPayload(bytes.NewReader(make([]byte, hook.MaxPayloadBytes+1))); !errors.Is(err, hook.ErrPayloadTooLarge) {
		t.Errorf("ReadPayload(over the cap) err = %v; want ErrPayloadTooLarge", err)
	}
	for v, want := range map[string]error{"": hook.ErrInstanceIDMissing, "abc/def": hook.ErrInstanceIDInvalid,
		"abc\x00def": hook.ErrInstanceIDInvalid, "abc\tdef": hook.ErrInstanceIDInvalid} {
		if _, err := hook.ResolveInstanceID(func(string) string { return v }); !errors.Is(err, want) {
			t.Errorf("ResolveInstanceID(%q) err = %v; want %v", v, err, want)
		}
	}
}
