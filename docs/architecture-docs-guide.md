# Architecture Doc Best Practices

`architecture.md` describes standards for writing code in this repo. This doc describes guidelines for updating architecture.md.

`architecture.md` also describes a high level overview of the system for humans (and a reference guide for LLMs). 

Architecture lives in that one file. It sets the architecture guidelines and serves as a quick reference to the design: its components, how they interact, and the decisions behind them. Don't include code, because agents can read the code themselves.

## Stay Generic
Agent Director must not build bespoke functionality for CSCB or any of its clients. It supports many potential users and clients and must remain generic.

Architecture docs describe agent-director's general contract, never one client's integration.

## Serve Human, Bot and Code Callers
Agent Director must support human callers, bot callers and code callers. It cannot assume intelligence behind the caller, but it can optionally include information if such an intelligence happens to be present.

Document the machine-readable contract (error names, fields, states) as the contract. Describe advice text as supplementary.

See also "Core Principle: Never Hardcode Claude Code's Terminal Text" in docs/engineering-guide.md, which applies this to outcomes only Claude Code's screen can show: Agent Director never hardcodes Claude Code's strings, patterns or layout to interpret that screen, and the caller may not be an intelligence either.

## Document Re-usable Components
Any time you create a component that is meant to be re-usable, update architecture.md with a statement requiring future code authors use it, and say so in its doc comment too. Be sure to describe what it does.

Claude Code often re-invents things that already exists. Noting the re-usable component in architecture.md guards against this.

## Keep high level overview up to date
Any time you change something which modifies the high level overview of the system in `architecture.md` you need to update the document to reflect the code changes you made.