const fs = require('fs');
const base = 'tickets/handoff/b.ftd/Plans/b.jf1/e3-integration-fixes';
const own = base + '/expiry-fixture-full';
for (const name of ['metadata.cjs', 'finalize.cjs']) {
  fs.writeFileSync(own + '/' + name, fs.readFileSync(base + '/startup-final-full/' + name, 'utf8').replaceAll('/startup-final-full', '/expiry-fixture-full'));
}
fs.writeFileSync(own + '/gitconfig', '[safe]\n\tdirectory = /work\n', {mode: 0o600});
fs.writeFileSync(own + '/container.env', 'HOME=/home/sandbox\nGIT_CONFIG_GLOBAL=/work/' + own + '/gitconfig\n');
