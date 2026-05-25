# @agent-director/darwin-arm64

This package ships the `agent-director` CLI binary for macOS ARM64 (Apple
Silicon). The TS Client (`agent-director` umbrella) spawns this binary as a
subprocess to invoke verbs.

## Binary source

The binary is **not committed to git**. It is dropped into `bin/agent-director`
by release.sh after cross-compiling the CLI:

```sh
make release-binaries   # builds dist/agent-director-darwin-arm64 (and others)
```

`release.sh` stages the darwin/arm64 build into
`platforms/darwin-arm64/bin/agent-director` before `npm publish`.

The `bin/` directory is listed in `.gitignore` so the binary is never committed.
