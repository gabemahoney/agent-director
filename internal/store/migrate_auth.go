package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/gabemahoney/agent-director/internal/trail"
)

// sentinelFilename is the fixed name of the administrator authorization
// sentinel. It lives in the SAME directory as the DB file it authorizes (a
// sibling of the resolved DB path), so it authorizes the right DB even under a
// custom store path. This name is an internal, admin/install-only detail — it
// must never appear in any agent-facing surface (help text, MCP tool
// descriptions, npm README/manifest, or the ErrSchemaMigrationRequired message).
const sentinelFilename = "migrate-authorized"

// migrationAuthorization is the on-disk sentinel payload: exactly one from→to
// transition. It authorizes migrating a DB currently at user_version==From up
// to schema version To, and nothing wider.
type migrationAuthorization struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// sentinelPath derives the authorization sentinel path as a sibling of the
// resolved DB path. Never hard-coded to ~/.agent-director so it works under
// custom store paths (SR-2.2).
func sentinelPath(dbPath string) string {
	return filepath.Join(filepath.Dir(dbPath), sentinelFilename)
}

// authorizeMigration gates an older-than-binary open. current is the DB's
// actual user_version. It resolves the sibling sentinel and dispatches:
//
//   - Missing sentinel → plain ErrSchemaMigrationRequired refusal, NO
//     expected-vs-found trail line. The DB is not touched.
//   - Present, valid JSON, exact-matches BOTH ends (From==current and
//     To==schemaVersion) → returns nil (migration authorized). The sentinel is
//     consumed only after the migration commits (see consumeAuthorization).
//   - Malformed JSON, or a mismatch on either end → the same refusal PLUS a
//     distinct trail line reporting expected-vs-found for both ends. The
//     sentinel is left in place as admin evidence and never partially honored.
//
// authorizeMigration never writes to the DB, so a refused open leaves the DB
// file byte-identical. Trail emission uses the store's fail-open `_ =`
// discipline and does not touch state.db.
func authorizeMigration(dbPath string, current int) error {
	path := sentinelPath(dbPath)

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Missing sentinel: plain dead-end refusal, no expected-vs-found
			// line (that line is reserved for malformed/mismatched sentinels).
			return buildMigrationRefusal(current)
		}
		// Any other read error (permissions, etc.) is a refusal as well; the
		// admin evidence, if any, is left untouched. Report it like a
		// malformed sentinel so operators see the discrepancy, carrying the
		// underlying read error as the diagnostic.
		emitMismatch(current, schemaVersion, -1, -1, "sentinel_unreadable", err)
		return buildMigrationRefusal(current)
	}

	auth, perr := parseAuthorization(data)
	if perr != nil {
		// Malformed JSON: refuse and emit expected-vs-found (found ends
		// unknown → reported as the raw parse failure carried in the trail).
		// Sentinel NOT deleted.
		emitMismatch(current, schemaVersion, -1, -1, "malformed_json", perr)
		return buildMigrationRefusal(current)
	}

	if auth.From != current || auth.To != schemaVersion {
		// Mismatch on either end: refuse, emit expected-vs-found for BOTH
		// ends, leave the sentinel in place as admin evidence.
		emitMismatch(current, schemaVersion, auth.From, auth.To, "version_mismatch", nil)
		return buildMigrationRefusal(current)
	}

	// Exact match on both ends: authorized. Consumption is deferred until the
	// migration commits so a mid-migration failure preserves the sentinel.
	return nil
}

// parseAuthorization strictly decodes the sentinel payload. Unknown fields and
// trailing garbage are rejected so a sentinel can never widen authorization or
// be honored ambiguously.
func parseAuthorization(data []byte) (migrationAuthorization, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var auth migrationAuthorization
	if err := dec.Decode(&auth); err != nil {
		return migrationAuthorization{}, err
	}
	// Reject any trailing tokens after the single JSON object.
	if dec.More() {
		return migrationAuthorization{}, errors.New("trailing data after authorization object")
	}
	return auth, nil
}

// consumeAuthorization deletes the sentinel after a migration commits and emits
// the audit trail line. Ordering is: migration already committed → delete
// sentinel → on delete failure emit a loud trail event but do NOT fail the
// open. A stale sentinel is provably inert: the next open short-circuits on the
// version match, and a future version bump mismatches `from` → refusal that
// preserves it as admin evidence.
func consumeAuthorization(dbPath string, from, to int) {
	path := sentinelPath(dbPath)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		// Loud, but fail-open: the migration is done and correct.
		_ = trail.Emit(context.Background(), "ad.schema.authorization_delete_failed", map[string]any{
			"from":   from,
			"to":     to,
			"error":  err.Error(),
			"source": "ad_store_schema",
		})
		return
	}
	// migrated <from>→<to>, consumed authorization
	_ = trail.Emit(context.Background(), "ad.schema.migrated", map[string]any{
		"from":    from,
		"to":      to,
		"message": migrationAuditMessage(from, to),
		"source":  "ad_store_schema",
	})
}

// migrationAuditMessage renders the required audit phrasing:
// `migrated <from>→<to>, consumed authorization`.
func migrationAuditMessage(from, to int) string {
	return fmt.Sprintf("migrated %d→%d, consumed authorization", from, to)
}

// emitMismatch emits the distinct expected-vs-found trail line for a
// malformed/mismatched sentinel. It reports expected and found for BOTH ends;
// foundFrom/foundTo of -1 signal "unknown" (malformed/unreadable sentinel). A
// non-nil diag carries the underlying read/parse failure text into the trail
// event so admin evidence includes the actual diagnostic (the sentinel path is
// never emitted). It never deletes the sentinel and never touches the DB.
func emitMismatch(expectedFrom, expectedTo, foundFrom, foundTo int, reason string, diag error) {
	fields := map[string]any{
		"reason":        reason,
		"expected_from": expectedFrom,
		"expected_to":   expectedTo,
		"found_from":    foundFrom,
		"found_to":      foundTo,
		"source":        "ad_store_schema",
	}
	if diag != nil {
		fields["error"] = diag.Error()
	}
	_ = trail.Emit(context.Background(), "ad.schema.authorization_mismatch", fields)
}
