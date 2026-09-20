package hook_test

// identity_capture_test.go — SR-6 identity + transcript capture on the
// SessionStart hook, driven in-process through hook.Handle against a real
// storefix store with an injected fake resolver (HandleConfig.Resolver).
//
// These are the handler-level tests for the widened SessionStart write site
// (RecordSessionStartIdentity). Store-level column semantics are pinned
// separately in internal/store/spawns_test.go; here we assert the HANDLER
// wiring: which events invoke the resolver, what pid/proc_starttime/jsonl_path
// land on the row (read back via GetSpawn, never internals), and the
// fail-open + log-line discipline per SRD §3.2 / SR-6.5.
//
// Each behavior is pinned so the OPPOSITE behavior fails:
//   - re-record: a second SessionStart with a different identity must show the
//     new values (fails under keep-old behavior).
//   - no-clobber: PreToolUse/Stop must leave identity+jsonl_path untouched AND
//     never invoke the resolver (fake counts calls).
//   - fail-open: a resolver error yields NULL identity (0/"") but STILL writes
//     jsonl_path, Handle returns nil, and a log line is emitted.
//   - preserve-when-empty: SessionStart with empty transcript_path preserves
//     session id / jsonl_path while identity is re-recorded.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/config"
	"github.com/gabemahoney/agent-director/internal/hook"
	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/procstarttimefix"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

// fakeResolver is an injectable hook.IdentityResolver double. It returns a
// fixed (pid, procStartTime, err) tuple and counts Resolve calls so tests can
// assert the resolver is (or is not) invoked. The recorded ids let callers
// confirm Resolve was keyed by the resolved instance id.
type fakeResolver struct {
	pid           int
	procStartTime string
	err           error

	calls   int
	lastID  string
	seenIDs []string
}

func (f *fakeResolver) Resolve(id string) (int, string, error) {
	f.calls++
	f.lastID = id
	f.seenIDs = append(f.seenIDs, id)
	return f.pid, f.procStartTime, f.err
}

// identityLogger builds a *log.Logger writing into buf so tests can assert a
// log line was produced (SR-6.5) without pinning exact phrasing.
func identityLogger() (*log.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return log.New(buf, "", 0), buf
}

// handleWith drives one in-process Handle call (no relay) against st with the
// given payload, resolver, and logger. Any Handle error is fatal — SR-6 paths
// are all fail-open (return nil), so a non-nil error is itself a bug.
func handleWith(t *testing.T, st hook.HookStore, id, payload string, r hook.IdentityResolver, logger *log.Logger) {
	t.Helper()
	if err := hook.Handle(
		context.Background(),
		strings.NewReader(payload),
		io.Discard,
		st,
		hook.HandleConfig{
			Env:      envHook(id, ""),
			Cfg:      config.Relay{},
			Resolver: r,
		},
		logger,
	); err != nil {
		t.Fatalf("Handle(%q): %v", payload, err)
	}
}

// TestSessionStartRecordsResolvedIdentity pins behavior 1: a SessionStart with
// a fake resolver returning (pid, proc_starttime) lands those values on the
// spawn row, and jsonl_path == the payload transcript_path. Asserted via
// GetSpawn (store read), never internals.
func TestSessionStartRecordsResolvedIdentity(t *testing.T) {
	const id = "ic-record-identity"
	const transcript = "/x/sess-abc123.jsonl"

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	r := &fakeResolver{pid: 4242, procStartTime: procstarttimefix.LinuxProcStarttime}
	logger, _ := identityLogger()

	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":"`+transcript+`"}`,
		r, logger)

	if r.calls != 1 {
		t.Errorf("resolver called %d times; want 1", r.calls)
	}
	if r.lastID != id {
		t.Errorf("resolver keyed by %q; want instance id %q", r.lastID, id)
	}

	row, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	if row.PID != 4242 {
		t.Errorf("PID = %d; want 4242", row.PID)
	}
	if row.ProcStarttime != procstarttimefix.LinuxProcStarttime {
		t.Errorf("ProcStarttime = %q; want %q", row.ProcStarttime, procstarttimefix.LinuxProcStarttime)
	}
	if row.JSONLPath != transcript {
		t.Errorf("JSONLPath = %q; want %q (payload transcript_path)", row.JSONLPath, transcript)
	}
}

// TestSessionStartRefreshesIdentity pins behavior 2: a second SessionStart with
// a DIFFERENT fake identity refreshes pid/proc_starttime on the row. This test
// FAILS under keep-old behavior (the assertions demand the new values).
func TestSessionStartRefreshesIdentity(t *testing.T) {
	const id = "ic-refresh-identity"

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	logger, _ := identityLogger()

	// First SessionStart: identity A.
	r1 := &fakeResolver{pid: 1111, procStartTime: "1001"}
	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":"/x/first.jsonl"}`,
		r1, logger)

	row, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn (after first): %v", err)
	}
	if row.PID != 1111 || row.ProcStarttime != "1001" {
		t.Fatalf("after first SessionStart: PID=%d proc=%q; want 1111/\"1001\"", row.PID, row.ProcStarttime)
	}

	// Second SessionStart: identity B — must REFRESH, not keep A.
	r2 := &fakeResolver{pid: 2222, procStartTime: "2002"}
	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":"/x/second.jsonl"}`,
		r2, logger)

	row, err = st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn (after second): %v", err)
	}
	if row.PID != 2222 {
		t.Errorf("PID = %d; want 2222 (refreshed, not kept-old 1111)", row.PID)
	}
	if row.ProcStarttime != "2002" {
		t.Errorf("ProcStarttime = %q; want \"2002\" (refreshed, not kept-old \"1001\")", row.ProcStarttime)
	}
}

// TestNonSessionStartLeavesIdentityUntouched pins behavior 3: after a good
// SessionStart, non-SessionStart events (PreToolUse, Stop) neither clobber the
// recorded identity/jsonl_path NOR invoke the resolver. The resolver's call
// count must remain zero across those events.
func TestNonSessionStartLeavesIdentityUntouched(t *testing.T) {
	const id = "ic-noclobber"
	const transcript = "/x/sess-noclobber.jsonl"

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	logger, _ := identityLogger()

	// Record a known identity via SessionStart.
	seedResolver := &fakeResolver{pid: 7777, procStartTime: "7007"}
	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":"`+transcript+`"}`,
		seedResolver, logger)

	before, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn (baseline): %v", err)
	}

	// Non-SessionStart events with a fresh resolver that MUST NOT be called.
	noCall := &fakeResolver{pid: 9999, procStartTime: "9009"}
	for _, payload := range []string{
		`{"hook_event_name":"PreToolUse","tool_name":"Bash"}`,
		`{"hook_event_name":"Stop"}`,
	} {
		handleWith(t, st, id, payload, noCall, logger)
	}

	if noCall.calls != 0 {
		t.Errorf("resolver invoked %d times on non-SessionStart events; want 0", noCall.calls)
	}

	after, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn (after non-SessionStart): %v", err)
	}
	if after.PID != before.PID {
		t.Errorf("PID clobbered: got %d; want %d (unchanged)", after.PID, before.PID)
	}
	if after.ProcStarttime != before.ProcStarttime {
		t.Errorf("ProcStarttime clobbered: got %q; want %q (unchanged)", after.ProcStarttime, before.ProcStarttime)
	}
	if after.JSONLPath != before.JSONLPath {
		t.Errorf("JSONLPath clobbered: got %q; want %q (unchanged)", after.JSONLPath, before.JSONLPath)
	}
	// Sanity: the seeded values are what we expect to still be there.
	if after.PID != 7777 || after.JSONLPath != transcript {
		t.Errorf("recorded identity lost: PID=%d JSONLPath=%q; want 7777/%q", after.PID, after.JSONLPath, transcript)
	}
}

// TestSessionStartResolverFailureFailsOpen pins behavior 4: when the resolver
// returns an error, the SessionStart write records NULL identity (PID=0 /
// ProcStarttime="" read back via GetSpawn) but STILL writes jsonl_path, Handle
// returns nil (fail-open exit-0 semantics), AND — PM-mandated — a log line
// mentioning the identity/resolve failure is produced on the injected logger.
func TestSessionStartResolverFailureFailsOpen(t *testing.T) {
	const id = "ic-resolver-failure"
	const transcript = "/x/sess-failopen.jsonl"

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	r := &fakeResolver{err: errors.New("ancestry walk failed")}
	logger, logbuf := identityLogger()

	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":"`+transcript+`"}`,
		r, logger)

	if r.calls != 1 {
		t.Errorf("resolver called %d times; want 1", r.calls)
	}

	row, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn: %v", err)
	}
	// NULL identity: zero values scanned back via COALESCE.
	if row.PID != 0 {
		t.Errorf("PID = %d; want 0 (NULL identity on resolver failure)", row.PID)
	}
	if row.ProcStarttime != "" {
		t.Errorf("ProcStarttime = %q; want \"\" (NULL identity on resolver failure)", row.ProcStarttime)
	}
	// jsonl_path is STILL written despite the identity failure.
	if row.JSONLPath != transcript {
		t.Errorf("JSONLPath = %q; want %q (transcript still written on fail-open)", row.JSONLPath, transcript)
	}

	// PM-mandated log-line assertion: capture is via the injected *log.Logger
	// writing into logbuf (the package's logf discipline). We assert the line
	// mentions the resolve/identity failure without pinning exact phrasing.
	logged := logbuf.String()
	if logged == "" {
		t.Fatal("expected a log line on resolver failure; log buffer empty")
	}
	low := strings.ToLower(logged)
	if !strings.Contains(low, "resolve") && !strings.Contains(low, "identity") {
		t.Errorf("log line does not mention resolve/identity failure; got %q", logged)
	}
	if !strings.Contains(low, "ancestry walk failed") {
		t.Errorf("log line does not carry the underlying resolver error; got %q", logged)
	}
}

// TestSessionStartEmptyTranscriptPreservesButRerecords pins behavior 5: a
// SessionStart with an EMPTY transcript_path after a prior good SessionStart
// preserves the existing claude_session_id / jsonl_path (COALESCE) while
// identity is still re-recorded from the new resolver.
func TestSessionStartEmptyTranscriptPreservesButRerecords(t *testing.T) {
	const id = "ic-empty-transcript"
	const transcript = "/x/sess-preserve.jsonl"
	const sessionID = "sess-preserve"

	st, _ := storefix.OpenTempStore(t)
	storefix.SeedSpawn(t, st, id)

	logger, _ := identityLogger()

	// Prior good SessionStart establishes session id + jsonl_path + identity A.
	// transcript_path basename → SessionID (the classifier derives it), so use a
	// path whose basename is the session id we assert on.
	r1 := &fakeResolver{pid: 3333, procStartTime: "3003"}
	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":"/x/`+sessionID+`.jsonl"}`,
		r1, logger)

	first, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn (after first): %v", err)
	}
	if first.JSONLPath == "" || first.ClaudeSessionID == "" {
		t.Fatalf("precondition: first SessionStart must populate jsonl_path/session id; got jsonl=%q sess=%q",
			first.JSONLPath, first.ClaudeSessionID)
	}

	// Second SessionStart with EMPTY transcript_path — session id / jsonl_path
	// must be PRESERVED, but identity B must REFRESH.
	r2 := &fakeResolver{pid: 4444, procStartTime: "4004"}
	handleWith(t, st, id,
		`{"hook_event_name":"SessionStart","transcript_path":""}`,
		r2, logger)

	after, err := st.GetSpawn(id)
	if err != nil {
		t.Fatalf("GetSpawn (after second): %v", err)
	}
	if after.JSONLPath != first.JSONLPath {
		t.Errorf("JSONLPath = %q; want %q (preserved on empty transcript)", after.JSONLPath, first.JSONLPath)
	}
	if after.ClaudeSessionID != first.ClaudeSessionID {
		t.Errorf("ClaudeSessionID = %q; want %q (preserved on empty transcript)", after.ClaudeSessionID, first.ClaudeSessionID)
	}
	// Identity still re-recorded from the new resolver.
	if after.PID != 4444 {
		t.Errorf("PID = %d; want 4444 (re-recorded even on empty transcript)", after.PID)
	}
	if after.ProcStarttime != "4004" {
		t.Errorf("ProcStarttime = %q; want \"4004\" (re-recorded even on empty transcript)", after.ProcStarttime)
	}
	// Guard against the assertion silently passing on preserved value equality.
	if after.PID == first.PID {
		t.Errorf("PID not refreshed: still %d (equal to first identity)", after.PID)
	}
}

// compile-time guard: the test asserts against a real store via HookStore.
var _ hook.HookStore = (*store.Store)(nil)
