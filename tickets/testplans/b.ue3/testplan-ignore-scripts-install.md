# testplan-ignore-scripts-install

`bun add --ignore-scripts agent-director` produces an identical
consumer install — the library has no lifecycle scripts after b.ue3 /
Epic 4, so the flag is a no-op.

## Container image

`ubuntu:22.04` with `bun`.

## Setup

```sh
export HOME=/root

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

mkdir -p /work && cd /work
bun init -y
# The "--ignore-scripts" flag is the contract under test.
bun add --ignore-scripts agent-director
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import {
  Client,
  resolveSystemBinary,
  MIN_BINARY_VERSION,
  DEV_SENTINEL_VERSION,
  ErrSystemInstallNotFound,
  ErrSystemInstallTooOld,
  ErrSystemInstallUnreachable,
} from "agent-director";

if (typeof Client?.create !== "function") {
  console.log("FAIL: Client.create not found");
  process.exit(2);
}
if (typeof resolveSystemBinary !== "function") {
  console.log("FAIL: resolveSystemBinary not found");
  process.exit(3);
}
if (typeof MIN_BINARY_VERSION !== "string" || MIN_BINARY_VERSION === "") {
  console.log("FAIL: MIN_BINARY_VERSION missing");
  process.exit(4);
}
if (DEV_SENTINEL_VERSION !== "0.0.0-dev") {
  console.log("FAIL: DEV_SENTINEL_VERSION");
  process.exit(5);
}
if (
  typeof ErrSystemInstallNotFound !== "function" ||
  typeof ErrSystemInstallTooOld !== "function" ||
  typeof ErrSystemInstallUnreachable !== "function"
) {
  console.log("FAIL: error classes missing");
  process.exit(6);
}
console.log("PASS");
EOF
cd /work && bun run probe.ts
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS`.
- All six imports resolve from the installed package without
  postinstall having run.
