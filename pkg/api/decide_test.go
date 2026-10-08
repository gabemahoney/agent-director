package api_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// decideA decides request A of id-d-1 with decision and reason at now under
// window; it returns how long decide waited for the relay hook (b.pzy).
func decideA(s *store.Store, window time.Duration, now time.Time, decision, reason string) (time.Duration, error) {
	var slept time.Duration
	_, err := api.DecideWithSleep(s, window, now, func(d time.Duration) { slept += d }, api.DecideParams{
		ClaudeInstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: decision, Reason: reason})
	return slept, err
}

// rowA returns id-d-1's request A.
func rowA(t *testing.T, s *store.Store) store.PermissionRow {
	t.Helper()
	row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	return row
}

// TestDecideRefusals: each refusal returns its error and records nothing. An
// empty token is refused by Decide itself, even with two open rows, before the
// store's ErrAmbiguousRequest guard is reached.
func TestDecideRefusals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		relay     string
		rows      int // open requests: 0, 1 (A) or 2 (A and B)
		id, token string
		decision  string
		want      error
	}{
		{"empty token", "on", 1, "id-d-1", "", "allow", api.ErrMissingRequestToken},
		{"empty token, two open rows", "on", 2, "id-d-1", "", "allow", api.ErrMissingRequestToken},
		{"relay off", "off", 1, "id-d-1", storefix.TestRequestTokenA, "allow", api.ErrRelayModeOff},
		{"unknown spawn", "on", 1, "absent", storefix.TestRequestTokenA, "allow", store.ErrSpawnNotFound},
		{"invalid decision", "on", 1, "id-d-1", storefix.TestRequestTokenA, "perhaps", api.ErrInvalidDecision},
		{"no open request", "on", 0, "id-d-1", storefix.TestRequestTokenA, "allow", store.ErrNoOpenPermissionRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, tc.relay)
			if tc.rows > 0 {
				apitest.SeedPermissionRow(t, s, "id-d-1")
			}
			if tc.rows > 1 {
				openAgentRequest(t, s, "id-d-1", storefix.TestRequestTokenB, "Read", `{"file":"/etc/hosts"}`, 0)
			}
			_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{ClaudeInstanceID: tc.id,
				RequestToken: tc.token, Decision: tc.decision})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v; want %v", err, tc.want)
			}
			if tc.rows > 0 && rowA(t, s).Decision != "" {
				t.Errorf("request A decided %q after a refusal; want it open", rowA(t, s).Decision)
			}
		})
	}
}

// TestDecideRecordsVerdict: an in-window allow records no reason and a deny
// always DecisionReasonOperator, never the caller's reason (Task E).
func TestDecideRecordsVerdict(t *testing.T) {
	t.Parallel()
	for decision, wantReason := range map[string]string{"allow": "", "deny": store.DecisionReasonOperator} {
		t.Run(decision, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			if _, err := decideA(s, 24*time.Hour, time.Now(), decision, "caller reason"); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if row := rowA(t, s); row.Decision != decision || row.DecisionReason != wantReason {
				t.Errorf("row = (%q, %q); want (%q, %q)", row.Decision, row.DecisionReason, decision, wantReason)
			}
		})
	}
}

// TestDecideFirstCallWins: a second decide on a decided row is ErrAlreadyDecided
// at once and leaves the first verdict; it beats ErrRelayFallenBack even once
// the row is aged out of the window, since fallen-back applies only to open
// rows, and does not wait for the relay hook (b.pzy).
func TestDecideFirstCallWins(t *testing.T) {
	t.Parallel()
	const window = 10 * time.Second
	for name, age := range map[string]time.Duration{"in window": 0, "aged out": window - api.RelayKillSafetyMargin + time.Second} {
		t.Run(name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			created := rowA(t, s).CreatedAt
			if _, err := decideA(s, window, created, "allow", "ok"); err != nil {
				t.Fatalf("first Decide: %v", err)
			}
			slept, err := decideA(s, window, created.Add(age), "deny", "no")
			if !errors.Is(err, store.ErrAlreadyDecided) || errors.Is(err, api.ErrRelayFallenBack) || slept != 0 {
				t.Fatalf("second Decide err = %v after waiting %v; want ErrAlreadyDecided only, at once", err, slept)
			}
			if row := rowA(t, s); row.Decision != "allow" || row.DecisionReason != "" {
				t.Errorf("row = (%q, %q); want the first verdict (allow, \"\")", row.Decision, row.DecisionReason)
			}
		})
	}
}

// TestDecideConcurrentFirstCallWins: of N parallel decides on one open row,
// the SQL `decision IS NULL` guard lets exactly one win; the rest get
// ErrAlreadyDecided.
func TestDecideConcurrentFirstCallWins(t *testing.T) {
	t.Parallel()
	const workers = 8
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		decision := []string{"allow", "deny"}[i%2]
		go func() {
			defer wg.Done()
			<-start
			_, err := decideA(s, 24*time.Hour, time.Now(), decision, fmt.Sprintf("w%d", i))
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	winners, losers := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, store.ErrAlreadyDecided):
			losers++
		default:
			t.Errorf("unexpected err from concurrent Decide: %v", err)
		}
	}
	if winners != 1 || losers != workers-1 {
		t.Errorf("winners %d, losers %d; want 1 and %d", winners, losers, workers-1)
	}
}

// TestDecideDeliverabilityBoundary pins, on the injected clock, that the row is
// deliverable iff now < created_at + window - margin; a refusal records nothing
// and is ErrRelayFallenBack only after decide waits out its relay hook, until
// created_at + window + margin + created_at's resolution, the send-keys
// guard's release (b.pzy, b.z6g).
func TestDecideDeliverabilityBoundary(t *testing.T) {
	t.Parallel()
	const window = 10 * time.Second
	edge := window - api.RelayKillSafetyMargin
	settled := window + api.RelayKillSafetyMargin + api.CreatedAtResolution
	cases := []struct {
		name    string
		age     time.Duration // now - created_at
		refused bool
		wait    time.Duration // decide's wait for the relay hook
	}{
		{"hook_settled_refused_at_once", settled, true, 0},
		{"just_before_hook_settled_refused_after_waiting", settled - time.Nanosecond, true, time.Nanosecond},
		{"margin_past_window_refused_after_waiting", window + api.RelayKillSafetyMargin, true, api.CreatedAtResolution},
		{"window_end_refused_after_waiting", window, true, api.RelayKillSafetyMargin + api.CreatedAtResolution},
		{"exact_equality_refused_after_waiting", edge, true, 2*api.RelayKillSafetyMargin + api.CreatedAtResolution}, // cutoff == created_at: not strictly after
		{"just_before_boundary_accepted", edge - time.Nanosecond, false, 0},
		{"comfortably_in_window_accepted", 0, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			slept, err := decideA(s, window, rowA(t, s).CreatedAt.Add(tc.age), "allow", "")
			want := "allow"
			if tc.refused {
				want = ""
				if !errors.Is(err, api.ErrRelayFallenBack) {
					t.Fatalf("err = %v; want ErrRelayFallenBack", err)
				}
			} else if err != nil {
				t.Fatalf("Decide: %v; want the verdict recorded", err)
			}
			if got := rowA(t, s).Decision; got != want || slept != tc.wait {
				t.Errorf("decision = %q after waiting %v; want %q after %v", got, slept, want, tc.wait)
			}
		})
	}
}

// TestClientDecideWaitsForRelayHook: Client.Decide at its earliest refusal waits
// the longest (3 s) on its own clock and sleep, and a timeout deny the live
// relay hook writes meanwhile comes back as ErrAlreadyDecided (b.pzy).
func TestClientDecideWaitsForRelayHook(t *testing.T) {
	t.Parallel()
	e := newKillEnv(t)
	r := seedRelayRow(t, e, storefix.TestRequestTokenA)
	createdAt := advRequestCreatedAt(t, e, r)
	c, _ := e.client(t)
	api.SetClockForTest(c, func() time.Time { return createdAt.Add(sendKeysWindow() - api.RelayKillSafetyMargin) })
	var slept time.Duration
	e.sleep = func(d time.Duration) {
		slept += d
		relayHookTimeout(t, e, r.ID, storefix.TestRequestTokenA)
	}

	_, err := c.Decide(api.DecideParams{ClaudeInstanceID: r.ID, RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

	assertOneSentinel(t, err, store.ErrAlreadyDecided)
	adviceAssertPhrase(t, err, advDecideHookTimedOut)
	if slept != 3*time.Second { // its Go doc: "at most 3 s"
		t.Errorf("Client.Decide waited %v through its sleep; want 3s", slept)
	}
}

// TestDecideFallenBackShownAlone: request A, open past its relay window, is ErrRelayFallenBack only while the Spawn is
// in check_permission with no request recorded after A and no other open; otherwise ErrNoOpenPermissionRequest naming
// why, with no pane answer advised, and A stays open (b.t6e).
func TestDecideFallenBackShownAlone(t *testing.T) {
	t.Parallel()
	tokB := storefix.TestRequestTokenB
	cases := []struct {
		name    string
		other   string // request B: recorded "older" or "later" than A; "": none
		decided bool   // B is decided
		move    string // the agent's move after A was recorded; "": none
		capped  bool   // another spawn then records a request with eviction cap 1
		why     string // decide's reason for refusing A; "": ErrRelayFallenBack
	}{
		{"A alone", "", false, "", false, ""},
		{"agent's Stop", "", false, store.StateWaiting, false, "the spawn is in state waiting"},
		// Known gap (b.omt): the move is held while A is open, so a dialog closed at the pane looks still up.
		{"agent's move to working held", "", false, store.StateWorking, false, ""},
		{"later open request", "later", false, "", false, "the spawn recorded request " + tokB + " after it"},
		{"later decided request", "later", true, "", false, "the spawn recorded request " + tokB + " after it"},
		// The cap eviction keeps B, the Spawn's newest request, while A is open.
		{"later decided request, another spawn's insert over the cap", "later", true, "", true,
			"the spawn recorded request " + tokB + " after it"},
		{"older open request", "older", false, "", false, "the spawn's request " + tokB + " is open too"},
		{"older decided request", "older", true, "", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := apitest.SeedDecideFixture(t, "on")
			recordB := func() { openAgentRequest(t, s, "id-d-1", tokB, "Read", `{"file":"/etc/hosts"}`, 0) }
			if tc.other == "older" {
				recordB()
				storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", tokB, 2*relayGuardWindow)
			}
			apitest.SeedPermissionRow(t, s, "id-d-1")
			storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, 2*relayGuardWindow)
			if tc.other == "later" {
				recordB()
			}
			if tc.decided {
				if _, err := s.DecidePermissionRequest("id-d-1", tokB, "allow", "", store.WriterProcessDecide); err != nil {
					t.Fatalf("decide B: %v", err)
				}
			}
			if tc.capped {
				if err := s.InsertPending(store.Spawn{ClaudeInstanceID: "id-d-2", CWD: "/tmp", TmuxSessionName: "cd-d-2",
					RelayMode: "on"}); err != nil {
					t.Fatalf("InsertPending(id-d-2): %v", err)
				}
				openAgentRequest(t, s, "id-d-2", storefix.TestRequestTokenC, "Bash", `{"cmd":"echo"}`, 1)
			}
			if tc.move != "" {
				if err := seedAgentState(s, dbPath, "id-d-1", tc.move); err != nil {
					t.Fatalf("agent's move to %s: %v", tc.move, err)
				}
			}

			_, err := decideA(s, relayGuardWindow, time.Now(), "allow", "")

			if tc.why == "" {
				assertOneSentinel(t, err, api.ErrRelayFallenBack)
			} else {
				assertOneSentinel(t, err, store.ErrNoOpenPermissionRequest)
				adviceAssertPhrase(t, err, tc.why)
				assertNoPaneAdvice(t, err)
			}
			if got := rowA(t, s).Decision; got != "" {
				t.Errorf("request A decided %q after a refusal; want it open", got)
			}
		})
	}
}

// decideInterleaved is a DecideStore that runs write where a write landing among decide's reads would: right after its
// last read of the request ("after request"), or right before or after its read of the Spawn's requests ("before
// requests", "after requests"); at "wait" the test's sleep runs it instead.
type decideInterleaved struct {
	*store.Store
	at    string
	write func()
}

func (d decideInterleaved) GetPermissionRequest(id, token string) (store.PermissionRow, error) {
	pr, err := d.Store.GetPermissionRequest(id, token)
	if d.at == "after request" {
		d.write()
	}
	return pr, err
}

func (d decideInterleaved) PermissionRequestsForSpawn(id string) ([]store.PermissionRow, error) {
	if d.at == "before requests" {
		d.write()
	}
	rows, err := d.Store.PermissionRequestsForSpawn(id)
	if d.at == "after requests" {
		d.write()
	}
	return rows, err
}

// TestDecideFallenBackWriteBetweenReads: request A, read open past its relay window or waited on as decide starts
// refusing it, with a write landing among decide's reads is refused with no pane advice (b.pzy, b.t6e).
func TestDecideFallenBackWriteBetweenReads(t *testing.T) {
	t.Parallel()
	deleteSpawn := func(_ *testing.T, s *store.Store, _ string) error { return s.DeleteSpawn("id-d-1") }
	cases := []struct {
		name   string
		move   string // the agent's move after A was recorded, before decide; "": none
		at     string // where write lands: a decideInterleaved point, or "wait": during decide's wait for A's relay hook
		write  func(t *testing.T, s *store.Store, dbPath string) error
		want   error
		phrase string // carried by the refusal; "": none checked
	}{
		{"spawn deleted after the request's read", "", "after request", deleteSpawn, store.ErrNoOpenPermissionRequest, ""},
		{"spawn deleted after the spawn's read", "", "before requests", deleteSpawn, store.ErrNoOpenPermissionRequest, ""},
		{"request denied by its relay hook after the request's read", "", "after request",
			func(_ *testing.T, s *store.Store, _ string) error {
				_, err := s.DecidePermissionRequest("id-d-1", storefix.TestRequestTokenA, "deny",
					store.DecisionReasonTimeout, store.WriterProcessHook)
				return err
			}, store.ErrAlreadyDecided, advDecideHookTimedOut},
		// decide reads the Spawn before its requests: the other way round it would read A alone and check_permission,
		// so ErrRelayFallenBack while B's dialog is up.
		{"later request's hook after the requests' read, after the agent's Stop", store.StateWaiting, "after requests",
			func(t *testing.T, s *store.Store, dbPath string) error {
				if err := seedAgentState(s, dbPath, "id-d-1", store.StateCheckPermission); err != nil {
					return err
				}
				openAgentRequest(t, s, "id-d-1", storefix.TestRequestTokenB, "Read", `{"file":"/etc/hosts"}`, 0)
				return nil
			}, store.ErrNoOpenPermissionRequest, "the spawn is in state waiting"},
		{"spawn deleted during the wait", "", "wait", deleteSpawn, store.ErrNoOpenPermissionRequest, ""},
		{"agent's Stop during the wait", "", "wait", func(_ *testing.T, s *store.Store, dbPath string) error {
			return seedAgentState(s, dbPath, "id-d-1", store.StateWaiting)
		}, store.ErrNoOpenPermissionRequest, "the spawn is in state waiting"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, dbPath := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			now := rowA(t, s).CreatedAt.Add(relayGuardWindow - api.RelayKillSafetyMargin) // decide waits out A's relay hook
			if tc.at != "wait" {
				storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, 2*relayGuardWindow)
				now = time.Now() // A's relay hook long settled: no wait
			}
			if tc.move != "" {
				if err := seedAgentState(s, dbPath, "id-d-1", tc.move); err != nil {
					t.Fatalf("agent's move to %s: %v", tc.move, err)
				}
			}
			write := func() {
				if err := tc.write(t, s, dbPath); err != nil {
					t.Errorf("write at %s: %v", tc.at, err)
				}
			}
			slept := false
			_, err := api.DecideWithSleep(decideInterleaved{Store: s, at: tc.at, write: write}, relayGuardWindow, now,
				func(time.Duration) {
					slept = true
					if tc.at == "wait" {
						write()
					}
				},
				api.DecideParams{ClaudeInstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

			assertOneSentinel(t, err, tc.want)
			if tc.phrase != "" {
				adviceAssertPhrase(t, err, tc.phrase)
			}
			assertNoPaneAdvice(t, err)
			if slept != (tc.at == "wait") {
				t.Errorf("decide waited: %v; want %v", slept, tc.at == "wait")
			}
		})
	}
}

// TestDecisionReasonOnlyCanonicalValues: no production DecidePermissionRequest
// call passes a reason literal other than "" or a DecisionReason* value.
func TestDecisionReasonOnlyCanonicalValues(t *testing.T) {
	t.Parallel()
	canonical := map[string]bool{`""`: true, `"operator"`: true, `"timeout"`: true, `"find_missing"`: true}
	skip := map[string]bool{"apitest": true, "testsupport": true, "testdata": true, "test": true, "vendor": true}
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")):
				return filepath.SkipDir
			case d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go"):
				return nil
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 4 {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "DecidePermissionRequest" {
					return true
				}
				if lit, ok := call.Args[3].(*ast.BasicLit); ok && lit.Kind == token.STRING && !canonical[lit.Value] {
					t.Errorf("%s: DecidePermissionRequest reason literal %s; use a DecisionReason* constant", fset.Position(call.Pos()), lit.Value)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}
