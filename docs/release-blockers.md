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

### Publish guard (still active)

A `prepublishOnly` hook in every sub-package `package.json` runs
`pkg/ts-bun-client/scripts/prepublish-guards.ts` with
`PREPUBLISH_GUARD_MODE=subpackage`, which exits 1 if `package.json`'s `name`
ever matches the placeholder sentinel set again. The guard is kept as a
forward-going tripwire against re-introducing a placeholder name in any future
refactor or rename. The canonical sentinel regex (`PLACEHOLDER_RE`,
`/^@?(CHANGEME-H3|TBD)\//`) is defined once at the top of
`prepublish-guards.ts` — a one-line change adds any new sentinel (SR-3.3).

---

_Add future blockers below using the same template: identifier, status, what it
gates, resolution steps._
