#!/usr/bin/env bats
#
# Tests for the darwin/arm64-offline halt path added by T9.
#
# Cases:
#   - AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1 → halts with the
#     specific "bring self-hosted runner online" message.
#   - gh run download failing for darwin-arm64 (no flag) → same message.
#   - gh run download failing for linux-amd64 → generic "re-run CI matrix"
#     message, not the darwin/arm64 one.

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

    REPO_ROOT_REAL="$(cd "$BATS_TEST_DIRNAME/../../.." && pwd)"
    RELEASE_SH="$REPO_ROOT_REAL/skills/release-agent-director/release.sh"
    _RELEASE_SH_SOURCE_ONLY=1 source "$RELEASE_SH"

    REPO_ROOT="$BATS_TEST_TMPDIR_FAKE"
    BRANCH="main"
    DRY_RUN=0
}

teardown() {
    rm -rf "$BATS_TEST_TMPDIR_FAKE"
}

write_gh_stub() {
    local download_fail_platform="${1:-}"
    cat > "$STUB_DIR/gh" <<EOF
#!/usr/bin/env bash
case "\$1" in
    run)
        case "\$2" in
            list)
                printf '%s' '[{"databaseId":42,"headSha":"abc","conclusion":"success","status":"completed"}]'
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
                fail_plat="$download_fail_platform"
                if [[ -n "\$fail_plat" && "\$art" == "pkg-cabi-\$fail_plat" ]]; then
                    exit 1
                fi
                mkdir -p "\$dir"
                case "\$art" in
                    pkg-cabi-linux-amd64)  touch "\$dir/libagent_director.so"  "\$dir/libagent_director.h" ;;
                    pkg-cabi-darwin-amd64) touch "\$dir/libagent_director.dylib" "\$dir/libagent_director.h" ;;
                    pkg-cabi-darwin-arm64) touch "\$dir/libagent_director.dylib" "\$dir/libagent_director.h" ;;
                esac
                exit 0
                ;;
        esac
        ;;
esac
EOF
    chmod +x "$STUB_DIR/gh"
}

@test "AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1 emits the self-hosted-runner message" {
    write_gh_stub ""
    AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1 run collect_cabi_artifacts
    [ "$status" -ne 0 ]
    [[ "$output" == *"darwin/arm64 leg unavailable; bring self-hosted runner online before retrying"* ]]
    # Belt-and-suspenders: the generic message must NOT also appear.
    [[ "$output" != *"linux-amd64 leg unavailable"* ]]
    [[ "$output" != *"darwin-amd64 leg unavailable"* ]]
}

@test "gh run download failure on darwin-arm64 emits the self-hosted-runner message" {
    write_gh_stub "darwin-arm64"
    run collect_cabi_artifacts
    [ "$status" -ne 0 ]
    [[ "$output" == *"darwin/arm64 leg unavailable; bring self-hosted runner online before retrying"* ]]
}

@test "gh run download failure on linux-amd64 emits the generic message, not the darwin one" {
    write_gh_stub "linux-amd64"
    run collect_cabi_artifacts
    [ "$status" -ne 0 ]
    [[ "$output" == *"linux-amd64 leg unavailable; re-run CI matrix before retrying"* ]]
    [[ "$output" != *"darwin/arm64 leg unavailable"* ]]
}

@test "no partial 2-of-3 release: halt names every missing platform" {
    # Simulate two legs missing: linux-amd64 + darwin-arm64.
    write_gh_stub "linux-amd64"
    AD_RELEASE_SIMULATE_DARWIN_ARM64_OFFLINE=1 run collect_cabi_artifacts
    [ "$status" -ne 0 ]
    [[ "$output" == *"linux-amd64 leg unavailable"* ]]
    [[ "$output" == *"darwin/arm64 leg unavailable"* ]]
    [[ "$output" == *"halting before tag/publish/gh-release"* ]]
}
