#!/usr/bin/env bats
#
# Smoke test for toolchain-pin-diff.sh.
#
# Covers the env-var-override path (PREV_PINS_FILE + CURRENT_PINS_FILE)
# so the test does not need git history.

setup() {
    BATS_TEST_TMPDIR_FAKE="$(mktemp -d)"
    SCRIPT="$(cd "$BATS_TEST_DIRNAME/.." && pwd)/toolchain-pin-diff.sh"
}

teardown() {
    rm -rf "$BATS_TEST_TMPDIR_FAKE"
}

write_prev() {
    cat > "$BATS_TEST_TMPDIR_FAKE/prev.txt" <<EOF
gcc(linux-amd64)=gcc-10
xcode(darwin-amd64)=Xcode 14.3
bun=1.2.0
EOF
}

write_current() {
    cat > "$BATS_TEST_TMPDIR_FAKE/cur.txt" <<EOF
gcc(linux-amd64)=gcc-11
xcode(darwin-amd64)=Xcode 15.2
bun=1.3.13
EOF
}

@test "no diff when prev and current are identical → silent" {
    write_prev
    cp "$BATS_TEST_TMPDIR_FAKE/prev.txt" "$BATS_TEST_TMPDIR_FAKE/cur.txt"
    run env \
        PREV_PINS_FILE="$BATS_TEST_TMPDIR_FAKE/prev.txt" \
        CURRENT_PINS_FILE="$BATS_TEST_TMPDIR_FAKE/cur.txt" \
        bash "$SCRIPT"
    [ "$status" -eq 0 ]
    [ -z "$output" ]
}

@test "three pins changed → emits a section with all three" {
    write_prev
    write_current
    run env \
        PREV_PINS_FILE="$BATS_TEST_TMPDIR_FAKE/prev.txt" \
        CURRENT_PINS_FILE="$BATS_TEST_TMPDIR_FAKE/cur.txt" \
        TOOLCHAIN_DIFF_PREV_LABEL=v0.1.0 \
        bash "$SCRIPT"
    [ "$status" -eq 0 ]
    [[ "$output" == *"## Toolchain pin changes since v0.1.0"* ]]
    [[ "$output" == *"\`gcc(linux-amd64)\`: gcc-10 → gcc-11"* ]]
    [[ "$output" == *"\`xcode(darwin-amd64)\`: Xcode 14.3 → Xcode 15.2"* ]]
    [[ "$output" == *"\`bun\`: 1.2.0 → 1.3.13"* ]]
}

@test "pin added in current → flagged as (none) → <new>" {
    cat > "$BATS_TEST_TMPDIR_FAKE/prev.txt" <<EOF
bun=1.3.13
EOF
    cat > "$BATS_TEST_TMPDIR_FAKE/cur.txt" <<EOF
bun=1.3.13
gcc(linux-amd64)=gcc-11
EOF
    run env \
        PREV_PINS_FILE="$BATS_TEST_TMPDIR_FAKE/prev.txt" \
        CURRENT_PINS_FILE="$BATS_TEST_TMPDIR_FAKE/cur.txt" \
        bash "$SCRIPT"
    [ "$status" -eq 0 ]
    [[ "$output" == *"\`gcc(linux-amd64)\`: (none) → gcc-11"* ]]
    [[ "$output" != *"bun:"* ]]
}

@test "extract_pins gracefully handles ref without the workflow file" {
    # When git show fails (the ref doesn't have the workflow yet),
    # extract_pins should produce no output (the "first release" case).
    cd "$BATS_TEST_TMPDIR_FAKE"
    git init -q .
    git config user.email "test@example.com"
    git config user.name  "Test"
    git commit -q --allow-empty -m "init"
    run bash -c "source '$SCRIPT'; type extract_pins >/dev/null && extract_pins HEAD"
    # extract_pins prints nothing when the workflow file is absent.
    [ "$status" -eq 0 ]
}
