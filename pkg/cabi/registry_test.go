package main

import (
	"fmt"
	"regexp"
	"sync"
	"testing"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// newReg returns a fresh handleRegistry for isolated testing.
func newReg() *handleRegistry {
	return &handleRegistry{}
}

// nilClient is a typed nil *pkgapi.Client used as a lightweight stand-in.
// The registry stores and returns the pointer verbatim; no Client methods are
// called, so a nil is safe for all registry-level tests.
var nilClient = (*pkgapi.Client)(nil)

// hexHandle matches the 32-character lower-hex handle format produced by mint.
var hexHandle = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestMintLookup verifies that a minted handle is immediately retrievable.
func TestMintLookup(t *testing.T) {
	r := newReg()
	handle := r.mint(nilClient)
	got, found := r.lookup(handle)
	if !found {
		t.Fatalf("lookup(%q): found=false, want true", handle)
	}
	if got != nilClient {
		t.Fatalf("lookup(%q): got %v, want %v", handle, got, nilClient)
	}
}

// TestLookupUnknown verifies that looking up a never-registered handle returns
// (nil, false).
func TestLookupUnknown(t *testing.T) {
	r := newReg()
	got, found := r.lookup("deadbeefdeadbeefdeadbeefdeadbeef")
	if found {
		t.Fatalf("lookup on unknown handle: found=true, want false")
	}
	if got != nil {
		t.Fatalf("lookup on unknown handle: got %v, want nil", got)
	}
}

// TestDelete verifies that after deletion the handle is no longer present.
func TestDelete(t *testing.T) {
	r := newReg()
	handle := r.mint(nilClient)

	deleted, ok := r.delete(handle)
	if !ok {
		t.Fatalf("delete(%q): found=false, want true", handle)
	}
	if deleted != nilClient {
		t.Fatalf("delete(%q): got %v, want %v", handle, deleted, nilClient)
	}

	got, found := r.lookup(handle)
	if found {
		t.Fatalf("lookup after delete(%q): found=true, want false", handle)
	}
	if got != nil {
		t.Fatalf("lookup after delete(%q): got %v, want nil", handle, got)
	}
}

// TestDeleteUnknown verifies that deleting a never-registered handle is a
// no-op: returns (nil, false) and does not panic.
func TestDeleteUnknown(t *testing.T) {
	r := newReg()
	got, found := r.delete("deadbeefdeadbeefdeadbeefdeadbeef")
	if found {
		t.Fatalf("delete on unknown handle: found=true, want false")
	}
	if got != nil {
		t.Fatalf("delete on unknown handle: got %v, want nil", got)
	}
}

// TestConcurrentMint spawns 100 goroutines that each call mint 100 times,
// producing 10 000 registrations total. It asserts that all returned handles
// are distinct and that every handle remains lookupable. Must pass under -race.
func TestConcurrentMint(t *testing.T) {
	const goroutines = 100
	const mintsPerGoroutine = 100

	r := newReg()
	var mu sync.Mutex
	allHandles := make([]string, 0, goroutines*mintsPerGoroutine)

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			local := make([]string, mintsPerGoroutine)
			for j := 0; j < mintsPerGoroutine; j++ {
				local[j] = r.mint(nilClient)
			}
			mu.Lock()
			allHandles = append(allHandles, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if got := len(allHandles); got != goroutines*mintsPerGoroutine {
		t.Fatalf("expected %d handles, got %d", goroutines*mintsPerGoroutine, got)
	}

	seen := make(map[string]struct{}, len(allHandles))
	for _, h := range allHandles {
		if _, dup := seen[h]; dup {
			t.Fatalf("duplicate handle: %q", h)
		}
		seen[h] = struct{}{}
	}

	for _, h := range allHandles {
		if _, found := r.lookup(h); !found {
			t.Fatalf("lookup(%q): not found after concurrent mint", h)
		}
	}
}

// TestMintsDiffer verifies that consecutive mints produce different handles.
func TestMintsDiffer(t *testing.T) {
	r := newReg()
	seen := make(map[string]struct{})
	for i := 0; i < 20; i++ {
		h := r.mint(nilClient)
		if _, dup := seen[h]; dup {
			t.Fatalf("mint produced duplicate handle %q on iteration %d", h, i)
		}
		seen[h] = struct{}{}
	}
}

// TestHandleFormat verifies that minted handles are exactly 32 lower-hex chars.
func TestHandleFormat(t *testing.T) {
	r := newReg()
	for i := 0; i < 10; i++ {
		h := r.mint(nilClient)
		if len(h) != 32 {
			t.Errorf("mint() len=%d, want 32 (handle %q)", len(h), h)
		}
		if !hexHandle.MatchString(h) {
			t.Errorf("mint() = %q, does not match [0-9a-f]{32}", h)
		}
	}
}

// TestEmptyHandleNeverMinted verifies that mint never returns the empty string.
func TestEmptyHandleNeverMinted(t *testing.T) {
	r := newReg()
	for i := 0; i < 1000; i++ {
		if h := r.mint(nilClient); h == "" {
			t.Fatalf("mint() returned empty string on iteration %d", i)
		}
	}
}

// TestLookupEmptyString verifies that the empty string is permanently reserved
// as a never-valid sentinel: lookup("") returns (nil, false).
func TestLookupEmptyString(t *testing.T) {
	r := newReg()
	got, found := r.lookup("")
	if found {
		t.Fatalf("lookup(%q): found=true, want false", "")
	}
	if got != nil {
		t.Fatalf("lookup(%q): got %v, want nil", "", got)
	}
}

// TestLookupMissVariants exercises lookup-miss across several never-registered
// inputs using a table to collapse the shared shape.
func TestLookupMissVariants(t *testing.T) {
	r := newReg()
	// mint something so the map is non-empty, ensuring misses are real misses.
	r.mint(nilClient)

	cases := []struct {
		name   string
		handle string
	}{
		{"empty", ""},
		{"all-zeros", fmt.Sprintf("%032d", 0)},
		{"garbage", "not-a-valid-handle"},
		{"short", "abcd"},
		{"long", "aabbccddeeff00112233445566778899aabb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, found := r.lookup(tc.handle)
			if found {
				t.Errorf("lookup(%q): found=true, want false", tc.handle)
			}
			if got != nil {
				t.Errorf("lookup(%q): got %v, want nil", tc.handle, got)
			}
		})
	}
}
