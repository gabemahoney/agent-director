package hook_test

// failclosed_test.go — Handle's error paths against a HookStore double: SRD
// §3.2 (state tracking is fail-open: log, exit 0, print nothing), SRD §6.4 (with
// relay_mode on, every failure of a PermissionRequest hook ends in a deny
// envelope) and b.45p (a hook that is not, or may not be, a PermissionRequest
// prints nothing). Poll's own fail-closed exits are pinned in polling_test.go;
// the timeout's DB-before-stdout order in timeout_writes_test.go.

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

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
)

// flakyRelayStore is a HookStore double with programmable errors and recorded
// calls. Its gated writes report applied unless they error (the hook is the
// row's own agent, SR-22.9); the gate itself is the store's and is tested
// against a real store. GetPermissionRequest returns getRows/getErrs in turn,
// the last entry sticky.
type flakyRelayStore struct {
	transitionErr, identityErr, upsertErr error
	getRows                               []store.PermissionRow
	getErrs                               []error
	idx                                   atomic.Int32
	identityN                             int
	decideArgs                            []decideCall
	transitionArgs                        []transitionCall
}

type decideCall struct{ InstanceID, RequestToken, Decision, Reason string }

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
func (f *flakyRelayStore) UpsertOpenPermissionRequest(string, store.HookGate, string, string, string, int, string) (store.HookApplied, error) {
	return store.HookApplied{Applied: f.upsertErr == nil}, f.upsertErr
}
func (f *flakyRelayStore) DecidePermissionRequest(instanceID, requestToken, decision, reason string, _ string) (bool, error) {
	f.decideArgs = append(f.decideArgs, decideCall{instanceID, requestToken, decision, reason})
	return true, nil
}
func (f *flakyRelayStore) GetPermissionRequest(_, _ string) (store.PermissionRow, error) {
	if len(f.getRows) == 0 {
		return store.PermissionRow{}, sql.ErrNoRows
	}
	i := min(int(f.idx.Add(1)-1), len(f.getRows)-1)
	return f.getRows[i], f.getErrs[i]
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
// the relayed request that is decided, the decision.
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
		{name: "PermissionRequest, transition error", env: relayOn, payload: pr, st: &flakyRelayStore{transitionErr: dbErr},
			transitions: 1, want: deny, outcome: "error"},
		{name: "PermissionRequest, upsert error", env: relayOn, payload: pr, st: &flakyRelayStore{upsertErr: dbErr},
			transitions: 1, want: deny, log: "relay: upsert", outcome: "error", noRelayLines: true},
		{name: "PermissionRequest, allowed", env: relayOn, payload: pr,
			st:          &flakyRelayStore{getRows: []store.PermissionRow{{Decision: "allow", DecisionReason: "trusted"}}, getErrs: []error{nil}},
			transitions: 1, want: hook.EncodeDecision(hook.EventNamePermissionRequest, "allow", "trusted") + "\n"},
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

			err := hook.Handle(context.Background(), stdin, &stdout, st,
				hook.HandleConfig{Env: tc.env, Cfg: config.Relay{TimeoutSeconds: 1}}, log.New(&logBuf, "", 0))

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
