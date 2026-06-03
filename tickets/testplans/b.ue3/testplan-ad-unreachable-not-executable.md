# testplan-ad-unreachable-not-executable

Fresh container; AD binary present at the standard install path but
`chmod -x`'d; `Client.create()` rejects with
`ErrSystemInstallUnreachable(reason="not-executable")`.

## Container image

`ubuntu:22.04` with `bun`.

## Setup

```sh
export HOME=/root

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Drop the binary at the standard install path, then strip the exec bit.
mkdir -p $HOME/.agent-director/bin
cp /path/to/agent-director-binary $HOME/.agent-director/bin/agent-director
chmod 0644 $HOME/.agent-director/bin/agent-director

# Install the library.
mkdir -p /work && cd /work
bun init -y
bun add agent-director
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import { Client, ErrSystemInstallUnreachable } from "agent-director";
try {
  await Client.create({});
  console.log("FAIL: expected rejection");
  process.exit(2);
} catch (e) {
  if (!(e instanceof ErrSystemInstallUnreachable)) {
    console.log(`FAIL: wrong class ${e?.constructor?.name}`);
    process.exit(3);
  }
  if (e.reason !== "not-executable") {
    console.log(`FAIL: reason=${e.reason}`);
    process.exit(4);
  }
  console.log(`PASS reason=${e.reason} binaryPath=${e.binaryPath}`);
}
EOF
cd /work && bun run probe.ts
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS reason=not-executable binaryPath=...`.
