# testplan-ad-on-path-only

Fresh container; install AD to a custom directory on `$PATH` (the
standard install path is not present); `Client.create()` resolves
via the PATH fallback (SR-1.1 step 2).

## Container image

`ubuntu:22.04` with `bun`.

## Setup

```sh
export HOME=/root

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Place the AD binary on PATH at a non-standard location.  Do NOT use
# install.sh (which would put it at the standard install path).
mkdir -p /usr/local/bin
cp /path/to/agent-director-binary /usr/local/bin/agent-director
chmod +x /usr/local/bin/agent-director

# Confirm the standard install path is absent.
test ! -e $HOME/.agent-director/bin/agent-director

# Install the library.
mkdir -p /work && cd /work
bun init -y
bun add agent-director
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import { Client } from "agent-director";
import { realpathSync } from "node:fs";
const c = await Client.create({});
const expected = realpathSync("/usr/local/bin/agent-director");
if (c.binaryPath !== expected) {
  console.log(`FAIL: binaryPath=${c.binaryPath}; expected ${expected}`);
  process.exit(2);
}
console.log("PASS");
EOF
cd /work && bun run probe.ts
```

## Expected outcome

- Exit code 0.
- `client.binaryPath` equals the PATH-canonicalized absolute path of
  the AD binary (`/usr/local/bin/agent-director`, after symlink
  resolution).
