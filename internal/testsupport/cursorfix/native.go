package cursorfix

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabemahoney/agent-director/internal/testsupport/cursorfix/nativecheckpoint"

	_ "modernc.org/sqlite"
)

// NativeHistory is source-derived synthetic ACP SQLite, never a captured or authenticated session.
type NativeHistory struct {
	ConfigRoot    string
	Latest, Older NativeConversation
}

type NativeConversation struct {
	ID, Database, Metadata       string
	DatabaseBytes, MetadataBytes []byte
}

// NewNativeHistory creates independent persisted checkpoints using pinned native DDL and serialization.
func NewNativeHistory(t testing.TB, cwd, latest, older string) NativeHistory {
	t.Helper()
	h := NativeHistory{ConfigRoot: filepath.Join(t.TempDir(), "cursor-config")}
	h.Latest = h.write(t, cwd, latest)
	h.Older = h.write(t, cwd, older)
	return h
}

func (h NativeHistory) write(t testing.TB, cwd, id string) NativeConversation {
	t.Helper()
	if !filepath.IsAbs(cwd) || id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		t.Fatal("native fixture requires absolute cwd and single-component session ID")
	}
	dir := filepath.Join(h.ConfigRoot, "acp-sessions", id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	c := NativeConversation{ID: id, Database: filepath.Join(dir, "store.db"), Metadata: filepath.Join(dir, "meta.json")}
	db, err := sql.Open("sqlite", c.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	seed, metadata, err := nativecheckpoint.Encode(cwd, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(seed); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	c.MetadataBytes = metadata
	if err = os.WriteFile(c.Metadata, c.MetadataBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(c.Database, 0600); err != nil {
		t.Fatal(err)
	}
	c.DatabaseBytes, err = os.ReadFile(c.Database)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// RemoveDatabase leaves the sidecar and every other conversation intact.
func (c NativeConversation) RemoveDatabase(t testing.TB) {
	t.Helper()
	if err := os.Remove(c.Database); err != nil {
		t.Fatal(err)
	}
}
