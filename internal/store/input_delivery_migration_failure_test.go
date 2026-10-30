package store_test

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/internal/store"
	"github.com/gabemahoney/agent-director/internal/testsupport/storefix"
)

func TestInputJournalMigrationAtomicMarkerFailure(t *testing.T) {
	f := store.NewJournalMigrationFailureFixture(t)
	remove := storefix.InjectWriteFailure(t, f.Path, storefix.WriteFailInputAuthority, "native_input_authority")
	before := f.Dump()
	legacy := map[string][]byte{}
	entries, err := os.ReadDir(f.LegacyDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		b, err := os.ReadFile(filepath.Join(f.LegacyDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		legacy[entry.Name()] = b
	}
	if r, err := f.Migrate(); err == nil || !strings.Contains(err.Error(), "injected write failure: input authority") || r.Authority != "" {
		t.Fatalf("failed marker published authority or missed injected boundary: %+v %v", r, err)
	}
	if !reflect.DeepEqual(before, f.Dump()) {
		t.Fatal("marker failure retained DDL/import/current/history/version")
	}
	for name, want := range legacy {
		if got, err := os.ReadFile(filepath.Join(f.LegacyDir, name)); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("rollback retired or changed legacy authority %s: %v", name, err)
		}
	}
	remove()
	if r, err := f.Migrate(); err != nil || r.Authority != "sqlite" {
		t.Fatalf("marker failure retry: %+v %v", r, err)
	}
}
