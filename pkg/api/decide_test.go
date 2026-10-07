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
// created_at + window + margin + created_at's resolution (b.pzy).
func TestDecideDeliverabilityBoundary(t *testing.T) {
	t.Parallel()
	const window = 10 * time.Second
	edge := window - api.RelayKillSafetyMargin
	release := window + api.RelayKillSafetyMargin
	settled := release + api.CreatedAtResolution
	cases := []struct {
		name    string
		age     time.Duration // now - created_at
		refused bool
		wait    time.Duration // decide's wait for the relay hook
	}{
		{"hook_settled_refused_at_once", settled, true, 0},
		{"just_before_hook_settled_refused_after_waiting", settled - time.Nanosecond, true, time.Nanosecond},
		{"guard_released_refused_after_waiting", release, true, api.CreatedAtResolution},
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

// TestDecideRequestGoneDuringWait: a request removed (its spawn deleted) while
// decide waits for its relay hook is ErrNoOpenPermissionRequest, without "answer at the pane" (b.pzy).
func TestDecideRequestGoneDuringWait(t *testing.T) {
	t.Parallel()
	const window = 10 * time.Second
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	slept := false
	_, err := api.DecideWithSleep(s, window, rowA(t, s).CreatedAt.Add(window), func(time.Duration) {
		slept = true
		if err := s.DeleteSpawn("id-d-1"); err != nil {
			t.Errorf("DeleteSpawn: %v", err)
		}
	}, api.DecideParams{ClaudeInstanceID: "id-d-1", RequestToken: storefix.TestRequestTokenA, Decision: "allow"})

	assertOneSentinel(t, err, store.ErrNoOpenPermissionRequest)
	if !slept || strings.Contains(errText(err), "answer at the pane") {
		t.Errorf("decide slept: %v, err: %v; want it to wait, then name no pane answer", slept, err)
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
