# @agent-director/linux-x64

This package ships the `agent-director` CLI binary for Linux x64. The TS Client
(`agent-director` umbrella) spawns this binary as a subprocess to invoke verbs.

## Binary source

The binary is **not committed to git**. It is dropped into `bin/agent-director`
by release.sh after cross-compiling the CLI:

```sh
make release-binaries   # builds dist/agent-director-linux-amd64 (and others)
```

`release.sh` stages the linux/amd64 build into
`platforms/linux-x64/bin/agent-director` before `npm publish`.

For local development, the test preload (`pkg/ts-bun-client/test/setup.ts`)
copies the host's built `bin/agent-director` into this directory automatically
so the subprocess Client's platform resolver can find it during tests.

The `bin/` directory is listed in `.gitignore` so the binary is never committed.
