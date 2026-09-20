package main_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// backdatePermissionRequest rewrites the created_at of an open
// permission_requests row (decision NULL) to now-age using a raw connection,
// mirroring the store-external backdating pattern used elsewhere in the CLI
// tests (the store API exposes no created_at mutation). Used to push an open
// request past the effective relay window so Decide surfaces ErrRelayFallenBack.
// The CLI binary runs on the real clock with the default 86400s window, so
// callers pass an age well past that plus RelayKillSafetyMargin (e.g. 48h).
func backdatePermissionRequest(t *testing.T, dbPath, instanceID, requestToken string, age time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	backdate := time.Now().UTC().Add(-age).Format("2006-01-02 15:04:05")
	res, err := db.Exec(
		`UPDATE permission_requests SET created_at = ? WHERE claude_instance_id = ? AND request_token = ? AND decision IS NULL`,
		backdate, instanceID, requestToken,
	)
	if err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		t.Fatalf("backdate affected %d rows; want 1 (row missing or already decided?)", n)
	}
}

// decideCalledLines filters trail lines for ad.decide.called events.
func decideCalledLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.decide.called" {
			out = append(out, l)
		}
	}
	return out
}

// trailLinesOrNil opens the trail file in trailDir and returns parsed lines,
// or nil when the file does not exist. Used by decide, find-missing and
// trail-emit CLI tests to assert row-mutation emission without failing on a
// missing file. trailDir is the directory that holds ad-trail.jsonl — for
// HOME-isolated CLI tests this is <home>/.agent-director (i.e. trailDir(home)).
func trailLinesOrNil(t *testing.T, adDir string) []map[string]any {
	t.Helper()
	path := filepath.Join(adDir, "ad-trail.jsonl")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	// readTrailLines takes the isolated HOME and appends .agent-director itself,
	// so hand it the parent of adDir (trailDir(home) -> home).
	return readTrailLines(t, filepath.Dir(adDir))
}

// trailDir returns the directory holding ad-trail.jsonl for a given isolated
// HOME. The trail always writes to $HOME/.agent-director/ad-trail.jsonl, so
// every subprocess CLI invocation with HOME=home lands here.
func trailDir(home string) string {
	return filepath.Join(home, ".agent-director")
}

// rowMutationCommittedLines filters lines for ad.row_mutation.committed events.
// Returns nil (not an empty slice) when none match, so callers can distinguish
// "no file" from "file present but no row-mutation lines".
func rowMutationCommittedLines(lines []map[string]any) []map[string]any {
	var out []map[string]any
	for _, l := range lines {
		if l["event"] == "ad.row_mutation.committed" {
			out = append(out, l)
		}
	}
	return out
}

// TestDecideMissingRequestTokenRejected verifies that the decide CLI verb
// requires --request-token: omitting it yields ErrInvalidFlags before any DB
// access, regardless of the spawn's state.
func TestDecideMissingRequestTokenRejected(t *testing.T) {
	fakeDir := buildFakeTmux(t)
	home := t.TempDir()
	bootstrapDB(t, home)
	dbPath := filepath.Join(home, ".agent-director", "state.db")
	const id = "id-no-token-1"
	seedSpawnRow(t, dbPath, id, "cd-no-token-1", "check_permission", "on")

	_, stderr, code := runSpawnCLI(t, home, fakeDir,
		"decide", "--claude-instance-id", id, "--decision", "allow")
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0 (stderr=%s)", stderr)
	}
	env := parseEnvelope(t, stderr)
	if env.ErrName != "ErrInvalidFlags" {
		t.Errorf("err_name = %q; want ErrInvalidFlags", env.ErrName)
	}
}

// TestDecideRaceLoserSeesErrAlreadyDecided verifies the first-call-wins
// contract at the CLI boundary: once a token has been decided, any
// subsequent decide call on the same token returns ErrAlreadyDecided,
// regardless of the first and second verdict combination. Three sub-cases
// cover allow→allow, deny→deny, and allow→deny.
//
// Trail assertions:
//   - First decide (success): exactly one ad.row_mutation.committed line with
//     writer_process="decide", decision=first, mutation_kind="update".
//   - Second decide (ErrAlreadyDecided): zero ad.row_mutation.committed lines —
//     the no-op UPDATE path must not emit.
func TestDecideRaceLoserSeesErrAlreadyDecided(t *testing.T) {
	cases := []struct {
		name   string
		first  string
		second string
	}{
		{"allow_then_allow", "allow", "allow"},
		{"deny_then_deny", "deny", "deny"},
		{"allow_then_deny", "allow", "deny"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeDir := buildFakeTmux(t)
			home := t.TempDir()
			bootstrapDB(t, home)
			dbPath := filepath.Join(home, ".agent-director", "state.db")
			const id = "id-race-1"
			seedSpawnRow(t, dbPath, id, "cd-race-1", "check_permission", "on")
			seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"echo"}`)

			// Both decides must run against the same DB (same token) to exercise
			// first-call-wins, so both share this test's isolated HOME — and thus
			// the same append-only trail at <home>/.agent-director/ad-trail.jsonl.
			// Per-call isolation is achieved with a checkpoint/delta line-count
			// pattern rather than distinct trail files.

			// First decide: must succeed (exit 0).
			_, stderr1, code1 := runSpawnCLI(t, home, fakeDir,
				"decide",
				"--claude-instance-id", id,
				"--request-token", testRequestToken,
				"--decision", tc.first)
			if code1 != 0 {
				t.Fatalf("first decide (%s) exit = %d; want 0 (stderr=%s)", tc.first, code1, stderr1)
			}

			// Trail after first decide: exactly one ad.row_mutation.committed
			// with writer_process="decide".
			linesAfterFirst := readTrailLines(t, home)
			rm1 := rowMutationCommittedLines(linesAfterFirst)
			if len(rm1) != 1 {
				t.Fatalf("first decide: ad.row_mutation.committed count = %d; want 1", len(rm1))
			}
			if rm1[0]["writer_process"] != "decide" {
				t.Errorf("first decide: writer_process = %v; want decide", rm1[0]["writer_process"])
			}
			if rm1[0]["decision"] != tc.first {
				t.Errorf("first decide: decision = %v; want %q", rm1[0]["decision"], tc.first)
			}
			if rm1[0]["mutation_kind"] != "update" {
				t.Errorf("first decide: mutation_kind = %v; want update", rm1[0]["mutation_kind"])
			}

			// Checkpoint the row_mutation line count so the second decide's
			// contribution can be measured as a delta against the shared trail.
			rmCheckpoint := len(rm1)

			// Second decide on the same token: must return ErrAlreadyDecided.
			_, stderr2, code2 := runSpawnCLI(t, home, fakeDir,
				"decide",
				"--claude-instance-id", id,
				"--request-token", testRequestToken,
				"--decision", tc.second)
			if code2 == 0 {
				t.Fatalf("second decide (%s) exit = 0; want non-zero (ErrAlreadyDecided)", tc.second)
			}
			env := parseEnvelope(t, stderr2)
			if env.ErrName != "ErrAlreadyDecided" {
				t.Errorf("err_name = %q; want ErrAlreadyDecided", env.ErrName)
			}

			// Trail delta: the second (no-op UPDATE) decide must add zero new
			// ad.row_mutation.committed lines to the shared trail.
			rm2 := rowMutationCommittedLines(readTrailLines(t, home))
			if got := len(rm2) - rmCheckpoint; got != 0 {
				t.Errorf("ErrAlreadyDecided path: expected 0 new ad.row_mutation.committed lines; got %d (total %d, checkpoint %d): %v", got, len(rm2), rmCheckpoint, rm2)
			}
		})
	}
}

// TestDecideCalledEmitsTrailLine is a table-driven test that covers the
// decide-verb outcomes and asserts the ad.decide.called trail line emitted
// on each path. Required top-level fields (source, ts, caller_*, outcome) are
// validated for every row. The ErrAlreadyDecided row additionally asserts zero
// ad.row_mutation.committed lines (Epic 3 no-op contract). The
// ErrRelayFallenBack row additionally asserts the typed err_name in the JSON
// error envelope on stderr (open-but-undeliverable path, SR-4.4).
//
// ErrAmbiguousRequest is skipped: the store guard only fires when requestToken
// is empty, but the API layer (pkg/api/decide.go) rejects an empty token with
// ErrMissingRequestToken before the store is reached. There is no CLI path
// that triggers ErrAmbiguousRequest.
func TestDecideCalledEmitsTrailLine(t *testing.T) {
	cases := []struct {
		name        string
		wantOutcome string
	}{
		{name: "ok", wantOutcome: "ok"},
		{name: "ErrAlreadyDecided", wantOutcome: "ErrAlreadyDecided"},
		{name: "ErrInvalidFlags", wantOutcome: "ErrInvalidFlags"},
		{name: "ErrRelayFallenBack", wantOutcome: "ErrRelayFallenBack"},
		{name: "ErrAmbiguousRequest", wantOutcome: "ErrAmbiguousRequest"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "ErrAmbiguousRequest" {
				t.Skip("ErrAmbiguousRequest unreachable via CLI: " +
					"API layer (pkg/api/decide.go) rejects empty request_token with " +
					"ErrMissingRequestToken before the store ambiguity guard fires. " +
					"No CLI path can supply an empty token (flag validator enforces --request-token).")
			}

			fakeDir := buildFakeTmux(t)
			home := t.TempDir()
			bootstrapDB(t, home)
			dbPath := filepath.Join(home, ".agent-director", "state.db")
			// The subprocess writes its trail to <home>/.agent-director/ad-trail.jsonl;
			// this test's isolated HOME keeps it off the real store.

			// checkpoint/rmCheckpoint track how many ad.decide.called and
			// ad.row_mutation.committed lines already exist in the shared trail
			// before the asserted decide runs. For ErrAlreadyDecided a warm-up
			// decide is emitted first, so the assertions target only the delta.
			checkpoint := 0
			rmCheckpoint := 0

			switch tc.name {
			case "ok":
				const id = "id-dc-trail-ok-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-ok-1", "check_permission", "on")
				seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)
				_, _, code := runSpawnCLI(t, home, fakeDir,
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code != 0 {
					t.Fatalf("decide exit = %d; want 0", code)
				}

			case "ErrAlreadyDecided":
				const id = "id-dc-trail-ad-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-ad-1", "check_permission", "on")
				seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)

				// First decide: succeeds and writes a ad.decide.called line to the
				// shared trail. Checkpoint that line count so the assertions below
				// target only the second decide's emission (the delta).
				_, _, code1 := runSpawnCLI(t, home, fakeDir,
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code1 != 0 {
					t.Fatalf("first decide exit = %d; want 0", code1)
				}
				afterFirst := readTrailLines(t, home)
				checkpoint = len(decideCalledLines(afterFirst))
				rmCheckpoint = len(rowMutationCommittedLines(afterFirst))

				// Second decide on the already-decided token: must return ErrAlreadyDecided.
				_, _, code2 := runSpawnCLI(t, home, fakeDir,
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code2 == 0 {
					t.Fatalf("second decide exit = 0; want non-zero (ErrAlreadyDecided)")
				}

			case "ErrRelayFallenBack":
				// Relay-on spawn with an open request whose created_at is
				// backdated far past the CLI binary's default effective window
				// (86400s) plus RelayKillSafetyMargin: the request is
				// open-but-undeliverable, so Decide refuses with
				// ErrRelayFallenBack (SR-4.4). The CLI runs on the real clock,
				// hence a 48h backdate rather than anything near the window.
				const id = "id-dc-trail-rfb-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-rfb-1", "check_permission", "on")
				seedOpenPermissionRequest(t, dbPath, id, testRequestToken, "Bash", `{"cmd":"ls"}`)
				backdatePermissionRequest(t, dbPath, id, testRequestToken, 48*time.Hour)

				_, stderr, code := runSpawnCLI(t, home, fakeDir,
					"decide",
					"--claude-instance-id", id,
					"--request-token", testRequestToken,
					"--decision", "allow",
				)
				if code == 0 {
					t.Fatalf("decide exit = 0; want non-zero (ErrRelayFallenBack)")
				}
				// (b) typed err_name must surface in the JSON error envelope.
				env := parseEnvelope(t, stderr)
				if env.ErrName != "ErrRelayFallenBack" {
					t.Errorf("err_name = %q; want ErrRelayFallenBack", env.ErrName)
				}

			case "ErrInvalidFlags":
				const id = "id-dc-trail-inv-1"
				seedSpawnRow(t, dbPath, id, "cd-dc-trail-inv-1", "check_permission", "on")
				// Omit --request-token: CLI validates it as required before calling the API,
				// so the emission comes from decideHandlerWith (outcome="ErrInvalidFlags").
				runSpawnCLI(t, home, fakeDir,
					"decide",
					"--claude-instance-id", id,
					"--decision", "allow",
					// intentionally no --request-token
				)
				// exit code is non-zero by design; ignore it here — the trail is the contract.
			}

			// ---- Trail assertions (common to all non-skipped cases) ----
			// The asserted decide.called line is the one emitted after the
			// checkpoint (the delta), so ErrAlreadyDecided's warm-up decide is
			// excluded even though it shares the trail file.

			lines := readTrailLines(t, home)
			dc := decideCalledLines(lines)
			if len(dc)-checkpoint != 1 {
				t.Fatalf("ad.decide.called delta = %d; want 1 (total %d, checkpoint %d, all trail lines: %v)", len(dc)-checkpoint, len(dc), checkpoint, lines)
			}
			row := dc[len(dc)-1]

			// outcome must match the expected value for this case.
			if row["outcome"] != tc.wantOutcome {
				t.Errorf("outcome = %v; want %q", row["outcome"], tc.wantOutcome)
			}
			// source is always "ad_decide" per SR-A-2.4.
			if row["source"] != "ad_decide" {
				t.Errorf("source = %v; want ad_decide", row["source"])
			}
			// ts must be a non-empty string.
			if ts, ok := row["ts"].(string); !ok || ts == "" {
				t.Errorf("ts = %v; want non-empty string", row["ts"])
			}
			// caller identity fields must be non-empty for all outcomes.
			if p, _ := row["caller_process"].(string); p == "" {
				t.Errorf("caller_process empty")
			}
			if h, _ := row["caller_hostname"].(string); h == "" {
				t.Errorf("caller_hostname empty")
			}
			if u, _ := row["caller_user"].(string); u == "" {
				t.Errorf("caller_user empty")
			}
			// caller_pid is the subprocess PID — assert non-zero.
			// (JSON unmarshals all numbers as float64 in map[string]any.)
			if pid, _ := row["caller_pid"].(float64); pid == 0 {
				t.Errorf("caller_pid = 0; want non-zero subprocess pid")
			}
			// required payload fields must be present (may be empty string for error paths).
			for _, field := range []string{"claude_instance_id", "request_token", "submitted_decision", "submitted_decision_reason"} {
				if _, present := row[field]; !present {
					t.Errorf("required field %q missing from ad.decide.called line", field)
				}
			}

			// ErrAlreadyDecided: the no-op UPDATE must not emit ad.row_mutation.committed.
			// Measured as a delta against the shared trail's checkpoint so the
			// warm-up decide's own row_mutation line is excluded.
			if tc.name == "ErrAlreadyDecided" {
				rm := rowMutationCommittedLines(lines)
				if got := len(rm) - rmCheckpoint; got != 0 {
					t.Errorf("ErrAlreadyDecided: expected 0 new ad.row_mutation.committed lines; got %d (total %d, checkpoint %d): %v", got, len(rm), rmCheckpoint, rm)
				}
			}

			// ErrRelayFallenBack: the refused guarded UPDATE emits no mutation
			// event either — the row's decision stays NULL, so no
			// ad.row_mutation.committed line is written. Mirrors the
			// ErrAlreadyDecided no-op assertion above. This case has no warm-up
			// decide, so rmCheckpoint is 0 and the delta is the total count.
			if tc.name == "ErrRelayFallenBack" {
				rm := rowMutationCommittedLines(lines)
				if got := len(rm) - rmCheckpoint; got != 0 {
					t.Errorf("ErrRelayFallenBack: expected 0 new ad.row_mutation.committed lines (refused guarded UPDATE emits no mutation); got %d (total %d, checkpoint %d): %v", got, len(rm), rmCheckpoint, rm)
				}
			}
		})
	}
}
