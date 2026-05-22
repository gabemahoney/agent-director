// error_cases_test.go implements:
//
//  1. TestEnvelopeDiff_Error — drives every row in errorCases through both the
//     CLI subprocess and the in-process Client and asserts err_name equality
//     plus err_description prefix-match.
//
//  2. TestErrorTableCoverage — coverage gate: iterates manifest.CallableVerbs()
//     and confirms every verb with non-empty ErrorNames has at least one row
//     in errorCases (subject to documented exemptions).
//
//  3. TestErrDescriptionPrefix — unit tests for errDescriptionPrefix and
//     errDescriptionsMatch (helpers in err_description_prefix.go).
//
// Envelope shape on error (E1/E3 pin): the CLI emits {"err_name",
// "err_description"} JSON to stderr and exits non-zero; pkg/api.Client returns
// a typed error carrying err_name + err_description. Both sides are reduced to
// the same {"err_name","err_description"} JSON object before comparison.
package envelope_diff

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// ── TestEnvelopeDiff_Error ────────────────────────────────────────────────────

// TestEnvelopeDiff_Error exercises every row in errorCases, one t.Run per
// (verb, errName) pair, and asserts that:
//
//   - The CLI exits non-zero and its stderr carries {"err_name":…,"err_description":…}.
//   - The Client returns an error envelope with the same fields.
//   - Both err_name values equal the row's expected errName.
//   - Both err_description values match under the prefix policy (errDescriptionsMatch).
func TestEnvelopeDiff_Error(t *testing.T) {
	binPath := buildCLI(t)

	for _, ec := range errorCases {
		ec := ec // capture loop variable

		t.Run(ec.verb+"/"+ec.errName, func(t *testing.T) {
			// Platform-specific skip (e.g. ErrProbeUnsupported on Linux).
			if ec.skip != nil {
				ec.skip(t)
			}

			// Clear instance-id env so parent_id is always NULL on both
			// sides (mirrors TestEnvelopeDiff_Success convention).
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

			// ── 1. Seed ────────────────────────────────────────────────
			srcDir, ctx := ec.seed(t)

			// ── 2. Copy (CLI) ──────────────────────────────────────────
			homeDir1, dbPath1 := copyFixtureStore(t, srcDir)
			t.Setenv("HOME", homeDir1)

			// ── 3. Run CLI ─────────────────────────────────────────────
			cliEnv, exitCode := runCLI(t, binPath, dbPath1, ec.cliArgv(ctx)...)
			if exitCode == 0 {
				t.Fatalf("CLI exited 0 on error path; stdout:\n%s", cliEnv)
			}

			// ── 4. Copy (Client) ───────────────────────────────────────
			homeDir2, dbPath2 := copyFixtureStore(t, srcDir)
			t.Setenv("HOME", homeDir2)

			// ── 5. Run Client ──────────────────────────────────────────
			clientEnv, errReturned := runClient(t, dbPath2, ec.verb, ec.params(ctx))
			if !errReturned {
				t.Fatalf("Client returned success envelope on error path:\n%s", clientEnv)
			}

			// ── 6. Decode both envelopes ───────────────────────────────
			var cliParsed, clientParsed struct {
				ErrName        string `json:"err_name"`
				ErrDescription string `json:"err_description"`
			}
			if err := json.Unmarshal(cliEnv, &cliParsed); err != nil {
				t.Fatalf("decode CLI error envelope: %v\nraw: %s", err, cliEnv)
			}
			if err := json.Unmarshal(clientEnv, &clientParsed); err != nil {
				t.Fatalf("decode Client error envelope: %v\nraw: %s", err, clientEnv)
			}

			// ── 7. Assert err_name ─────────────────────────────────────
			if cliParsed.ErrName != ec.errName {
				t.Errorf("CLI err_name = %q; want %q\nenvelope: %s",
					cliParsed.ErrName, ec.errName, cliEnv)
			}
			if clientParsed.ErrName != ec.errName {
				t.Errorf("Client err_name = %q; want %q\nenvelope: %s",
					clientParsed.ErrName, ec.errName, clientEnv)
			}
			if cliParsed.ErrName != clientParsed.ErrName {
				t.Errorf("err_name mismatch: CLI=%q Client=%q",
					cliParsed.ErrName, clientParsed.ErrName)
			}

			// ── 8. Assert err_description prefix-match ─────────────────
			if !errDescriptionsMatch(cliParsed.ErrDescription, clientParsed.ErrDescription) {
				t.Errorf("err_description mismatch (prefix policy):\n  CLI:    %q\n  Client: %q",
					cliParsed.ErrDescription, clientParsed.ErrDescription)
			}

			// ── 9. Structural diff on the error envelope ───────────────
			// Verify overall envelope structure is identical; err_description
			// is suppressed here since the prefix check above covers it.
			cliNorm, err := normalize(cliEnv)
			if err != nil {
				t.Fatalf("normalize CLI error envelope: %v", err)
			}
			clientNorm, err := normalize(clientEnv)
			if err != nil {
				t.Fatalf("normalize Client error envelope: %v", err)
			}
			// Ignore the err_description value; structural presence of the
			// key is still verified (both envelopes must carry it).
			diffs := structuralDiff(cliNorm, clientNorm, []string{"err_description"})
			if len(diffs) > 0 {
				t.Errorf("envelope-diff (error): %q/%q: %d structural field(s) differ:",
					ec.verb, ec.errName, len(diffs))
				for _, d := range diffs {
					t.Errorf("  %s", d)
				}
				t.Logf("cli    envelope: %s", cliNorm)
				t.Logf("client envelope: %s", clientNorm)
			}
		})
	}
}

// ── TestErrorTableCoverage ────────────────────────────────────────────────────

// TestErrorTableCoverage is the coverage gate. It iterates
// manifest.CallableVerbs() and fails if any callable verb with non-empty
// ErrorNames lacks at least one row in errorCases.
//
// The init() guard in error_cases.go panics at program startup on the same
// condition; this test provides a friendly t.Error report in the test output.
//
// Rows with a skip hook (e.g. find-missing/ErrProbeUnsupported) must still
// appear in errorCases — the row is counted for coverage even when the
// subtest itself will be skipped on this platform.
func TestErrorTableCoverage(t *testing.T) {
	// Index error cases by verb.
	covered := make(map[string][]string)
	for _, ec := range errorCases {
		covered[ec.verb] = append(covered[ec.verb], ec.errName)
	}

	// Defensive: confirm no non-callable verb snuck into errorCases.
	callableSet := make(map[string]bool)
	for _, v := range manifest.CallableVerbs() {
		callableSet[v.Name] = true
	}
	for _, ec := range errorCases {
		if !callableSet[ec.verb] {
			t.Errorf("errorCases: verb %q is not callable (serve/hook/help are excluded)", ec.verb)
		}
	}

	// For each callable verb with ErrorNames, verify at least one row exists.
	for _, v := range manifest.CallableVerbs() {
		if len(v.ErrorNames) == 0 {
			continue // verbs without documented error paths are out of scope
		}
		if len(covered[v.Name]) == 0 {
			t.Errorf("verb %q has ErrorNames %v but no row in errorCases; add a row (with skip hook if needed on this platform)",
				v.Name, v.ErrorNames)
		}
	}

	// Optional strict mode: require every errName to have a dedicated row.
	// Activated via ENVELOPE_DIFF_FULL_ERROR_COVERAGE=1. Normal CI only
	// requires at least one per verb; the errnames catalog test (pkg/api/errnames)
	// enforces full per-err_name parity across the SRD §3.2 contract.
	if os.Getenv("ENVELOPE_DIFF_FULL_ERROR_COVERAGE") == "1" {
		errCovered := make(map[string]map[string]bool)
		for _, ec := range errorCases {
			if errCovered[ec.verb] == nil {
				errCovered[ec.verb] = make(map[string]bool)
			}
			errCovered[ec.verb][ec.errName] = true
		}
		for _, v := range manifest.CallableVerbs() {
			for _, en := range v.ErrorNames {
				if !errCovered[v.Name][en] {
					t.Errorf("strict: verb %q errName %q has no error_cases row", v.Name, en)
				}
			}
		}
	}
}


// ── TestErrDescriptionPrefix ──────────────────────────────────────────────────

// TestErrDescriptionPrefix exercises errDescriptionPrefix and
// errDescriptionsMatch against the documented colon / no-colon cases from
// err_description_prefix.go.
func TestErrDescriptionPrefix(t *testing.T) {
	prefixCases := []struct {
		in   string
		want string
	}{
		{"tmux session create: no such file", "tmux session create:"},
		{"spawn id-1 state ended is not interactive", "spawn id-1 state ended is not interactive"},
		{"", ""},
		{"single:", "single:"},
		{"a:b:c", "a:"},
		{"nonexistent-id", "nonexistent-id"},
	}
	for _, tc := range prefixCases {
		got := errDescriptionPrefix(tc.in)
		if got != tc.want {
			t.Errorf("errDescriptionPrefix(%q) = %q; want %q", tc.in, got, tc.want)
		}
	}

	matchCases := []struct {
		cli, client string
		want        bool
		desc        string
	}{
		// Both no-colon: full equality required.
		{"same description", "same description", true, "both no-colon equal"},
		{"different", "also different", false, "both no-colon different"},
		// Both have colon: prefix equality.
		{"tmux session create: linux error", "tmux session create: darwin error", true, "same prefix different OS tail"},
		{"tmux session create: linux", "tmux kill: linux", false, "different prefixes"},
		// Mixed: one has colon, one doesn't.
		{"tmux session create: os error", "tmux session create:", true, "client is bare prefix"},
		{"tmux session create:", "tmux session create: os error", true, "cli is bare prefix"},
		// Full match when no-colon values are identical.
		{"nonexistent-id", "nonexistent-id", true, "no-colon full equality"},
	}
	for _, tc := range matchCases {
		got := errDescriptionsMatch(tc.cli, tc.client)
		if got != tc.want {
			t.Errorf("errDescriptionsMatch(%q, %q) [%s] = %v; want %v",
				tc.cli, tc.client, tc.desc, got, tc.want)
		}
	}
}
