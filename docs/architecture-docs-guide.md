# Architecture Doc Best Practices

The architecture docs describe standards for writing code in this repo. This doc describes guidelines for updating them.

`architecture.md` also describes a high level overview of the system for humans (and a reference guide for LLMs). 

Keep `docs/architecture.md` as a short core with detail in linked `docs/architecture-<topic>.md` pages. Keep the pages flat in `docs/` so the `docs/*.md` test scans cover them. Together they set the architecture guidelines and serve as a quick reference to the design: its components, how they interact, and the decisions behind them. Don't include code, because agents can read the code themselves.

Wherever a guide or doc names `architecture.md` as the place for a fact, read that as the core or the linked page that owns it.

When moving content, keep every current fact and every link working; drop only duplicates, ticket IDs, SR-x IDs and superseded history. Keep reusable test-fixture registrations together in `docs/architecture-test-harness.md`.

Aim for about 500 lines per architecture page, following the size guidance in [Write and trim](user-docs-guide.md#write-and-trim).

## Stay Generic
Agent Director must not build bespoke functionality for CSCB or any of its clients. It supports many potential users and clients and must remain generic.

Architecture docs describe agent-director's general contract, never one client's integration.

## Serve Human, Bot and Code Callers
Agent Director must support human callers, bot callers and code callers. It cannot assume intelligence behind the caller, but it can optionally include information if such an intelligence happens to be present.

Document the machine-readable contract (error names, fields, states) as the contract. Describe advice text as supplementary.

See also "Core Principle: Never Hardcode Claude Code's Terminal Text" in docs/engineering-guide.md, which applies this to outcomes only Claude Code's screen can show: Agent Director never hardcodes Claude Code's strings, patterns or layout to interpret that screen, and the caller may not be an intelligence either.

## Document Re-usable Components
Any time you create a component that is meant to be re-usable, update its owning architecture page (the core or a linked page; test fixtures belong in `docs/architecture-test-harness.md`) with a statement requiring future code authors use it, and say so in its doc comment too. Be sure to describe what it does.

Claude Code often re-invents things that already exists. Noting the re-usable component on its owning architecture page guards against this.

## Keep high level overview up to date
Any time you change something which modifies the high level overview of the system in `architecture.md` you need to update the document to reflect the code changes you made.
