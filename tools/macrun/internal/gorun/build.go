package gorun

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/golist"
)

// Build is how one package's test binary built.
type Build struct {
	// Binary is the binary's absolute path, when it built.
	Binary string
	// Built reports whether it built.
	Built bool
	// Output is the build's output when it didn't.
	Output string
}

// buildSettings are the variables Build sets, as the plan lists the package:
// darwin/arm64, cgo as golist lists it, and no flags such as -tags from the
// environment.
var buildSettings = []string{
	"GOOS=darwin",
	"GOARCH=arm64",
	"CGO_ENABLED=" + cgoValue(golist.DarwinCgoEnabled),
	"GOFLAGS=",
}

func cgoValue(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

// Build builds the test binary of the package at dir, relative to the module
// root root, into workDir, an absolute directory, as go test -c does, with
// env and buildSettings in place of env's own. go test -c runs go test's vet
// checks too (cmd/go/internal/test makes vet a dependency of its print
// action), so a vet finding fails the build as it fails go test. A failed
// build returns its output, not an error; err is only for a go command that
// couldn't run.
func (r Runner) Build(ctx context.Context, root, dir, workDir string, env []string) (Build, error) {
	binary := filepath.Join(workDir, binaryName(dir))
	cmd := Cmd{
		Path: r.Go,
		Args: []string{"test", "-c", "-o", binary, "./" + dir},
		Dir:  root,
		Env:  buildEnv(env),
	}
	out, ok, err := r.Exec.Output(ctx, cmd)
	if err != nil {
		return Build{}, fmt.Errorf("build tests of %s: %w", dir, err)
	}
	if !ok {
		return Build{Output: string(out)}, nil
	}
	return Build{Binary: binary, Built: true}, nil
}

// binaryName names dir's binary: dir escaped, so no two dirs share a name,
// and ending .test as go test's binaries do.
func binaryName(dir string) string {
	return url.PathEscape(dir) + ".test"
}

// buildEnv returns env with buildSettings in place of its own values.
func buildEnv(env []string) []string {
	set := map[string]bool{}
	for _, s := range buildSettings {
		name, _, _ := strings.Cut(s, "=")
		set[name] = true
	}
	var out []string
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !set[name] {
			out = append(out, kv)
		}
	}
	return append(out, buildSettings...)
}
