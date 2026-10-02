// Command measure-exit is the in-container driver of the Epic 21 exit-time
// measurement harness (SRD Open Questions RN-6, RN-2, RN-9, with RN-7's
// record inside RN-9). It spawns agents through the v-next agent-director
// binary on a private tmux server, performs exactly one measured action per
// sample (a pane kill, pause's /exit or a natural-exit keystroke) and polls
// until the exit is observed.
//
// The harness has four parts, all under tools/measure-exit/:
//
//   - this Go driver, which runs only inside the measurement container (real
//     mode) or the repo's Docker sandbox (dry mode). It never runs on a host:
//     the isolation preflight refuses without the container marker;
//   - guard.sh, the host-state guard, which only reads the host's real
//     ~/.agent-director files (checksums in quiet-host mode, appended trail
//     bytes in busy-host mode) and never runs an agent-director artifact;
//   - run.sh, the host runner (shell only, print-only unless --run), and
//     Dockerfile, the measurement image built FROM the test image with Claude
//     Code under its own tag (default 2.1.280, the deployed version);
//   - stub/, the dry-run stub claude and the version probe's two stubs (dry
//     runs only; never in the image), and dryrun.sh, the sandbox dry run.
//
// # Subcommands
//
// The first argument names a subcommand from the commands table (main.go):
//
//	measure-exit run -mode real|dry|probe -out DIR [flags]
//	measure-exit decide -in DIR [-in DIR]... [-supersede ID]...
//	measure-exit record -out FILE      (a hook program; payload on stdin)
//
// run measures the selected cases: RN-6 (rn6.go) and RN-2 (rn2.go), sampled;
// RN-9's scenarios with RN-7's record (rn9.go, evaluated in rn9eval.go); and,
// in probe mode, the exec-form version probe (probe.go). decide applies the
// decision rules to results directories (decide.go). record is the RN-9
// recorder the generated layers register (recorder.go).
//
// # Isolation
//
// Before anything else, run's preflight (preflight.go) checks the container
// marker, that TMUX is unset, that neither $HOME nor the passwd-entry home
// holds a .agent-director, the per-case sample floor and (in real mode) the
// Claude Code version floor (2.1.280), plus the gateway's ANTHROPIC_AUTH_TOKEN
// and ANTHROPIC_MODEL (the runner's pinned model) in real mode, which
// refuses ANTHROPIC_API_KEY, the
// OAuth token and the Bedrock/AWS variables (the gateway only) and runs only
// the cases its -cases names; probe mode refuses any credential but
// the runner's dummy token, and dry mode refuses a claude that is not the
// stub and forwards no credential. It then creates a private TMUX_TMPDIR (mode
// 0700) that every agent-director and tmux call receives, and the first
// spawned row's recorded socket must lie under it. Every child process gets
// an environment built from an allowlist (env.go): TMUX, the host's Claude
// Code session variables and AGENT_DIRECTOR_* never reach a child.
//
// The driver never opens the agent-director store. Store reads go through
// the read-only verb get; tmux reads are list-sessions, list-panes and
// capture-pane (an agent team lead's input-ready check and the pane
// captures of a cut-short team run, capture.go) on the private socket,
// or on a claude-swarm server's socket beside it. Every agent-director and tmux call is
// written to the run log (argv only, credential values scrubbed) with its
// action kind, so the operator can audit that only the allowed actions ran.
//
// # Extension points for the cases
//
// A case registers a caseSpec (cases.go) with a stable id, its family (RN-6,
// RN-2 or RN-9) and whether the 20-sample floor applies, and implements
// run(h *harness) caseResult using the primitives in agent.go and poll.go.
package main
