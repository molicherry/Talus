import assert from 'node:assert/strict';
import { execFile, execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from 'node:fs/promises';
import { createServer } from 'node:https';
import { join } from 'node:path';
import { getCACertificates, setDefaultCACertificates } from 'node:tls';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { promisify } from 'node:util';
import test from 'node:test';
import { Context, Service } from '@deepseek-ai/cordis';
import Loader from '@deepseek-ai/cordis-plugin-loader';
import { CredentialProvider, credentialRef } from '@deepseek-ai/dsh-credentials';
import Settings, { redactSecrets } from '@deepseek-ai/dsh-settings';
import Skills from '@deepseek-ai/dsh-skill';
import * as filesystem from '@deepseek-ai/dsh-skill-filesystem';
import * as shellEnv from '@deepseek-ai/dsh-shell-env';
import SystemPrompt from '@deepseek-ai/dsh-system-prompt';
import Tools from '@deepseek-ai/dsh-tools';
import { parse } from 'yaml';

const root = fileURLToPath(new URL('../', import.meta.url));
const run = promisify(execFile);
const reference = url => credentialRef('TALUS_' + createHash('sha256')
  .update(new URL(url.trim()).toString().replace(/\/+$/, '')).digest('hex').toUpperCase());

// Exercise the native service contract without writing a user credential file.
class TestCredentials extends CredentialProvider {
  values = new Map();
  beforeResolve;
  resolve(ref) {
    const value = this.values.get(ref);
    const hit = value ? { value, source: 'test-store' } : undefined;
    return this.beforeResolve ? Promise.resolve(this.beforeResolve(ref)).then(() => hit) : hit;
  }
  describe(ref) {
    return { configured: this.values.has(ref), writable: true,
      ...(this.values.has(ref) ? { source: 'test-store' } : {}) };
  }
  set(ref, value) {
    assert.ok(value);
    this.values.set(ref, value);
    this.notifyUpdated(ref);
  }
  unset(ref) {
    this.values.delete(ref);
    this.notifyUpdated(ref);
  }
}

// Real SettingsForms owns redaction/revisions. The fixture replaces persistence
// while edits still pass through the actual Loader volatile-update machinery.
class TestConfigEditor extends Service {
  static inject = ['loader'];
  pending = Promise.resolve();
  beforeEdit;
  constructor(ctx) { super(ctx, 'configEditor'); }
  get documentPath() { return join(this.ctx.profileContext.home, 'cordis.patch.yml'); }
  entries() { return [...this.ctx.loader.entries()]; }
  configuration() {
    return this.entries().map(entry => ({ entry, inherited: {},
      override: structuredClone(entry.options.config ?? {}) }));
  }
  edit(entry, change) {
    const task = this.pending.then(async () => {
      await this.beforeEdit?.();
      const next = change(structuredClone(entry.options.config ?? {}), {});
      await this.ctx.loader.update(entry.id, { config: next });
    });
    this.pending = task.catch(() => {});
    return task;
  }
}

async function eventually(check) {
  const deadline = Date.now() + 5000;
  for (;;) {
    try { return await check(); }
    catch (error) {
      if (Date.now() >= deadline) throw error;
      await new Promise(resolve => setTimeout(resolve, 10));
    }
  }
}

test('packed bundle loads through DSH, preserves other skills, and unloads cleanly', async (t) => {
  // The extracted package is independent of the Talus source checkout. Peers
  // resolve through its parent node_modules, as in an installed DSH profile.
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
  assert.equal(manifest.dsh.bundle.patch, './cordis.patch.yml');
  assert.equal(patch[0].insert[0].name, manifest.name);
  const plugin = await import(pathToFileURL(join(installed, manifest.main)));
  const customRoot = join(scratch, 'existing-skills');
  await mkdir(customRoot);
  await writeFile(join(customRoot, 'existing.md'),
    '---\nname: existing\ndescription: Existing user skill.\n---\nKeep this skill.\n');
  ctx.provide('profileContext', { home: scratch, name: 'test', dir: scratch });
  await ctx.plugin(Loader).await();
  await ctx.plugin(TestCredentials).await();
  await ctx.plugin(TestConfigEditor).await();
  await ctx.plugin(Settings).await();
  await ctx.plugin(SystemPrompt).await();
  await ctx.plugin(Tools).await();
  await ctx.plugin(Skills).await();
  await ctx.plugin(shellEnv).await();
  await ctx.plugin(filesystem, {
    providerName: 'existing', includeDefaultRoots: false,
    customSkillDirs: [customRoot], watch: false,
  }).await();
  ctx.loader.builtins.talus = plugin;
  await ctx.loader.create({ id: 'talus', name: 'cordis:talus' });
  const entry = ctx.loader.resolve('talus');
  await entry.fiber.await();
  const tool = () => {
    const definition = ctx.tools.get('talus_request');
    assert.ok(definition, 'the host must register its authenticated request tool');
    return definition;
  };
  const execute = (args, signal = new AbortController().signal) => tool().execute(args, { signal });
  const update = config => ctx.loader.update('talus', { config });

  await t.test('settings reads redact the legacy API key and config validates URLs', async () => {
    const redacted = redactSecrets(plugin.Config, { url: 'https://talus.example.test', apiKey: 'private-test-key' });
    assert.deepEqual(redacted.value, { url: 'https://talus.example.test' });
    assert.deepEqual(redacted.secrets, [{ path: ['apiKey'], set: true }]);
    assert.ok(!JSON.stringify(redacted).includes('private-test-key'));
    for (const url of ['javascript:alert(1)', 'https://user:password@example.test', 'https://talus.example.test/api/v1/']) {
      assert.throws(() => entry.fiber.resolveConfig({ url }));
    }
    assert.throws(() => entry.fiber.resolveConfig({ url: 'https://talus.example.test', apiKey: 'invalid\nkey' }));
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
    for (const text of ['X-API-Key', 'talus_request', 'DSH_TALUS_BROKER', 'DSH_TALUS_HELPER', 'Plugins → Talus']) {
      assert.ok(skill.content.includes(text));
    }
    assert.ok(!skill.content.startsWith('---'), 'DSH must parse YAML frontmatter');
    assert.equal(skill.resourceBase.kind, 'directory');
    assert.equal(skill.resourceBase.path, join(installed, 'skills/talus'));
    for (const resource of ['references/files.md', 'references/metrics.md',
      'scripts/request.mjs', 'scripts/broker.mjs', 'scripts/private-client.mjs']) {
      assert.ok((await readFile(join(skill.resourceBase.path, resource), 'utf8')).length > 0);
    }
    assert.ok(packed.files.every(file => !/^(scripts|tests|node_modules)\//.test(file.path)));
  });

  await t.test('native credentials reach HTTPS requests without entering the shell and rotate live', async (t) => {
    const oldEnvironment = { TALUS_URL: process.env.TALUS_URL, TALUS_API_KEY: process.env.TALUS_API_KEY };
    process.env.TALUS_URL = 'https://ignored.example.test';
    process.env.TALUS_API_KEY = 'ignored-ambient-private-key';
    t.after(() => {
      for (const [name, value] of Object.entries(oldEnvironment)) {
        if (value === undefined) delete process.env[name]; else process.env[name] = value;
      }
    });
    const certPath = join(scratch, 'ca.pem'), keyPath = join(scratch, 'tls-key.pem');
    execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes',
      '-keyout', keyPath, '-out', certPath, '-subj', '/CN=Talus plugin test', '-days', '1',
      '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost',
      '-addext', 'basicConstraints=critical,CA:TRUE'], { stdio: 'pipe' });
    const requests = [];
    const server = createServer({ key: await readFile(keyPath), cert: await readFile(certPath) }, (request, response) => {
      requests.push({ path: request.url, key: request.headers['x-api-key'] });
      if (request.url.endsWith('/redirect')) {
        response.writeHead(302, { location: `https://127.0.0.1:${server.address().port}/leaked` });
        response.end();
      } else {
        response.setHeader('Content-Type', 'application/json');
        response.end(JSON.stringify({ data: [{ id: 7 }] }));
      }
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const originalCAs = getCACertificates('default');
    t.after(async () => {
      setDefaultCACertificates(originalCAs);
      server.closeAllConnections();
      await new Promise(resolve => server.close(resolve));
    });
    const address = `https://127.0.0.1:${server.address().port}`;
    const first = `${address}/first`, second = `${address}/second`;
    assert.equal(plugin.credentialReference(first + '/'), reference(first));
    await ctx.credentials.set(reference(first), 'first-native-private-key');
    await update({ url: first });
    await assert.rejects(execute({ path: '/api/v1/servers' }), /TLS|trust|request failed/i);
    assert.equal(requests.length, 0, 'untrusted TLS must not receive an authenticated HTTP request');
    setDefaultCACertificates([...originalCAs, await readFile(certPath, 'utf8')]);
    const result = await execute({ path: '/api/v1/servers' });
    assert.equal(result.status, 200);
    assert.deepEqual(JSON.parse(result.body), { data: [{ id: 7 }] });
    assert.deepEqual(requests.at(-1), { path: '/first/api/v1/servers', key: 'first-native-private-key' });
    assert.ok(!JSON.stringify(result).includes('first-native-private-key'));
    const env = ctx.shellEnv.collect({});
    assert.deepEqual(Object.keys(env).filter(name => name.startsWith('DSH_TALUS_')).sort(),
      ['DSH_TALUS_BROKER', 'DSH_TALUS_HELPER']);
    assert.ok(!JSON.stringify(env).includes('private-key'));
    assert.ok(!JSON.stringify(ctx.shellEnv.list()).includes('private-key'));
    const descriptor = await readFile(env.DSH_TALUS_BROKER, 'utf8');
    assert.ok(!descriptor.includes('first-native-private-key'));
    if (process.platform !== 'win32') assert.equal((await stat(env.DSH_TALUS_BROKER)).mode & 0o077, 0);
    const args = [env.DSH_TALUS_HELPER, '--broker', env.DSH_TALUS_BROKER, '--path', '/api/v1/servers'];
    const childEnvironment = { PATH: process.env.PATH, NODE_EXTRA_CA_CERTS: certPath };
    assert.ok(!JSON.stringify({ args, env: childEnvironment }).includes('private-key'));
    const child = await run(process.execPath, args, { env: childEnvironment });
    assert.deepEqual(JSON.parse(child.stdout), { data: [{ id: 7 }] });
    assert.deepEqual(requests.at(-1), { path: '/first/api/v1/servers', key: 'first-native-private-key' });
    await ctx.credentials.set(reference(first), 'rotated-native-private-key');
    await execute({ path: '/api/v1/servers' });
    assert.equal(requests.at(-1).key, 'rotated-native-private-key');
    const mountedFiber = entry.fiber;
    await update({ url: second });
    assert.equal(entry.fiber, mountedFiber, 'URL changes apply without remounting');
    const before = requests.length;
    await assert.rejects(execute({ path: '/api/v1/servers' }), /key|credential|configur/i);
    assert.equal(requests.length, before, 'a new target must not receive the previous target key');
    await ctx.credentials.set(reference(second), 'second-native-private-key');
    await execute({ path: '/api/v1/servers' });
    assert.deepEqual(requests.at(-1), { path: '/second/api/v1/servers', key: 'second-native-private-key' });
    await ctx.credentials.unset(reference(second));
    await assert.rejects(execute({ path: '/api/v1/servers' }), /key|credential|configur/i);
    await update({ url: first });
    const prior = requests.length;
    await assert.rejects(execute({ path: 'https://example.test/api/v1/servers' }), /path|relative/i);
    await assert.rejects(execute({ path: '/api/v1/servers', headers: { 'X-API-Key': 'caller-key' } }), /header|auth/i);
    assert.equal(requests.length, prior);
    await assert.rejects(execute({ path: '/api/v1/redirect' }), /redirect/i);
    assert.ok(!requests.some(request => request.path === '/leaked'));
    await update({ url: address.replace('https:', 'http:') });
    await assert.rejects(execute({ path: '/api/v1/servers' }), /HTTPS/i);
    await update({});
    await assert.rejects(execute({ path: '/api/v1/servers' }), /URL|configur|HTTPS/i);
    assert.equal(process.env.TALUS_API_KEY, 'ignored-ambient-private-key');
    const legacyUrl = `${address}/legacy`;
    await update({ url: legacyUrl, apiKey: 'legacy-private-key' });
    const beforeLegacy = requests.length;
    await assert.rejects(execute({ path: '/api/v1/servers' }), /key|credential|configur/i);
    assert.equal(requests.length, beforeLegacy, 'legacy plaintext config must not authenticate');
    assert.equal(ctx.credentials.resolve(reference(legacyUrl)), undefined, 'legacy keys never overwrite the native store');
    await ctx.credentials.set(reference(legacyUrl), 'replacement-native-private-key');
    await execute({ path: '/api/v1/servers' });
    assert.equal(requests.at(-1).key, 'replacement-native-private-key');
    await eventually(() => assert.ok(!entry.options.config?.apiKey, 'public config must clear the obsolete secret'));
    assert.ok(!JSON.stringify(ctx.settings.describe({ redactSecrets: true })).includes('private-key'));

    // Native writes and public settings edits are separate operations. A stale
    // cleanup must retain a newer UI edit, including its still-unmigrated key.
    const conflictUrl = `${address}/conflict`, newerUrl = `${address}/newer`;
    await update({ url: conflictUrl, apiKey: 'obsolete-private-key' });
    ctx.configEditor.beforeEdit = async () => {
      ctx.configEditor.beforeEdit = undefined;
      await update({ url: newerUrl, apiKey: 'concurrent-private-key' });
    };
    await ctx.credentials.set(reference(conflictUrl), 'conflict-native-private-key');
    await eventually(() => assert.equal(ctx.configEditor.beforeEdit, undefined));
    await ctx.configEditor.pending;
    assert.deepEqual(entry.options.config, { url: newerUrl, apiKey: 'concurrent-private-key' });
    const beforeConflictRequest = requests.length;
    await assert.rejects(execute({ path: '/api/v1/servers' }), /key|credential|configur/i);
    assert.equal(requests.length, beforeConflictRequest);

    // A second address must still get a cleanup attempt if it is configured
    // while a stale cleanup for the first address is waiting to commit.
    const queuedFirst = `${address}/queued-first`, queuedSecond = `${address}/queued-second`;
    await update({ url: queuedFirst, apiKey: 'queued-old-first-key' });
    let releaseCleanup, enteredCleanup;
    const waitingCleanup = new Promise(resolve => { releaseCleanup = resolve; });
    const cleanupStarted = new Promise(resolve => { enteredCleanup = resolve; });
    t.after(() => releaseCleanup());
    ctx.configEditor.beforeEdit = async () => {
      ctx.configEditor.beforeEdit = undefined;
      enteredCleanup();
      await waitingCleanup;
    };
    await ctx.credentials.set(reference(queuedFirst), 'queued-native-first-key');
    await Promise.race([cleanupStarted, new Promise((_, reject) => {
      const timer = setTimeout(() => reject(new Error('legacy cleanup did not start')), 5000);
      timer.unref();
    })]);
    await update({ url: queuedSecond, apiKey: 'queued-old-second-key' });
    await ctx.credentials.set(reference(queuedSecond), 'queued-native-second-key');
    await new Promise(resolve => setImmediate(resolve));
    releaseCleanup();
    await eventually(() => assert.ok(!entry.options.config?.apiKey, 'the second address must not lose its cleanup'));
    assert.equal(entry.options.config.url, queuedSecond);

    // Snapshot the destination before asynchronously resolving its key: a live
    // URL change cannot pair the old key with the newly configured destination.
    await update({ url: first });
    ctx.credentials.beforeResolve = async () => {
      ctx.credentials.beforeResolve = undefined;
      await update({ url: newerUrl });
    };
    await execute({ path: '/api/v1/servers' });
    assert.deepEqual(requests.at(-1), { path: '/first/api/v1/servers', key: 'rotated-native-private-key' });

    // Credential providers may finish after a deadline or caller cancellation.
    // Stop waiting promptly, and never let a late secret start an HTTP request.
    await update({ url: first });
    ctx.credentials.beforeResolve = async () => {
      ctx.credentials.beforeResolve = undefined;
      throw new Error('Provider parse failure containing synthetic-private-material');
    };
    await assert.rejects(execute({ path: '/api/v1/servers' }), error => {
      assert.match(error.message, /credential store/i);
      assert.ok(!error.message.includes('synthetic-private-material'));
      return true;
    });
    for (const cancellation of ['deadline', 'caller']) {
      let release;
      const resolution = new Promise(resolve => { release = resolve; });
      ctx.credentials.beforeResolve = () => {
        ctx.credentials.beforeResolve = undefined;
        return resolution;
      };
      const controller = new AbortController();
      const beforeBlockedRequest = requests.length;
      const pending = execute({ path: '/api/v1/servers',
        ...(cancellation === 'deadline' ? { timeout_ms: 25 } : {}) }, controller.signal);
      if (cancellation === 'caller') controller.abort();
      let watchdog;
      try {
        await Promise.race([
          assert.rejects(pending, /cancelled|timed out|abort/i),
          new Promise((_, reject) => {
            watchdog = setTimeout(() => reject(new Error('credential resolution did not stop within its cancellation budget')), 2000);
          }),
        ]);
        assert.equal(requests.length, beforeBlockedRequest);
      } finally {
        clearTimeout(watchdog);
        release();
        ctx.credentials.beforeResolve = undefined;
      }
      await new Promise(resolve => setImmediate(resolve));
      assert.equal(requests.length, beforeBlockedRequest, 'late credential resolution must not start a request');
    }
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

  await t.test('disabling and re-enabling cleans up the tool, broker and shell registration', async () => {
    const descriptorPath = ctx.shellEnv.collect({}).DSH_TALUS_BROKER;
    await entry.fiber.dispose();
    assert.ok(!ctx.shellEnv.list().some(variable => variable.contributor === 'talus'));
    assert.equal(ctx.tools.get('talus_request'), undefined);
    await assert.rejects(readFile(descriptorPath), { code: 'ENOENT' });
    assert.deepEqual((await ctx.skills.list()).map(skill => skill.name), ['existing']);
    assert.equal(await ctx.skills.get('talus'), undefined);
    await ctx.loader.update('talus', { disabled: false, config: {} });
    await entry.fiber.await();
    assert.equal((await ctx.skills.get('talus')).provider, 'talus');
    assert.ok(ctx.tools.get('talus_request'));
    await entry.fiber.dispose();
  });
});
