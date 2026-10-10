// b.1ce: verify-installed-pkg-full and verify-prerelease-linux staged the CLI
// into, and bun added, the deleted pkg/ts-bun-client/platforms/<host>
// sub-package. This file reuses the rule parser in require_sandbox_test.go.

package sandboxrequiresandbox_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/gabemahoney/agent-director/test/sandbox/internal/sandboxtest"
)

var (
	// verifyRun matches the command that runs the verify script with --full and
	// captures its AD_CLI_PATH value, bun's flags and the script bun runs.
	verifyRun = regexp.MustCompile(`AD_CLI_PATH=(\S+) (?:\S+=\S+ )*bun ((?:-\S+ )*)(\S+) --full`)
	// shellVar matches a value that is one shell variable, quoted or escaped,
	// and captures its name.
	shellVar = regexp.MustCompile(`^\\?"?\$\$\{?(\w+)\}?\\?"?$`)
	// cdTo matches a cd and captures its target.
	cdTo = regexp.MustCompile(`\bcd ([^\s;&|]+)`)
	// unquote strips the quotes, escapes and braces from a recipe word.
	unquote = strings.NewReplacer(`\`, "", `"`, "", "{", "", "}", "")
)

// mountAt matches a container mount at path and captures its host source.
func mountAt(path string) *regexp.Regexp {
	return regexp.MustCompile(`-v (\S+):` + regexp.QuoteMeta(path) + `(?::ro)?\s`)
}

// resolve returns word, or what recipe assigns it when word is one shell variable.
func resolve(t *testing.T, recipe, word string) string {
	t.Helper()
	v := shellVar.FindStringSubmatch(word)
	if v == nil {
		return word
	}
	a := regexp.MustCompile(`\b` + v[1] + `=(\S+);`).FindStringSubmatch(recipe)
	if a == nil {
		t.Fatalf("%s is $%s, which the recipe never sets", word, v[1])
	}
	return a[1]
}

// TestVerifyPkgTargets_UseStampedCLI (b.1ce): neither target names platforms/; each runs the verify
// script with bun --no-install from the tarball's consumer dir, AD_CLI_PATH at the stamped dist/
// binary (mounted there by every docker run). Fails on the pre-fix Makefile via MAKEFILE_UNDER_TEST.
func TestVerifyPkgTargets_UseStampedCLI(t *testing.T) {
	recipes := map[string]string{}
	for _, r := range rules(t, sandboxtest.MakefileUnderTest(t)) {
		for _, tg := range r.targets {
			recipes[tg] = strings.Join(r.recipe, "\n")
		}
	}
	for _, tc := range []struct {
		target    string
		container bool // the verify script runs in a container: AD_CLI_PATH is a mount point
	}{
		{target: "verify-installed-pkg-full"},
		{target: "verify-prerelease-linux", container: true},
	} {
		t.Run(tc.target, func(t *testing.T) {
			recipe := recipes[tc.target]
			if recipe == "" {
				t.Fatalf("Makefile has no recipe for %s", tc.target)
			}
			if n := strings.Count(recipe, "platforms/"); n > 0 {
				t.Errorf("recipe names the deleted platforms/ sub-package %d times", n)
			}
			loc := verifyRun.FindStringSubmatchIndex(recipe)
			if loc == nil {
				t.Fatalf("recipe runs no `AD_CLI_PATH=<cli> bun <script> --full`:\n%s", recipe)
			}
			cli, flags, script := recipe[loc[2]:loc[3]], recipe[loc[4]:loc[5]], recipe[loc[6]:loc[7]]
			if !slices.Contains(strings.Fields(flags), "--no-install") {
				t.Errorf("bun runs %s without --no-install, so an import the consumer dir cannot "+
					"resolve is installed from npm instead of failing", script)
			}

			if tc.container {
				// Every docker run, the dry-run echo and the real one alike.
				runs := strings.Split(recipe, "docker run ")[1:]
				if len(runs) == 0 {
					t.Fatalf("AD_CLI_PATH=%s, but the recipe has no docker run to mount it", cli)
				}
				for i, run := range runs {
					m := mountAt(unquote.Replace(cli)).FindStringSubmatch(run)
					if m == nil {
						t.Errorf("docker run %d of %d mounts nothing at AD_CLI_PATH=%s", i+1, len(runs), cli)
					} else if src := resolve(t, recipe, m[1]); !strings.Contains(src, "/dist/agent-director-") {
						t.Errorf("docker run %d of %d mounts %s at AD_CLI_PATH=%s, want the stamped "+
							"dist/agent-director-<os>-<arch> binary", i+1, len(runs), src, cli)
					}
				}
			} else if src := resolve(t, recipe, cli); !strings.Contains(src, "/dist/agent-director-") {
				t.Errorf("AD_CLI_PATH=%s is %s, want the stamped dist/agent-director-<os>-<arch> binary", cli, src)
			}

			// The consumer dir is the last cd before the last bun add ahead of the run.
			before := recipe[:loc[0]]
			add := strings.LastIndex(before, "bun add ")
			if add < 0 {
				t.Fatalf("recipe runs bun add nowhere before the verify run")
			}
			cds := cdTo.FindAllStringSubmatch(before[:add], -1)
			if len(cds) == 0 {
				t.Fatalf("recipe runs bun add before any cd, so it has no consumer dir")
			}
			dir, s := unquote.Replace(cds[len(cds)-1][1]), unquote.Replace(script)
			relative := !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "$") && !strings.HasPrefix(s, "~") &&
				!cdTo.MatchString(before[add:])
			if strings.Contains(s, "..") || !strings.HasPrefix(s, dir+"/") && !relative {
				t.Errorf("bun runs %s, outside the consumer dir %s, so it resolves the published "+
					"package, not the tarball", script, dir)
			}
		})
	}
}
