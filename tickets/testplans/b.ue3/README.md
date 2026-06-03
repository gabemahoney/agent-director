# Testplans — b.ue3 (Drop vendored agent-director, find system install)

Nine container-based end-to-end testplans that validate the assembled
b.ue3 / Stream A delivery (Epics 1–5).  Each is markdown documenting
the container image, setup steps, verification command, and expected
outcome.  Together they cover scenarios the in-process unit and
integration suites can't reach.

Release-blocking per SR-8.11: a failure on any one blocks release.

## Testplans

| File | Scenario |
|---|---|
| `testplan-no-ad-installed.md` | Fresh container, no AD anywhere → `ErrSystemInstallNotFound`. |
| `testplan-ad-at-standard-path.md` | AD installed via `install.sh` at the standard path → resolves. |
| `testplan-ad-on-path-only.md` | AD on `$PATH` only (no standard install) → resolves via PATH. |
| `testplan-ad-too-old.md` | AD installed but below `MIN_BINARY_VERSION` → `ErrSystemInstallTooOld`. |
| `testplan-ad-unreachable-not-executable.md` | Binary present but `chmod -x`'d → `ErrSystemInstallUnreachable(reason="not-executable")`. |
| `testplan-ignore-scripts-install.md` | `bun add --ignore-scripts agent-director` → identical functionality. |
| `testplan-home-unset.md` | `unset HOME` + AD on PATH → resolves via PATH fallback. |
| `testplan-bash-version-floor-read.md` | Bash `jq -r .min_binary_version` → exit 0, no JS runtime spawned. |
| `testplan-in-place-reinstall-survives-hooks.md` | `install.sh` overwrite leaves spawn-side hook commands callable. |

## Conventions

Each testplan body has four sections in order:

1. **Container image** — the base image the testplan runs in.
2. **Setup** — commands run before the verification step.
3. **Verification command** — the single command the testplan's
   pass/fail hinges on.
4. **Expected outcome** — exit code + stdout / stderr expectations.

Run with: a release-engineering harness (e.g. `release.sh` integration
or a sibling Docker driver). Manual invocation: `docker run` the named
image with the setup steps applied, then exec the verification command
and compare against the expected outcome.

## See also

- Plan bee: `b.ue3` (Stream A library-side delivery).
- SRD: `t1.w3q.u6` §SR-8.10, §SR-8.11.
- Sibling Epics 1–5 land the library and Go-side changes these
  testplans exercise.
