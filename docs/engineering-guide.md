# Engineering Guide

Background for the agent-director rules in the Project's `guide:engineering-best-practices.md`, which wins where the two differ. General engineering, testing, release and review rules live in the Project's guides, not here.

## Core Principle: Stay Generic

**Agent Director must not build bespoke functionality for CSCB or any of its clients.** It supports many potential users and clients and must remain generic.

- **Design a client's request as a general capability.** Build what any caller could use, not what one client asked for.
- **No client specifics in the product.** Client names, client-specific behaviour and client-specific wording never go into code, errors or the API.

## Core Principle: Serve Human, Bot and Code Callers

**Agent Director must support human callers, bot callers and code callers.** It cannot assume intelligence behind the caller, but it can optionally include information if such an intelligence happens to be present.

- **Every outcome a caller must act on is machine-readable** — an error name or a structured field.
- **English advice is an optional extra.** Advice in a description helps humans and agents, but it is never the only way to learn the next step.
- **Prefer behaviour that's safe to repeat over advice to follow.**

See also "Core Principle: Never Hardcode Claude Code's Terminal Text" below, which applies this to outcomes only Claude Code's screen can show.

## Core Principle: Ship Features to Every Supported Agent at Once

**A new feature lands for every supported agent (Claude Code, Codex and Cursor) in the same release, or as close to it as the agents allow.** Callers should not have to learn which agent got a capability first.

- **Plan for all supported agents from the PRD onward.** Requirements, design and tests cover each one; a feature is not complete while one is missing without a stated reason.
- **When an agent genuinely can't support it yet, say so explicitly.** Record the evidence (what was measured and why it falls short), make the gap a documented, machine-readable outcome for that agent (for example an `unknown` value), and keep a follow-up ticket to close it. Never leave it silently unsupported.
- **Measure before deciding an agent can't.** Lack of a ready signal is a reason to investigate, not a reason to skip.

## Core Principle: Never Hardcode Claude Code's Terminal Text

**Agent Director's product code never hardcodes Claude Code's strings, patterns or layout** — no string, regex, layout rule, line position or glyph that encodes what Claude Code's prompts, menus, dialogs or key layouts mean. That text and layout change between Claude Code releases without notice. Reading the pane is not the problem; giving it a meaning written into the code is.

- **Forbidden: knowledge of Claude Code's UI written into product code.** For example: matching "Do you want to proceed?", "Yes" or `❯`; treating the last two lines as the status bar; treating option 1 as "allow".
- **Allowed: content-agnostic use of what Agent Director itself captured from the pane.** For example: checking whether the pane changed between capture A and capture B, byte for byte, by hash or by diff (such as refusing to send keys when the pane's hash no longer matches the one the caller read); picking strings or substrings out of an earlier capture (a whole region or just a few words) and searching the current pane for them, to see whether each is still present, has moved or has changed. That search does match text, but the text comes from Agent Director's own earlier capture, not from a Claude Code literal written into the code (as long as the piece was not chosen by a Claude Code string or layout rule). None of these encodes meaning, so none breaks when Claude Code changes its wording.
- **Learn Claude Code's state only from its documented interfaces:** hooks and their JSON, settings, exit codes and transcript files.
- **When only the screen can tell what is happening, report a machine-readable outcome.** Return an error name that means "something that can look at this session must decide and send keys", and leave the looking and the typing to the caller. `read-pane` and `send-keys` carry text between the pane and the caller and never interpret it. (Formatting is not interpretation: `read-pane` removes ANSI escape codes by default, and `send-keys` removes CR bytes and adds Enter.)
- **The caller may not be an intelligence either.** It may be code, such as a relay library or a bot server, that can no more read a screen than Agent Director can. The outcome must be something such a caller can pass unchanged up its own chain until it reaches an intelligence (an agent or a human) that can look, decide and type. Never assume the first caller is that intelligence, and never rely on English advice to carry this outcome.
- **Tests may use fake screens; product code may not hardcode Claude Code's.** No product code path branches on a Claude Code string literal, pattern or layout rule.

## 2. CLI/MCP parity

Every CLI verb must have an MCP-tool equivalent, and both must go through the shared `pkg/api` client — no capability may exist on one surface only, and no surface may bypass `pkg/api` to reach the store directly. New flags/parameters count: adding a flag to a verb means adding the matching parameter to the MCP tool *in the same change*. Agents driving agent-director over MCP must never be less capable than a human on the CLI. This parity is only partly structural: the MCP tool list (`internal/mcp/server.go` iterates `pkg/api/manifest.Verbs`) and both reference docs (`docs/cli-reference.md` and `docs/mcp-reference.md`, generated by `tools/gen-docs` from `pkg/api/manifest.Verbs`) come from that single verb list, but the CLI does not — its verbs are hand-wired in the `handlers()` map in `cmd/agent-director/main.go` with hand-written per-verb flag parsers (`spawn_cmd.go`, `flags.go`), and no non-test source under `cmd/agent-director` imports `pkg/api/manifest`. CLI-side parity is instead structurally checked by `cmd/agent-director/cli_manifest_parity_test.go`, which fails when the CLI's verbs and the manifest's verbs differ, a main-CLI flag is not its verb's manifest param, a manifest param has no flag, a flag has no usage text, or a flag set cannot be tied to a verb. It does not see arguments a verb parses by hand without the flag package. When you add or change a verb or flag, you must still hand-update the CLI handler to match the manifest. **Deliberate exceptions** (the only verbs that are CLI-only, filtered out of the MCP tool list by `internal/mcp.ExposedVerb`): `hook` (internal — invoked only by Claude Code's lifecycle-hook machinery, reads a payload from stdin), `serve` (starts the MCP server itself — exposing it as a tool would be self-referential), `trail-emit` (a CLI-only write path that runs without opening `state.db` for corrupted-state recovery, with no MCP dispatch handler), and `cursor-host` (internal launch plumbing: the Cursor row's direct pane gateway owns one ACP runtime and its private pipes; it is not a public control operation and cannot be started through MCP). There are no other exceptions: every other verb maps 1:1 to an MCP tool with identical parameters. `agent-director-admin` is a separate operator tool outside this CLI/MCP parity rule. When you add a new verb or flag, either it lands on both surfaces or you add it to the `ExposedVerb` exception list *and* document why here.

## 10. Sandboxed execution (`make test-sandbox`)

This section explains the sandbox. Where each command runs on this Project's
host (test suites go to the job scheduler) is set by the Project's
`guide:build-commands.md`.

### Why this exists

The b.8dr incident: running `bun test` on the b.aaj branch (schema v3
migration code) auto-migrated the production `~/.agent-director/state.db`
(agent-director's directory then; it is `~/.config/agent-director` now, and a
host that has not yet moved still has the old one, so every guard below covers
both)
from v2 to v3, breaking all consumers of the installed v0.7.8 binary.
Host-side `$HOME` redirection is not a reliable boundary: a test that does
not redirect it, an absolute path to the real store, a child binary started
with the host's environment, or a home lookup that reads `/etc/passwd`
instead of `$HOME` (a spawn cwd's `~` resolves that way, SRD §7.2) all still
reach real host state. The store also chmods the DB 0600 on every open,
preventing file-permission workarounds. A container whose HOME has neither
`.config/agent-director` nor the old `.agent-director` is the only isolation
boundary that holds.

**The isolation invariant.** All agent-director state — both the store
(`state.db`) and the trail (`ad-trail.jsonl`) — lives at
`~/.config/agent-director`, resolved from the invocation's effective home
(`$HOME` only; `XDG_CONFIG_HOME` is never read). The persistent
environment-variable relocation switch is gone: no env var and no config key
relocates that path; state always resolves from the effective home (via
`$HOME` / `os.UserHomeDir`) or an explicit `--store-path`. The per-invocation
global flags `--home` and `--store-path` are request-scoped targeting — they
point one invocation at a different home or store, not a persistent
relocation mechanism, and the trail follows the effective home. Isolation is
therefore not something you configure via redirection; it is achieved solely
by the sandbox, a container whose HOME has neither directory. Inside that boundary tests
isolate individual cases by redirecting `$HOME` to a temp directory; that
`$HOME` redirection is never the outer boundary (it does not stop the paths
above), only per-test hygiene within the container.

### The rule: edit on the host, execute in the sandbox

Editing source on the host is always fine — nothing runs. The danger is
*execution*. Never run a built artifact on the host: not `go test`, not a
one-off `./bin/agent-director …`, not `go generate`, not a bun script.
Every executed artifact can open the store. Run all of it through
`make test-sandbox` / `make sandbox-shell` / `make sandbox CMD="…"`, which
put the process inside a container whose HOME has neither `.config/agent-director`
nor `.agent-director`. This holds on every branch; a schema change also
follows docs/migration-guide.md.

### Fail-fast sandbox guard

The `make sandbox*` targets export `AGENT_DIRECTOR_TEST_SANDBOX=1` into the
container. `TestMain` in the state/exec-touching Go packages and the bun
preload (`pkg/ts-bun-client/test/setup.ts`) check that marker and **refuse to
run without it** — a stray host-side `go test`/`bun test` fails immediately
with a message pointing here, before it can touch the real
`~/.config/agent-director` (or the old `~/.agent-director`; the refusal names
both). The Go check is `sandboxguard.Require()` from
`internal/testsupport/sandboxguard`, called at the top of `TestMain`.

This is an accident-prevention gate for humans and agents alike, **not a
security boundary** (the marker is a plain env var). It complements — does not
replace — the `test/smoke/go` snapshot canary, which stays as-is.

**The CI bypass (`BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS`).** The Go guard
has a second, explicit way through: `sandboxguard.Require()` also proceeds when
`BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` is set (`sandboxguard.BypassEnvVar`).
Absence of *both* variables still refuses — the gate is fail-closed, and the
refusal message names both escape hatches. The bypass exists for one situation
only: a runner that has **no real `~/.config/agent-director` or old
`~/.agent-director` to damage**, where
containerizing the run protects nothing. An ephemeral GitHub-hosted runner is
that situation; a development host never is. On a host with a store, use
`make test-sandbox` — setting the bypass there reintroduces b.8dr. The bypass is
Go-only; the bun preload still checks the sandbox marker alone.

Prefer the bypass over exporting `AGENT_DIRECTOR_TEST_SANDBOX=1` in a workflow.
Setting the marker outside the container makes the process *lie about where it
is* and defeats the guard for anything that later reads the marker; the bypass
states the actual intent ("there is nothing here to protect").

**Guard criterion — which packages carry the guard:** every package whose
tests write agent-director state (open the store, emit trail events) or exec a
built binary. Currently: `internal/trail`, `internal/store`, `internal/hook`,
`internal/spawn`, `pkg/api`, `internal/mcp`, `cmd/agent-director`,
`internal/testsupport/storefix`, `internal/testsupport/tmuxfix`,
`test/smoke/go`, `test/smoke/ts-helper`, `test/envelope-diff`,
`test/grounding-replay`, `test/realtmux`, `test/reboot-recovery`,
`test/tla`, `test/driver-scripts`,
`tools/measure-exit` and `tools/measure-exit/dryrun`. Pure-logic packages with no
state or exec surface (e.g. `pkg/api/manifest`, `pkg/api/errnames`) may skip
it, as do the `test/sandbox/` Makefile tests (`gitmount`, `cmdinject`,
`prebuild`, `cigates`), which run only `make` and shell fakes. When you add a package that
opens the store or execs a binary, add `sandboxguard.Require()` to its
`TestMain`.

**CI:** run the suite via `make test-sandbox` (which sets the marker), or — on a
GitHub-**hosted**, ephemeral runner only — set
`BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS: "1"` in the workflow step that runs
`go test` directly. Never export `AGENT_DIRECTOR_TEST_SANDBOX` in a workflow.

The bypass carries a hard placement rule (b.175). It may be set **only at the
`job:`/`step:` level of the GitHub-hosted workflows** — today `go-smoke.yml` and
`integration.yml`, each with a comment at the set-site explaining why it is safe
there. It must **never** be set:

- as a repository- or organization-level Actions variable or secret — those
  apply to every workflow, including the self-hosted one; or
- anywhere in `.github/workflows/pre-release-verify-mac.yml`, which runs on
  `[self-hosted, macOS, ARM64]` — a persistent machine that plausibly holds a
  real `~/.config/agent-director` or old `~/.agent-director`.

Because there is no store-presence probe, workflow-authoring discipline is the
only thing protecting that self-hosted runner, so it is enforced mechanically:
`make check-sandbox-bypass` greps `pre-release-verify-mac.yml` for the bypass
string and fails if it appears (or if the file is missing). The doc-drift
workflow runs it on every PR and push to `main`.

### Targets

```
make test-sandbox        # full suite: build pkg/ts-bun-client, then go test ./... AND bun test
make sandbox-shell       # interactive bash inside the container + mounts
make sandbox CMD="…"     # run an arbitrary command in the container + mounts
make test-install-sh-advice  # install.sh's literal-follow tests alone (test-sandbox runs them too)
```

Before either suite, `make test-sandbox` runs `bun install --frozen-lockfile`
and `bun run build` in `pkg/ts-bun-client` (b.2b3). Its `node_modules/` and
`dist/` are gitignored, so a fresh worktree or clone has neither, and tests in
both suites read `dist/`. If either step fails, the target fails (non-zero
exit) with `ERROR: test-sandbox: …; no tests ran.` and runs no tests. A
`make sandbox CMD="…"` run does not install or build; do both first when
running a test that reads `dist/` on its own.

Every `go test` the Makefile runs (`test`, `test-sandbox`, `err-coherence`,
`release-smoke`) passes `-timeout $(GO_TEST_TIMEOUT)`, 30m by default, instead
of Go's 10-minute default. That limit is a budget for a whole package. On the
loaded shared dev host the largest packages came near or past 10 minutes with
no test hung, so a run failed for no reason (b.yo5). 30m is about three times
the slowest package seen, so only a real hang trips it. On a slower machine,
raise it: `make test-sandbox GO_TEST_TIMEOUT=60m`. A one-off `make sandbox
CMD="go test …"` does not get it; pass `-timeout` yourself.

`install.sh`'s literal-follow tests (`test/install-sh/advice_follow.sh`) skip
the ones whose advice is known not to work as written;
`AGENT_DIRECTOR_RUN_KNOWN_BROKEN_ADVICE=1` runs them
(docs/test-writing-guide.md "Literal-follow tests for error advice"). A few
of them build the previous release's binary and store from its git tag and
skip when the checkout lacks it (a checkout without the `v0.11.1` tag; no
workflow runs `test/install-sh`, so nothing in CI depends on it).

The suite exit code propagates to the caller; output streams live. The
image is built on first use and cached by the engine's layer cache
thereafter — subsequent invocations with an unchanged Dockerfile are fast
no-ops.

### Engine and host detection (Makefile-owned)

The `make sandbox*` targets are engine- and host-generic: the image
(`test/sandbox/Dockerfile`), the run-tests skill, and the CLAUDE.md note
mention no engine or flags. **All environment detection lives in the
Makefile.** Every run prints a `[sandbox] engine=… net=… pid=… uidmap=…`
line so it is self-documenting. What the Makefile decides:

- **Container engine** — prefers `podman`, else `docker`. Override with
  `make test-sandbox CONTAINER_ENGINE=docker`.
- **Uid mapping** — podman uses `--userns=keep-id` (maps the container's
  non-root `sandbox` user to the invoking host user); docker uses
  `--user $(id -u):$(id -g) --group-add 0`. Running non-root is deliberate:
  some tests assert a filesystem permission is *denied* (e.g.
  `internal/trail`'s read-only-dir case), and a root container would bypass
  DAC checks and break them. `keep-id` is podman-only (it errors on docker),
  hence the split. The docker `--user` uid is an arbitrary host uid that does
  not own the image's uid-1000 HOME, so `--group-add 0` joins group 0 and the
  image makes HOME gid-0-writable — that is what lets the docker process
  populate GOPATH/GOCACHE/bun cache. (The podman leg maps to uid 1000 and owns
  HOME outright, so it needs neither `--user` nor `--group-add`.)
- **Network namespace** — always `--network=host`, for both the image build
  and every run target. Host networking sidesteps the bridge-vs-uplink MTU
  blackhole seen 2026-09-19 on the DGXC/k8s pod: docker's default bridge
  (`docker0` MTU 1500) sits above the pod uplink (`eth0` MTU 1460), so large
  inbound TLS segments from Fastly (`release-assets.githubusercontent.com`,
  serving the bun release asset) exceed the uplink MTU, the PMTUD ICMP is
  dropped, and the handshake stalls forever — hanging the bun download during
  the image build. Host netns has no such MTU step, and also covers the
  original no-`/dev/net/tun` case (rootless network backend fails), so the old
  tun detection is superseded (b.rx8). Safe: the sandbox boundary is the
  filesystem/HOME, not networking, and no sandbox target publishes ports.
- **PID namespace** — default (isolated) on a normal host; falls back to
  `--pid=host` only when a fresh `/proc` mount at container start is blocked
  (the k8s pod masks `/proc`, so the new-PID-namespace proc mount gets
  `EPERM`). This is the one signal that needs a real container probe, so it
  is probed once right after the image builds and **cached** in a per-engine
  tmp marker; repeat runs read the marker and add no latency (delete it to
  re-probe).
- **`SANDBOX_FLAGS`** — appended to `run` last, so it overrides detection for
  any host the heuristics miss, e.g.
  `make test-sandbox SANDBOX_FLAGS="--pid=host"`.
- **`DOCKER_CONFIG`** — neutralized (`DOCKER_CONFIG=`) only for podman,
  because a stale host value pointing at a nonexistent `~/.docker` aborts the
  run; docker keeps it (it needs it for auth).
- **`BUILDAH_ISOLATION=chroot`** is set on the *build* so podman's RUN steps
  work where `/proc` remounting is blocked; it is a podman/buildah variable
  that docker ignores.

On the DGXC/k8s pod this repo is developed on (docker engine), detection lands
on `net='--network=host' pid='' uidmap='--user 1000:1000 --group-add 0'` (the
`[sandbox]` line prints `engine=docker`; the docker probe yields an empty pid
flag — an isolated PID namespace — on this pod). On a laptop with docker it
lands on `net='--network=host'`, an isolated PID namespace, and
`--user $(id -u):$(id -g)` (only the network flag is now unconditional; PID and
uid mapping still vary by host).

### Known caveats

The sandbox removes the ambient system install (a clean HOME with neither
`~/.config/agent-director` nor the old `~/.agent-director`) and shares one
`/work` mount across the parallel test run. A caveat to be aware of:

- **One bun serialization test is timing-sensitive** (asserts a >5 ms gap
  between serialized spawns); on this fast host it occasionally measures
  ~4 ms and flakes.

The `test/smoke/go` canary — which snapshots the real container HOME's
`~/.config/agent-director` and old `~/.agent-director` before and after the
smoke package's `m.Run()` and fails if any file appears or changes in either
— is a true regression guard and must stay quiet on a
clean tree. It previously fired *intermittently* under `go test ./...`, and
that was a real defect, not expected behaviour: `trail.Default()` is a
process-wide `sync.Once` singleton that pins its file path from `$HOME` on the
first `Emit` and never re-resolves. Two test packages
(`pkg/api/apitest` and `test/envelope-diff`) drove in-process store/verb calls
that emit trail events without redirecting `$HOME` in a package `TestMain`, so
their singleton pinned the real container HOME and appended
`ad-trail.jsonl` there. Because `go test ./...` runs packages in parallel,
that write sometimes landed inside the smoke canary's before/after window and
tripped it (and was quiet when the timing missed). Both packages now redirect
`$HOME` to a temp dir in their `TestMain` before `m.Run()` (matching
`internal/store`, `pkg/api`, and `internal/hook`), so the singleton can only
ever resolve under a throwaway dir. If the canary fires now, it is reporting a
genuine leak — do not dismiss it.

### CI parity and docker-leg verification

The sandbox image (`test/sandbox/Dockerfile`) uses `debian:bookworm-slim`
with no host paths baked in. It can be used in GitHub Actions without
modification: mount the checkout at `/work` and the Go module cache at
`/go/pkg/mod`. The hosted-runner test workflows (`go-smoke.yml`,
`integration.yml`) do not do this today — they run `go test` directly on the
ephemeral runner and set `BYPASS_CONTAINER_FOR_AGENT_DIRECTOR_TESTS` instead,
because a runner with neither `~/.config/agent-director` nor `~/.agent-director` has nothing for the container to
protect (see §10).

The **docker leg** of the harness is exercised on the DGXC/k8s pod this repo
is developed on (docker 29.7.2, no podman on PATH, so `CONTAINER_ENGINE`
resolves to docker). There `make test-sandbox` detects host-network +
`--user $(id -u):$(id -g) --group-add 0` + an isolated PID namespace and runs
the full suite. The **podman leg** cannot be exercised on that pod (no podman
installed), so it is implemented to podman's documented semantics
(`--userns=keep-id`, `DOCKER_CONFIG=` neutralized, `BUILDAH_ISOLATION=chroot`
on the build, `--network=host`). **GitHub Actions is the free verification
path for the docker leg** — its runners have docker natively, so a CI job that
runs `make test-sandbox` (engine auto-detected as docker) validates it; the
podman leg still relies on its documented semantics until exercised on a
podman host.

## 11. Commit subjects

The rule is in the Project's `guide:engineering-best-practices.md`. Why the
subject matters: the release-notes generator (`notes.generate` in
docs/release-skill.md) groups commits by the Idea's or Bug's ID with
`EPIC_RE = /^\w+\((b\.\w+)\)/` in
`pkg/ts-bun-client/scripts/generate-release-notes.ts`, matched against the
subject. A subject that regex can't lead-match (missing parentheses, a dropped
`b.` prefix, leading whitespace, an emoji, or the ID only in the body) drops
the commit from its group in the notes; the work still ships. Commits with no
ticket (release chores, standalone doc fixes) use a plain scope or none:
`chore: release v0.7.8`. If `EPIC_RE` changes, update this section.
