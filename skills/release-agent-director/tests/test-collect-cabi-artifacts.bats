#!/usr/bin/env bats
#
# Tests for collect_cabi_artifacts() in release.sh.
#
# Stubs `gh` on PATH and exercises:
#   - single-match success → populates ./dist/cabi/<platform>/ for the
#     three v1 platforms + canonical header.
#   - zero-match halt with a specific error message.
#   - multi-match halt with a specific error message.
#
# Requires bats-core (bats >= 1.7) and `git` available. No network.

setup() {
    BATS_TEST_TMPDIR_FAKE="$(mktemp -d)"
    cd "$BATS_TEST_TMPDIR_FAKE"

    git init -q .
    git config user.email "test@example.com"
    git config user.name  "Test"
    git commit -q --allow-empty -m "init"

    mkdir -p stubs
    STUB_DIR="$BATS_TEST_TMPDIR_FAKE/stubs"
    PATH="$STUB_DIR:$PATH"
    export PATH

    # Source release.sh in source-only mode. Pull the file from the
    # path established by the bats runner (BATS_TEST_DIRNAME).
    REPO_ROOT_REAL="$(cd "$BATS_TEST_DIRNAME/../../.." && pwd)"
    RELEASE_SH="$REPO_ROOT_REAL/skills/release-agent-director/release.sh"
    _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"

    # Fake the variables collect_cabi_artifacts reads.
    REPO_ROOT="$BATS_TEST_TMPDIR_FAKE"
    BRANCH="main"
    DRY_RUN=0
}

teardown() {
    rm -rf "$BATS_TEST_TMPDIR_FAKE"
}

write_gh_stub() {
    local list_json="$1"
    cat > "$STUB_DIR/gh" <<EOF
#!/usr/bin/env bash
case "\$1" in
    run)
        case "\$2" in
            list)
                # Emit the canned JSON regardless of args.
                printf '%s' '$list_json'
                exit 0
                ;;
            download)
                # \$3 is run id; --name <art> --dir <dir>.
                while [[ \$# -gt 0 ]]; do
                    case "\$1" in
                        --name) art="\$2"; shift 2 ;;
                        --dir)  dir="\$2"; shift 2 ;;
                        *) shift ;;
                    esac
                done
                mkdir -p "\$dir"
                # Synthesize the expected files.
                case "\$art" in
                    pkg-cabi-linux-amd64)  touch "\$dir/libagent_director.so"  "\$dir/libagent_director.h" ;;
                    pkg-cabi-darwin-amd64) touch "\$dir/libagent_director.dylib" "\$dir/libagent_director.h" ;;
                    pkg-cabi-darwin-arm64) touch "\$dir/libagent_director.dylib" "\$dir/libagent_director.h" ;;
                    *) echo "unknown artifact: \$art" >&2; exit 1 ;;
                esac
                exit 0
                ;;
        esac
        ;;
esac
EOF
    chmod +x "$STUB_DIR/gh"
}

@test "single-match success populates per-platform dist/cabi and canonical header" {
    write_gh_stub '[{"databaseId": 42, "headSha":"abc","conclusion":"success","status":"completed"}]'
    run collect_cabi_artifacts
    [ "$status" -eq 0 ]
    [ -f "dist/cabi/linux-amd64/libagent_director.so" ]
    [ -f "dist/cabi/linux-amd64/libagent_director.h" ]
    [ -f "dist/cabi/darwin-amd64/libagent_director.dylib" ]
    [ -f "dist/cabi/darwin-amd64/libagent_director.h" ]
    [ -f "dist/cabi/darwin-arm64/libagent_director.dylib" ]
    [ -f "dist/cabi/darwin-arm64/libagent_director.h" ]
    [ -f "dist/cabi/include/libagent_director.h" ]
}

@test "zero-match halts with descriptive error" {
    write_gh_stub '[]'
    run collect_cabi_artifacts
    [ "$status" -ne 0 ]
    [[ "$output" == *"no successful"* ]]
    [[ "$output" == *"cabi-matrix.yml"* ]]
}

@test "multi-match halts with ambiguity error" {
    write_gh_stub '[{"databaseId":1,"headSha":"a","conclusion":"success","status":"completed"},{"databaseId":2,"headSha":"a","conclusion":"success","status":"completed"}]'
    run collect_cabi_artifacts
    [ "$status" -ne 0 ]
    [[ "$output" == *"ambiguous"* ]]
}

@test "AD_RELEASE_SKIP_CABI=1 short-circuits collection" {
    write_gh_stub '[]'  # would otherwise zero-match
    AD_RELEASE_SKIP_CABI=1 run collect_cabi_artifacts
    [ "$status" -eq 0 ]
    [[ "$output" == *"AD_RELEASE_SKIP_CABI=1"* ]]
}
