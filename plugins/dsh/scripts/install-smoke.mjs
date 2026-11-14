// Exercise both documented installs in profiles that cannot resolve checkout devDependencies.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { createServer } from 'node:https';
import { mkdtemp, mkdir, readFile, rm, writeFile, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const scratch = await mkdtemp(join(tmpdir(), 'talus-dsh-install-'));
const testKey = 'talus-isolated-install-fixture-key';
const sensitiveValues = Object.entries(process.env)
  .filter(([name, value]) => /(?:TOKEN|PASSWORD|SECRET|API_KEY|AUTH)/i.test(name) && value && value.length >= 4)
  .map(([, value]) => value);
const inherited = { ...process.env };
delete inherited.TALUS_API_KEY;
delete inherited.DSH_TALUS_API_KEY;
delete inherited.NODE_PATH;
delete inherited.NODE_OPTIONS;
let server;
const processes = new Set();

function clean(text) {
  let result = text.replace(/(token=)[^\s&]+/gi, '$1[redacted]').replaceAll(testKey, '[fixture-key]');
  for (const value of sensitiveValues) result = result.replaceAll(value, '[redacted]');
  return result;
}
function run(command, args, { cwd = scratch, env = inherited, timeout = 180_000, marker } = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, env, stdio: ['ignore', 'pipe', 'pipe'], detached: process.platform !== 'win32' });
    processes.add(child);
    let output = '', stdout = '', markerSeen = false, failureSeen = false, settled = false;
    const stop = signal => {
      try { if (process.platform !== 'win32') process.kill(-child.pid, signal); else child.kill(signal); } catch {}
    };
    const timer = setTimeout(() => { stop('SIGKILL'); finish(new Error(`${command}: timed out\n${clean(output)}`)); }, timeout);
    const finish = (error, result) => {
      if (settled) return;
      settled = true; clearTimeout(timer); processes.delete(child);
      if (error) reject(error); else resolve(result);
    };
    const collect = chunk => {
      output = (output + chunk.toString()).slice(-100_000);
      if (marker && output.includes(marker) && !markerSeen) { markerSeen = true; stop('SIGTERM'); }
      if (marker && output.includes('TALUS_INSTALL_SMOKE_ERROR:') && !failureSeen) { failureSeen = true; stop('SIGTERM'); }
    };
    child.stdout.on('data', chunk => { stdout += chunk.toString(); collect(chunk); }); child.stderr.on('data', collect);
    child.once('error', error => finish(error));
    child.once('close', (code, signal) => {
      if (markerSeen) return finish(undefined, output);
      if (code === 0 && !marker) return finish(undefined, stdout);
      finish(new Error(`${command}: ${marker ? 'plugin did not become active' : `exit ${code ?? signal}`}\n${clean(output)}`));
    });
  });
}

try {
  const cert = join(scratch, 'fixture-cert.pem'), key = join(scratch, 'fixture-key.pem');
  await run('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-keyout', key,
    '-out', cert, '-subj', '/CN=Talus DSH install test', '-addext', 'subjectAltName=IP:127.0.0.1',
    '-addext', 'basicConstraints=critical,CA:TRUE']);
  const seen = [];
  server = createServer({ key: await readFile(key), cert: await readFile(cert) }, (req, res) => {
    seen.push({ path: req.url, key: req.headers['x-api-key'] });
    res.writeHead(req.headers['x-api-key'] === testKey ? 200 : 401, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ data: [] }));
  });
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const url = `https://127.0.0.1:${server.address().port}`;
  const packed = JSON.parse(await run('npm', ['pack', '--json', '--ignore-scripts', '--pack-destination', scratch], { cwd: root }));
  const archive = join(scratch, packed[0].filename);
  const extracted = join(scratch, 'extracted');
  await mkdir(extracted);
  await run('tar', ['-xzf', archive, '-C', extracted]);
  const sourceDir = join(extracted, 'package');
  // A linked directory cannot inherit devDependencies from the checkout.
  await run('npm', ['install', '--omit=dev', '--ignore-scripts'], { cwd: sourceDir });
  const observer = join(scratch, 'observer.mjs');
  await writeFile(observer, `
import { createHash } from 'node:crypto';
export const name = 'talus-smoke-observer';
export const inject = ['loader', 'skills', 'tools', 'shellEnv', 'credentials'];
export function apply(ctx) {
  let busy = false, done = false;
  const timer = setInterval(async () => {
    if (busy || done) return;
    const target = [...ctx.loader.entries()].find(entry => entry.options.id === 'talus');
    if (target?.fiber?.state !== 2) return;
    busy = true;
    try {
      const skill = await ctx.skills.get('talus');
      const shell = ctx.shellEnv.collect({});
      const tool = ctx.tools.get('talus_request');
      if (skill?.provider !== 'talus' || !tool || !shell.DSH_TALUS_BROKER || !shell.DSH_TALUS_HELPER) throw new Error('missing plugin contribution');
      if (JSON.stringify(shell).includes(${JSON.stringify(testKey)})) throw new Error('credential entered shell environment');
      const ref = 'TALUS_' + createHash('sha256').update(${JSON.stringify(url)}).digest('hex').toUpperCase();
      await ctx.credentials.set(ref, ${JSON.stringify(testKey)});
      const batch = await ctx.tools.execute({
        calls: [{ type: 'tool-call', id: 'talus-install-smoke', name: 'talus_request',
          arguments: JSON.stringify({ path: '/api/v1/servers' }) }],
        signal: new AbortController().signal,
      });
      const result = batch.results[0]?.result;
      if (!result || result.isError || result.value.status !== 200 || JSON.parse(result.value.body).data.length !== 0) throw new Error('tool dispatch failed');
      if (JSON.stringify(result.content).includes(${JSON.stringify(testKey)})) throw new Error('credential entered tool output');
      if (target.fiber.state !== 2) throw new Error('plugin stopped');
      done = true; console.log('TALUS_INSTALL_SMOKE_ACTIVE');
    } catch (error) { done = true; console.error('TALUS_INSTALL_SMOKE_ERROR:', error.message); process.exitCode = 1; }
  }, 200);
  ctx.effect(() => () => clearInterval(timer));
}
`);
  for (const [mode, spec] of [['archive', archive], ['link', sourceDir]]) {
    const base = join(scratch, mode), documents = join(base, 'documents');
    await mkdir(documents, { recursive: true });
    const env = { ...inherited, DSH_HOME: join(base, 'dsh'), DSH_AGENTS_HOME: join(base, 'agents'),
      XDG_CACHE_HOME: join(scratch, 'cache'), XDG_DATA_HOME: join(scratch, 'data') };
    await run('dsh', ['plugin', '--profile', 'web', 'add', spec], { env });
    const installed = join(env.DSH_HOME, 'profiles/web/node_modules/dsh-plugin-talus');
    if (mode === 'link') assert.equal(await realpath(installed), await realpath(sourceDir), 'directory install must exercise a real link');
    const patch = join(base, 'observer.yml');
    await writeFile(patch, `- id: talus\n  config:\n    url: ${JSON.stringify(url)}\n- id: workspace-controller\n  config:\n    documentsDirectory: ${JSON.stringify(documents)}\n- insert:\n    - id: talus-smoke-observer\n      name: ${JSON.stringify(observer)}\n`);
    const before = seen.length;
    await run('dsh', ['--profile', 'web', '--patch', patch, '--no-open', '--host', '127.0.0.1', '--port', '0'],
      { env: { ...env, NODE_EXTRA_CA_CERTS: cert }, timeout: 90_000, marker: 'TALUS_INSTALL_SMOKE_ACTIVE' });
    assert.deepEqual(seen.slice(before), [{ path: '/api/v1/servers', key: testKey }]);
    console.log(`✓ ${mode}: clean DSH profile activated the plugin and made a verified HTTPS tool request`);
  }
} finally {
  for (const child of processes) { try { if (process.platform !== 'win32') process.kill(-child.pid, 'SIGKILL'); else child.kill('SIGKILL'); } catch {} }
  if (server) { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
  await rm(scratch, { recursive: true, force: true });
}
