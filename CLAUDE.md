# agent-director

## Documentation Locations

- **Engineering best practices**: docs/engineering-guide.md
- **Internal architecture docs**: docs/architecture.md
- **Customer-facing docs**: README.md
- **Test writing guide**: docs/test-writing-guide.md
- **Schema migration guide**: docs/migration-guide.md
- **Doc writing guide**: docs/readme-guide.md, docs/architecture-docs-guide.md

## Running tests / built artifacts

Edit on the host freely, but **never execute this repo's tests or built
binaries on the host** — doing so can silently rewrite the real
`~/.agent-director` store (the b.8dr incident). Host `go test`, `bun test`,
`go run`, and direct `bin/` executions are **denied** by
`.claude/settings.json`.

Run everything inside the sandbox instead:

- `make test-sandbox` — full suite (`go test ./...` + `bun test`)
- `make sandbox CMD="…"` — any one-off command (build, `go generate`, a
  binary run, a bun script)

The make target auto-detects the container engine and platform for you.
See the **run-tests** skill (`.claude/skills/run-tests/`) for usage and how
to read results, and docs/engineering-guide.md "Sandboxed execution" for the
full rationale.
