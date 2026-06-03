# testplan-ad-at-standard-path

Fresh container; install AD via `install.sh` to the standard path;
the library's `Client.create()` resolves successfully against it.

## Container image

`ubuntu:22.04` with `bun`, `curl`, and a writable `$HOME` (the
container runs as root or any user with a home directory).

## Setup

```sh
export HOME=/root
mkdir -p $HOME

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Install the AD CLI via install.sh (from the release tarball or a
# local copy).  install.sh drops the binary at $HOME/.agent-director/
# bin/agent-director.
curl -fsSL https://github.com/<org>/agent-director/releases/latest/download/install.sh | bash

# Install the library.
mkdir -p /work && cd /work
bun init -y
bun add agent-director
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import { Client } from "agent-director";
const c = await Client.create({});
const expected = `${process.env.HOME}/.agent-director/bin/agent-director`;
console.log(`binaryPath=${c.binaryPath}`);
if (c.binaryPath !== expected) {
  console.log(`FAIL: expected ${expected}`);
  process.exit(2);
}
console.log("PASS");
EOF
cd /work && bun run probe.ts
```

## Expected outcome

- Exit code 0.
- Stdout includes `PASS`.
- `client.binaryPath` equals `$HOME/.agent-director/bin/agent-director`
  (with `$HOME` expanded to the container's actual home directory).
