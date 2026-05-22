// success_cases_test.go implements TestEnvelopeDiff_Success, the top-level
// harness test that exercises every callable verb on its happy path and asserts
// that the CLI subprocess and the in-process Client produce the same JSON
// envelope.
//
// Test flow per verb:
//
//  1. Seed   — build a fresh fixture store via successCase.seed.
//  2. Copy   — call copyFixtureStore twice (one CLI copy, one Client copy)
//              so mutations from each execution path stay isolated.
//  3. Setup  — if successCase.extraSetup is non-nil, call it once per copy
//              with HOME pointing at that copy's homeDir.  For most verbs
//              this is a no-op; for resume it creates the JSONL transcript.
//  4. Run    — collect JSON envelopes from runCLI and runClient.
//  5. Diff   — normalize both envelopes and compare via structuralDiff,
//              suppressing fields listed in nondeterministic.json.
//  6. Assert — t.Errorf if any diff entries remain after suppression.
package envelope_diff

import (
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

func TestEnvelopeDiff_Success(t *testing.T) {
	binPath := buildCLI(t)
	nonDet := loadNonDetManifest(t)

	for _, verb := range manifest.CallableVerbs() {
		verb := verb // capture loop variable for subtest closure

		t.Run(verb.Name, func(t *testing.T) {
			sc, ok := lookupSuccessCase(verb.Name)
			if !ok {
				// init() panics if any callable verb is missing, so this
				// path is only reachable if the binary was built without
				// the init() check (shouldn't happen; belt-and-suspenders).
				t.Fatalf("no successCase registered for callable verb %q", verb.Name)
			}

			// Clear AGENT_DIRECTOR_INSTANCE_ID for every subtest.
			// The CLI subprocess receives only HOME+PATH in its env
			// (runCLI hard-codes cmd.Env), so it always sees an empty
			// instance ID and writes parent_id=NULL.  The Client runs
			// in-process and would inherit whatever the test runner's
			// env carries; blanking the var keeps CLI and Client in sync
			// (both produce parent_id=NULL / parent="" on spawn/resume).
			t.Setenv("AGENT_DIRECTOR_INSTANCE_ID", "")

			// ── 1. Seed ────────────────────────────────────────────────
			srcDir, ctx := sc.seed(t)

			// ── 2 & 3. Copy + Setup (CLI) ──────────────────────────────
			homeDir1, dbPath1 := copyFixtureStore(t, srcDir)
			if sc.extraSetup != nil {
				// Point HOME at the CLI copy's homeDir before extraSetup
				// so helpers that call os.UserHomeDir() (e.g.
				// apitest.SeedJsonl) create files in the right base dir.
				t.Setenv("HOME", homeDir1)
				sc.extraSetup(t, homeDir1, ctx)
			}

			// ── 2 & 3. Copy + Setup (Client) ───────────────────────────
			homeDir2, dbPath2 := copyFixtureStore(t, srcDir)
			// Always point HOME at the Client copy's homeDir before the
			// Client run.  runClient doesn't override HOME in the process
			// environment, so verbs that call os.UserHomeDir() (resume,
			// make-template, spawn pre-trust) must find their files here.
			t.Setenv("HOME", homeDir2)
			if sc.extraSetup != nil {
				sc.extraSetup(t, homeDir2, ctx)
			}
			// HOME = homeDir2 for the remainder of this subtest.

			// ── 4. Run ─────────────────────────────────────────────────
			cliEnv, exitCode := runCLI(t, binPath, dbPath1, sc.cliArgv(ctx)...)
			if exitCode != 0 {
				t.Fatalf("CLI exited %d on success path; stderr:\n%s",
					exitCode, cliEnv)
			}

			clientEnv, errReturned := runClient(t, dbPath2, verb.Name, sc.params(ctx))
			if errReturned {
				t.Fatalf("Client returned error envelope on success path:\n%s",
					clientEnv)
			}

			// ── 5. Normalize ───────────────────────────────────────────
			cliNorm, err := normalize(cliEnv)
			if err != nil {
				t.Fatalf("normalize CLI envelope for %q: %v\nraw: %s",
					verb.Name, err, cliEnv)
			}
			clientNorm, err := normalize(clientEnv)
			if err != nil {
				t.Fatalf("normalize Client envelope for %q: %v\nraw: %s",
					verb.Name, err, clientEnv)
			}

			// ── 5. Load ignore selectors ───────────────────────────────
			sels, err := nonDet.Selectors(verb.Name)
			if err != nil {
				// nondeterministic.json must cover every callable verb;
				// a missing key is a CI configuration error.
				t.Fatalf("nonDet.Selectors(%q): %v", verb.Name, err)
			}

			// ── 6. Diff + Assert ───────────────────────────────────────
			diffs := structuralDiff(cliNorm, clientNorm, sels)
			if len(diffs) == 0 {
				return // envelopes are equivalent; happy path.
			}

			t.Errorf("envelope-diff: %q: %d field(s) differ after suppression:",
				verb.Name, len(diffs))
			for _, d := range diffs {
				t.Errorf("  %s", d)
			}
			t.Logf("cli    envelope: %s", cliNorm)
			t.Logf("client envelope: %s", clientNorm)
		})
	}
}
