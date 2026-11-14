import { randomBytes, timingSafeEqual } from 'node:crypto';
import { createServer } from 'node:http';
import { constants } from 'node:fs';
import { chmod, lstat, mkdir, mkdtemp, open, readFile, realpath, rename, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, parse, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
import { MAX_BODY_BYTES, DEFAULT_TIMEOUT_MS, TalusRequestError, request, validateBaseUrl } from './private-client.mjs';

const TOKEN_HEADER = 'x-talus-broker-token';
const TIMEOUT_HEADER = 'x-talus-broker-timeout-ms';
const TRANSPORT_HEADERS = new Set([
  'host', 'content-length', 'connection', 'transfer-encoding', 'keep-alive',
  'proxy-connection', 'te', 'trailer', 'expect',
]);
const MAX_ACTIVE = 4;

function sameToken(actual, expected) {
  if (typeof actual !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(actual)) return false;
  return timingSafeEqual(Buffer.from(actual), Buffer.from(expected));
}

function publicError(res, status, code, message) {
  if (res.destroyed || res.writableEnded) return;
  const body = Buffer.from(JSON.stringify({ error: { code, message } }));
  res.writeHead(status, { 'content-type': 'application/json', 'content-length': String(body.length), connection: 'close' });
  res.end(body);
}

function readIncoming(req, signal, limit) {
  return new Promise((fulfill, reject) => {
    const chunks = [];
    let length = 0;
    function cleanup() {
      req.off('data', onData); req.off('end', onEnd); req.off('error', onError);
      req.off('aborted', onAbort); signal.removeEventListener('abort', onAbort);
    }
    function refuse(error) { cleanup(); req.pause(); reject(error); }
    function onData(chunk) {
      length += chunk.length;
      if (length > limit) refuse(new TalusRequestError('BODY_TOO_LARGE', 'The request body exceeds its byte limit.'));
      else chunks.push(chunk);
    }
    function onEnd() { cleanup(); fulfill(Buffer.concat(chunks, length)); }
    function onError() { refuse(new TalusRequestError('REQUEST_ABORTED', 'The local request was interrupted.')); }
    function onAbort() { refuse(new TalusRequestError('REQUEST_ABORTED', 'The local request was cancelled.')); }
    if (signal.aborted) return onAbort();
    req.on('data', onData); req.once('end', onEnd); req.once('error', onError); req.once('aborted', onAbort);
    signal.addEventListener('abort', onAbort, { once: true });
  });
}

function resolveWhileActive(resolveConfig, signal) {
  return new Promise((fulfill, reject) => {
    const abort = () => reject(new TalusRequestError('REQUEST_ABORTED', 'The local request was cancelled.'));
    if (signal.aborted) return abort();
    signal.addEventListener('abort', abort, { once: true });
    // A credential provider may not support cancellation. Stop waiting for its
    // result on abort, and never start a remote request from a late result.
    Promise.resolve().then(() => resolveConfig({ signal })).then(fulfill, reject)
      .finally(() => signal.removeEventListener('abort', abort));
  });
}

/**
 * Create a host-owned credential bridge on a private Unix socket (or a random
 * Windows named pipe). The descriptor contains only a local capability token,
 * never the Talus URL or API key. Only resolveConfig accesses real credentials.
 * `allowHttpLoopback` in its result is reserved for explicitly controlled tests.
 */
export async function createBroker({ resolveConfig, directory, maxBodyBytes = MAX_BODY_BYTES }) {
  if (typeof resolveConfig !== 'function') throw new TypeError('A host configuration resolver is required.');
  if (!Number.isSafeInteger(maxBodyBytes) || maxBodyBytes <= 0 || maxBodyBytes > MAX_BODY_BYTES) {
    throw new TypeError('The broker body limit must be between 1 byte and 64 MiB.');
  }
  const parent = await realpath(resolve(directory ?? tmpdir()));
  const ownedDirectory = await mkdtemp(join(parent, 'talus-broker-'));
  await chmod(ownedDirectory, 0o700);
  const socketPath = process.platform === 'win32'
    ? `\\\\.\\pipe\\talus-${randomBytes(24).toString('hex')}`
    : join(ownedDirectory, 'broker.sock');
  const token = randomBytes(32).toString('base64url');
  const descriptorPath = join(ownedDirectory, 'broker.json');
  const active = new Set();
  const pending = new Set();
  const connections = new Set();
  let closing;
  let closed = false;
  async function handle(req, res) {
    // Disconnects during cancellation must not become unhandled stream errors.
    req.on('error', () => {});
    res.on('error', () => {});
    if (closed) return publicError(res, 503, 'BROKER_CLOSED', 'The Talus host is shutting down.');
    const tokens = req.headersDistinct?.[TOKEN_HEADER] ?? [];
    if (tokens.length !== 1 || !sameToken(tokens[0], token)) {
      req.resume();
      return publicError(res, 401, 'BROKER_UNAUTHORIZED', 'The local Talus capability is invalid.');
    }
    if (active.size >= MAX_ACTIVE) {
      req.resume();
      return publicError(res, 503, 'BROKER_BUSY', 'The Talus host is busy; try again after current requests finish.');
    }
    const declared = req.headers['content-length'];
    if (declared !== undefined && (!/^\d+$/.test(declared) || Number(declared) > maxBodyBytes)) {
      req.resume();
      return publicError(res, 413, 'BODY_TOO_LARGE', 'The request body exceeds its byte limit.');
    }
    const timeoutValue = req.headers[TIMEOUT_HEADER];
    const timeoutMs = timeoutValue === undefined ? DEFAULT_TIMEOUT_MS : Number(timeoutValue);
    if ((timeoutValue !== undefined && !/^\d+$/.test(timeoutValue)) || !Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 300_000) {
      req.resume();
      return publicError(res, 400, 'INVALID_TIMEOUT', 'The timeout must be between 1 and 300000 milliseconds.');
    }
    const outgoing = {};
    for (const [name, value] of Object.entries(req.headers)) {
      if (name === TOKEN_HEADER || name === TIMEOUT_HEADER || TRANSPORT_HEADERS.has(name)) continue;
      if (name === 'authorization' || name === 'proxy-authorization' || name === 'x-api-key' || name === 'cookie' || name.startsWith('x-talus-') || typeof value !== 'string') {
        req.resume();
        return publicError(res, 400, 'AUTH_HEADER_FORBIDDEN', 'Authentication headers belong to the Talus host.');
      }
      outgoing[name] = value;
    }
    const controller = new AbortController();
    active.add(controller);
    const abort = () => controller.abort();
    req.once('aborted', abort);
    res.once('close', () => { if (!res.writableEnded) abort(); });
    const timer = setTimeout(abort, timeoutMs);
    timer.unref?.();
    try {
      const body = await readIncoming(req, controller.signal, maxBodyBytes);
      const config = await resolveWhileActive(resolveConfig, controller.signal);
      if (controller.signal.aborted) throw new TalusRequestError('REQUEST_ABORTED', 'The local request was cancelled.');
      const response = await request({
        baseUrl: config?.url, apiKey: config?.apiKey, allowHttpLoopback: config?.allowHttpLoopback === true,
        method: req.method, path: req.url, headers: outgoing, body,
        signal: controller.signal, timeoutMs, maxBodyBytes,
      });
      const bytes = Buffer.from(await response.arrayBuffer());
      const headers = {};
      for (const [name, value] of response.headers) {
        if (!TRANSPORT_HEADERS.has(name) && name !== TOKEN_HEADER && !value.includes(token)) headers[name] = value;
      }
      const headLength = response.headers.get('content-length');
      headers['content-length'] = req.method === 'HEAD' && headLength && /^\d+$/.test(headLength) ? headLength : String(bytes.length);
      headers.connection = 'close';
      res.writeHead(response.status, headers);
      res.end(bytes);
    } catch (error) {
      const code = error instanceof TalusRequestError ? error.code : 'BROKER_REQUEST_FAILED';
      const status = code === 'BODY_TOO_LARGE' ? 413 : code.startsWith('INVALID_') || code === 'AUTH_HEADER_FORBIDDEN' ? 400
        : code === 'REQUEST_TIMEOUT' || controller.signal.aborted ? 504 : 502;
      publicError(res, status, code, 'The Talus request could not be completed; check the host configuration.');
    } finally {
      clearTimeout(timer); req.off('aborted', abort); active.delete(controller);
    }
  }
  const server = createServer({ maxHeaderSize: 16 * 1024 }, (req, res) => {
    const operation = handle(req, res);
    pending.add(operation);
    operation.catch(() => publicError(res, 502, 'BROKER_REQUEST_FAILED', 'The Talus request could not be completed.'))
      .finally(() => pending.delete(operation));
  });
  server.keepAliveTimeout = 1_000;
  server.headersTimeout = 10_000;
  server.requestTimeout = 300_000;
  server.on('connection', socket => {
    connections.add(socket);
    socket.once('close', () => connections.delete(socket));
  });
  server.on('clientError', (_error, socket) => socket.destroy());
  async function close() {
    if (closing) return closing;
    closed = true;
    closing = (async () => {
      for (const controller of active) controller.abort();
      const stopped = new Promise(resolveClose => server.close(() => resolveClose()));
      server.closeAllConnections();
      for (const connection of connections) connection.destroy();
      await stopped;
      await Promise.allSettled([...pending]);
      await rm(ownedDirectory, { recursive: true, force: true });
    })();
    return closing;
  }
  try {
    await new Promise((fulfill, reject) => {
      server.once('error', reject);
      server.listen(socketPath, () => { server.off('error', reject); fulfill(); });
    });
    if (process.platform !== 'win32') await chmod(socketPath, 0o600);
    await writeFile(descriptorPath, JSON.stringify({ version: 1, socketPath, token }) + '\n', { mode: 0o600, flag: 'wx' });
    return { descriptorPath, close };
  } catch {
    await close();
    throw new Error('The private Talus broker could not start.');
  }
}

async function rejectSymlinkAncestors(path) {
  const absolute = resolve(path);
  let current = parse(absolute).root;
  for (const part of absolute.slice(current.length).split(/[\\/]/).filter(Boolean)) {
    current = join(current, part);
    const info = await lstat(current);
    if (info.isSymbolicLink()) throw new Error('Credential paths must not contain symlinks.');
  }
  return absolute;
}

/** Host-only private configuration loader. The no-key request CLI never calls this. */
export async function readHostConfig(path) {
  let handle;
  try {
    const absolute = await rejectSymlinkAncestors(path);
    handle = await open(absolute, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
    const stat = await handle.stat();
    if (!stat.isFile() || stat.size > 64 * 1024 || (process.platform !== 'win32' &&
      ((stat.mode & 0o077) !== 0 || stat.uid !== process.getuid()))) throw new Error('private file required');
    const value = JSON.parse(await handle.readFile('utf8'));
    if (!value || typeof value !== 'object' || Object.keys(value).some(key => !['url', 'apiKey'].includes(key)) ||
      typeof value.url !== 'string' || typeof value.apiKey !== 'string' || !value.apiKey || /[^\x21-\x7e]/.test(value.apiKey)) {
      throw new Error('invalid configuration');
    }
    validateBaseUrl(value.url);
    return value;
  } catch {
    throw new Error('The host requires an owner-only regular JSON configuration with a Talus HTTPS URL and API key.');
  } finally { await handle?.close(); }
}

async function hiddenInput() {
  if (!process.stdin.isTTY || !process.stderr.isTTY) throw new Error('Run configuration interactively in a trusted host terminal.');
  process.stderr.write('Talus API key (hidden): ');
  const raw = process.stdin.isRaw;
  process.stdin.setRawMode(true);
  process.stdin.setEncoding('utf8');
  process.stdin.resume();
  return new Promise((fulfill, reject) => {
    let value = '';
    function finish(error) {
      process.stdin.off('data', data); process.stdin.setRawMode(raw); process.stdin.pause();
      process.stderr.write('\n');
      if (error) reject(error); else fulfill(value);
    }
    function data(chunk) {
      for (const character of chunk) {
        if (character === '\u0003' || character === '\u0004') return finish(new Error('Configuration cancelled.'));
        if (character === '\r' || character === '\n') return finish();
        if (character === '\u007f' || character === '\b') value = value.slice(0, -1);
        else if (/^[\x21-\x7e]$/.test(character)) value += character;
      }
    }
    process.stdin.on('data', data);
  });
}

async function saveHostConfig(path, value) {
  const absolute = resolve(path);
  await mkdir(dirname(absolute), { recursive: true, mode: 0o700 });
  await rejectSymlinkAncestors(dirname(absolute));
  try {
    const current = await lstat(absolute);
    if (!current.isFile() || current.isSymbolicLink()) throw new Error('Configuration must be a regular file.');
  } catch (error) { if (error.code !== 'ENOENT') throw error; }
  const temporary = join(dirname(absolute), `.talus-config-${randomBytes(16).toString('hex')}.tmp`);
  try {
    await writeFile(temporary, JSON.stringify(value) + '\n', { mode: 0o600, flag: 'wx' });
    await rename(temporary, absolute);
  } finally { await rm(temporary, { force: true }); }
}

/** Manually launched on the trusted host; this command never accepts a key argument or environment variable. */
export async function runBrokerCLI(argv) {
  const configure = argv[0] === 'configure';
  const args = configure ? argv.slice(1) : argv;
  const values = {};
  for (let i = 0; i < args.length; i++) {
    const flag = args[i];
    if (!['--config', ...(configure ? ['--url'] : [])].includes(flag) || values[flag] !== undefined || !args[i + 1]) {
      throw new Error('Usage: broker.mjs [configure --url HTTPS_URL] --config PRIVATE_JSON_FILE');
    }
    values[flag] = args[++i];
  }
  if (!values['--config'] || (configure && !values['--url'])) throw new Error('A host configuration path and, for configure, HTTPS URL are required.');
  if (configure) {
    const url = validateBaseUrl(values['--url']).href.replace(/\/+$/, '');
    const apiKey = await hiddenInput();
    if (!apiKey) throw new Error('A Talus API key is required.');
    await saveHostConfig(values['--config'], { url, apiKey });
    process.stdout.write('Talus host configuration saved.\n');
    return;
  }
  await readHostConfig(values['--config']);
  const broker = await createBroker({ resolveConfig: () => readHostConfig(values['--config']) });
  process.stdout.write(`${broker.descriptorPath}\n`);
  await new Promise(fulfill => {
    const stop = () => {
      process.off('SIGINT', stop); process.off('SIGTERM', stop);
      broker.close().then(fulfill, fulfill);
    };
    process.once('SIGINT', stop); process.once('SIGTERM', stop);
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  runBrokerCLI(process.argv.slice(2)).catch(() => {
    process.stderr.write('Talus broker failed. Check the private host configuration and HTTPS endpoint.\n');
    process.exitCode = 1;
  });
}
