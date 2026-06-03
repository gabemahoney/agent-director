# testplan-home-unset

Container with `unset HOME` and AD on `$PATH`. `Client.create()`
resolves via the PATH fallback (SR-1.1 HOME-unset sub-clause).

## Container image

`ubuntu:22.04` with `bun`.

## Setup

```sh
# Install Bun (still needs HOME for the installer; do it before unsetting).
export HOME=/root
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Drop the AD binary on PATH at a non-standard location.
mkdir -p /opt/ad-bin
cp /path/to/agent-director-binary /opt/ad-bin/agent-director
chmod +x /opt/ad-bin/agent-director
export PATH="/opt/ad-bin:$PATH"

# Install the library.
mkdir -p /work && cd /work
bun init -y
bun add agent-director

# Now unset HOME for the verification step.
unset HOME
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import { Client } from "agent-director";
import { realpathSync } from "node:fs";
const c = await Client.create({});
const expected = realpathSync("/opt/ad-bin/agent-director");
if (c.binaryPath !== expected) {
  console.log(`FAIL: binaryPath=${c.binaryPath}; expected ${expected}`);
  process.exit(2);
}
console.log("PASS");
EOF
cd /work && env -u HOME bun run probe.ts
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS`.
- The discovery pipeline manufactured no standard-install-path candidate
  (HOME unset → step 1 skipped) and resolved via PATH.
