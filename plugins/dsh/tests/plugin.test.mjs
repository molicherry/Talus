import assert from 'node:assert/strict';
import { execFile, execFileSync } from 'node:child_process';
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';
import Skills from '@deepseek-ai/dsh-skill';
import * as filesystem from '@deepseek-ai/dsh-skill-filesystem';
import * as shellEnv from '@deepseek-ai/dsh-shell-env';
import { createServer } from 'node:http';
import { promisify } from 'node:util';
import { redactSecrets } from '@deepseek-ai/dsh-settings';
import { parse } from 'yaml';

const root = fileURLToPath(new URL('../', import.meta.url));
const run = promisify(execFile);

test('packed bundle loads through DSH, preserves other skills, and unloads cleanly', async (t) => {
  // The extracted package has no Talus checkout next to it. Only its declared
  // peers resolve through the parent node_modules, as in an installed profile.
  const scratch = await mkdtemp(join(root, 'node_modules/.talus-package-test-'));
  const ctx = new Context();
  t.after(async () => {
    await ctx.fiber.dispose();
    await rm(scratch, { recursive: true, force: true });
  });
  const [packed] = JSON.parse(execFileSync('npm', [
    'pack', '--json', '--ignore-scripts', '--cache', join(scratch, 'cache'),
    '--pack-destination', scratch,
  ], { cwd: root, encoding: 'utf8' }));
  execFileSync('tar', ['-xzf', join(scratch, packed.filename), '-C', scratch]);
  const installed = join(scratch, 'package');
  const manifest = JSON.parse(await readFile(join(installed, 'package.json'), 'utf8'));
  const patch = parse(await readFile(join(installed, manifest.dsh.bundle.patch), 'utf8'));
  assert.equal(manifest.dsh.manifestVersion, 1);
  assert.equal(patch[0].insert[0].name, manifest.name);
  const plugin = await import(pathToFileURL(join(installed, manifest.main)));

  const customRoot = join(scratch, 'existing-skills');
  await mkdir(customRoot);
  await writeFile(join(customRoot, 'existing.md'),
    '---\nname: existing\ndescription: Existing user skill.\n---\nKeep this skill.\n');
  await ctx.plugin(Skills).await();
  await ctx.plugin(shellEnv).await();
  await ctx.plugin(filesystem, {
    providerName: 'existing', includeDefaultRoots: false,
    customSkillDirs: [customRoot], watch: false,
  }).await();
  const mounted = ctx.plugin(plugin);
  await mounted.await();

  await t.test('settings reads redact the API key and config validates HTTP URLs', async () => {
    const redacted = redactSecrets(plugin.Config, { url: 'https://talus.example.test', apiKey: 'private-test-key' });
    assert.deepEqual(redacted.value, { url: 'https://talus.example.test' });
    assert.deepEqual(redacted.secrets, [{ path: ['apiKey'], set: true }]);
    assert.ok(!JSON.stringify(redacted).includes('private-test-key'));
    assert.throws(() => mounted.resolveConfig({ url: 'javascript:alert(1)', apiKey: '' }));
    assert.throws(() => mounted.resolveConfig({ url: 'https://user:password@example.test', apiKey: '' }));
    assert.throws(() => mounted.resolveConfig({ url: 'https://talus.example.test/api/v1/', apiKey: '' }));
    assert.throws(() => mounted.resolveConfig({ url: 'https://talus.example.test', apiKey: 'invalid\nkey' }));
    assert.equal(manifest.exports['./client'], './client.js');
    assert.ok(packed.files.some(file => file.path === 'client.js'));
  });

  await t.test('discovers the packaged skill and resolves its bundled references', async () => {
    const snapshot = await ctx.skills.snapshot();
    assert.equal(snapshot.complete, true);
    assert.deepEqual(snapshot.skills.map(skill => skill.name), ['existing', 'talus']);
    const skill = await ctx.skills.get('talus');
    assert.equal(skill.provider, 'talus');
    assert.equal(skill.source, 'bundled');
    assert.deepEqual(skill.invocation, { modelInvocable: true, userInvocable: true });
    assert.match(skill.description, /Manage servers registered in a running Talus instance/);
    assert.match(skill.content, /X-API-Key/);
    assert.match(skill.content, /TALUS_API_KEY/);
    assert.match(skill.content, /DSH_TALUS_API_KEY/);
    assert.match(skill.content, /Plugins → Talus/);
    assert.ok(!skill.content.startsWith('---'), 'DSH must parse YAML frontmatter');
    assert.equal(skill.resourceBase.kind, 'directory');
    assert.equal(skill.resourceBase.path, join(installed, 'skills/talus'));
    for (const reference of ['references/files.md', 'references/metrics.md']) {
      assert.ok(skill.content.includes(reference));
      assert.ok((await readFile(join(skill.resourceBase.path, reference), 'utf8')).length > 0);
    }
    assert.ok(packed.files.every(file => !/^(scripts|tests|node_modules)\//.test(file.path)));
  });

  await t.test('UI config reaches real HTTP requests and updates without a reload', async (t) => {
    const original = {
      TALUS_URL: process.env.TALUS_URL,
      TALUS_API_KEY: process.env.TALUS_API_KEY,
    };
    const requests = [];
    const server = createServer((request, response) => {
      requests.push({ path: request.url, key: request.headers['x-api-key'] });
      response.setHeader('Content-Type', 'application/json');
      response.end(JSON.stringify({ data: [] }));
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    t.after(async () => {
      await new Promise(resolve => server.close(resolve));
      for (const [key, value] of Object.entries(original)) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
    });
    const address = `http://127.0.0.1:${server.address().port}`;
    process.env.TALUS_URL = `${address}/environment`;
    process.env.TALUS_API_KEY = 'environment-test-key';
    async function request() {
      const env = ctx.shellEnv.collect({});
      return run('bash', ['-c',
        'export TALUS_URL="$DSH_TALUS_URL" TALUS_API_KEY="$DSH_TALUS_API_KEY"; curl --silent --show-error --fail --max-time 5 "$TALUS_URL/api/v1/servers" -H "X-API-Key: $TALUS_API_KEY"',
      ], { env: { ...process.env, ...env } });
    }
    await mounted.update({ url: `${address}/first/`, apiKey: 'first-ui-test-key' });
    const firstSkill = await ctx.skills.get('talus');
    assert.ok(!firstSkill.content.includes('first-ui-test-key'));
    assert.ok(!JSON.stringify(ctx.shellEnv.list()).includes('first-ui-test-key'));
    await request();
    assert.deepEqual(requests.at(-1), { path: '/first/api/v1/servers', key: 'first-ui-test-key' });
    await mounted.update({ url: `${address}/second`, apiKey: 'second-ui-test-key' });
    await request();
    assert.deepEqual(requests.at(-1), { path: '/second/api/v1/servers', key: 'second-ui-test-key' });
    assert.equal(process.env.TALUS_URL, `${address}/environment`);
    assert.equal(process.env.TALUS_API_KEY, 'environment-test-key');
    await mounted.update({ url: '', apiKey: '' });
    await request();
    assert.deepEqual(requests.at(-1), { path: '/environment/api/v1/servers', key: 'environment-test-key' });
  });

  await t.test('an existing higher-priority talus skill still wins', async () => {
    const overrideRoot = join(scratch, 'override');
    await mkdir(overrideRoot);
    await writeFile(join(overrideRoot, 'talus.md'),
      '---\nname: talus\ndescription: User override.\n---\nMy Talus instructions.\n');
    const override = ctx.plugin(filesystem, {
      providerName: 'override', includeDefaultRoots: false,
      customSkillDirs: [overrideRoot], watch: false,
    });
    await override.await();
    assert.equal((await ctx.skills.get('talus')).provider, 'override');
    await override.dispose();
    assert.equal((await ctx.skills.get('talus')).provider, 'talus');
  });

  await t.test('disabling and re-enabling the plugin cleans up its registration', async () => {
    await mounted.dispose();
    assert.ok(!ctx.shellEnv.list().some(variable => variable.contributor === 'talus'));
    assert.deepEqual((await ctx.skills.list()).map(skill => skill.name), ['existing']);
    assert.equal(await ctx.skills.get('talus'), undefined);
    const remounted = ctx.plugin(plugin);
    await remounted.await();
    assert.equal((await ctx.skills.get('talus')).provider, 'talus');
    await remounted.dispose();
  });
});
