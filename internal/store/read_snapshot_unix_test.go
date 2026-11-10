//go:build linux || darwin

package store

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The child owns real SQLite locks in another process; no lock protocol is
// reproduced here. EOF commits held transactions, or crashes without closing
// SQLite after a WAL commit or an uncommitted rollback-journal cache spill.
func TestReadSnapshotSQLiteProcess(t *testing.T) {
	path := os.Getenv("AD_SNAPSHOT_TEST_DB")
	if path == "" {
		return
	}
	db := openRaw(t, path)
	db.SetMaxOpenConns(1)
	mustExec(t, db, "PRAGMA wal_autocheckpoint=0")
	mode := os.Getenv("AD_SNAPSHOT_TEST_MODE")
	if mode == "hot-journal" {
		mustExec(t, db, "PRAGMA synchronous=FULL; PRAGMA cache_size=2; PRAGMA cache_spill=ON")
	}
	if mode != "wal-crash" {
		mustExec(t, db, "BEGIN IMMEDIATE")
	}
	mustExec(t, db, "UPDATE evidence SET value=1")
	if mode == "hot-journal" {
		mustExec(t, db, "UPDATE pages SET payload=randomblob(4096)")
	}
	fmt.Println("sqlite-ready")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
	if mode != "hold" {
		os.Exit(77) // No close, checkpoint, or rollback.
	}
	mustExec(t, db, "COMMIT")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func snapshotSQLiteWriter(t *testing.T, path, mode string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReadSnapshotSQLiteProcess$", "-test.count=1")
	cmd.Env = append(os.Environ(), "AD_SNAPSHOT_TEST_DB="+path, "AD_SNAPSHOT_TEST_MODE="+mode)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "sqlite-ready\n" {
		t.Fatalf("SQLite writer readiness = %q, %v", line, err)
	}
	return func() {
		t.Helper()
		if err := stdin.Close(); err != nil {
			t.Fatal(err)
		}
		err := cmd.Wait()
		waited = true
		want := 0
		if mode != "hold" {
			want = 77
		}
		if cmd.ProcessState.ExitCode() != want {
			t.Fatalf("SQLite writer exit = %v; want %d: %s", err, want, stderr.String())
		}
	}
}

func snapshotFixture(t *testing.T, mode string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	db := openRaw(t, path)
	mustExec(t, db, "PRAGMA journal_mode="+mode)
	mustExec(t, db, "CREATE TABLE evidence(value INTEGER); INSERT INTO evidence VALUES(0)")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Capture physical evidence without opening SQLite, including every sidecar
// and directory mtime. Snapshot destinations live outside this source directory.
func snapshotPhysicalFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := fmt.Sprintf("%s %d %d", info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += fmt.Sprintf(" %x", sha256.Sum256(data))
		}
		result[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func snapshotValue(t *testing.T, path string, want int) {
	t.Helper()
	withRaw(t, path, func(db *sql.DB) {
		var got int
		if err := db.QueryRow("SELECT value FROM evidence").Scan(&got); err != nil || got != want {
			t.Fatalf("snapshot value = %d, %v; want %d", got, err, want)
		}
	})
}

func snapshotDirectory(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "snapshot")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestReadSnapshotCommittedWALLeavesOriginalUntouched(t *testing.T) {
	path := snapshotFixture(t, "WAL")
	finish := snapshotSQLiteWriter(t, path, "wal-crash")
	finish()
	mainBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mainOnly := filepath.Join(t.TempDir(), "main.db")
	if err := os.WriteFile(mainOnly, mainBytes, 0600); err != nil {
		t.Fatal(err)
	}
	snapshotValue(t, mainOnly, 0) // Prove the committed value exists only in WAL.
	for _, missingSHM := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing_SHM_%t", missingSHM), func(t *testing.T) {
			if missingSHM {
				if err := os.Remove(path + "-shm"); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotPhysicalFiles(t, filepath.Dir(path))
			copy, err := ReadSnapshot(context.Background(), path, snapshotDirectory(t), 1000)
			if err != nil {
				t.Fatal(err)
			}
			snapshotValue(t, copy, 1)
			if after := snapshotPhysicalFiles(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
				t.Fatalf("source mutated:\nbefore %v\nafter %v", before, after)
			}
		})
	}
}

func TestReadSnapshotBusyWriterRefusesThenRecovers(t *testing.T) {
	for _, mode := range []string{"WAL", "DELETE"} {
		t.Run(mode, func(t *testing.T) {
			path := snapshotFixture(t, mode)
			finish := snapshotSQLiteWriter(t, path, "hold")
			before := snapshotPhysicalFiles(t, filepath.Dir(path))
			directory := snapshotDirectory(t)
			copy, err := ReadSnapshot(context.Background(), path, directory, 50)
			if copy != "" || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("busy snapshot = %q, %v; want no path and deadline refusal", copy, err)
			}
			if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
				t.Fatalf("refused snapshot left files: %v, %v", entries, err)
			}
			if after := snapshotPhysicalFiles(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
				t.Fatalf("busy source mutated:\nbefore %v\nafter %v", before, after)
			}
			finish()
			copy, err = ReadSnapshot(context.Background(), path, directory, 1000)
			if err != nil {
				t.Fatal(err)
			}
			snapshotValue(t, copy, 1)
		})
	}
}

func TestReadSnapshotHotRollbackJournalRefusesWithoutMutation(t *testing.T) {
	path := snapshotFixture(t, "DELETE")
	withRaw(t, path, func(db *sql.DB) {
		mustExec(t, db, `CREATE TABLE pages(payload BLOB);
			WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<64)
			INSERT INTO pages SELECT zeroblob(4096) FROM n`)
	})
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	finish := snapshotSQLiteWriter(t, path, "hot-journal")
	finish() // Reaped process: no writer remains and no rollback ran.
	spilled, err := os.ReadFile(path)
	if err != nil || bytes.Equal(committed, spilled) {
		t.Fatalf("uncommitted writes did not spill to the source database: %v", err)
	}
	journal, err := os.ReadFile(path + "-journal")
	if err != nil || len(journal) <= 512 || !bytes.Equal(journal[:8], []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}) {
		t.Fatalf("SQLite did not leave a genuine hot rollback journal: length=%d, error=%v", len(journal), err)
	}
	// No SQLite connection has opened the original since the child crashed.
	before := snapshotPhysicalFiles(t, filepath.Dir(path))
	directory := snapshotDirectory(t)
	copy, err := ReadSnapshot(context.Background(), path, directory, 1000)
	if copy != "" || err == nil || err.Error() != "readonly SQLite snapshot refuses a rollback journal requiring recovery" {
		t.Fatalf("hot journal snapshot = %q, %v; want conservative recovery refusal", copy, err)
	}
	if entries, err := os.ReadDir(directory); err != nil || len(entries) != 0 {
		t.Fatalf("refused snapshot left files: %v, %v", entries, err)
	}
	if after := snapshotPhysicalFiles(t, filepath.Dir(path)); !reflect.DeepEqual(before, after) {
		t.Fatalf("hot-journal source mutated:\nbefore %v\nafter %v", before, after)
	}
	// A real SQLite open now recovers the abandoned transaction. It restores
	// the committed value and pages; the same snapshot destination then works.
	snapshotValue(t, path, 0)
	withRaw(t, path, func(db *sql.DB) {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM pages WHERE payload=zeroblob(4096)").Scan(&count); err != nil || count != 64 {
			t.Fatalf("recovered pages = %d, %v; want all 64 committed pages", count, err)
		}
	})
	copy, err = ReadSnapshot(context.Background(), path, directory, 1000)
	if err != nil {
		t.Fatal(err)
	}
	snapshotValue(t, copy, 0)
}
