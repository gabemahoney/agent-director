# testplan-in-place-reinstall-survives-hooks

Container with AD installed at the standard install path; spawn a
Claude session and capture its hook commands; re-run `install.sh`
to overwrite the binary at the same path; assert the hook commands'
paths still point at a callable binary (SR-1.8 + SR-8.8).

## Container image

`ubuntu:22.04` with `bun`, `tmux` (or fake-tmux), and `curl`.

## Setup

```sh
export HOME=/root

# Install Bun.
curl -fsSL https://bun.sh/install | bash
export PATH="$HOME/.bun/bin:$PATH"

# First install of the AD CLI via install.sh (drops at standard path).
curl -fsSL https://github.com/<org>/agent-director/releases/latest/download/install.sh | bash

# Install the library + a fake-tmux stub if needed.  For this testplan
# we drive the spawn via the CLI directly (no need for the library
# install), but we use fake-tmux to capture the spawn argv without
# launching real Claude.
apt-get update && apt-get install -y tmux

# Capture the AD binary's stamp + sha-256 before the upgrade.
BIN=$HOME/.agent-director/bin/agent-director
ORIGINAL_SHA=$(sha256sum $BIN | cut -d' ' -f1)
```

## Verification command

```sh
# Step 1: spawn a Claude session against fake-tmux; capture the
# settings.json argv passed to claude --settings.
mkdir -p /tmp/spawn-work
cd /tmp/spawn-work

# Use a small driver that captures the spawn-side argv into a log
# (the harness can use the fake-tmux stub from
# test/fake-tmux/main.go for this purpose).
export FAKE_TMUX_LOG=/tmp/fake-tmux.log
$HOME/.agent-director/bin/agent-director spawn --cwd /tmp/spawn-work \
  > /tmp/spawn-stdout 2> /tmp/spawn-stderr || {
    echo "FAIL: spawn exited non-zero"
    cat /tmp/spawn-stderr
    exit 2
}

# Extract the hook command path from FAKE_TMUX_LOG (look for
# --settings followed by JSON; parse hooks[*].hooks[*].command).
HOOK_PATH=$(grep -A1 -F '\--settings' /tmp/fake-tmux.log | tail -n1 \
  | jq -r '.hooks.SessionStart[0].hooks[0].command' \
  | sed 's/ hook$//')

if [ -z "$HOOK_PATH" ] || [ ! -x "$HOOK_PATH" ]; then
  echo "FAIL: captured hook path missing or not executable: $HOOK_PATH"
  exit 3
fi

# Step 2: simulate `install.sh` re-run by overwriting the binary at
# the same path.  In production the operator runs install.sh; here we
# write a fresh copy with potentially different bytes.
cp $HOME/.agent-director/bin/agent-director /tmp/upgraded
# (In a real run, install.sh would fetch a newer binary; for the
# testplan we just rewrite the file.)
cat /tmp/upgraded > $HOME/.agent-director/bin/agent-director
chmod +x $HOME/.agent-director/bin/agent-director

# Step 3: the captured hook path must STILL be callable.
if ! "$HOOK_PATH" --version >/dev/null 2>&1 \
   && ! "$HOOK_PATH" version >/dev/null 2>&1; then
  echo "FAIL: hook path stopped being callable after in-place reinstall"
  exit 4
fi

echo "PASS hook_path=$HOOK_PATH"
```

## Expected outcome

- Exit code 0.
- Stdout: `PASS hook_path=<resolved-absolute-path>`.
- The captured hook command path is an absolute path pointing at
  `$HOME/.agent-director/bin/agent-director` (the standard install
  path) and remains callable after the in-place reinstall.
