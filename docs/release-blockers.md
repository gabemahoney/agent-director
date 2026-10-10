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

- it must not be released without steps 2b and 2c in the same release; and
- it must not be released before the CSCB coordination bee, b.u7d, is
  finished: CSCB ships its handling of `delivery`, `max_wait_ms`,
  `ErrStoreBusy` and the step 2b and 2c surfaces, and bumps its pinned
  client, first.

### Upgrade note the release must carry

Release notes are built from commit messages, so the release that carries
step 2b states this in a commit body (b.3oc): after the store is migrated
to v7, a permission request recorded before the upgrade that fell back by
time and was never decided (typically one answered at the pane after its
relay hook was killed) refuses plain `send-keys` to its agent with
`ErrRelayFallenBack`, in every live state of the row, until it is closed
with `record-pane-answer --as unknown` (or a pane answer), or its row ends
or is marked `missing`. `decide` does not close it, and for such a stale
record may return `ErrNoOpenPermissionRequest` while plain `send-keys`
still returns `ErrRelayFallenBack`. See docs/migration-guide.md, "What the
upgrade changes for an existing request".

### Resolution steps

1. Steps 2, 2b and 2c are merged on the release branch.
2. b.u7d is closed: CSCB confirms the release that carries its items and its
   pinned AD client equals this release.
3. The release notes carry the upgrade note above.
4. Mark this blocker resolved here with the date.

---

_Add future blockers below using the same template: identifier, status, what it
gates, resolution steps._
