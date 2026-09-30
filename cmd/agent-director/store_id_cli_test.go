package main_test

import (
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/pkg/api/apitest"
)

// TestStoreOpeningVerbOnStoreWithoutValidStoreID drives `list` on a v5 store
// whose store_id row was removed or hand-edited: the open fails with the
// ErrSchemaMismatch envelope, which never carries the id (SR-5.1, SR-15).
func TestStoreOpeningVerbOnStoreWithoutValidStoreID(t *testing.T) {
	cases := []struct {
		name   string
		value  string // planted value when remove is false
		remove bool
	}{
		{name: "row removed", remove: true},
		{name: "not hex", value: "SECRETVALUEXXXXX"},
		{name: "uppercase", value: "ABCDEF0123456789"},
		{name: "15 chars", value: "abcdef012345678"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			if stdout, stderr, code := runCLIWithHome(t, home, "list"); code != 0 {
				t.Fatalf("bootstrap `list` exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			origID, err := apitest.ReadStoreID(stateDB(home))
			if err != nil {
				t.Fatalf("ReadStoreID: %v", err)
			}
			tamperStoreID(t, home, tc.value, tc.remove)

			stdout, stderr, code := runCLIWithHome(t, home, "list")
			if code == 0 {
				t.Fatalf("exit=0 want non-zero; stdout=%q stderr=%q", stdout, stderr)
			}
			if stdout != "" {
				t.Errorf("stdout=%q want empty on error", stdout)
			}
			if env := parseEnvelope(t, stderr); env.ErrName != "ErrSchemaMismatch" {
				t.Errorf("err_name=%q want %q", env.ErrName, "ErrSchemaMismatch")
			}
			for _, secret := range []string{origID, tc.value} {
				if secret != "" && strings.Contains(stderr, secret) {
					t.Errorf("stderr %q contains store id value %q", stderr, secret)
				}
			}
		})
	}
}

// TestStoreOpeningVerbOnHealthyStoreKeepsStoreID checks `list` succeeds on a
// store with a valid store_id and leaves the id unchanged.
func TestStoreOpeningVerbOnHealthyStoreKeepsStoreID(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if stdout, stderr, code := runCLIWithHome(t, home, "list"); code != 0 {
		t.Fatalf("bootstrap `list` exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	before, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID: %v", err)
	}

	stdout, stderr, code := runCLIWithHome(t, home, "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if stdout == "" {
		t.Errorf("stdout empty; want list JSON")
	}
	after, err := apitest.ReadStoreID(stateDB(home))
	if err != nil {
		t.Fatalf("ReadStoreID after list: %v", err)
	}
	if after != before {
		t.Errorf("store id changed across `list`: %q -> %q", before, after)
	}
}
