# agent-director

## Documentation Locations

- **Engineering rules**: docs/engineering-guide.md "Rules"
- **Build and test commands**: docs/engineering-guide.md §10 "Build and test commands"
- **Test rules and conventions**: docs/test-writing-guide.md
- **Doc rules, and the docs to keep current**: docs/user-docs-guide.md, plus docs/architecture-docs-guide.md
- **Bug writing guide**: docs/bug-writing-guide.md
- **Schema migration guide**: docs/migration-guide.md

**Commit subjects** follow docs/engineering-guide.md "Rules":
the scope is the Idea's or Bug's ID (`fix(b.zr5): …`).

## Running tests / built artifacts

Edit on the host freely, but **never execute this repo's tests or built
binaries on the host** — doing so can silently rewrite the real
`~/.config/agent-director` store, or the `~/.agent-director` one it replaced on
a host not yet moved (the b.8dr incident hit the latter). Host `go test`, `bun test`,
`go run`, and direct `bin/` executions are **denied** by
`.claude/settings.json`.

Run them as docs/engineering-guide.md §10 "Build and test commands" says:
one-off commands in `make sandbox CMD="…"`, test suites in an isolated
environment. See docs/engineering-guide.md "Sandboxed execution" for the full
rationale.
