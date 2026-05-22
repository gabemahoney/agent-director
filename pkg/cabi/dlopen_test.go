//go:build cabi_dlopen

package main

// dlopen_test.go provides the TestMain that builds the C-shared library and
// opens it via dlopen, plus shared helper functions used by the success and
// error wire-test files (dlopen_success_test.go, dlopen_error_test.go).
//
// Design constraints:
//   - import "C" is NOT used here. Go prohibits cgo in _test.go files of
//     packages that contain //export directives. All cgo is isolated in the
//     non-test file dlopen_helper.go, which exposes pure-Go APIs using
//     unsafe.Pointer for dlopen/dlsym handles.
//   - The .so is built WITHOUT the cabi_dlopen tag so that dlopen_helper.go
//     (a test-only cgo helper) is excluded from the production artifact.
//   - The package-level globals dlopenSOHandle and dlopenFreeSym are populated
//     by TestMain and read by all helper functions in this file.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

// dlopenSOHandle is the .so library handle returned by soOpen; set in TestMain.
var dlopenSOHandle unsafe.Pointer

// dlopenFreeSym is the resolved symbol for ad_free_cstring; set in TestMain
// and passed to soCallAdFunc so every call can free its returned C string.
var dlopenFreeSym unsafe.Pointer

// dlopenParentID is the value of AGENT_DIRECTOR_INSTANCE_ID captured just
// before the .so is loaded. The .so's Go runtime captures environ at dlopen
// time, so this reflects what the .so will use as parent_id on ad_spawn calls.
// Tests that call ad_spawn must seed the DB with a row whose claude_instance_id
// equals dlopenParentID (when non-empty) to satisfy the FK constraint.
var dlopenParentID string

// TestMain builds the C-ABI shared library into a temp directory, opens it
// via dlopen, resolves ad_free_cstring, and then runs the test suite.
//
// The .so is built without -tags cabi_dlopen to produce the production
// artifact (dlopen_helper.go is excluded from it). CGO_ENABLED=1 is required
// for -buildmode=c-shared.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

func runTestMain(m *testing.M) int {
	// Locate the package source directory via the compile-time file path so
	// the go build command always targets the correct directory regardless of
	// the caller's working directory.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		log.Fatal("TestMain: runtime.Caller(0) failed — cannot determine package dir")
	}
	pkgDir := filepath.Dir(thisFile)

	// Capture AGENT_DIRECTOR_INSTANCE_ID BEFORE loading the .so. The .so's Go
	// runtime snapshots environ at dlopen time; tests that call ad_spawn need
	// to seed the store with a parent row matching this ID.
	dlopenParentID = os.Getenv("AGENT_DIRECTOR_INSTANCE_ID")

	// Build the .so into a temp directory. Use os.MkdirTemp (not t.TempDir)
	// since we're outside a *testing.T context here.
	tmpDir, err := os.MkdirTemp("", "cabi_dlopen_test_*")
	if err != nil {
		log.Fatalf("TestMain: MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	soPath := filepath.Join(tmpDir, "libagent_director_test.so")

	// Build WITHOUT cabi_dlopen tag: the .so must be the production artifact.
	// CGO_ENABLED=1 is forced regardless of the host environment setting.
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", soPath, ".")
	cmd.Dir = pkgDir
	cmd.Env = envWithCGO(os.Environ())
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		log.Fatalf("TestMain: go build -buildmode=c-shared failed:\n%s\n%v", out, buildErr)
	}

	// dlopen the built .so.
	dlopenSOHandle, err = soOpen(soPath)
	if err != nil {
		log.Fatalf("TestMain: soOpen(%q): %v", soPath, err)
	}
	defer soClose(dlopenSOHandle)

	// Resolve ad_free_cstring once; reused by every soCallAdFunc invocation.
	dlopenFreeSym, err = soSym(dlopenSOHandle, "ad_free_cstring")
	if err != nil {
		log.Fatalf("TestMain: soSym(ad_free_cstring): %v", err)
	}

	return m.Run()
}

// envWithCGO returns a copy of env with CGO_ENABLED set to "1",
// replacing any existing CGO_ENABLED entry.
func envWithCGO(env []string) []string {
	result := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "CGO_ENABLED=") {
			result = append(result, e)
		}
	}
	return append(result, "CGO_ENABLED=1")
}

// ─── per-test helpers ─────────────────────────────────────────────────────────

// dlopenSym looks up a symbol in the loaded .so. Fatals the test on error.
func dlopenSym(t *testing.T, name string) unsafe.Pointer {
	t.Helper()
	sym, err := soSym(dlopenSOHandle, name)
	if err != nil {
		t.Fatalf("soSym(%q): %v", name, err)
	}
	return sym
}

// dlopenInvoke resolves funcName in the loaded .so, calls it with paramsJSON,
// frees the returned C string via ad_free_cstring, and returns the raw JSON
// bytes. Fatals the test if the symbol cannot be resolved.
func dlopenInvoke(t *testing.T, funcName string, paramsJSON []byte) []byte {
	t.Helper()
	sym := dlopenSym(t, funcName)
	return soCallAdFunc(sym, dlopenFreeSym, paramsJSON)
}

// parseEnvelope parses the JSON envelope returned by dlopenInvoke into a
// map[string]any. Fatals the test if the JSON is invalid or not an object.
func parseEnvelope(t *testing.T, data []byte) map[string]any {
	t.Helper()
	return unmarshalObj(t, data)
}

// dlopenOpen calls ad_open in the loaded .so with the given storePath and
// tmuxCmd, returning the opaque handle string. It always sets
// create_if_missing=true (safe when the DB already exists — OpenOrInit is
// idempotent). A cleanup to call ad_close is registered with t.
func dlopenOpen(t *testing.T, storePath, tmuxCmd string) string {
	t.Helper()
	type openP struct {
		StorePath       string `json:"store_path"`
		CreateIfMissing bool   `json:"create_if_missing"`
		TmuxCommand     string `json:"tmux_command,omitempty"`
	}
	params, err := json.Marshal(openP{
		StorePath:       storePath,
		CreateIfMissing: true,
		TmuxCommand:     tmuxCmd,
	})
	if err != nil {
		t.Fatalf("dlopenOpen: json.Marshal: %v", err)
	}

	raw := dlopenInvoke(t, "ad_open", params)
	m := parseEnvelope(t, raw)
	if errName, has := m["err_name"]; has {
		t.Fatalf("ad_open failed: err_name=%v (params=%s)", errName, params)
	}
	handle, ok := m["handle"].(string)
	if !ok || handle == "" {
		t.Fatalf("ad_open returned no handle: %v", m)
	}

	t.Cleanup(func() {
		closeParams := fmt.Sprintf(`{"handle":%q}`, handle)
		dlopenInvoke(t, "ad_close", []byte(closeParams))
	})
	return handle
}

// assertDlopenSuccess fails the test if the envelope contains an err_name key.
func assertDlopenSuccess(t *testing.T, env []byte) {
	t.Helper()
	m := parseEnvelope(t, env)
	if name, has := m["err_name"]; has {
		t.Errorf("expected success envelope; got err_name=%v — full: %s", name, env)
	}
}

// assertDlopenErrName fails the test if the envelope's err_name does not equal
// want, or if err_description is empty.
func assertDlopenErrName(t *testing.T, env []byte, want string) {
	t.Helper()
	m := parseEnvelope(t, env)
	if got := m["err_name"]; got != want {
		t.Errorf("err_name = %v; want %q — full: %s", got, want, env)
	}
	if desc, _ := m["err_description"].(string); desc == "" {
		t.Errorf("err_description is empty for err_name=%q", want)
	}
}
