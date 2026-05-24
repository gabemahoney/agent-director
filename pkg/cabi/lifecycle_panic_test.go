//go:build cabi_panic_inject

package main

// TestAdOpenPanicRecovers verifies that a deliberate panic injected via the
// _inject_panic seam (compiled only in cabi_panic_inject builds) is caught by
// the recover() site in adOpenStr and converted to an ErrInternal envelope.
// The process must not crash.
//
// The same recover() closure is shared by the production ad_open C-export,
// so exercising it here validates the production panic-recovery path.
//
// Run with: go test -tags cabi_panic_inject -race ./pkg/cabi/...

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestAdOpenPanicRecovers(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "state.db")
	params := fmt.Sprintf(
		`{"store_path":%q,"create_if_missing":true,"_inject_panic":true}`,
		storePath,
	)

	m := unmarshalObj(t, []byte(adOpenStr(params)))
	if got := m["err_name"]; got != "ErrInternal" {
		t.Fatalf("err_name = %v; want \"ErrInternal\" — panic should have been recovered", got)
	}
}
