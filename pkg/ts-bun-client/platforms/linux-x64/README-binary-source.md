# @agent-director/linux-x64

This package ships the native shared library `libagent_director.so` for Linux x64.

## Binary source

The binary is **not committed to git**. It is dropped into this directory by CI
after building for the target platform:

```sh
make libagent_director   # builds dist/libagent_director.so (linux/amd64)
cp dist/libagent_director.so pkg/ts-bun-client/platforms/linux-x64/
```

For local development, run the `prepare-platforms` script from `pkg/ts-bun-client/`:

```sh
bun run prepare-platforms
```

This copies the already-built `.so` from the repo-root `dist/` directory into
`platforms/linux-x64/`. The file is listed in `.gitignore` so it is never
committed.
