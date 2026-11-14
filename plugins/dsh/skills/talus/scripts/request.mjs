#!/usr/bin/env node
// This client talks only to a local Talus broker. It never reads a Talus key.
import http from 'node:http';
import { constants } from 'node:fs';
import { open, lstat, rename, rm, realpath } from 'node:fs/promises';
import { dirname, isAbsolute, join, parse, relative, resolve, sep } from 'node:path';
import { randomBytes } from 'node:crypto';
import { pathToFileURL } from 'node:url';

const MAX_BODY_BYTES = 64 * 1024 * 1024;
const MAX_STDOUT_BYTES = 1024 * 1024;
const MAX_ERROR_BYTES = 8192;
const MAX_DESCRIPTOR_BYTES = 16384;
const FORBIDDEN_HEADERS = new Set([
  'authorization', 'proxy-authorization', 'x-api-key', 'cookie', 'host', 'connection',
  'proxy-connection', 'content-length', 'transfer-encoding', 'trailer', 'te',
  'upgrade', 'expect',
]);
const HIDDEN_RESPONSE_HEADERS = new Set(['authorization', 'proxy-authorization', 'set-cookie', 'x-api-key']);

class ClientError extends Error {
  constructor(message, exitCode = 1) { super(message); this.exitCode = exitCode; }
}

function argumentsFor(argv) {
  const options = {
    broker: process.env.TALUS_BROKER || process.env.DSH_TALUS_BROKER,
    method: 'GET', headers: [], query: [], timeoutMs: 30000,
  };
  const names = {
    '--broker': 'broker', '--method': 'method', '--path': 'path', '--json': 'json',
    '--json-file': 'jsonFile', '--data-file': 'dataFile', '--output': 'output',
    '--headers-output': 'headersOutput', '--timeout-ms': 'timeoutMs',
  };
  const seen = new Set();
  for (let index = 0; index < argv.length; index++) {
    const argument = argv[index];
    if (argument === '--help' || argument === '-h') { options.help = true; continue; }
    if (argument === '--header' || argument === '--query') {
      if (argv[index + 1] === undefined || argv[index + 1].startsWith('--')) throw new ClientError('An option is missing its value.');
      options[argument === '--header' ? 'headers' : 'query'].push(argv[++index]);
      continue;
    }
    const name = names[argument];
    if (!name || seen.has(name)) throw new ClientError('Unsupported or repeated option. Use --help.');
    if (argv[index + 1] === undefined || argv[index + 1].startsWith('--')) throw new ClientError('An option is missing its value.');
    seen.add(name); options[name] = argv[++index];
  }
  if (options.help) return options;
  if (typeof options.broker !== 'string' || !options.broker) throw new ClientError('A broker descriptor is required. Use --broker FILE or set TALUS_BROKER to its path.');
  if (!options.path) throw new ClientError('Use --path with a relative /api/v1/ API path.');
  if (!/^(?:GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$/.test(options.method)) throw new ClientError('The HTTP method is unsupported; use an uppercase method.');
  if ([options.json, options.jsonFile, options.dataFile].filter(value => value !== undefined).length > 1) throw new ClientError('--json, --json-file and --data-file are mutually exclusive.');
  if (!/^\d+$/.test(String(options.timeoutMs)) || Number(options.timeoutMs) < 1 || Number(options.timeoutMs) > 300000) throw new ClientError('--timeout-ms must be an integer from 1 to 300000.');
  options.timeoutMs = Number(options.timeoutMs);
  return options;
}

// Refuse symlink parents as well as symlink leaf files. These checks do not
// provide isolation from another process with the same OS-user privileges.
async function safeParents(filename) {
  const absolute = resolve(filename), parent = dirname(absolute), root = parse(parent).root;
  let current = root;
  for (const part of relative(root, parent).split(sep).filter(Boolean)) {
    current = join(current, part);
    const info = await lstat(current);
    if (info.isSymbolicLink() || !info.isDirectory()) throw new ClientError('Symbolic-link or non-directory parents are not allowed.');
  }
  const canonical = await realpath(parent);
  if ((process.platform === 'win32' ? canonical.toLowerCase() !== parent.toLowerCase() : canonical !== parent)) throw new ClientError('Symbolic-link directory aliases are not allowed.');
  return absolute;
}

async function regularFile(filename, { privateFile = false, maxBytes = MAX_BODY_BYTES } = {}) {
  const absolute = privateFile ? await safeParents(filename) : resolve(filename);
  const before = await lstat(absolute);
  if (before.isSymbolicLink() || !before.isFile()) throw new ClientError('Only regular input files are accepted; symbolic links are not allowed.');
  let handle;
  try {
    handle = await open(absolute, constants.O_RDONLY | (constants.O_NOFOLLOW ?? 0));
    const info = await handle.stat();
    if (!info.isFile() || info.dev !== before.dev || info.ino !== before.ino) throw new ClientError('The input file changed while it was opened.');
    if (privateFile && process.platform !== 'win32') {
      if ((info.mode & 0o077) !== 0 || (typeof process.getuid === 'function' && info.uid !== process.getuid())) throw new ClientError('The broker descriptor must be owned by this user and have owner-only permissions.');
    }
    if (info.size > maxBytes) throw new ClientError('An input exceeds the supported size limit.');
    const chunks = []; let size = 0;
    for await (const chunk of handle.createReadStream({ autoClose: false })) {
      size += chunk.length;
      if (size > maxBytes) throw new ClientError('An input exceeds the supported size limit.');
      chunks.push(chunk);
    }
    return Buffer.concat(chunks, size);
  } finally { await handle?.close(); }
}

async function descriptor(filename) {
  let document;
  try { document = JSON.parse((await regularFile(filename, { privateFile: true, maxBytes: MAX_DESCRIPTOR_BYTES })).toString('utf8')); }
  catch (error) {
    if (error instanceof ClientError) throw error;
    throw new ClientError('The private broker descriptor is unavailable or invalid.');
  }
  if (!document || typeof document !== 'object' || Array.isArray(document) || document.version !== 1 ||
      typeof document.socketPath !== 'string' || typeof document.token !== 'string') throw new ClientError('The broker descriptor has an unsupported format.');
  const token = document.token;
  const base64 = /^[A-Za-z0-9_-]{43}$/.test(token) && Buffer.from(token, 'base64url').length === 32 && Buffer.from(token, 'base64url').toString('base64url') === token;
  if (!/^[a-fA-F0-9]{64}$/.test(token) && !base64) throw new ClientError('The broker descriptor has an invalid capability.');
  const socketPath = document.socketPath;
  if (process.platform === 'win32') {
    if (!/^\\\\\.\\pipe\\[A-Za-z0-9._-]{1,200}$/.test(socketPath)) throw new ClientError('The broker must use a local named pipe.');
  } else {
    if (!isAbsolute(socketPath) || /[\0\r\n]/.test(socketPath)) throw new ClientError('The broker must use an absolute local socket path.');
    try {
      await safeParents(socketPath);
      const info = await lstat(socketPath);
      if (info.isSymbolicLink() || !info.isSocket() || (typeof process.getuid === 'function' && info.uid !== process.getuid())) throw new Error('invalid socket');
    } catch { throw new ClientError('The local broker socket is unavailable or unsafe.'); }
  }
  return { socketPath, token };
}

function requestPath(path, query) {
  if (typeof path !== 'string' || /[\0\r\n#\\]/.test(path) || !path.startsWith('/api/v1/') || path.startsWith('//')) throw new ClientError('Requests must stay under the relative /api/v1/ API path.');
  const [pathname] = path.split('?', 1);
  let decoded;
  try { decoded = decodeURIComponent(pathname); }
  catch { throw new ClientError('The API path has invalid percent encoding.'); }
  if (decoded.includes('\\') || /[\0\r\n]/.test(decoded) || decoded.split('/').some(part => part === '.' || part === '..') || /%2f|%5c/i.test(pathname)) throw new ClientError('Path traversal and encoded separators are not allowed.');
  let result = path;
  for (const item of query) {
    const split = item.indexOf('=');
    if (split <= 0) throw new ClientError('Each --query requires KEY=VALUE.');
    result += `${result.includes('?') ? '&' : '?'}${encodeURIComponent(item.slice(0, split))}=${encodeURIComponent(item.slice(split + 1))}`;
  }
  return result;
}

function businessHeaders(lines) {
  const headers = {};
  for (const line of lines) {
    const split = line.indexOf('=');
    if (split <= 0 || /[\r\n\0]/.test(line)) throw new ClientError('Each --header must be one NAME=VALUE line.');
    const name = line.slice(0, split).trim(), normalized = name.toLowerCase();
    if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/.test(name) || FORBIDDEN_HEADERS.has(normalized) || normalized.startsWith('x-talus-')) throw new ClientError('An authentication, routing or transport header cannot be supplied.');
    if (Object.hasOwn(headers, normalized)) throw new ClientError('Duplicate request headers are not allowed.');
    headers[normalized] = line.slice(split + 1).trim();
  }
  return headers;
}

async function destination(filename) {
  const absolute = await safeParents(filename);
  try {
    const info = await lstat(absolute);
    if (info.isSymbolicLink() || !info.isFile()) throw new ClientError('An output must be a regular file, never a symbolic link.');
  } catch (error) { if (error.code !== 'ENOENT') throw error; }
  return absolute;
}

async function atomicWrite(filename, write) {
  const target = await destination(filename);
  const temporary = join(dirname(target), `.talus-${randomBytes(16).toString('hex')}.tmp`);
  let handle;
  try {
    handle = await open(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | (constants.O_NOFOLLOW ?? 0), 0o600);
    await write(handle);
    await handle.sync(); await handle.close(); handle = undefined;
    await destination(target);
    await rename(temporary, target);
  } finally {
    await handle?.close();
    await rm(temporary, { force: true }).catch(() => {});
  }
}

async function collect(response, limit) {
  const chunks = []; let size = 0;
  for await (const chunk of response) {
    size += chunk.length;
    if (size > limit) { response.destroy(); throw new ClientError('The response exceeds the safe output limit.'); }
    chunks.push(chunk);
  }
  if (!response.complete) throw new ClientError('The broker response ended before completion.');
  return Buffer.concat(chunks, size);
}

function redact(value, token) { return value.split(token).join('[redacted]'); }

async function send(options) {
  const path = requestPath(options.path, options.query);
  const headers = businessHeaders(options.headers);
  const local = await descriptor(options.broker);
  const descriptorPath = resolve(options.broker);
  for (const output of [options.output, options.headersOutput].filter(Boolean)) {
    if (resolve(output) === descriptorPath || resolve(output) === resolve(local.socketPath)) throw new ClientError('An output cannot replace the broker descriptor or socket.');
    await destination(output);
  }
  if (options.output && options.headersOutput && resolve(options.output) === resolve(options.headersOutput)) throw new ClientError('The body and headers need separate output files.');
  let body;
  if (options.dataFile) body = await regularFile(options.dataFile);
  if (options.json !== undefined || options.jsonFile !== undefined) {
    const text = options.jsonFile ? (await regularFile(options.jsonFile)).toString('utf8') : options.json;
    try { body = Buffer.from(JSON.stringify(JSON.parse(text))); }
    catch { throw new ClientError('The JSON request body is invalid.'); }
    headers['content-type'] ??= 'application/json';
  }
  if (body && body.length > MAX_BODY_BYTES) throw new ClientError('The request exceeds the 64 MiB limit.');
  if (body?.length && (options.method === 'GET' || options.method === 'HEAD')) throw new ClientError('GET and HEAD requests do not accept a body.');
  headers['x-talus-broker-token'] = local.token;
  headers['x-talus-broker-timeout-ms'] = String(options.timeoutMs);
  headers.host = 'localhost';
  headers['content-length'] = String(body?.length ?? 0);
  const controller = new AbortController();
  let interrupted = false, expired = false, outgoing, response;
  const interrupt = () => { interrupted = true; controller.abort(); };
  process.once('SIGINT', interrupt);
  const timeout = setTimeout(() => { expired = true; controller.abort(); }, options.timeoutMs);
  timeout.unref();
  try {
    response = await new Promise((accept, reject) => {
      outgoing = http.request({ socketPath: local.socketPath, path, method: options.method, headers, agent: false, signal: controller.signal }, accept);
      outgoing.once('error', reject);
      outgoing.end(body);
    });
    const status = response.statusCode ?? 502;
    if (status < 200 || status >= 300) {
      let detail = '';
      try { detail = redact((await collect(response, MAX_ERROR_BYTES)).toString('utf8'), local.token); }
      catch { detail = 'Response details could not be read within the safe limit.'; }
      throw new ClientError(`Talus returned HTTP ${status}${detail ? `: ${detail}` : ''}`, 2);
    }
    if (options.output) {
      await atomicWrite(options.output, async handle => {
        let size = 0;
        for await (const chunk of response) {
          size += chunk.length;
          if (size > MAX_BODY_BYTES) throw new ClientError('The download exceeds the 64 MiB limit.');
          await handle.writeFile(chunk);
        }
        if (!response.complete) throw new ClientError('The broker response ended before completion.');
      });
    } else {
      const type = String(response.headers['content-type'] || '').toLowerCase();
      if (options.method !== 'HEAD' && status !== 204 && status !== 205 &&
          type && !type.startsWith('text/') && !/(?:json|xml)(?:;|$)/.test(type)) throw new ClientError('Binary responses require --output FILE.');
      const bytes = await collect(response, MAX_STDOUT_BYTES);
      let text;
      try { text = new TextDecoder('utf-8', { fatal: true }).decode(bytes); }
      catch { throw new ClientError('Binary responses require --output FILE.'); }
      if (text.includes('\0')) throw new ClientError('Binary responses require --output FILE.');
      text = redact(text, local.token);
      process.stdout.write(text);
      if (text && !text.endsWith('\n')) process.stdout.write('\n');
    }
    if (options.headersOutput) {
      const safe = Object.fromEntries(Object.entries(response.headers).filter(([name]) => !HIDDEN_RESPONSE_HEADERS.has(name) && !name.startsWith('x-talus-')));
      await atomicWrite(options.headersOutput, handle => handle.writeFile(`${redact(JSON.stringify({ status, headers: safe }, null, 2), local.token)}\n`, 'utf8'));
    }
  } catch (error) {
    if (interrupted) throw new ClientError('The broker request was cancelled.', 130);
    if (expired) throw new ClientError('The broker request timed out.', 124);
    if (error instanceof ClientError) throw error;
    throw new ClientError('The local broker request failed. Check that the Talus integration is running.');
  } finally {
    clearTimeout(timeout); process.removeListener('SIGINT', interrupt);
    response?.destroy(); outgoing?.destroy();
  }
}

const HELP = `Usage:
  node request.mjs --broker FILE --method GET --path /api/v1/servers
  node request.mjs --method POST --path /api/v1/servers/7/exec --json-file request.json

Options:
  --broker FILE             Private local broker descriptor (or TALUS_BROKER).
  --method METHOD          GET by default; uppercase HTTP method.
  --path /api/v1/...        Relative API path, optionally with an encoded query.
  --query KEY=VALUE        Append a query parameter; repeatable.
  --header NAME=VALUE      Business header only; repeatable.
  --json JSON              JSON body, or use --json-file FILE / --data-file FILE.
  --output FILE            Atomic binary/text download, up to 64 MiB.
  --headers-output FILE    Save filtered response headers privately.
  --timeout-ms NUMBER      Entire exchange timeout, 1–300000 ms (default 30000).

This client never receives a Talus API key or a remote URL. A running local
broker owns HTTPS authentication. DSH_TALUS_BROKER is also accepted as a
descriptor-path fallback. Redirects and retries are never performed.
`;

export async function runCLI(argv = process.argv.slice(2)) {
  try {
    const options = argumentsFor(argv);
    if (options.help) process.stdout.write(HELP);
    else await send(options);
    return 0;
  } catch (error) {
    process.stderr.write(`talus: ${error instanceof ClientError ? error.message : 'The local operation failed without exposing its private descriptor.'}\n`);
    return error instanceof ClientError ? error.exitCode : 1;
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) process.exitCode = await runCLI();
