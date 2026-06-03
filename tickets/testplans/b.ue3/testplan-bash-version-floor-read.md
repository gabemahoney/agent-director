# testplan-bash-version-floor-read

The bash one-liner `jq -r .min_binary_version < node_modules/agent-
director/dist/version-floor.json` works against the installed
package; no JS runtime is spawned (SR-5.5 / SR-5.6 / SR-8.6).

## Container image

`ubuntu:22.04` with `bun` and `jq`.

## Setup

```sh
export HOME=/root

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# Install jq if not present.
apt-get update && apt-get install -y jq

# Install the library only.
mkdir -p /work && cd /work
bun init -y
bun add agent-director
```

## Verification command

```sh
# Read the floor via jq; capture process list during the read.
cd /work
# Snapshot baseline of bun/node processes (should be empty).
BEFORE=$(ps -e -o comm= | grep -E '^(bun|node)$' | wc -l)

# Run the documented bash one-liner.
FLOOR=$(jq -r .min_binary_version < node_modules/agent-director/dist/version-floor.json)
JQ_RC=$?

# Snapshot after; jq exits quickly so the diff is at the point of inspection.
AFTER=$(ps -e -o comm= | grep -E '^(bun|node)$' | wc -l)

if [ $JQ_RC -ne 0 ]; then
  echo "FAIL: jq exit code $JQ_RC"
  exit 2
fi
if [ -z "$FLOOR" ]; then
  echo "FAIL: empty floor"
  exit 3
fi
if [ "$BEFORE" -ne "$AFTER" ]; then
  echo "FAIL: bun/node process count changed ($BEFORE -> $AFTER)"
  exit 4
fi
echo "PASS floor=$FLOOR"
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS floor=<X.Y.Z>` (the floor value matches the
  library's `MIN_BINARY_VERSION` export).
- No `bun` or `node` process spawned during the `jq` invocation.
