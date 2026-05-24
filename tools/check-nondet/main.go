// Command check-nondet verifies that test/envelope-diff/nondeterministic.json
// is aligned with manifest.CallableVerbs().
//
// Every callable verb must appear as a top-level key in the JSON; every
// top-level key must name a callable verb. Exits 0 on success, 1 on any
// mismatch.
//
// Usage:
//
//	go run ./tools/check-nondet [-manifest path] [-quiet]
//
// Flags:
//
//	-manifest path   Path to nondeterministic.json
//	                 (default: test/envelope-diff/nondeterministic.json)
//	-quiet           Suppress the success line; only errors are emitted.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/gabemahoney/agent-director/pkg/api/manifest"
)

const defaultManifest = "test/envelope-diff/nondeterministic.json"

func main() {
	manifestPath := flag.String("manifest", defaultManifest, "path to nondeterministic.json")
	quiet := flag.Bool("quiet", false, "suppress success line")
	flag.Parse()

	// Also accept a bare positional argument for backward-compat with the
	// Makefile invocation `go run ./tools/check-nondet path`.
	if flag.NArg() > 0 {
		*manifestPath = flag.Arg(0)
	}

	n, err := check(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-nondet: %v\n", err)
		os.Exit(1)
	}
	if !*quiet {
		fmt.Printf("OK: nondeterministic.json covers %d callable verbs\n", n)
	}
}

// Check verifies that the nondeterministic.json at manifestPath contains
// exactly one top-level key per name in verbs — no more, no fewer. It returns
// a non-nil error listing every offending name when the sets are misaligned.
// Callers that want to check against the live manifest should pass the names
// from manifest.CallableVerbs(); tests inject fixture verb lists instead.
func Check(verbs []string, manifestPath string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", manifestPath, err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse %s: %w", manifestPath, err)
	}

	if len(raw) == 0 && len(verbs) > 0 {
		return fmt.Errorf("%s contains zero keys; expected %d callable verb entries", manifestPath, len(verbs))
	}

	verbSet := make(map[string]struct{}, len(verbs))
	for _, v := range verbs {
		verbSet[v] = struct{}{}
	}

	var missing, extraneous []string

	for _, v := range verbs {
		if _, ok := raw[v]; !ok {
			missing = append(missing, v)
		}
	}
	for k := range raw {
		if _, ok := verbSet[k]; !ok {
			extraneous = append(extraneous, k)
		}
	}

	if len(missing) == 0 && len(extraneous) == 0 {
		return nil
	}

	sort.Strings(missing)
	sort.Strings(extraneous)

	var lines []string
	for _, v := range missing {
		lines = append(lines, fmt.Sprintf("verb %q in manifest.CallableVerbs() but not in nondeterministic.json", v))
	}
	for _, k := range extraneous {
		lines = append(lines, fmt.Sprintf("key %q in nondeterministic.json but not in manifest.CallableVerbs()", k))
	}
	return fmt.Errorf("%s", joinLines(lines))
}

// check is the internal entry point used by main; it resolves verb names from
// the live manifest and delegates to Check.
func check(path string) (n int, err error) {
	callable := manifest.CallableVerbs()
	verbs := make([]string, len(callable))
	for i, v := range callable {
		verbs[i] = v.Name
	}
	return len(callable), Check(verbs, path)
}

func joinLines(ss []string) string {
	var out string
	for i, s := range ss {
		if i > 0 {
			out += "\n"
		}
		out += s
	}
	return out
}
