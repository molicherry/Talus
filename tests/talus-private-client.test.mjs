import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { chmod, lstat, mkdtemp, readFile, rm, symlink, writeFile } from 'node:fs/promises';
import { createServer as createHTTPServer, request as httpRequest } from 'node:http';
import { createServer as createHTTPSServer } from 'node:https';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { getCACertificates, setDefaultCACertificates } from 'node:tls';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { createBroker, readHostConfig } from '../skills/talus/scripts/broker.mjs';
import { request, resolveApiUrl, validateBaseUrl } from '../skills/talus/scripts/private-client.mjs';

const cliPath = fileURLToPath(new URL('../skills/talus/scripts/request.mjs', import.meta.url));
const secret = 'private-host-test-key-' + randomBytes(20).toString('hex');
const binary = Buffer.from([0, 255, 128, 13, 10, 1, 2, 3]);
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

async function listening(server, path) {
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(path ?? { host: '127.0.0.1', port: 0 }, resolve);
  });
  return server;
}

async function closeServer(server) {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
}

async function localRequest(descriptor, { token = descriptor.token, path = '/api/v1/echo', method = 'GET', body, headers = {} } = {}) {
  return new Promise((resolve, reject) => {
    const outgoing = httpRequest({ socketPath: descriptor.socketPath, path, method, agent: false,
      headers: { ...(token === undefined ? {} : { 'x-talus-broker-token': token }), ...headers } }, response => {
      const chunks = [];
      response.on('data', chunk => chunks.push(chunk));
      response.once('error', reject);
      response.once('end', () => resolve({ status: response.statusCode, headers: response.headers, body: Buffer.concat(chunks) }));
    });
    outgoing.once('error', reject);
    outgoing.end(body);
  });
}

async function cli(args, env = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(process.execPath, [cliPath, ...args], {
      env: { ...process.env, TALUS_API_KEY: 'ambient-key-must-be-ignored', TALUS_URL: 'https://ignored.invalid', ...env },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    const stdout = [], stderr = [];
    child.stdout.on('data', chunk => stdout.push(chunk));
    child.stderr.on('data', chunk => stderr.push(chunk));
    child.once('error', reject);
    child.once('close', code => resolve({ code, stdout: Buffer.concat(stdout).toString(), stderr: Buffer.concat(stderr).toString() }));
  });
}

test('private Talus client keeps credentials in the HTTPS host and transfers files over private IPC', async t => {
  const scratch = await mkdtemp(join(tmpdir(), 'talus-private-client-test-'));
  const originals = getCACertificates('default');
  t.after(async () => {
    setDefaultCACertificates(originals);
    await rm(scratch, { recursive: true, force: true });
  });
  const keyPath = join(scratch, 'tls-key.pem'), certPath = join(scratch, 'ca.pem');
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes',
    '-keyout', keyPath, '-out', certPath, '-days', '1', '-subj', '/CN=Talus local test',
    '-addext', 'subjectAltName=IP:127.0.0.1,DNS:localhost',
    '-addext', 'basicConstraints=critical,CA:TRUE'], { stdio: 'ignore' });
  const seen = [];
  let redirected = 0, slowClosed = 0;
  const other = await listening(createHTTPSServer({ key: await readFile(keyPath), cert: await readFile(certPath) }, (_req, res) => {
    redirected++; res.end('unexpected redirect');
  }));
  const otherUrl = `https://127.0.0.1:${other.address().port}`;
  const remote = await listening(createHTTPSServer({ key: await readFile(keyPath), cert: await readFile(certPath) }, async (req, res) => {
    const chunks = [];
    for await (const chunk of req) chunks.push(chunk);
    const body = Buffer.concat(chunks);
    seen.push({ method: req.method, path: req.url, headers: req.headers, body });
    const path = req.url.split('?')[0];
    if (path.endsWith('/redirect')) { res.writeHead(302, { location: `${otherUrl}/api/v1/echo` }); return res.end(); }
    if (path.endsWith('/error')) { res.writeHead(403, { 'content-type': 'application/json' }); return res.end('{"error":"denied"}'); }
    if (path.endsWith('/bytes')) { res.writeHead(200, { 'content-type': 'application/octet-stream', 'content-length': String(binary.length), etag: '"binary"' }); return res.end(binary); }
    if (path.endsWith('/large')) { res.writeHead(200, { 'content-type': 'text/plain' }); return res.end('x'.repeat(129)); }
    if (path.endsWith('/leak')) { res.writeHead(200, { 'content-type': 'text/plain' }); return res.end(secret); }
    if (path.endsWith('/header-leak')) { res.writeHead(200, secret, { 'x-api-key': secret, 'set-cookie': 'private-session=value', 'x-echo-secret': secret, 'content-type': 'text/plain' }); return res.end('safe'); }
    if (path.endsWith('/slow')) {
      res.writeHead(200, { 'content-type': 'text/plain' }); res.write('started');
      const timer = setTimeout(() => res.end('finished'), 500);
      res.once('close', () => { slowClosed++; clearTimeout(timer); }); return;
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end(JSON.stringify({ method: req.method, path: req.url, body: body.toString('base64'), contentType: req.headers['content-type'], range: req.headers.range }));
  }));
  t.after(async () => { await closeServer(remote); await closeServer(other); });
  const baseUrl = `https://127.0.0.1:${remote.address().port}`;
  const call = options => request({ baseUrl, apiKey: secret, path: '/api/v1/echo', ...options });

  await t.test('requires trusted HTTPS, rejects TLS bypass, and keeps the key inside host requests', async () => {
    await assert.rejects(call({}), { code: 'REQUEST_FAILED' });
    setDefaultCACertificates([...originals, await readFile(certPath, 'utf8')]);
    const response = await call({ method: 'POST', path: '/api/v1/echo?q=%2Fa&q=b+z', json: { command: 'printf test' } });
    assert.equal(response.status, 200);
    const result = await response.json();
    assert.equal(result.path, '/api/v1/echo?q=%2Fa&q=b+z');
    assert.equal(Buffer.from(result.body, 'base64').toString(), '{"command":"printf test"}');
    assert.equal(seen.at(-1).headers['x-api-key'], secret);
    assert.equal(seen.at(-1).headers['x-talus-broker-token'], undefined);
    assert.equal(seen.at(-1).headers['x-talus-signature'], undefined);
    // Isolate this deliberately unsafe environment from the test runner. The
    // child stubs fetch, so a broken guard cannot send even a test credential.
    const probe = await new Promise((resolve, reject) => {
      const child = spawn(process.execPath, ['--input-type=module', '-e', `
        import assert from 'node:assert/strict';
        const { request } = await import(process.argv[1]);
        let called = false;
        globalThis.fetch = () => { called = true; throw new Error('Unexpected network request'); };
        await assert.rejects(request({ baseUrl: 'https://127.0.0.1:1', apiKey: 'test-key',
          path: '/api/v1/echo' }), { code: 'UNSAFE_TLS_CONFIGURATION' });
        assert.equal(called, false);
      `, new URL('../skills/talus/scripts/private-client.mjs', import.meta.url).href], {
        env: { NODE_TLS_REJECT_UNAUTHORIZED: '0', ...(process.env.SystemRoot ? { SystemRoot: process.env.SystemRoot } : {}) },
        stdio: ['ignore', 'ignore', 'pipe'],
      });
      const stderr = [];
      child.stderr.on('data', chunk => stderr.push(chunk));
      child.once('error', reject);
      child.once('close', code => resolve({ code, stderr: Buffer.concat(stderr).toString() }));
    });
    assert.equal(probe.code, 0, probe.stderr);
  });

  await t.test('rejects HTTP, alternate targets, traversal, and caller authentication', async () => {
    for (const url of ['http://127.0.0.1:80', 'http://talus.example.test', 'https://u:p@talus.example.test',
      'https://talus.example.test/#fragment', 'https://talus.example.test/?q=1', 'https://talus.example.test/api/v1/']) {
      assert.throws(() => validateBaseUrl(url));
    }
    assert.equal(validateBaseUrl('http://127.0.0.1:80', { allowHttpLoopback: true }).protocol, 'http:');
    assert.throws(() => validateBaseUrl('http://example.test', { allowHttpLoopback: true }));
    for (const path of ['https://other.test/api/v1/echo', '//other.test/api/v1/echo', '/api/v1/../secret',
      '/api/v1/%2e%2e/secret', '/api/v1/a%2fb', '/api/v1/a\\b', '/api/v1/echo#fragment']) {
      assert.throws(() => resolveApiUrl(baseUrl, path));
    }
    for (const name of ['Authorization', 'Proxy-Authorization', 'X-API-Key', 'Host', 'Content-Length', 'Expect', 'Keep-Alive', 'X-Talus-Broker-Token']) {
      await assert.rejects(call({ headers: { [name]: 'forbidden' } }), { code: 'AUTH_HEADER_FORBIDDEN' });
    }
  });

  await t.test('does not follow redirects and bounds response reads, cancellation, and credential output', async () => {
    const start = seen.length;
    await assert.rejects(call({ method: 'POST', path: '/api/v1/redirect', json: {} }), { code: 'REDIRECT_FORBIDDEN' });
    assert.equal(redirected, 0);
    assert.equal(seen.length, start + 1, 'side-effecting requests are not retried');
    await assert.rejects(call({ path: '/api/v1/large', maxBodyBytes: 128 }), { code: 'RESPONSE_TOO_LARGE' });
    await assert.rejects(call({ method: 'POST', body: Buffer.alloc(129), maxBodyBytes: 128 }), { code: 'BODY_TOO_LARGE' });
    await assert.rejects(call({ path: '/api/v1/slow', timeoutMs: 50 }), { code: 'REQUEST_TIMEOUT' });
    const controller = new AbortController();
    const pending = call({ path: '/api/v1/slow', signal: controller.signal });
    setTimeout(() => controller.abort(), 30);
    await assert.rejects(pending, { code: 'REQUEST_ABORTED' });
    await assert.rejects(call({ path: '/api/v1/leak' }), { code: 'CREDENTIAL_IN_RESPONSE' });
    const safe = await call({ path: '/api/v1/header-leak' });
    assert.equal(safe.statusText, '');
    assert.equal(safe.headers.get('x-api-key'), null);
    assert.equal(safe.headers.get('x-echo-secret'), null);
    assert.equal(safe.headers.get('set-cookie'), null);
    assert.ok(slowClosed > 0);
  });

  const broker = await createBroker({ directory: scratch, resolveConfig: () => ({ url: baseUrl, apiKey: secret }) });
  t.after(() => broker.close());
  const descriptorText = await readFile(broker.descriptorPath, 'utf8');
  const descriptor = JSON.parse(descriptorText);

  await t.test('exposes only a private local capability and rejects malformed tokens without crashing', async () => {
    assert.equal(descriptor.version, 1);
    assert.ok(!descriptorText.includes(secret));
    assert.ok(!descriptorText.includes(baseUrl));
    if (process.platform !== 'win32') {
      assert.equal((await lstat(dirname(broker.descriptorPath))).mode & 0o777, 0o700);
      assert.equal((await lstat(broker.descriptorPath)).mode & 0o777, 0o600);
      assert.equal((await lstat(descriptor.socketPath)).mode & 0o777, 0o600);
      assert.ok((await lstat(descriptor.socketPath)).isSocket());
    }
    for (const token of ['', 'wrong', 'é' + 'a'.repeat(42)]) assert.equal((await localRequest(descriptor, { token })).status, 401);
    const valid = await localRequest(descriptor);
    assert.equal(valid.status, 200);
    assert.ok(!valid.body.includes(Buffer.from(secret)));
    assert.equal(seen.at(-1).headers['x-talus-broker-token'], undefined);
    const head = await localRequest(descriptor, { method: 'HEAD', path: '/api/v1/bytes' });
    assert.equal(head.headers['content-length'], String(binary.length));
    assert.equal(head.body.length, 0);
    assert.equal((await localRequest(descriptor, { headers: { authorization: 'forbidden' } })).status, 400);
    assert.equal((await localRequest(descriptor, { path: '/api/v1/%2e%2e/secret' })).status, 400);
  });

  await t.test('CLI uploads and atomically downloads bytes without API key arguments or environment', async () => {
    const upload = join(scratch, 'upload.bin'), output = join(scratch, 'download.bin'), headers = join(scratch, 'headers.json');
    await writeFile(upload, binary);
    const common = ['--broker', broker.descriptorPath];
    const sent = await cli([...common, '--method', 'POST', '--path', '/api/v1/echo', '--data-file', upload, '--header', 'Content-Type=application/octet-stream', '--query', 'path=/a b']);
    assert.equal(sent.code, 0, sent.stderr);
    const result = JSON.parse(sent.stdout);
    assert.equal(result.path, '/api/v1/echo?path=%2Fa%20b');
    assert.deepEqual(Buffer.from(result.body, 'base64'), binary);
    assert.equal(seen.at(-1).headers['x-api-key'], secret);
    assert.equal(seen.at(-1).headers['x-talus-broker-timeout-ms'], undefined);
    const downloaded = await cli([...common, '--path', '/api/v1/bytes', '--output', output, '--headers-output', headers]);
    assert.equal(downloaded.code, 0, downloaded.stderr);
    assert.deepEqual(await readFile(output), binary);
    assert.equal(JSON.parse(await readFile(headers, 'utf8')).headers.etag, '"binary"');
    if (process.platform !== 'win32') {
      assert.equal((await lstat(output)).mode & 0o777, 0o600);
      assert.equal((await lstat(headers)).mode & 0o777, 0o600);
    }
    const jsonFile = join(scratch, 'request.json');
    await writeFile(jsonFile, '{"path":"/tmp/a"}');
    for (const bodyArgs of [['--json-file', jsonFile], ['--json', '{"path":"/tmp/a"}']]) {
      const sentJSON = await cli([...common, '--method', 'POST', '--path', '/api/v1/echo', ...bodyArgs]);
      assert.equal(sentJSON.code, 0, sentJSON.stderr);
      assert.equal(Buffer.from(JSON.parse(sentJSON.stdout).body, 'base64').toString(), '{"path":"/tmp/a"}');
    }
    for (const path of ['/api/v1/error', '/api/v1/redirect']) {
      const failed = await cli([...common, '--path', path, '--output', output, '--headers-output', headers]);
      assert.equal(failed.code, 2, failed.stderr);
      assert.deepEqual(await readFile(output), binary);
      assert.equal(JSON.parse(await readFile(headers, 'utf8')).status, 200);
      assert.ok(!failed.stderr.includes(secret));
      assert.ok(!failed.stderr.includes(descriptor.token));
    }
    const timedOut = await cli([...common, '--path', '/api/v1/slow', '--timeout-ms', '50']);
    assert.equal(timedOut.code, 124, timedOut.stderr);
    const binaryStdout = await cli([...common, '--path', '/api/v1/bytes']);
    assert.equal(binaryStdout.code, 1);
    for (const args of [['--api-key', 'forbidden'], ['--config', 'forbidden'], ['--header', 'X-API-Key=forbidden']]) {
      assert.equal((await cli([...common, '--path', '/api/v1/echo', ...args])).code, 1);
    }
    const viaPathEnv = await cli(['--path', '/api/v1/echo'], { TALUS_BROKER: broker.descriptorPath });
    assert.equal(viaPathEnv.code, 0, viaPathEnv.stderr);
    const linkedOutput = join(scratch, 'linked-output');
    await symlink(output, linkedOutput);
    assert.equal((await cli([...common, '--path', '/api/v1/bytes', '--output', linkedOutput])).code, 1);
    const linkedDescriptor = join(scratch, 'linked-descriptor');
    await symlink(broker.descriptorPath, linkedDescriptor);
    assert.equal((await cli(['--broker', linkedDescriptor, '--path', '/api/v1/echo'])).code, 1);
    if (process.platform !== 'win32') {
      await chmod(broker.descriptorPath, 0o644);
      assert.equal((await cli([...common, '--path', '/api/v1/echo'])).code, 1);
      await chmod(broker.descriptorPath, 0o600);
    }
  });

  await t.test('CLI passes its complete deadline to the broker as local metadata only', async () => {
    const socketPath = join(scratch, 'deadline.sock');
    const token = randomBytes(32).toString('base64url');
    const mock = await listening(createHTTPServer((req, res) => {
      res.writeHead(200, { 'content-type': 'application/json' });
      res.end(JSON.stringify({ timeout: req.headers['x-talus-broker-timeout-ms'], authenticated: req.headers['x-talus-broker-token'] === token }));
    }), socketPath);
    try {
      const path = join(scratch, 'deadline.json');
      await writeFile(path, JSON.stringify({ version: 1, socketPath, token }), { mode: 0o600 });
      const result = await cli(['--broker', path, '--path', '/api/v1/echo', '--timeout-ms', '60000']);
      assert.equal(result.code, 0, result.stderr);
      assert.deepEqual(JSON.parse(result.stdout), { timeout: '60000', authenticated: true });
    } finally { await closeServer(mock); }
  });

  await t.test('host configuration is private and cannot downgrade HTTPS', async () => {
    const path = join(scratch, 'host.json');
    await writeFile(path, JSON.stringify({ url: baseUrl, apiKey: secret }), { mode: 0o600 });
    assert.deepEqual(await readHostConfig(path), { url: baseUrl, apiKey: secret });
    const linked = join(scratch, 'host-link.json');
    await symlink(path, linked);
    await assert.rejects(readHostConfig(linked));
    if (process.platform !== 'win32') {
      await chmod(path, 0o644);
      await assert.rejects(readHostConfig(path));
      await chmod(path, 0o600);
    }
    await writeFile(path, JSON.stringify({ url: 'http://127.0.0.1', apiKey: secret }));
    await assert.rejects(readHostConfig(path));
  });

  await t.test('closing aborts active requests, settles handlers, and removes its private directory', async () => {
    let beganResolve;
    const started = new Promise(resolve => { beganResolve = resolve; });
    let finishResolve;
    const held = new Promise(resolve => { finishResolve = resolve; });
    let resolverSignal;
    const closingBroker = await createBroker({ directory: scratch, resolveConfig: ({ signal }) => { resolverSignal = signal; beganResolve(); return held; } });
    const local = JSON.parse(await readFile(closingBroker.descriptorPath, 'utf8'));
    const outstanding = localRequest(local).catch(() => undefined);
    await started;
    const callsBefore = seen.length;
    await Promise.race([closingBroker.close(), delay(1000).then(() => assert.fail('broker close must cancel a hung credential resolver'))]);
    await outstanding;
    assert.ok(resolverSignal.aborted);
    await assert.rejects(lstat(dirname(closingBroker.descriptorPath)), { code: 'ENOENT' });
    finishResolve({ url: baseUrl, apiKey: secret });
    await delay(10);
    assert.equal(seen.length, callsBefore, 'late credential results must never make remote requests');
    await closingBroker.close();
    await broker.close();
    await assert.rejects(lstat(broker.descriptorPath), { code: 'ENOENT' });
  });
});
