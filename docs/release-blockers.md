# Release Blockers

Open operator-gated questions that must be resolved before the packages in this
repository can be published to npm. Each blocker has a unique identifier, a
description of what it gates, and the steps to resolve it.

---

## H3 — npm package name (RESOLVED 2026-05-24)

**Status:** Resolved.

### Resolution

The npm packages shipped by Epic 5 (`pkg/ts-bun-client/`) have been
renamed off the `@CHANGEME-H3/` placeholder scope. The resolved layout follows
the [esbuild distribution model](https://esbuild.github.io/getting-started/#download-a-build):
an unscoped umbrella package plus per-platform scoped sub-packages.

| Resolved name | Directory |
| --- | --- |
| `agent-director` | `pkg/ts-bun-client/` |
| `@agent-director/linux-x64` | `pkg/ts-bun-client/platforms/linux-x64/` |
| `@agent-director/darwin-arm64` | `pkg/ts-bun-client/platforms/darwin-arm64/` |

The `@agent-director` npm org is claimed separately by the operator before the
first live publish.

> **Platform-set update (2026-05-24).** `@agent-director/darwin-x64` (Intel
> Mac) was dropped from the v1 set on the same day H3 was resolved. The
> remaining packages above are the full v1 publishing set.

### What H3 gated (historical)

- **Epic 5** — npm publish of the packages above.
- **Epic 7** — the npm-publish step in the coordinated release pipeline.

---

## B146 — the relay rewrite ships whole, after CSCB (OPEN)

**Status:** Open.

### What it gates

Any release whose binary carries b.146 step 2 (b.q2i: schema v7, `decide`'s
`delivery`, `max_wait_ms` and `ErrStoreBusy`, the relay hook's ack). Step 2
changes what `decide` returns and when `ErrRelayFallenBack` comes, and schema
v7 is one migration shared with steps 2b (b.3oc) and 2c (b.8t7), so:

- it must not be released without steps 2b and 2c in the same release;
- it should not be released without b.zdz in the same release. `pause`
  types `/exit` and Enter into a `waiting` relay-on row with no relay
  check (no dialog hold, no `ErrSendKeysWhileRelayed`, no
  `ErrRelayFallenBack`), and a `waiting` row can still have a request not
  proven gone (a subagent's after the main agent's `Stop`, or a
  pre-upgrade one on an agent idle at its prompt). Without b.zdz,
  `pause` is an unheld path to the Enter step 2c holds. b.zdz adds
  errors to `pause`, which CSCB consumes, so it adds a b.u7d item; and
- it must not be released before the CSCB coordination bee, b.u7d, is
  finished: CSCB ships its handling of `delivery`, `max_wait_ms`,
  `ErrStoreBusy` and the step 2b and 2c surfaces (step 2c:
  `ErrDialogMaybeOpen`, retried and then shown to a human with a "send
  anyway" that passes the hash of the pane the human looked at; the
  unproven report; and never passing `--expect-pane-sha256` from an
  automatic flow), and bumps its pinned client, first.

### Upgrade notes the release must carry

Release notes are built from commit messages, so the release states these
in commit bodies.

Step 2b (b.3oc): after the store is migrated
to v7, a permission request recorded before the upgrade that fell back by
time and was never decided (typically one answered at the pane after its
relay hook was killed) refuses plain `send-keys` to its agent with
`ErrRelayFallenBack`, in every live state of the row, until it is closed
with `record-pane-answer --as unknown` (or a pane answer), or its row ends
or is marked `missing`. `decide` does not close it, and for such a stale
record may return `ErrNoOpenPermissionRequest` while plain `send-keys`
still returns `ErrRelayFallenBack`. See docs/migration-guide.md, "What the
upgrade changes for an existing request".

Step 2c (b.8t7): the migration to v7 proves gone (`agent_gone`) every
permission request of a row already `ended` or `missing`; every other
request recorded before the upgrade, decided or not, is not proven gone,
so a live relay-on agent with such a request refuses a plain `send-keys`
without `--expect-pane-sha256` with `ErrDialogMaybeOpen` until its main
agent's next `Stop` or idle-prompt Notification, or its row ends or is
marked `missing`. An agent already idle at its prompt (its
idle-prompt Notification already sent) gets neither until a new turn
starts, so its first plain `send-keys` needs a person or an LLM to
`read-pane` and send with that read's `pane_sha256`. `record-pane-answer`
no longer frees plain `send-keys` by itself: the request stays held until
Claude Code proves it gone. See docs/migration-guide.md, "What the upgrade
changes for an existing request".

### Resolution steps

1. Steps 2, 2b and 2c are merged on the release branch, and so is b.zdz
   (`pause` held like a plain `send-keys`).
2. b.u7d is closed: CSCB confirms the release that carries its items (b.zdz's
   new `pause` errors included) and its pinned AD client equals this
   release.
3. The release notes carry the upgrade notes above.
4. Mark this blocker resolved here with the date.

---

_Add future blockers below using the same template: identifier, status, what it
gates, resolution steps._
