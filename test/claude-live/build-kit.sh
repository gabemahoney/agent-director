#!/usr/bin/env bash
# Called by prepare.sh inside an isolated scheduler container. No Claude run.
set -euo pipefail
umask 077
[[ $# == 6 && ${AGENT_DIRECTOR_TEST_SANDBOX:-} == 1 ]] || exit 2
source_dir=$1 identity=$2 source_tar=$3 out=$4 target=$5 version=$6
base=$(jq -er .revision "$identity")
tree=$(jq -er .tree "$identity")
arch=${target#linux/}
build_os=$(go env GOOS)
build_arch=$(go env GOARCH)
[[ $build_os/$build_arch == "$target" ]] || { echo 'native target required; no emulation or cross-run' >&2; exit 2; }
build_version=$(go env GOVERSION)
source_version=$(awk '$1=="go" {print $2}' "$source_dir/go.mod")
[[ $build_version == "go$source_version" ]] || { echo 'toolchain differs from snapshot go.mod' >&2; exit 2; }
[[ ! -L $out ]] || exit 2
if [[ -e $out ]]; then
    [[ -d $out ]] || exit 2
    output_entry=$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)
    [[ -z $output_entry ]] || exit 2
fi
work=$(mktemp -d "${TMPDIR:-/tmp}/claude-live-build.XXXXXX")
published=()
temporary=()
finished=0
cleanup() {
    local file
    for file in "${temporary[@]}"; do rm -f -- "$file"; done
    if (( ! finished )); then
        for file in "${published[@]}"; do rm -f -- "$file"; done
    fi
    rm -rf -- "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
bash "$source_dir/test/driver/platform-env.sh" > "$work/platform-env.json"
platform_record=$(cat "$work/platform-env.json")
printf 'PLATFORM_ENV %s\n' "$platform_record"
kit=$work/kit
mkdir -p "$kit/bin" "$kit/payload" "$kit/skills/install-agent-director/schedule" "$kit/docs" "$kit/qualification"
flags="-s -w -X github.com/gabemahoney/agent-director/internal/version.Version=$version -X github.com/gabemahoney/agent-director/internal/version.Commit=$base"
for component in agent-director agent-director-admin; do
    # Match make build: -trimpath omits -ldflags from Go build metadata,
    # preventing the VM from checking both stamps before executing a payload.
    (cd "$source_dir" && CGO_ENABLED=0 go build -ldflags="$flags" -o "$kit/payload/$component-linux-$arch" "./cmd/$component")
    "$kit/payload/$component-linux-$arch" version > "$work/$component-version.json"
    jq -e --arg version "$version" --arg commit "$base" '.version==$version and .commit==$commit' "$work/$component-version.json" >/dev/null
done
(cd "$source_dir" && CGO_ENABLED=0 go build -trimpath -o "$kit/bin/claude-live" ./test/claude-live/cmd/claude-live)
cp "$source_dir/skills/install-agent-director/install.sh" "$kit/skills/install-agent-director/install.sh"
for name in agent-director.cron agent-director-find-missing.service agent-director-find-missing.timer agent-director-expire.service agent-director-expire.timer; do
    cp "$source_dir/skills/install-agent-director/schedule/$name" "$kit/skills/install-agent-director/schedule/$name"
done
source_hash=$(sha256sum < "$source_tar"); source_hash=${source_hash%% *}
# Separate the unmodified product build inputs from harness-only tree changes.
(cd "$source_dir" && CGO_ENABLED=0 go list -deps -json ./cmd/agent-director ./cmd/agent-director-admin) > "$work/product-packages.json"
"$kit/bin/claude-live" qualification-build-source --source-root "$source_dir" --source-inventory "$identity" --package-graph "$work/product-packages.json" --source-archive "$source_hash" --build-platform "$target" --toolchain "$build_version" --out "$kit/qualification/source.json"
cp "$source_dir/docs/caller-contract.md" "$kit/docs/caller-contract.md"
cp "$source_dir/internal/spawn/launch.go" "$kit/qualification/launch.go"
cp "$source_dir/internal/store/spawns.go" "$kit/qualification/spawns.go"
cp "$identity" "$kit/qualification/source-inventory.json"

git -C "$source_dir" diff --binary HEAD > "$work/source.diff"
diff_hash=$(sha256sum < "$work/source.diff"); diff_hash=${diff_hash%% *}
head_tree=$(git -C "$source_dir" rev-parse 'HEAD^{tree}')
dirty=false
if [[ -s $work/source.diff || $head_tree != "$tree" ]]; then dirty=true; fi
printf '{}\n' > "$work/files.json"
find "$kit" -type f -print0 > "$work/kit-files"
while IFS= read -r -d '' file; do
    name=${file#"$kit/"}
    hash=$(sha256sum < "$file"); hash=${hash%% *}
    size=$(wc -c < "$file")
    jq --arg name "$name" --arg hash "$hash" --argjson size "$size" '. + {($name):{sha256:$hash,bytes:$size}}' "$work/files.json" > "$work/files.next"
    mv "$work/files.next" "$work/files.json"
done < "$work/kit-files"
jq -n --arg base "$base" --arg tree "$tree" --arg source "$source_hash" --arg diff "$diff_hash" --argjson dirty "$dirty" --arg version "$version" --arg platform "$target" --slurpfile files "$work/files.json" \
    '{schema:1,base_commit:$base,snapshot_tree:$tree,source_sha256:$source,diff_sha256:$diff,dirty:$dirty,version:$version,commit_stamp:$base,platform:$platform,files:$files[0]}' > "$kit/kit.json"
"$kit/bin/claude-live" verify-kit --kit "$kit" > "$work/verified.json"
tar -C "$kit" --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner -czf "$work/claude-live-kit.tar.gz" bin payload skills docs qualification kit.json
archive_hash=$(sha256sum < "$work/claude-live-kit.tar.gz"); archive_hash=${archive_hash%% *}
printf '%s  claude-live-kit.tar.gz\n' "$archive_hash" > "$work/claude-live-kit.tar.gz.sha256"
cp "$kit/kit.json" "$work/kit.json"
mkdir -p "$out"
# Hard links publish only absent destinations; never overwrite another kit.
for name in claude-live-kit.tar.gz claude-live-kit.tar.gz.sha256 kit.json platform-env.json; do
    (set -C; : > "$out/.$name.$$")
    temporary+=("$out/.$name.$$")
    cp "$work/$name" "$out/.$name.$$"
    ln "$out/.$name.$$" "$out/$name"
    published+=("$out/$name")
    rm "$out/.$name.$$"
done
printf 'KIT_BUILT %s %s\n' "$archive_hash" "$target"
# Return a scheduler artifact using the existing check-runner log envelope.
# jq/base64 are build tools only; the VM needs neither Go nor Python.
tar -C "$out" -czf "$work/result.tar.gz" claude-live-kit.tar.gz claude-live-kit.tar.gz.sha256 kit.json platform-env.json
result_hash=$(sha256sum < "$work/result.tar.gz"); result_hash=${result_hash%% *}
size=$(wc -c < "$work/result.tar.gz")
jq -cn --argjson bytes "$size" --arg hash "$result_hash" '{CHECK_ARCHIVE_BEGIN:{bytes:$bytes,sha256:$hash}}'
split -b 32768 "$work/result.tar.gz" "$work/chunk-"
index=0
for chunk in "$work"/chunk-*; do
    hash=$(sha256sum < "$chunk"); hash=${hash%% *}
    data=$(base64 -w0 "$chunk")
    jq -cn --argjson index "$index" --arg data "$data" --arg hash "$hash" '{CHECK_ARCHIVE_CHUNK:{index:$index,data:$data,sha256:$hash}}'
    index=$((index+1))
done
jq -cn --arg hash "$result_hash" '{CHECK_ARCHIVE_END:{sha256:$hash}}'
finished=1
