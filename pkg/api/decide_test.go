package api_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
	"github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

func TestDecideEmptyRequestToken(t *testing.T) {
	// api.Decide must reject an empty RequestToken at the API layer,
	// independent of CLI gating. One open row exists so the store's
	// ErrAmbiguousRequest guard would not fire — the rejection must come
	// from Decide itself.
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     "", // empty — should be rejected before the store is called
		Decision:         "allow",
	})
	if !errors.Is(err, api.ErrMissingRequestToken) {
		t.Fatalf("err = %v; want ErrMissingRequestToken", err)
	}
}

func TestDecideRelayOffRejected(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "off")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
	})
	if !errors.Is(err, api.ErrRelayModeOff) {
		t.Fatalf("err = %v; want ErrRelayModeOff", err)
	}
}

func TestDecideUnknownSpawn(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "absent",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
	})
	if !errors.Is(err, store.ErrSpawnNotFound) {
		t.Fatalf("err = %v; want ErrSpawnNotFound", err)
	}
}

func TestDecideInvalidDecision(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "perhaps",
	})
	if !errors.Is(err, api.ErrInvalidDecision) {
		t.Fatalf("err = %v; want ErrInvalidDecision", err)
	}
}

func TestDecideFirstCallWins(t *testing.T) {
	// Two consecutive decides on the same open row. The first writes
	// allow; the second sees the populated decision column and the
	// `decision IS NULL` guard short-circuits the UPDATE.
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")

	if _, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
		Reason:           "ok",
	}); err != nil {
		t.Fatalf("first Decide: %v", err)
	}

	// Read directly to verify the write landed.
	// For allow, api.Decide writes dbReason="" regardless of params.Reason.
	row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	if row.Decision != "allow" || row.DecisionReason != "" {
		t.Errorf("row after first decide: decision=%q reason=%q; want (allow, \"\")", row.Decision, row.DecisionReason)
	}

	// Second call → ErrAlreadyDecided.
	_, err = api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "deny",
		Reason:           "no",
	})
	if !errors.Is(err, store.ErrAlreadyDecided) {
		t.Fatalf("second Decide err = %v; want ErrAlreadyDecided", err)
	}

	// The first decide's values must not have been clobbered.
	row, _ = s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
	if row.Decision != "allow" || row.DecisionReason != "" {
		t.Errorf("row after second decide: decision=%q reason=%q; want unchanged (allow, \"\")", row.Decision, row.DecisionReason)
	}
}

// TestDecideConcurrentFirstCallWins drives N parallel Decide calls
// against the same open row. The SQL `decision IS NULL` guard makes the
// update first-call-wins; exactly one goroutine must succeed and the
// rest must observe ErrAlreadyDecided. Contention surface is the SQL
// boundary, not the Go-level test.
func TestDecideConcurrentFirstCallWins(t *testing.T) {
	const workers = 8
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")

	start := make(chan struct{})
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		decision := "allow"
		if i%2 == 1 {
			decision = "deny"
		}
		reason := fmt.Sprintf("w%d", i)
		go func() {
			defer wg.Done()
			<-start
			_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
				ClaudeInstanceID: "id-d-1",
				RequestToken:     storefix.TestRequestTokenA,
				Decision:         decision,
				Reason:           reason,
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var winners, losers, other int
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, store.ErrAlreadyDecided):
			losers++
		default:
			other++
			t.Errorf("unexpected err from concurrent Decide: %v", err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d; want exactly 1", winners)
	}
	if losers != workers-1 {
		t.Errorf("losers = %d; want %d", losers, workers-1)
	}
	if other != 0 {
		t.Errorf("unexpected non-ErrAlreadyDecided errors: %d", other)
	}
}

func TestDecideNoOpenPermissionRequest(t *testing.T) {
	// Spawn exists, relay_mode=on, but no row in permission_requests.
	// The verb surfaces ErrNoOpenPermissionRequest after the UPDATE
	// no-ops and the follow-up SELECT returns sql.ErrNoRows.
	s, _ := apitest.SeedDecideFixture(t, "on")
	_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
	})
	if !errors.Is(err, store.ErrNoOpenPermissionRequest) {
		t.Fatalf("err = %v; want ErrNoOpenPermissionRequest", err)
	}
}

func TestDecideDenyDefaultEnvelopeReasonNotWritten(t *testing.T) {
	// Task E: for a deny, the store always records DecisionReasonOperator;
	// params.Reason is NOT written to the DB row. The canonical "operator"
	// reason string is what the polling loop (and hook.EncodeDecision) will
	// read back, not a copy of params.Reason. This test pins the write-site
	// invariant: deny → decision_reason=store.DecisionReasonOperator always.
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	if _, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "deny",
		Reason:           "",
	}); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	row, _ := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
	if row.Decision != "deny" {
		t.Errorf("Decision = %q; want deny", row.Decision)
	}
	// decision_reason must be the canonical "operator" constant, not the
	// raw params.Reason value (""). The store always records one of the
	// DecisionReason* constants for operator-originated decisions.
	if row.DecisionReason != store.DecisionReasonOperator {
		t.Errorf("DecisionReason = %q; want %q (DecisionReasonOperator always written for deny)",
			row.DecisionReason, store.DecisionReasonOperator)
	}
}

// TestAmbiguousDecide verifies that when empty RequestToken is passed to
// api.Decide, ErrMissingRequestToken is returned at the API layer before the
// store's ErrAmbiguousRequest defense-in-depth guard is reached. The store's
// guard remains as defense-in-depth for direct DecidePermissionRequest callers;
// api.Decide's early check supersedes it.
func TestAmbiguousDecide(t *testing.T) {
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	// Seed a second open row with a distinct token.
	if err := s.UpsertOpenPermissionRequest("id-d-1", storefix.TestRequestTokenB, "Read", `{"file":"/etc/hosts"}`, 0, ""); err != nil {
		t.Fatalf("UpsertOpenPermissionRequest (second row): %v", err)
	}
	_, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     "", // empty — rejected at API layer before store
		Decision:         "allow",
	})
	if !errors.Is(err, api.ErrMissingRequestToken) {
		t.Fatalf("err = %v; want ErrMissingRequestToken", err)
	}
}

// TestDecisionReasonOnlyCanonicalValues has two sub-cases:
//
//   - source_walk: walk all non-test production .go files and assert that
//     no DecidePermissionRequest call has a raw non-empty string literal
//     as its reason argument. All reason arguments must reference a
//     store.DecisionReason* constant or the empty string.
//
//   - operator_runtime: call api.Decide with Decision="deny" and verify
//     the raw decision_reason column carries store.DecisionReasonOperator
//     ("operator"), confirming the canonical constant is used at runtime.
func TestDecisionReasonOnlyCanonicalValues(t *testing.T) {
	t.Run("source_walk", func(t *testing.T) {
		root := findModuleRoot(t)

		// Canonical reason string literals — any BasicLit STRING value that is not
		// in this set as the 4th argument to DecidePermissionRequest is a violation.
		// AST BasicLit.Value includes surrounding quotes, so the entries are quoted.
		// Identifier/constant references (e.g. store.DecisionReasonOperator) are
		// never BasicLit and are always presumed canonical.
		canonicalReasons := map[string]bool{
			`""`:             true,
			`"operator"`:     true,
			`"timeout"`:      true,
			`"find_missing"`: true,
		}

		skipDirs := map[string]bool{
			"apitest": true, "testsupport": true, "testdata": true,
			"test": true, "vendor": true,
		}

		scanDirs := []string{
			filepath.Join(root, "internal"),
			filepath.Join(root, "pkg"),
			filepath.Join(root, "cmd"),
		}

		var violations []string
		for _, dir := range scanDirs {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				continue
			}
			err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					base := filepath.Base(path)
					if skipDirs[base] || strings.HasPrefix(base, ".") {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}

				fset := token.NewFileSet()
				f, parseErr := parser.ParseFile(fset, path, nil, 0)
				if parseErr != nil {
					return fmt.Errorf("parse %s: %w", path, parseErr)
				}

				ast.Inspect(f, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if sel.Sel.Name != "DecidePermissionRequest" {
						return true
					}
					if len(call.Args) < 4 {
						return true
					}
					lit, ok := call.Args[3].(*ast.BasicLit)
					if !ok {
						// Identifier or expression — presumed canonical constant.
						return true
					}
					if lit.Kind != token.STRING {
						return true
					}
					if !canonicalReasons[lit.Value] {
						pos := fset.Position(call.Pos())
						relPath, _ := filepath.Rel(root, path)
						violations = append(violations, fmt.Sprintf("%s:%d: reason literal %s",
							relPath, pos.Line, lit.Value))
					}
					return true
				})
				return nil
			})
			if err != nil {
				t.Fatalf("source walk: %v", err)
			}
		}
		if len(violations) > 0 {
			t.Errorf("DecidePermissionRequest calls with raw string literal reason (use a DecisionReason* constant instead):\n%s",
				strings.Join(violations, "\n"))
		}
	})

	t.Run("operator_runtime", func(t *testing.T) {
		// api.Decide with Decision="deny" must write DecisionReasonOperator
		// to the decision_reason column, not params.Reason.
		s, _ := apitest.SeedDecideFixture(t, "on")
		apitest.SeedPermissionRow(t, s, "id-d-1")
		if _, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
			ClaudeInstanceID: "id-d-1",
			RequestToken:     storefix.TestRequestTokenA,
			Decision:         "deny",
		}); err != nil {
			t.Fatalf("Decide: %v", err)
		}
		row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if err != nil {
			t.Fatalf("GetPermissionRequest: %v", err)
		}
		if row.DecisionReason != store.DecisionReasonOperator {
			t.Errorf("DecisionReason = %q; want %q (DecisionReasonOperator)",
				row.DecisionReason, store.DecisionReasonOperator)
		}
	})
}

func TestDecideRelayFallenBack(t *testing.T) {
	// A backdated (undeliverable) open request: Decide must refuse with
	// ErrRelayFallenBack and MUST NOT record a decision — the row's decision
	// column stays NULL so the refusal is never mistaken for a success.
	s, dbPath := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")
	// Backdate created_at well past a 1h window so the row is undeliverable.
	storefix.SeedUndeliverablePermissionRequest(t, s, dbPath, "id-d-1", storefix.TestRequestTokenA, 2*time.Hour)

	_, err := api.Decide(s, time.Hour, time.Now(), api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
		Reason:           "too late",
	})
	if !errors.Is(err, api.ErrRelayFallenBack) {
		t.Fatalf("err = %v; want ErrRelayFallenBack", err)
	}

	// The refusal must not have written a decision.
	row, gErr := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
	if gErr != nil {
		t.Fatalf("GetPermissionRequest: %v", gErr)
	}
	if row.Decision != "" {
		t.Errorf("decision = %q after fallen-back refusal; want NULL/empty", row.Decision)
	}
}

func TestDecideInWindowRecordsDecision(t *testing.T) {
	// An in-window request records allow and deny exactly as before. Two
	// sub-cases exercise both verdicts against a comfortably-deliverable row
	// (created just now, wide window).
	for _, tc := range []struct {
		name       string
		decision   string
		wantReason string
	}{
		{"allow", "allow", ""},
		{"deny", "deny", store.DecisionReasonOperator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := apitest.SeedDecideFixture(t, "on")
			apitest.SeedPermissionRow(t, s, "id-d-1")

			if _, err := api.Decide(s, 24*time.Hour, time.Now(), api.DecideParams{
				ClaudeInstanceID: "id-d-1",
				RequestToken:     storefix.TestRequestTokenA,
				Decision:         tc.decision,
			}); err != nil {
				t.Fatalf("Decide: %v", err)
			}
			row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
			if err != nil {
				t.Fatalf("GetPermissionRequest: %v", err)
			}
			if row.Decision != tc.decision || row.DecisionReason != tc.wantReason {
				t.Errorf("row: decision=%q reason=%q; want (%q, %q)",
					row.Decision, row.DecisionReason, tc.decision, tc.wantReason)
			}
		})
	}
}

func TestDecideDeliverabilityBoundary(t *testing.T) {
	// Boundary/safety-margin exercised via the injected clock and window — no
	// sleeps, no backdating. The row's created_at is real-now (T0). Cutoff =
	// now - (window - margin); the row is deliverable iff created_at > cutoff,
	// i.e. iff now < T0 + window - margin. We pin both sides of the boundary by
	// choosing `now` relative to T0.
	//
	//   - refused: now = T0 + window, so cutoff = T0 + margin > T0 (undeliverable).
	//   - accepted: now = T0, cutoff = T0 - (window - margin) << T0 (deliverable).
	const window = 10 * time.Second

	t.Run("aged_at_boundary_refused", func(t *testing.T) {
		s, _ := apitest.SeedDecideFixture(t, "on")
		apitest.SeedPermissionRow(t, s, "id-d-1")
		// Read the actual stored created_at so the boundary math is exact
		// against the second-truncated column, avoiding a flaky epsilon.
		row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if err != nil {
			t.Fatalf("GetPermissionRequest: %v", err)
		}
		// now just past the boundary: cutoff = now - (window-margin) lands one
		// second after created_at, so created_at is NOT strictly after cutoff.
		now := row.CreatedAt.Add(window - api.RelayKillSafetyMargin + time.Second)
		_, err = api.Decide(s, window, now, api.DecideParams{
			ClaudeInstanceID: "id-d-1",
			RequestToken:     storefix.TestRequestTokenA,
			Decision:         "allow",
		})
		if !errors.Is(err, api.ErrRelayFallenBack) {
			t.Fatalf("err = %v; want ErrRelayFallenBack at boundary", err)
		}
		row, _ = s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if row.Decision != "" {
			t.Errorf("decision = %q at refused boundary; want NULL/empty", row.Decision)
		}
	})

	t.Run("exact_equality_refused", func(t *testing.T) {
		// The exact-equality boundary: now = created_at + window - margin, so
		// cutoff = now - (window - margin) == created_at exactly. A row is
		// deliverable iff created_at is STRICTLY after the cutoff, so a row whose
		// created_at equals the cutoff is undeliverable — Decide must REFUSE.
		// Deterministic because created_at is second-truncated in the column, so
		// the equality holds exactly against the read-back value.
		s, _ := apitest.SeedDecideFixture(t, "on")
		apitest.SeedPermissionRow(t, s, "id-d-1")
		row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if err != nil {
			t.Fatalf("GetPermissionRequest: %v", err)
		}
		// cutoff == created_at exactly.
		now := row.CreatedAt.Add(window - api.RelayKillSafetyMargin)
		_, err = api.Decide(s, window, now, api.DecideParams{
			ClaudeInstanceID: "id-d-1",
			RequestToken:     storefix.TestRequestTokenA,
			Decision:         "allow",
		})
		if !errors.Is(err, api.ErrRelayFallenBack) {
			t.Fatalf("err = %v; want ErrRelayFallenBack at exact-equality boundary (cutoff == created_at)", err)
		}
		row, _ = s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if row.Decision != "" {
			t.Errorf("decision = %q at exact-equality boundary; want NULL/empty", row.Decision)
		}
	})

	t.Run("comfortably_in_window_accepted", func(t *testing.T) {
		s, _ := apitest.SeedDecideFixture(t, "on")
		apitest.SeedPermissionRow(t, s, "id-d-1")
		row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if err != nil {
			t.Fatalf("GetPermissionRequest: %v", err)
		}
		// now = created_at: cutoff is a full window in the past; row is well
		// inside the deliverability window.
		if _, err := api.Decide(s, window, row.CreatedAt, api.DecideParams{
			ClaudeInstanceID: "id-d-1",
			RequestToken:     storefix.TestRequestTokenA,
			Decision:         "allow",
		}); err != nil {
			t.Fatalf("Decide (in-window): %v", err)
		}
		row, _ = s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
		if row.Decision != "allow" {
			t.Errorf("decision = %q comfortably in-window; want allow", row.Decision)
		}
	})
}

func TestDecideAlreadyDecidedBeatsFallenBack(t *testing.T) {
	// Precedence: a decided row that is ALSO aged out of the window must return
	// ErrAlreadyDecided, not ErrRelayFallenBack — fallen-back applies only to
	// open rows. First decide in-window (records allow), then re-decide with an
	// advanced clock that makes the row undeliverable; the guarded UPDATE
	// no-ops and the follow-up SELECT sees a decided row.
	s, _ := apitest.SeedDecideFixture(t, "on")
	apitest.SeedPermissionRow(t, s, "id-d-1")

	row, err := s.GetPermissionRequest("id-d-1", storefix.TestRequestTokenA)
	if err != nil {
		t.Fatalf("GetPermissionRequest: %v", err)
	}
	const window = 10 * time.Second
	if _, err := api.Decide(s, window, row.CreatedAt, api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "allow",
	}); err != nil {
		t.Fatalf("first Decide: %v", err)
	}

	// Advance the clock past the deliverability boundary and re-decide.
	aged := row.CreatedAt.Add(window - api.RelayKillSafetyMargin + time.Second)
	_, err = api.Decide(s, window, aged, api.DecideParams{
		ClaudeInstanceID: "id-d-1",
		RequestToken:     storefix.TestRequestTokenA,
		Decision:         "deny",
	})
	if !errors.Is(err, store.ErrAlreadyDecided) {
		t.Fatalf("err = %v; want ErrAlreadyDecided (must beat ErrRelayFallenBack)", err)
	}
	if errors.Is(err, api.ErrRelayFallenBack) {
		t.Fatalf("err = %v; ErrRelayFallenBack must not fire for a decided row", err)
	}
}

// findModuleRoot walks up from this test file's location to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod from %s", file)
		}
		dir = parent
	}
}
