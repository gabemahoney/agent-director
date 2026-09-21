package rebootrecovery_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	_ "modernc.org/sqlite"
)

// readinessQuery selects the four columns a store-readiness poll cares about
// (state, identity pid+starttime, jsonl_path) for one instance. Shared by
// rowReady (the poll CONDITION) and observeRow (the timeout DIAGNOSTIC) so the
// dump can never silently diverge from the condition it explains (b.129).
const readinessQuery = `SELECT state, pid, proc_starttime, jsonl_path FROM spawns WHERE claude_instance_id = ?`

// rowReady reports whether the SessionStart hook has run for instanceID: state
// has left `pending` (→ waiting) AND pid, proc_starttime, and jsonl_path are all
// non-NULL. That is exactly the identity find-missing's checker needs and the
// transcript path resume's pre-flight stats.
func rowReady(t *testing.T, dbPath, instanceID string) bool {
	t.Helper()
	db := openDB(t, dbPath)
	defer db.Close()
	var state string
	var pid sql.NullInt64
	var starttime, jsonlPath sql.NullString
	err := db.QueryRow(readinessQuery, instanceID).Scan(&state, &pid, &starttime, &jsonlPath)
	if err != nil {
		if err == sql.ErrNoRows {
			return false
		}
		t.Fatalf("rowReady query: %v", err)
	}
	return state != "pending" && pid.Valid && starttime.Valid && jsonlPath.Valid
}

// observeRow renders the row fields a store poll cares about (state, identity
// pid+starttime, jsonl_path) as a single diagnostic line, for inclusion in a
// waitFor timeout message (b.129). It never fails the test: a poll that is
// already timing out must not be masked by a second failure, so query/row
// errors are folded into the returned string. NULL columns render as "NULL",
// which is itself the signal (SessionStart hasn't populated them yet).
func observeRow(dbPath, instanceID string) func() string {
	return func() string {
		db, err := sql.Open("sqlite", dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
		if err != nil {
			return "row " + instanceID + ": open db failed: " + err.Error()
		}
		defer db.Close()
		var state string
		var pid sql.NullInt64
		var starttime, jsonlPath sql.NullString
		err = db.QueryRow(readinessQuery, instanceID).Scan(&state, &pid, &starttime, &jsonlPath)
		if err == sql.ErrNoRows {
			return "row " + instanceID + ": no row (SessionStart not yet recorded)"
		}
		if err != nil {
			return "row " + instanceID + ": query failed: " + err.Error()
		}
		return "row " + instanceID +
			": state=" + state +
			" pid=" + nullInt(pid) +
			" proc_starttime=" + nullStr(starttime) +
			" jsonl_path=" + nullStr(jsonlPath)
	}
}

// nullStr renders a nullable text column: "NULL" or the quoted value.
func nullStr(s sql.NullString) string {
	if !s.Valid {
		return "NULL"
	}
	return strconv.Quote(s.String)
}

// nullInt renders a nullable integer column: "NULL" or the decimal value.
func nullInt(n sql.NullInt64) string {
	if !n.Valid {
		return "NULL"
	}
	return strconv.FormatInt(n.Int64, 10)
}

// getState returns the current state string for a row.
func getState(t *testing.T, dbPath, instanceID string) string {
	t.Helper()
	db := openDB(t, dbPath)
	defer db.Close()
	var state string
	if err := db.QueryRow(
		`SELECT state FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&state); err != nil {
		t.Fatalf("getState query: %v", err)
	}
	return state
}

// openDB opens the sqlite store for assertions. A CLI subprocess may be
// mid-write (WAL) when a poll fires, so we open read-only with a busy timeout so
// a concurrent writer yields SQLITE_BUSY-free reads instead of failing the poll.
// The connection is single-use per poll and closed by the caller.
func openDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	// _pragma=busy_timeout(5000): wait up to 5s for a writer's lock to clear.
	// mode=ro: assertions never mutate the store.
	db, err := sql.Open("sqlite", dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db
}

// readEnviron parses /proc/<pid>/environ into a KEY→VAL map. Returns ok=false
// when the environ is unreadable (process gone, or foreign-uid EACCES).
func readEnviron(pid int) (map[string]string, bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return nil, false
	}
	out := map[string]string{}
	for _, kv := range bytes.Split(data, []byte{0}) {
		if len(kv) == 0 {
			continue
		}
		if i := bytes.IndexByte(kv, '='); i >= 0 {
			out[string(kv[:i])] = string(kv[i+1:])
		}
	}
	return out, true
}

// parseFindMissing unmarshals the find-missing JSON envelope.
func parseFindMissing(t *testing.T, stdout string) findMissingResult {
	t.Helper()
	var fm findMissingResult
	if err := json.Unmarshal([]byte(stdout), &fm); err != nil {
		t.Fatalf("parse find-missing %q: %v", stdout, err)
	}
	return fm
}

// containsID reports whether ids contains want.
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// itoa renders an int64 pid as a decimal string for /proc path composition.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// nullOutJsonlPath sets jsonl_path = NULL on a row, so resume's pre-flight can
// no longer take the persisted-path branch and MUST exercise the b.1ba
// CONFIG_DIR-aware fallback (AC6: the persisted-path branch must NOT be what's
// exercised). Opens read-write (a distinct connection from openDB's mode=ro).
func nullOutJsonlPath(t *testing.T, dbPath, instanceID string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("open db rw: %v", err)
	}
	defer db.Close()
	res, err := db.Exec(`UPDATE spawns SET jsonl_path = NULL WHERE claude_instance_id = ?`, instanceID)
	if err != nil {
		t.Fatalf("null out jsonl_path: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("null out jsonl_path: rows affected = %d; want 1", n)
	}
}

// jsonlPathColumn returns the persisted jsonl_path (or "" when NULL). Used to
// prove the fallback re-persists the correct path on a successful resume.
func jsonlPathColumn(t *testing.T, dbPath, instanceID string) string {
	t.Helper()
	db := openDB(t, dbPath)
	defer db.Close()
	var jp sql.NullString
	if err := db.QueryRow(
		`SELECT jsonl_path FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&jp); err != nil {
		t.Fatalf("jsonlPathColumn query: %v", err)
	}
	if !jp.Valid {
		return ""
	}
	return jp.String
}

// recordedPID returns the pid persisted on the row (the identity SessionStart
// captured). Fails the test if NULL/zero.
func recordedPID(t *testing.T, dbPath, instanceID string) int64 {
	t.Helper()
	db := openDB(t, dbPath)
	defer db.Close()
	var pid sql.NullInt64
	if err := db.QueryRow(`SELECT pid FROM spawns WHERE claude_instance_id = ?`, instanceID).Scan(&pid); err != nil {
		t.Fatalf("recordedPID query: %v", err)
	}
	if !pid.Valid || pid.Int64 == 0 {
		t.Fatalf("recordedPID: row %s has no recorded pid", instanceID)
	}
	return pid.Int64
}
