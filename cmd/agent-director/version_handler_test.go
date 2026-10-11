package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pkgapi "github.com/gabemahoney/agent-director/pkg/api"
)

// TestVersionHandlerClientErrorIsClassified: a Version() failure (a closed
// Client) is named by errnames.Classify, ErrInternal, never ErrJSONMarshal (b.3jc).
func TestVersionHandlerClientErrorIsClassified(t *testing.T) {
	dir := t.TempDir()
	client, err := pkgapi.New(pkgapi.Options{StorePath: filepath.Join(dir, "state.db"),
		ConfigPath: filepath.Join(dir, "config.toml"), CreateIfMissing: true})
	if err != nil {
		t.Fatalf("pkgapi.New: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	env := captureEnvelope(t, func() error { return versionHandler(client, nil) })
	if want := (errorEnvelope{ErrName: "ErrInternal", ErrDescription: pkgapi.ErrClientClosed.Error()}); env != want {
		t.Errorf("envelope = %+v; want %+v", env, want)
	}
}

// captureEnvelope runs fn under captureStdio and returns the one error envelope
// it wrote to stderr; fn must return errDispatch and write nothing to stdout.
func captureEnvelope(t *testing.T, fn func() error) errorEnvelope {
	t.Helper()
	stdout, stderr, err := captureStdio(t, fn)
	if !errors.Is(err, errDispatch) || stdout != "" {
		t.Fatalf("handler = %v, stdout = %q; want errDispatch and empty stdout (stderr=%q)", err, stdout, stderr)
	}
	var env errorEnvelope
	dec := json.NewDecoder(strings.NewReader(stderr))
	if derr := dec.Decode(&env); derr != nil || dec.More() {
		t.Fatalf("stderr = %q; want exactly one JSON error envelope (decode: %v)", stderr, derr)
	}
	return env
}

// captureStdio runs fn with os.Stdout and os.Stderr sent to files and returns
// what it wrote to each; it swaps process-wide state, so its callers are serial.
func captureStdio(t *testing.T, fn func() error) (stdout, stderr string, err error) {
	t.Helper()
	dir := t.TempDir()
	outPath, errPath := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	outF, oerr := os.Create(outPath)
	if oerr != nil {
		t.Fatalf("create %s: %v", outPath, oerr)
	}
	defer outF.Close()
	errF, eerr := os.Create(errPath)
	if eerr != nil {
		t.Fatalf("create %s: %v", errPath, eerr)
	}
	defer errF.Close()

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		err = fn()
	}()

	for path, dst := range map[string]*string{outPath: &stdout, errPath: &stderr} {
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			t.Fatalf("read %s: %v", path, rerr)
		}
		*dst = string(b)
	}
	return stdout, stderr, err
}
