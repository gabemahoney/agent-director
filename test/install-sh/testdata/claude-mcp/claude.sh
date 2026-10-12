#!/bin/bash
# A stand-in `claude` for the install.sh owned-entries tests. It records every
# call, then answers it:
#
#   --version                  prints a version line
#   mcp get agent-director     replays <CLAUDE_STANDIN_CAPTURE>.stdout (and
#                              .stderr, when the file exists) and exits with the
#                              status in <CLAUDE_STANDIN_CAPTURE>.exit; @HOME@ in
#                              a capture is replaced with $HOME, so a capture of
#                              the real Claude Code, scrubbed to that
#                              placeholder, needs no change here
#   mcp get <another name>     answers as a registration naming the old
#                              directory's binary (a trap for an installer that
#                              matches names loosely, such as agent_director)
#   mcp list                   prints ten servers, agent_director among them
#   mcp add                    refuses an option it does not know before the
#                              `--` (so the form without `--`, `mcp add
#                              agent-director <path> serve --stdio`) exactly
#                              as Claude Code 2.1.280 and later does: stderr
#                              `error: unknown option '--stdio'`, exit 1,
#                              nothing registered. With the `--` it succeeds
#                              and prints what Claude Code prints (`Added
#                              stdio MCP server ... to <scope> config`, scope
#                              local without -s). CLAUDE_STANDIN_ADD_FAIL
#                              makes an add that is accepted fail (exit 1, with
#                              an error on stderr; the value `silent`: none).
#   mcp remove                 succeeds, unless CLAUDE_STANDIN_REMOVE_FAIL is
#                              set (exit 1). Without -s,
#                              CLAUDE_STANDIN_REMOVE_MULTI makes it refuse as
#                              Claude Code does for a name registered in
#                              several scopes (the capture
#                              remove-noscope-multi.stderr beside
#                              <CLAUDE_STANDIN_CAPTURE>, exit 1);
#                              CLAUDE_STANDIN_REMOVE_NONE makes it answer a name
#                              that is registered nowhere with the not-found text
#                              of `mcp get` (none-found-many-others.stderr
#                              beside the capture, exit 1; Claude Code builds
#                              both from one function, not captured for remove)
#
# Each call is appended to $CLAUDE_STANDIN_LOG as a line
#   CALL<TAB>working directory<TAB>new_bin<TAB>kept
# followed by one `ARG<TAB>argument` line per argument. new_bin is 1 when
# ~/.config/agent-director/bin/agent-director exists at the call, and kept is
# 1 when a ~/.agent-director.pre-move-* directory does: a call that is made
# after the move has both at 1.

: "${CLAUDE_STANDIN_LOG:?}"
new_bin=0
[[ -e "$HOME/.config/agent-director/bin/agent-director" ]] && new_bin=1
kept=0
for d in "$HOME"/.agent-director.pre-move-*; do
    [[ -e "$d" ]] && kept=1
done
{
    printf 'CALL\t%s\t%s\t%s\n' "$PWD" "$new_bin" "$kept"
    for a in "$@"; do
        printf 'ARG\t%s\n' "$a"
    done
} >>"$CLAUDE_STANDIN_LOG"

# replay <file>: print the file with @HOME@ filled in, line by line.
replay() {
    local line
    while IFS= read -r line || [[ -n "$line" ]]; do
        printf '%s\n' "${line//@HOME@/$HOME}"
    done <"$1"
}

if [[ "$1" == --version ]]; then
    echo "2.1.999 (Claude Code stand-in)"
    exit 0
fi
[[ "$1" == mcp ]] || exit 0
case "$2" in
    get)
        if [[ "$3" == agent-director ]]; then
            cap="${CLAUDE_STANDIN_CAPTURE:?}"
            [[ -f "$cap.stdout" ]] && replay "$cap.stdout"
            [[ -f "$cap.stderr" ]] && replay "$cap.stderr" >&2
            exit "$(<"$cap.exit")"
        fi
        printf '%s:\n  Scope: User config (available in all your projects)\n  Status: ✓ Connected\n  Type: stdio\n  Command: %s/.agent-director/bin/agent-director\n  Args: serve --stdio\n\nTo remove this server, run: claude mcp remove "%s" -s user\n' "$3" "$HOME" "$3"
        ;;
    list)
        for n in alpha bravo charlie delta echo foxtrot golf hotel india agent_director; do
            printf '%s: /opt/mcp/%s serve - ✓ Connected\n' "$n" "$n"
        done
        ;;
    add)
        shift 2
        scope=local
        name=""
        after_dashes=0
        cmd_words=()
        while [[ $# -gt 0 ]]; do
            a="$1"
            shift
            if [[ "$after_dashes" -eq 1 ]]; then
                cmd_words+=("$a")
                continue
            fi
            case "$a" in
                --) after_dashes=1 ;;
                -s | --scope)
                    scope="$1"
                    shift
                    ;;
                -e | --env | -t | --transport)
                    shift
                    ;;
                -*)
                    echo "error: unknown option '$a'" >&2
                    exit 1
                    ;;
                *)
                    if [[ -z "$name" ]]; then
                        name="$a"
                    else
                        cmd_words+=("$a")
                    fi
                    ;;
            esac
        done
        if [[ -n "$CLAUDE_STANDIN_ADD_FAIL" ]]; then
            [[ "$CLAUDE_STANDIN_ADD_FAIL" == silent ]] || echo "Error: could not write the MCP configuration" >&2
            exit 1
        fi
        echo "Added stdio MCP server $name with command: ${cmd_words[*]} to $scope config"
        case "$scope" in
            project) echo "File modified: $PWD/.mcp.json" ;;
            user) echo "File modified: $HOME/.claude.json" ;;
            *) echo "File modified: $HOME/.claude.json [project: $PWD]" ;;
        esac
        ;;
    remove)
        shift 2
        scope=""
        while [[ $# -gt 0 ]]; do
            case "$1" in
                -s | --scope)
                    scope="$2"
                    shift
                    ;;
            esac
            shift
        done
        if [[ -n "$CLAUDE_STANDIN_REMOVE_FAIL" ]]; then
            echo "Error: could not write the MCP configuration" >&2
            exit 1
        fi
        if [[ -z "$scope" && -n "$CLAUDE_STANDIN_REMOVE_MULTI" ]]; then
            replay "$(dirname "${CLAUDE_STANDIN_CAPTURE:?}")/remove-noscope-multi.stderr" >&2
            exit 1
        fi
        if [[ -n "$CLAUDE_STANDIN_REMOVE_NONE" ]]; then
            replay "$(dirname "${CLAUDE_STANDIN_CAPTURE:?}")/none-found-many-others.stderr" >&2
            exit 1
        fi
        echo "Removed MCP server agent-director from ${scope:-local} config"
        ;;
esac
exit 0
