# testplan-no-ad-installed

Fresh container with no agent-director CLI on disk anywhere. The
library's `Client.create()` must reject with `ErrSystemInstallNotFound`
naming both checked locations (standard install path and PATH lookup).

## Container image

`ubuntu:22.04` (or any linux/x64 base) with `bun` installed and the
agent-director npm package available as a local tarball or
GitHub-Release dependency.

## Setup

```sh
# Inside the container:
export HOME=/root
# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Install the library only.  Do NOT install the CLI binary.
mkdir -p /work && cd /work
bun init -y
bun add agent-director
```

## Verification command

```sh
cat > /work/probe.ts <<'EOF'
import { Client, ErrSystemInstallNotFound } from "agent-director";
try {
  await Client.create({});
  console.log("FAIL: expected rejection");
  process.exit(2);
} catch (e) {
  if (!(e instanceof ErrSystemInstallNotFound)) {
    console.log(`FAIL: wrong class ${e?.constructor?.name}`);
    process.exit(3);
  }
  const locs = e.checkedLocations.map((l: any) => l.kind).sort();
  const ok =
    locs.length === 2 &&
    locs.includes("standard-install-path") &&
    locs.includes("path-lookup");
  if (!ok) {
    console.log(`FAIL: checkedLocations=${JSON.stringify(e.checkedLocations)}`);
    process.exit(4);
  }
  console.log("PASS");
}
EOF
cd /work && bun run probe.ts
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS`.
- `checkedLocations` contains exactly two entries, one with
  `kind="standard-install-path"` and one with `kind="path-lookup"`.
