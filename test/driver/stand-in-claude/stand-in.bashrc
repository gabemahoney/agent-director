# Read by stand-in-claude/claude's interactive bash in place of ~/.bashrc.
# Claude Code exits on SIGTERM, which keyless pause sends (SR-6.4); an
# interactive bash would ignore it.
trap 'exit 143' TERM
