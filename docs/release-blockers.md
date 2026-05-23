# Release Blockers

Open operator-gated questions that must be resolved before the packages in this
repository can be published to npm. Each blocker has a unique identifier, a
description of what it gates, and the steps to resolve it.

---

## H3 — npm package name (OPEN)

**Status:** Unresolved. No publish may proceed until H3 is claimed.

### What H3 is

The four npm packages shipped by Epic 5 (`pkg/ts-bun-client/`) currently carry
the placeholder scope `@CHANGEME-H3/`. The placeholder is intentional: any
accidental `npm publish` fails on the invalid scope, and every site that needs
updating is grep-able via `CHANGEME-H3`.

The four packages are:

| Package | Directory |
| --- | --- |
| `@CHANGEME-H3/agent-director` | `pkg/ts-bun-client/` |
| `@CHANGEME-H3/agent-director-linux-x64` | `pkg/ts-bun-client/platforms/linux-x64/` |
| `@CHANGEME-H3/agent-director-darwin-x64` | `pkg/ts-bun-client/platforms/darwin-x64/` |
| `@CHANGEME-H3/agent-director-darwin-arm64` | `pkg/ts-bun-client/platforms/darwin-arm64/` |

### What H3 gates

- **Epic 5** — npm publish of the four packages above.
- **Epic 7** — the npm-publish step in the coordinated release pipeline depends
  on the real scope being set before it runs.

### Resolution steps

1. **Claim the scope.** Operator registers or confirms a scoped npm org (e.g.
   `@my-org`) on npmjs.com.
2. **Update the four `package.json` files in lockstep.** Replace every
   `@CHANGEME-H3/` occurrence with the real scope in:
   - `pkg/ts-bun-client/package.json`
   - `pkg/ts-bun-client/platforms/linux-x64/package.json`
   - `pkg/ts-bun-client/platforms/darwin-x64/package.json`
   - `pkg/ts-bun-client/platforms/darwin-arm64/package.json`
3. **Update `optionalDependencies`.** The top-level `package.json` references
   the three sub-packages by name; these references must also be updated.
4. **Update `README.md`.** Replace all `@CHANGEME-H3/` references with the real
   scope in `pkg/ts-bun-client/README.md`.
5. **Update `docs/architecture.md`.** Replace placeholder scope strings in the
   TS/Bun client section.
6. **Remove the `// BLOCKED-on-H3` comment fields** from all four
   `package.json` files.
7. **Smoke-test via a test registry.** Run `npm publish --dry-run` from each of
   the four package directories; confirm each exits 0 (prepublishOnly guard
   passes, scope resolves).
8. **Publish publicly.** Run the Epic 7 coordinated release pipeline.

### Publish guard

A `prepublishOnly` hook in every `package.json` runs
`pkg/ts-bun-client/scripts/check-not-placeholder.ts`, which exits 1 if
`package.json`'s `name` still contains `CHANGEME-H3`. The guard fires before
any registry communication, so an accidental `npm publish` with the placeholder
in place is stopped immediately.

---

_Add future blockers below using the same template: identifier, status, what it
gates, resolution steps._
