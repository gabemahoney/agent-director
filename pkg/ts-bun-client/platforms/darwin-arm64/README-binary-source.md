# @CHANGEME-H3/agent-director-darwin-arm64

This package ships the native shared library `libagent_director.dylib` for macOS ARM64 (Apple Silicon).

## Binary source

The binary is **not committed to git**. It is dropped into this directory by CI
after cross-compiling for the target platform:

```sh
make libagent_director   # builds dist/libagent_director.dylib (darwin/arm64)
cp dist/libagent_director.dylib pkg/ts-bun-client/platforms/darwin-arm64/
```

The file is listed in `.gitignore` so it is never committed.
