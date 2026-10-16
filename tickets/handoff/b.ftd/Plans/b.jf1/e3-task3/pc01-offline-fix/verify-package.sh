#!/usr/bin/env bash
set -euo pipefail
for key in $(compgen -e); do
 case "$key" in AGENT_DIRECTOR_*) [[ $key == AGENT_DIRECTOR_TEST_SANDBOX ]] || unset "$key";; esac
done
unset TMUX TMUX_PANE GIT_CONFIG_GLOBAL
work=$(mktemp -d /tmp/pc01-package-XXXXXX)
export GIT_CONFIG_GLOBAL="$work/gitconfig"
touch "$GIT_CONFIG_GLOBAL"
mkdir -p "$work/source/pkg/ts-bun-client" "$work/source/pkg/api/errnames" "$work/pack" "$work/consumer" "$work/private"
cp /work/pkg/api/errnames/catalog.json "$work/source/pkg/api/errnames/"
inputs=(package.json bun.lock bunfig.toml build.ts tsconfig.json tsconfig.typecheck.json version-floor.json README.md src)
for entry in "${inputs[@]}"; do cp -R "/work/pkg/ts-bun-client/$entry" "$work/source/pkg/ts-bun-client/"; done
# Use already provisioned toolchain dependencies; no registry install is needed.
cp -R /work/pkg/ts-bun-client/node_modules "$work/source/pkg/ts-bun-client/"
manifest() {
 (cd "$1"; { printf '%s\0' package.json bun.lock bunfig.toml build.ts tsconfig.json tsconfig.typecheck.json version-floor.json README.md ../api/errnames/catalog.json; find src -type f -print0; } | sort -z | xargs -0 sha256sum)
}
manifest "$work/source/pkg/ts-bun-client" > "$work/baked-sha256"
manifest /work/pkg/ts-bun-client > "$work/mounted-sha256"
cmp "$work/baked-sha256" "$work/mounted-sha256"
(cd "$work/source/pkg/ts-bun-client"; bun run build; bun pm pack --destination "$work/pack")
printf '{"name":"pane-controls-consumer","type":"module"}\n' > "$work/consumer/package.json"
tarballs=("$work/pack/"*.tgz)
test "${#tarballs[@]}" = 1
(cd "$work/consumer"; bun add "${tarballs[0]}" --no-progress)
cp -R "$work/consumer/." "$work/private/"
test -s "$work/private/node_modules/agent-director/dist/index.d.ts"
printf "import {Client} from 'agent-director'; if(typeof Client.create!=='function')throw Error('public Client missing'); console.log('PASS public packed consumer import');\n" > "$work/private/caller.mjs"
(cd "$work/private"; bun --no-install caller.mjs)
printf '\n// stale-image probe\n' >> "$work/source/pkg/ts-bun-client/src/index.ts"
manifest "$work/source/pkg/ts-bun-client" > "$work/stale-sha256"
if cmp -s "$work/baked-sha256" "$work/stale-sha256"; then exit 1; fi
printf 'PASS changed-source manifest rejected\n'
touch "$work/source/pkg/ts-bun-client/src/pc01-added-file.ts"
manifest "$work/source/pkg/ts-bun-client" > "$work/added-sha256"
if cmp -s "$work/stale-sha256" "$work/added-sha256"; then exit 1; fi
printf 'PASS added-source manifest rejected\n'
bash -n /work/test/driver/cursor-pane-controls.sh
printf 'PASS shell syntax; package verification complete\n'
