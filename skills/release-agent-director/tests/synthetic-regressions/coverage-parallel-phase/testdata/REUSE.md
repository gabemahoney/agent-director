# Toy gates-config reuse anchor (SR-7.4, for Epic t1.2mt.wc)

This `testdata/` directory is the path-addressable anchor for the reusable toy
gates-config mechanism defined by the `coverage-parallel-phase` package. It is
skipped by the outer `go test ./...` package walk (the Go toolchain never
descends into `testdata/`).

The mechanism drives `skills/release-agent-director/gates/lib/run-parallel.sh`
with a cheap, dependency-free toy gates-config so executor semantics can be
proven without running any real coverage gate (SR-7.5). The SR-2.3 report-shape
check in the `release-postconditions` package needs consolidated executor
output produced by this same mechanism.

## How to reuse from another test package

Go test helpers are not importable across packages, so reuse is this documented
pattern plus path-addressable artifacts — not shared Go functions. From the
consuming package (e.g. `release-postconditions`):

1. Resolve `repoRoot` with the package's own go.mod walk-up helper.
2. Materialize a gates-config into the test's own `t.TempDir()` with the schema
   `run-parallel.sh` consumes:

   ```json
   {
     "phase_name": "coverage-toy",
     "gates": [
       {"name": "coverage.toy-pass", "command": "true", "cwd": "."},
       {"name": "coverage.toy-fail",
        "command": "printf '%s\\n' '{\"gate\":\"coverage.toy-fail\",...}' >&2; exit 1",
        "cwd": "."}
     ],
     "max_parallel": 2
   }
   ```

   The toy command snippets (a bare `true`; a
   `printf <SR-14-json> >&2; exit 1`) are copy-stable and dependency-free. See
   `toyconfig_test.go` in the parent directory for the canonical builders
   (`allPassingConfig` / `singleFailureConfig` / `multiFailureConfig`) and the
   `writeToyConfig` materialize seam.

3. Invoke `run-parallel.sh` (resolved from `repoRoot`) with the config path —
   the only interface it exposes; there is no test-only seam.

4. Parse stdout into the SR-2.1 consolidated shape: `phase_name`, `outcome`,
   `sub_checks[]` with `name`, `outcome`, `duration_ms`, `exit_code`,
   `stderr_excerpt`, `diagnostics`.

The stable, path-addressable parts are: (a) this schema, (b) the
`run-parallel.sh` path under `repoRoot`, and (c) the toy command snippets. A
consumer package reproduces steps 1–4 with its own local helpers; nothing in
`coverage-parallel-phase` needs to be exported.
