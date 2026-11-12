#!/usr/bin/env bash
# prepare.sh — package the Release Check Kit from one exact tree (SRD SR-8.2),
# for any native linux/arm64 Docker engine to build and run.
#
# Usage, from any directory of the agent-director git repository's working
# tree (relative paths are taken from that directory):
#
#   prepare.sh <tag> <SHA256SUMS> <out-dir>
#       Published: <tag> is a release tag v<version>; the kit is that tag's
#       commit, installing the published release.
#   prepare.sh --candidate <commit> <expected-version> <SHA256SUMS> <payload-dir> <out-dir>
#       Candidate: the kit is <commit>, installing the pair in <payload-dir>
#       (agent-director-linux-arm64, agent-director-admin-linux-arm64), which
#       must report <expected-version> (bare, no leading v) and <commit>.
#
# <SHA256SUMS> is a sha256sum-format file with exactly one entry for each
# linux-arm64 asset, named as the asset or as a path ending in /<asset>.
#
# It writes three files into <out-dir>, which must be absent or empty:
#
#   release-check-kit.tar.gz          the kit: check.sh's header gives its layout
#                                     and release.env
#   release-check-kit.tar.gz.sha256   the archive's hash, sha256sum format
#   INSTRUCTIONS.txt                  how to verify, build, run, collect and
#                                     clean up on a native linux/arm64 engine
#
# The kit comes only from the commit's own tree (git archive), never the
# working tree. It uses only git, tar, gzip, jq and sha256sum (or shasum -a
# 256), and never runs agent-director, agent-director-admin, install.sh or a
# test. Everything is staged in a private temporary directory and moved into
# <out-dir> last; on any refusal, error or interruption only what this run
# created is removed, so anything already in or around <out-dir> is left as
# it was.
#
# Exit status: 0 done; 2 refused (the message says what to pass), before
# anything is written to <out-dir>; 1 any other failure.

set -euo pipefail

readonly AD_ASSET=agent-director-linux-arm64
readonly ADMIN_ASSET=agent-director-admin-linux-arm64
readonly INSTALL_SH=skills/install-agent-director/install.sh
readonly PACKAGE_JSON=pkg/ts-bun-client/package.json
readonly KIT_DIR=test/release-check
# Every path the kit takes from the tree; check.sh's header is the layout.
LAYOUT_PATHS=(
    "$KIT_DIR"
    test/driver/platform-env.sh
    test/driver/sql.sh
    test/driver/pane-hook.sh
    test/driver/stand-in-claude
    test/driver/stand-in-codex
    "$INSTALL_SH"
)
readonly LAYOUT_PATHS
readonly ARCHIVE=release-check-kit.tar.gz
readonly ARCHIVE_SHA=release-check-kit.tar.gz.sha256
readonly INSTRUCTIONS=INSTRUCTIONS.txt
readonly REFUSED_EXIT=2
readonly USAGE="usage: prepare.sh <tag> <SHA256SUMS> <out-dir>
       prepare.sh --candidate <commit> <expected-version> <SHA256SUMS> <payload-dir> <out-dir>"

refuse() {
    printf 'prepare.sh: %s\n' "$*" >&2
    exit "$REFUSED_EXIT"
}

die() {
    printf 'prepare.sh: %s\n' "$*" >&2
    exit 1
}

# ---- arguments -------------------------------------------------------------

mode="" tag="" commit_arg="" version="" sums="" payload="" out=""
if [[ $# -ge 1 && "$1" == --candidate ]]; then
    [[ $# -eq 6 ]] || refuse "--candidate takes <commit> <expected-version> <SHA256SUMS> <payload-dir> <out-dir>
$USAGE"
    mode=candidate commit_arg="$2" version="$3" sums="$4" payload="$5" out="$6"
elif [[ $# -eq 3 && "$1" != -* ]]; then
    mode=published tag="$1" sums="$2" out="$3"
else
    refuse "pass a release tag, its SHA256SUMS and an out-dir, or --candidate with its five arguments
$USAGE"
fi

missing=()
for tool in git tar gzip jq; do
    command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
if command -v sha256sum >/dev/null 2>&1; then
    hash_cmd=(sha256sum)
elif command -v shasum >/dev/null 2>&1; then
    hash_cmd=(shasum -a 256)
else
    missing+=("sha256sum (or shasum)")
fi
((${#missing[@]} == 0)) || refuse "install these and rerun: ${missing[*]}"

# sha256_of <file>: print its SHA-256 in lowercase hex.
sha256_of() {
    local line
    line="$("${hash_cmd[@]}" <"$1")" || return 1
    printf '%s\n' "${line%% *}"
}

# Every git operation runs at the repository's root, because the layout's
# paths are root-relative and git reads a pathspec relative to the current
# directory. The argument paths stay relative to the caller's directory.
if ! repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || [[ -z "$repo_root" ]]; then
    refuse "run prepare.sh from inside the agent-director git repository's working tree"
fi
rgit() { git -C "$repo_root" "$@"; }

if [[ "$mode" == published ]]; then
    [[ "$tag" == v?* ]] || refuse "tag $(printf '%q' "$tag") has no leading v: pass a release tag v<version>"
    commit="$(rgit rev-parse --verify --quiet "refs/tags/${tag}^{commit}")" ||
        refuse "no tag $(printf '%q' "$tag") in this repository: pass an existing release tag v<version> (fetch tags first)"
    version="${tag#v}"
    tree_version="$(rgit show "${commit}:${PACKAGE_JSON}" 2>/dev/null | jq -er '.version | strings' 2>/dev/null)" ||
        refuse "tag $tag's tree has no readable .version in $PACKAGE_JSON: pass a release tag made by the release tooling"
    [[ "$tree_version" == "$version" ]] ||
        refuse "tag $tag names version $version but its $PACKAGE_JSON .version is $tree_version: pass the tag for that tree's release, v$tree_version"
else
    commit="$(rgit rev-parse --verify --quiet "${commit_arg}^{commit}")" ||
        refuse "no commit $(printf '%q' "$commit_arg") in this repository: pass the commit the payload pair was built from"
    if [[ -z "$version" || "$version" =~ [[:space:]] || "$version" == v* ]]; then
        refuse "expected version $(printf '%q' "$version") must be the bare version the pair reports: not empty, no whitespace, no leading v"
    fi
fi

rgit cat-file -e "${commit}:${KIT_DIR}" 2>/dev/null ||
    refuse "commit $commit has no $KIT_DIR/ (it predates the Release Check Kit): pass a tag or commit that has it"
for path in "${LAYOUT_PATHS[@]}"; do
    rgit cat-file -e "${commit}:${path}" 2>/dev/null ||
        refuse "commit $commit has no $path, which the kit needs: pass a tag or commit whose tree has the whole kit"
done

# SHA256SUMS: exactly one well-formed entry per linux-arm64 asset.
[[ -f "$sums" && -r "$sums" ]] || refuse "no readable SHA256SUMS file at $(printf '%q' "$sums"): pass the release's SHA256SUMS"
ad_count=0 admin_count=0 ad_sha="" admin_sha=""
while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line%$'\r'}"
    if [[ "$line" =~ ^([0-9a-f]{64})\ [\ *](.+)$ ]]; then
        name="${BASH_REMATCH[2]}"
        if [[ "$name" == "$AD_ASSET" || "$name" == */"$AD_ASSET" ]]; then
            ad_count=$((ad_count + 1)) ad_sha="${BASH_REMATCH[1]}"
        elif [[ "$name" == "$ADMIN_ASSET" || "$name" == */"$ADMIN_ASSET" ]]; then
            admin_count=$((admin_count + 1)) admin_sha="${BASH_REMATCH[1]}"
        fi
    elif [[ "$line" == *"$AD_ASSET"* || "$line" == *"$ADMIN_ASSET"* ]]; then
        refuse "SHA256SUMS line $(printf '%q' "$line") is not a well-formed '<64 lowercase hex>  <name>' entry: pass the release's own SHA256SUMS"
    fi
done <"$sums"
((ad_count == 1)) || refuse "SHA256SUMS has $ad_count entries for $AD_ASSET; it needs exactly one: pass the release's own SHA256SUMS"
((admin_count == 1)) || refuse "SHA256SUMS has $admin_count entries for $ADMIN_ASSET; it needs exactly one: pass the release's own SHA256SUMS"

if [[ "$mode" == candidate ]]; then
    [[ -d "$payload" ]] || refuse "no payload directory at $(printf '%q' "$payload"): pass the directory holding $AD_ASSET and $ADMIN_ASSET"
    for asset in "$AD_ASSET" "$ADMIN_ASSET"; do
        [[ -f "$payload/$asset" ]] || refuse "the payload has no $asset: put the built pair in $payload"
        want="$ad_sha"
        [[ "$asset" == "$AD_ASSET" ]] || want="$admin_sha"
        got="$(sha256_of "$payload/$asset")" || die "could not hash $payload/$asset"
        [[ "$got" == "$want" ]] ||
            refuse "payload $asset sha256 mismatch: SHA256SUMS has $want, the file is $got; pass the pair SHA256SUMS describes"
    done
fi

if [[ -e "$out" || -L "$out" ]]; then
    [[ -d "$out" && ! -L "$out" ]] || refuse "out-dir $(printf '%q' "$out") exists and is not a plain directory: pass an absent or empty directory"
    [[ -r "$out" && -w "$out" && -x "$out" ]] || refuse "out-dir $(printf '%q' "$out") cannot be read and written: pass an absent or empty directory you own"
    [[ -z "$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)" ]] ||
        refuse "out-dir $(printf '%q' "$out") is not empty: pass an absent or empty directory"
fi

# ---- staging and output safety --------------------------------------------

work=""
created_out=0
finished=0
# Each output reaches <out-dir> as a hidden file this run creates there
# (part_path, part_made once created), renamed to its name (part_name) with
# `mv -n`, which never replaces a file already there.
part_path=()
part_name=()
part_made=()
# cleanup removes the staging directory and, unless the run finished, every
# file this run created in <out-dir> and an <out-dir> it created. A hidden
# file still there is this run's; once it is gone after being made, the
# rename happened, so the named output is this run's.
cleanup() {
    local i
    if ((!finished)); then
        for ((i = 0; i < ${#part_path[@]}; i++)); do
            [[ -n "${part_path[i]}" ]] || continue
            if [[ -e "${part_path[i]}" ]]; then
                rm -f -- "${part_path[i]}"
            elif ((part_made[i])); then
                rm -f -- "${out:?}/${part_name[i]}"
            fi
        done
    fi
    [[ -z "$work" ]] || rm -rf -- "$work"
    if ((!finished && created_out)); then
        rmdir -- "$out" 2>/dev/null || true
    fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

work="$(mktemp -d "${TMPDIR:-/tmp}/release-check-prepare.XXXXXX")" || die "could not make a temporary directory"
kit="$work/kit"
mkdir -- "$kit"
rgit archive --format=tar "$commit" -- "${LAYOUT_PATHS[@]}" | tar -xf - -C "$kit" ||
    die "git archive of $commit failed"

install_sha="$(sha256_of "$kit/$INSTALL_SH")" || die "could not hash $INSTALL_SH"
release_tag=""
[[ "$mode" == candidate ]] || release_tag="$tag"
{
    printf 'RELEASE_CHECK_MODE=%s\n' "$mode"
    printf 'RELEASE_TAG=%s\n' "$release_tag"
    printf 'EXPECTED_VERSION=%s\n' "$version"
    printf 'EXPECTED_COMMIT=%s\n' "$commit"
    printf 'AD_SHA256=%s\n' "$ad_sha"
    printf 'ADMIN_SHA256=%s\n' "$admin_sha"
    printf 'INSTALL_SH_SHA256=%s\n' "$install_sha"
} >"$kit/release.env"

if [[ "$mode" == candidate ]]; then
    mkdir -- "$kit/payload"
    for asset in "$AD_ASSET" "$ADMIN_ASSET"; do
        cp -- "$payload/$asset" "$kit/payload/$asset"
        chmod 0755 "$kit/payload/$asset"
    done
    # The copies are what the kit carries, so they are what is checked.
    [[ "$(sha256_of "$kit/payload/$AD_ASSET")" == "$ad_sha" && "$(sha256_of "$kit/payload/$ADMIN_ASSET")" == "$admin_sha" ]] ||
        die "the payload changed while it was copied; run prepare.sh again"
fi

# COPYFILE_DISABLE: macOS tar would otherwise add ._ metadata files.
(cd "$kit" && COPYFILE_DISABLE=1 tar -cf - .) | gzip -n >"$work/$ARCHIVE" || die "could not write $ARCHIVE"
archive_sha="$(sha256_of "$work/$ARCHIVE")" || die "could not hash $ARCHIVE"
printf '%s  %s\n' "$archive_sha" "$ARCHIVE" >"$work/$ARCHIVE_SHA"

if [[ "$mode" == published ]]; then
    identity="Release tag:              $tag"
else
    identity="Candidate (no release tag)"
fi

cat >"$work/$INSTRUCTIONS" <<EOF
Release Check Kit: $mode check of agent-director $version

Archive:                  $ARCHIVE
Archive SHA-256:          $archive_sha
Mode:                     $mode
$identity
Expected version:         $version
Expected commit:          $commit
agent-director-linux-arm64 SHA-256:        $ad_sha
agent-director-admin-linux-arm64 SHA-256:  $admin_sha
install.sh SHA-256:       $install_sha

The kit installs that exact pair with install.sh from the same commit, then
checks it with stand-in agents. Run these steps in order on a host with a
native linux/arm64 Docker engine. Keep every output unchanged.

1. Verify the archive before unpacking. In the directory holding
   $ARCHIVE and $ARCHIVE_SHA, run
       sha256sum -c $ARCHIVE_SHA
   (or: shasum -a 256 -c $ARCHIVE_SHA). Stop if it does not report OK.

2. Unpack into a fresh throwaway directory:
       work=\$(mktemp -d)
       tar -xzf $ARCHIVE -C "\$work"

3. Check the engine. Keep the full output of
       docker version
   and run
       docker version --format '{{.Server.Os}}/{{.Server.Arch}}'
   It must print linux/arm64. Otherwise stop: never use --platform,
   emulation or an amd64 engine.

4. Build from the unpacked root, pulling the base fresh, with plain progress.
   The build installs the pair (it needs network) and fails if the
   installation fails. Keep the whole build log unchanged and its exit
   status. Never retry silently.
       cd "\$work"
       docker build --pull --progress=plain --iidfile image.id \\
           -f test/release-check/Dockerfile . > build.log 2>&1
       echo \$? > build.exit

5. Record the image ID (the contents of image.id) and the resolved digest of
   node:24-bookworm: the sha256 digest the build log shows for
   docker.io/library/node:24-bookworm.

6. Run a fresh container with no network, no volume or bind mounts, no host
   HOME, config or credentials and no Docker socket. Keep its full output and
   exit status.
       docker run --rm --network none --memory 8g "\$(cat image.id)" > run.log 2>&1
       echo \$? > run.exit

7. Return these facts, for the caller to judge:
   - the identities above and the archive SHA-256;
   - the full docker version output;
   - the image ID and the node:24-bookworm digest;
   - build.log and run.log unchanged, with build.exit and run.exit;
   - the Environment Record (run.log's first line, or its
     "environment: unavailable" line);
   - every check line (each "<check>: PASS" or "<check>: FAIL <reason>"),
     each "diagnostics <check>: begin" to "diagnostics <check>: end" block
     in run.log (the complete output behind a failed check, or behind a
     failed call in a check that then passed), the build log's
     "install: refused" or "install: exit" line, and every observed
     failure;
   - the final "VERDICT:" line, or the step where the work stopped and what
     it showed.

8. Clean up only this run's image and throwaway directory:
       docker image rm "\$(cat "\$work/image.id")"
       rm -rf "\$work"

Outcome contract (SRD SR-5.2, SR-6.2, SR-8.5), for the caller's judgment:

- pass requires successful completed checks, exit status 0, and a valid
  native engine, Environment Record and evidence: a linux/arm64 engine, a
  record showing uname -m aarch64, dpkg arm64, ID debian and VERSION_ID 12,
  "VERDICT: PASS", and all returned evidence.
- not completed, with its reason: an infrastructure, network or platform
  problem, missing or invalid evidence (including an absent or mismatched
  Environment Record, whatever the verdict), or unfinished work.
- fail: a completed test failure or product install failure, even when other
  work is incomplete.
- In every case, keep and return every observed failure and the raw output
  unchanged.
EOF

# ---- move into out-dir -----------------------------------------------------

if [[ ! -d "$out" ]]; then
    mkdir -- "$out" || die "could not create out-dir $out; its parent directory must exist"
    created_out=1
fi
for f in "$ARCHIVE" "$ARCHIVE_SHA" "$INSTRUCTIONS"; do
    i=${#part_path[@]}
    part_name[i]="$f"
    part_made[i]=0
    part_path[i]="$out/.$f.prepare-$$-$RANDOM"
    if ! (set -C && : >"${part_path[i]}") 2>/dev/null; then
        part_path[i]=""
        die "could not create a file in $out"
    fi
    part_made[i]=1
    cat -- "$work/$f" >"${part_path[i]}" || die "could not write $f into $out"
    mv -n -- "${part_path[i]}" "$out/$f" || die "could not move $f into place in $out"
    [[ ! -e "${part_path[i]}" ]] || die "$out/$f appeared while preparing; nothing of it was replaced"
done
finished=1
printf 'prepare.sh: wrote %s, %s and %s to %s\n' "$ARCHIVE" "$ARCHIVE_SHA" "$INSTRUCTIONS" "$out"
