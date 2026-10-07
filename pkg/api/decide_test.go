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

// decideA decides request A of id-d-1 with decision and reason at now under window.
func decideA(s *store.Store, window time.Duration, now time.Time, decision, reason string) error {
	_, err := api.Decide(s, window, now, api.DecideParams{ClaudeInstanceID: "id-d-1",
		RequestToken: storefix.TestRequestTokenA, Decision: decision, Reason: reason})
	return err
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
			if err := decideA(s, 24*time.Hour, time.Now(), decision, "caller reason"); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			if row := rowA(t, s); row.Decision != decision || row.DecisionReason != wantReason {
				t.Errorf("row = (%q, %q); want (%q, %q)", row.Decision, row.DecisionReason, decision, wantReason)
			}
		})
	}
}

// TestDecideFirstCallWins: a second decide on a decided row is ErrAlreadyDecided
// and leaves the first verdict; it beats ErrRelayFallenBack even once the row
// is aged out of the window, since fallen-back applies only to open rows.
func TestDecideFirstCallWins(t *testing.T) {
	t.Parallel()
	const window = 10 * time.Second
	for name, age := range map[string]time.Duration{"in window": 0, "aged out": window - api.RelayKillSafetyMargin + time.Second} {
		t.Run(name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			created := rowA(t, s).CreatedAt
			if err := decideA(s, window, created, "allow", "ok"); err != nil {
				t.Fatalf("first Decide: %v", err)
			}
			err := decideA(s, window, created.Add(age), "deny", "no")
			if !errors.Is(err, store.ErrAlreadyDecided) || errors.Is(err, api.ErrRelayFallenBack) {
				t.Fatalf("second Decide err = %v; want ErrAlreadyDecided only", err)
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
			results <- decideA(s, 24*time.Hour, time.Now(), decision, fmt.Sprintf("w%d", i))
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

// TestDecideDeliverabilityBoundary pins the deliverability boundary on the
// injected clock: the row is deliverable iff now < created_at + window - margin
// (b.2b8's "at most 2 s"); a refusal is ErrRelayFallenBack and records nothing.
func TestDecideDeliverabilityBoundary(t *testing.T) {
	t.Parallel()
	const window = 10 * time.Second
	edge := window - api.RelayKillSafetyMargin
	cases := []struct {
		name    string
		age     time.Duration // now - created_at
		refused bool
	}{
		{"aged_at_boundary_refused", edge + time.Second, true},
		{"exact_equality_refused", edge, true}, // cutoff == created_at: not strictly after
		{"just_before_boundary_accepted", edge - time.Nanosecond, false},
		{"comfortably_in_window_accepted", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")
			err := decideA(s, window, rowA(t, s).CreatedAt.Add(tc.age), "allow", "")
			want := "allow"
			if tc.refused {
				want = ""
				if !errors.Is(err, api.ErrRelayFallenBack) {
					t.Fatalf("err = %v; want ErrRelayFallenBack", err)
				}
			} else if err != nil {
				t.Fatalf("Decide: %v; want the verdict recorded", err)
			}
			if got := rowA(t, s).Decision; got != want {
				t.Errorf("decision = %q; want %q", got, want)
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
