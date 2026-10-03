# Running the model check on a laptop

`run.sh` runs the TLA+ suite with the vendored TLC (`../tla2tools.jar`) on
your own machine. It exists for bee b.66h while the job scheduler is off. It
runs the b.66h cfgs (`../launch/`) and the fast tier of `../ci/suite.tsv` as
a regression run.

**Never run it on a shared dev host.** TLC uses every core and many GB of
memory and disk. A TLC run once OOM-killed the dev VM. The script refuses to
start on a Horde dev VM.

## What you need

- bash 3.2 or later (the macOS default is fine).
- A Java 11+ runtime. If `java` is missing, the script stops and prints a
  one-line install hint:
  - macOS (Apple Silicon or Intel): `brew install --cask temurin`
  - Debian or Ubuntu: `sudo apt-get install -y openjdk-17-jre-headless`
- About 40 GB of free disk for the largest runs. The state files are deleted
  after each run.

## Run it

From the repo root:

```
bash spec/tla/laptop/run.sh             # parse check, the b.66h runs, then the fast tier
bash spec/tla/laptop/run.sh --launch    # parse check and the b.66h runs only
bash spec/tla/laptop/run.sh --parse-only
bash spec/tla/laptop/run.sh ls_C_S1o_lab ci_S1o_lab   # just these cfgs
```

It runs in this order:

1. **A parse check.** It checks every b.66h cfg and one cfg per spec
   module: TLC parses the spec and the cfg, then takes one step. If any of
   them fails, the script stops there.
2. **The b.66h runs**, small ones first.
3. **The fast tier** of `../ci/suite.tsv` (71 runs). `--regress` runs only
   this step. `TLA_TIER=full` runs all 92 rows.

## Settings

All settings are optional environment variables.

| Variable | Default | Meaning |
|---|---|---|
| `TLA_JAVA` | `$JAVA_HOME/bin/java`, else `java` | The java binary. |
| `TLA_XMX` | a quarter of RAM, 2g to 8g | The JVM heap cap, for example `TLA_XMX=6g`. |
| `TLA_DIRECT` | half the heap | JVM direct memory, used by TLC's fingerprint set. |
| `TLA_WORKERS` | every core | TLC worker threads. |
| `TLA_CAP_S` | 3600 | Wall-clock cap per run, in seconds. `0` means no cap. |
| `TLA_DISK_GB` | 40 | Cap on one run's state files. |
| `TLA_WORKDIR` | a new directory under `$TMPDIR` | Where TLC runs. The logs stay there. |

## Reading the output

Each run prints one line:

```
PASS|FAIL <cfg> <result> distinct=<n> secs=<s> -- <what> (<note>)
```

The last line is `LAPTOP-VERDICT PASS` or `LAPTOP-VERDICT FAIL`, with the
count of runs that met their expectation. The exit status is 0 only on PASS.

The run kinds:

- **A pass run** is ok when TLC finds no error. A b.zuj run must also reach
  the exact state count that b.zuj CI run 4 recorded (`run4.tsv`). The b.66h
  knobs are off in those cfgs, so the model change must leave their state
  graph unchanged. A different count is a FAIL.
- **A must-fail run** (control, probe or residual) is ok when TLC reports a
  violation. A b.66h run must violate one of the properties its row in
  `../launch/suite.tsv` names. For a b.zuj run, a different property from
  run 4 is only noted.
- **Not ok:** INCOMPLETE (the time or disk cap), ERROR, and a parse failure.

For each b.66h control the script prints the counterexample in short form:
one line per step, with the variables that changed.

`run4.tsv` holds each b.zuj cfg's result, state count and seconds from b.zuj
CI run 4 (2026-09-30, 4 workers). It was copied from that run's job logs.
