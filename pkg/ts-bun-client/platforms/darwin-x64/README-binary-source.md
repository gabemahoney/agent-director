# @CHANGEME-H3/agent-director-darwin-x64

This package ships the native shared library `libagent_director.dylib` for macOS x64.

## Binary source

The binary is **not committed to git**. It is dropped into this directory by CI
after cross-compiling for the target platform:

```sh
make libagent_director   # builds dist/libagent_director.dylib (darwin/amd64)
cp dist/libagent_director.dylib pkg/ts-bun-client/platforms/darwin-x64/
```

The file is listed in `.gitignore` so it is never committed.
