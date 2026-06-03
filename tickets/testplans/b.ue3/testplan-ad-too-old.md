# testplan-ad-too-old

Fresh container; install an older AD release (below the library's
`MIN_BINARY_VERSION`); `Client.create()` rejects with
`ErrSystemInstallTooOld`.

## Container image

`ubuntu:22.04` with `bun`.

## Setup

```sh
export HOME=/root

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Install an older AD CLI release that stamps a version below the
# library's MIN_BINARY_VERSION (e.g. v0.6.3 if the floor is 0.7.0).
curl -fsSL https://github.com/<org>/agent-director/releases/download/v0.6.3/install.sh | bash

# Install the latest library (whose MIN_BINARY_VERSION will be above
# the installed CLI's stamped version).
mkdir -p /work && cd /work
bun init -y
bun add agent-director
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import { Client, ErrSystemInstallTooOld, MIN_BINARY_VERSION } from "agent-director";
try {
  await Client.create({});
  console.log("FAIL: expected rejection");
  process.exit(2);
} catch (e) {
  if (!(e instanceof ErrSystemInstallTooOld)) {
    console.log(`FAIL: wrong class ${e?.constructor?.name}`);
    process.exit(3);
  }
  if (typeof e.actualVersion !== "string" || e.actualVersion === "") {
    console.log("FAIL: actualVersion missing");
    process.exit(4);
  }
  if (e.requiredVersion !== MIN_BINARY_VERSION) {
    console.log(`FAIL: requiredVersion=${e.requiredVersion}; expected ${MIN_BINARY_VERSION}`);
    process.exit(5);
  }
  if (typeof e.binaryPath !== "string" || !e.binaryPath.startsWith("/")) {
    console.log(`FAIL: binaryPath=${e.binaryPath}`);
    process.exit(6);
  }
  console.log(`PASS actualVersion=${e.actualVersion} required=${e.requiredVersion}`);
}
EOF
cd /work && bun run probe.ts
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS actualVersion=<old> required=<floor>`.
- `actualVersion`, `requiredVersion`, and `binaryPath` all populated.
