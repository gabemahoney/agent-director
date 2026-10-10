# Release Blockers

Open operator-gated questions that must be resolved before this
repository's npm package can be published. Each blocker has a unique
identifier, a description of what it gates, and the steps to resolve it.

---

## H3 — npm package name (RESOLVED 2026-05-24)

**Status:** Resolved.

### Resolution

The npm package built from `pkg/ts-bun-client/` was renamed off the
`@CHANGEME-H3/` placeholder scope and publishes as `agent-director`
(unscoped). It is the only npm package in this repository: it ships no CLI
binary and has no `optionalDependencies` and no per-platform sub-packages
(see [npm packaging and version scripts](architecture.md#npm-packaging-and-version-scripts)).

| Resolved name | Directory |
| --- | --- |
| `agent-director` | `pkg/ts-bun-client/` |

> **History.** As resolved on 2026-05-24, the layout followed the
> [esbuild distribution model](https://esbuild.github.io/getting-started/#download-a-build):
> the unscoped package plus per-platform scoped sub-packages that carried
> each platform's native build, `@agent-director/linux-x64` and
> `@agent-director/darwin-arm64` (`@agent-director/darwin-x64` was dropped
> the same day). b.w3q removed the sub-packages, first released in 0.7.0;
> the package now drives the CLI the host has installed.

### What H3 gated (historical)

- **Epic 5** — npm publish of the packages H3 named (see History above).
- **Epic 7** — the npm-publish step in the coordinated release pipeline.

---

_Add future blockers below using the same template: identifier, status, what it
gates, resolution steps._
