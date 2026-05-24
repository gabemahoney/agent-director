package main

// dispatch_test.go covers the shared dispatch helpers: isHandleFree,
// resolveHandle, contextFromParams, and runVerb.
//
// NOTE: import "C" is NOT used here. Go prohibits cgo in test files of
// packages with //export directives. All helpers under test are pure Go,
// so no cgo boundary is exercised or needed.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
	"github.com/gabemahoney/agent-director/pkg/api/errnames"
	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

// ─── isHandleFree ─────────────────────────────────────────────────────────────

// TestIsHandleFree iterates every callable manifest verb, derives the
// corresponding C-ABI name ("ad_" + name with dashes replaced by underscores),
// and verifies that isHandleFree matches the manifest's HandleFree field.
// Only ad_version should return true; all other callable verbs require a handle.
func TestIsHandleFree(t *testing.T) {
	for _, v := range manifest.CallableVerbs() {
		cabiName := "ad_" + strings.ReplaceAll(v.Name, "-", "_")
		got := isHandleFree(cabiName)
		if got != v.HandleFree {
			t.Errorf("isHandleFree(%q) = %v; manifest.HandleFree = %v", cabiName, got, v.HandleFree)
		}
	}
}

// ─── resolveHandle ────────────────────────────────────────────────────────────

// errUnknownHandleName is the catalog name for ErrUnknownHandle, resolved once
// via errnames.Classify so no string literal appears in assertions.
var errUnknownHandleName, _ = errnames.Classify(errnames.ErrUnknownHandle)

// assertResolveHandleErr verifies that env is a JSON error envelope whose
// err_name matches the catalog name for ErrUnknownHandle.
func assertResolveHandleErr(t *testing.T, env []byte) {
	t.Helper()
	m := unmarshalObj(t, env)
	if got := m["err_name"]; got != errUnknownHandleName {
		t.Errorf("err_name = %v; want %q", got, errUnknownHandleName)
	}
}

// TestResolveHandleMissing verifies that params without a "handle" field returns
// ok=false and an ErrUnknownHandle envelope.
func TestResolveHandleMissing(t *testing.T) {
	_, env, ok := resolveHandle([]byte(`{"foo":"bar"}`))
	if ok {
		t.Fatal("resolveHandle: ok=true; want false when handle field is absent")
	}
	assertResolveHandleErr(t, env)
}

// TestResolveHandleEmpty verifies that params with "handle":"" returns ok=false
// and an ErrUnknownHandle envelope. The empty string is the reserved
// never-valid sentinel.
func TestResolveHandleEmpty(t *testing.T) {
	_, env, ok := resolveHandle([]byte(`{"handle":""}`))
	if ok {
		t.Fatal("resolveHandle: ok=true; want false for empty handle")
	}
	assertResolveHandleErr(t, env)
}

// TestResolveHandleUnknown verifies that a well-formed 32-hex handle that is not
// in the registry returns ok=false and an ErrUnknownHandle envelope.
func TestResolveHandleUnknown(t *testing.T) {
	_, env, ok := resolveHandle([]byte(`{"handle":"deadbeefdeadbeefdeadbeefdeadbeef"}`))
	if ok {
		t.Fatal("resolveHandle: ok=true; want false for unregistered handle")
	}
	assertResolveHandleErr(t, env)
}

// TestResolveHandlePresent mints a real *pkgapi.Client into the global registry
// and verifies that resolveHandle returns ok=true with the correct client and
// stripped params (handle key removed, other keys preserved).
func TestResolveHandlePresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "state.db")
	c, err := pkgapi.New(pkgapi.Options{
		StorePath:       dbPath,
		CreateIfMissing: true,
	})
	if err != nil {
		t.Fatalf("pkgapi.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	handle := registry.mint(c)
	t.Cleanup(func() { registry.delete(handle) })

	params := []byte(fmt.Sprintf(`{"handle":%q,"extra":"val"}`, handle))
	gotClient, stripped, ok := resolveHandle(params)
	if !ok {
		t.Fatalf("resolveHandle: ok=false, envelope=%s", stripped)
	}
	if gotClient != c {
		t.Error("resolveHandle: returned wrong *Client pointer")
	}
	var m map[string]any
	if err := json.Unmarshal(stripped, &m); err != nil {
		t.Fatalf("stripped params not valid JSON: %v", err)
	}
	if _, has := m["handle"]; has {
		t.Errorf("stripped params still contain handle key: %s", stripped)
	}
	if got := m["extra"]; got != "val" {
		t.Errorf("stripped params: extra = %v; want \"val\"", got)
	}
}

// ─── contextFromParams ────────────────────────────────────────────────────────

// TestContextFromParamsTimeout verifies that {"timeout_ms":500} returns a
// context with a deadline approximately 500 ms from now.
func TestContextFromParamsTimeout(t *testing.T) {
	before := time.Now()
	ctx, cancel := contextFromParams([]byte(`{"timeout_ms":500}`))
	defer cancel()
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected context to have a deadline; got none")
	}
	expected := before.Add(500 * time.Millisecond)
	diff := dl.Sub(expected)
	if diff < -100*time.Millisecond || diff > 200*time.Millisecond {
		t.Errorf("deadline differs from expected by %v; want within ±100ms of 500ms", diff)
	}
}

// TestContextFromParamsZero verifies that {"timeout_ms":0} returns a context
// without a deadline (same semantics as context.Background).
func TestContextFromParamsZero(t *testing.T) {
	ctx, cancel := contextFromParams([]byte(`{"timeout_ms":0}`))
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("expected no deadline for timeout_ms=0; got one")
	}
}

// TestContextFromParamsAbsent verifies that params without a timeout_ms field
// returns a context without a deadline.
func TestContextFromParamsAbsent(t *testing.T) {
	ctx, cancel := contextFromParams([]byte(`{}`))
	defer cancel()
	if _, ok := ctx.Deadline(); ok {
		t.Error("expected no deadline for absent timeout_ms; got one")
	}
}

// ─── runVerb ──────────────────────────────────────────────────────────────────

// TestRunVerbSuccess verifies that a fn returning (payload, nil) produces a
// success envelope: payload keys present, no err_name key.
func TestRunVerbSuccess(t *testing.T) {
	result := runVerb("ad_version", []byte(`{}`), func(_ *pkgapi.Client, _ []byte) (any, error) {
		return map[string]string{"mykey": "myval"}, nil
	})
	m := unmarshalObj(t, result)
	if _, has := m["err_name"]; has {
		t.Errorf("unexpected err_name=%v in success path; full: %s", m["err_name"], result)
	}
	if got := m["mykey"]; got != "myval" {
		t.Errorf("mykey = %v; want \"myval\"", got)
	}
}

// TestRunVerbError verifies that a fn returning (nil, documented-error) produces
// an error envelope whose err_name matches the catalog name and whose
// err_description is non-empty.
func TestRunVerbError(t *testing.T) {
	result := runVerb("ad_version", []byte(`{}`), func(_ *pkgapi.Client, _ []byte) (any, error) {
		return nil, pkgapi.ErrSpawnNotInteractive
	})
	m := unmarshalObj(t, result)
	if got := m["err_name"]; got != "ErrSpawnNotInteractive" {
		t.Errorf("err_name = %v; want \"ErrSpawnNotInteractive\"", got)
	}
	if desc, _ := m["err_description"].(string); desc == "" {
		t.Error("err_description must be non-empty for documented error")
	}
}

// TestRunVerbPanicRecovers verifies that a fn that panics does not propagate
// the panic past runVerb; the caller receives an ErrInternal envelope.
func TestRunVerbPanicRecovers(t *testing.T) {
	result := runVerb("ad_version", []byte(`{}`), func(_ *pkgapi.Client, _ []byte) (any, error) {
		panic("deliberate test panic for recovery verification")
	})
	m := unmarshalObj(t, result)
	if got := m["err_name"]; got != "ErrInternal" {
		t.Errorf("err_name = %v after panic; want \"ErrInternal\"", got)
	}
}
