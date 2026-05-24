package main

import (
	"crypto/rand"
	"encoding/hex"
	"sync"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// handleRegistry is an internal thread-safe map from opaque handle strings to
// live *pkgapi.Client instances. Keeping clients in the map prevents Go's GC
// from collecting a Client that is still in use by a foreign caller.
//
// The empty string is permanently reserved as the never-valid handle sentinel;
// mint never returns it and lookup/delete reject it without touching the map.
type handleRegistry struct {
	m sync.Map
}

// mint stores client in the registry under a freshly generated, unguessable
// handle and returns the handle string. Handles are 16 bytes of
// crypto/rand-sourced entropy, hex-encoded to a 32-character ASCII string.
//
// On the vanishingly unlikely event that crypto/rand fails, mint panics so
// that the surrounding exported function's recover() site converts the
// failure to an ErrInternal envelope before it crosses the C boundary.
func (r *handleRegistry) mint(client *pkgapi.Client) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is unrecoverable in normal operation.
		// Panic here; the exported-function recover() site catches it.
		panic("cabi: crypto/rand failed: " + err.Error())
	}
	handle := hex.EncodeToString(b)
	r.m.Store(handle, client)
	return handle
}

// lookup returns the *pkgapi.Client registered under handle along with a
// boolean found flag. Returns (nil, false) for the empty string or any
// unregistered handle.
func (r *handleRegistry) lookup(handle string) (*pkgapi.Client, bool) {
	if handle == "" {
		return nil, false
	}
	v, ok := r.m.Load(handle)
	if !ok {
		return nil, false
	}
	return v.(*pkgapi.Client), true
}

// delete atomically removes handle from the registry and returns the
// previously registered *pkgapi.Client together with a boolean indicating
// whether the handle was present. Returns (nil, false) for the empty string
// or any unknown handle. The caller is responsible for invoking Close() on
// the returned Client.
func (r *handleRegistry) delete(handle string) (*pkgapi.Client, bool) {
	if handle == "" {
		return nil, false
	}
	v, ok := r.m.LoadAndDelete(handle)
	if !ok {
		return nil, false
	}
	return v.(*pkgapi.Client), true
}

// registry is the package-level singleton used by all C exports to track
// live Client instances across ad_open / verb / ad_close call sequences.
var registry = &handleRegistry{m: sync.Map{}}
