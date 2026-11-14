import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const scripts = fileURLToPath(new URL('../scripts/', import.meta.url));

test('bundled skill replaces portable setup and fails closed on source drift', async t => {
  const root = await mkdtemp(join(tmpdir(), 'talus-skill-sync-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const scriptDir = join(root, 'plugins/dsh/scripts');
  const sourceDir = join(root, 'skills/talus');
  await mkdir(scriptDir, { recursive: true });
  await mkdir(sourceDir, { recursive: true });
  await cp(join(scripts, 'sync-skills.mjs'), join(scriptDir, 'sync-skills.mjs'));
  await writeFile(join(scriptDir, 'skill-configuration.md'), 'DSH manages the broker.\n');
  await writeFile(join(root, 'LICENSE'), 'fixture license\n');
  const source = join(sourceDir, 'SKILL.md');
  const original = '# Skill\n\n## Quick Start\n\n<!-- talus:portable-setup:start -->\nStart the portable broker.\n<!-- talus:portable-setup:end -->\n\nShared request instructions.\n\n## Next\n';
  await writeFile(source, original);
  const command = join(scriptDir, 'sync-skills.mjs');
  const run = (...args) => spawnSync(process.execPath, [command, ...args], { encoding: 'utf8' });
  assert.equal(run().status, 0);
  const generated = await readFile(join(root, 'plugins/dsh/skills/talus/SKILL.md'), 'utf8');
  assert.match(generated, /DSH manages the broker/);
  assert.doesNotMatch(generated, /Start the portable broker/);
  assert.match(generated, /Shared request instructions/);
  assert.equal(run('--check').status, 0);
  await writeFile(source, original.replace('Shared request instructions.', 'Changed instructions.'));
  assert.notEqual(run('--check').status, 0, 'changed source must fail the check');
  await writeFile(source, original.replace('<!-- talus:portable-setup:start -->', ''));
  assert.notEqual(run('--check').status, 0, 'a missing setup marker must fail the check');
  assert.notEqual(run().status, 0, 'generation must reject a missing setup marker');
  assert.equal(await readFile(join(root, 'plugins/dsh/skills/talus/SKILL.md'), 'utf8'), generated,
    'a rejected source must not replace the existing bundle');
});
