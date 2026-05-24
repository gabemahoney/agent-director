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

A `prepublishOnly` hook in every `package.json` runs
`pkg/ts-bun-client/scripts/check-not-placeholder.ts`, which exits 1 if
`package.json`'s `name` ever contains `CHANGEME-H3` again. The guard is kept
as a forward-going tripwire against re-introducing a placeholder name in any
future refactor or rename. The matching sentinel regex
(`^@?(CHANGEME-H3|TBD)/`) in `skills/release-agent-director/release.sh` is
kept in place for the same reason; it currently finds zero matches in the
cleaned package.jsons.

---

_Add future blockers below using the same template: identifier, status, what it
gates, resolution steps._
