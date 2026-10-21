# Bug Writing Guide

How to write up an agent-director Bug. The filer records what was seen; whoever triages it validates and reproduces it, so no root cause, fix or acceptance criteria are needed.

## Report

A Bug's `## Report` lets someone who has never seen the problem reproduce it and see exactly what's wrong. Write it under these headings, in this order:

- **Short description:** one or two sentences on what's wrong, and when it happened (absolute dates, e.g. `2026-10-20`).
- **Host and environment:** everything needed to reproduce it in the same conditions, always including `agent-director version` output (version, commit, `config_dir`), the installed release or the dev build and its branch, host or sandbox, and the agent (Claude Code, Codex or Cursor) and its version when it matters.
- **Steps performed:** numbered, in order, with exact commands, flags, inputs and starting state. Write any step that runs this repo's tests or binaries as docs/engineering-guide.md §10 "Build and test commands" runs them, never as a bare `go test`, `bun test`, `go run` or `bin/` run, because those can rewrite the real agent-director store.
- **Expected result:** what should have happened, naming the doc or contract that says so when there is one.
- **Actual result:** what happened instead, with exact errors and output verbatim, including `err_name`.
- **Logs and telemetry:** the relevant log lines, trail events or transcript excerpts, or paths to them on disk.

Keep each report to one bug, and state facts you saw rather than guesses about the cause. If it doesn't reproduce reliably, say so and list what you tried.
