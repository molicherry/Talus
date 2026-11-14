import { isIP } from 'node:net';

export const MAX_BODY_BYTES = 64 * 1024 * 1024;
export const DEFAULT_TIMEOUT_MS = 30_000;
const METHODS = new Set(['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS']);
const REDIRECT_STATUSES = new Set([301, 302, 303, 307, 308]);
const FORBIDDEN_HEADERS = new Set([
  'authorization', 'proxy-authorization', 'x-api-key', 'cookie', 'host', 'content-length',
  'transfer-encoding', 'connection', 'upgrade', 'proxy-connection', 'trailer', 'te',
  'expect', 'keep-alive',
]);
const RESPONSE_PRIVATE_HEADERS = new Set([
  'authorization', 'proxy-authorization', 'x-api-key', 'x-talus-broker-token', 'set-cookie',
  'connection', 'transfer-encoding', 'keep-alive', 'proxy-connection', 'trailer',
]);

export class TalusRequestError extends Error {
  constructor(code, message) {
    super(message);
    this.name = 'TalusRequestError';
    this.code = code;
  }
}

function fail(code, message) { throw new TalusRequestError(code, message); }
function noNewlines(value) {
  if (/[\r\n]/.test(value)) fail('INVALID_FIELD', 'Request fields must not contain line breaks.');
  return value;
}

function parsedUrl(value) {
  let url;
  if (typeof value === 'string') noNewlines(value);
  try { url = value instanceof URL ? new URL(value.href) : new URL(value); }
  catch { fail('INVALID_URL', 'A valid Talus HTTPS URL is required.'); }
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.hash) {
    fail('INVALID_URL', 'Use a Talus HTTPS URL without credentials or a fragment.');
  }
  return url;
}

function loopback(hostname) {
  const host = hostname.toLowerCase().replace(/^\[|\]$/g, '');
  return host === 'localhost' || host === '::1' || (isIP(host) === 4 && host.split('.')[0] === '127');
}

/** Validate a Talus base URL. The loopback HTTP switch is for controlled tests only. */
export function validateBaseUrl(baseUrl, { allowHttpLoopback = false } = {}) {
  const url = parsedUrl(baseUrl);
  if (url.search || url.href.includes('?') || /\/api\/v1\/*$/i.test(url.pathname)) {
    fail('INVALID_URL', 'Use the Talus base URL without a query or /api/v1.');
  }
  if (url.protocol !== 'https:' && !(allowHttpLoopback === true && loopback(url.hostname))) {
    fail('HTTPS_REQUIRED', 'Talus connections require HTTPS with certificate verification.');
  }
  return url;
}

/** Accept only relative management API targets; preserve query encoding and order. */
export function resolveApiUrl(baseUrl, path, options = {}) {
  const base = validateBaseUrl(baseUrl, options);
  if (typeof path !== 'string' || !/^\/api\/v1(?:\/|\?|$)/.test(path) || /[\x00-\x20\x7f\\#]/.test(path)) {
    fail('INVALID_PATH', 'Use a relative /api/v1 path without control characters or a fragment.');
  }
  for (const segment of path.split('?')[0].split('/')) {
    let decoded;
    try { decoded = decodeURIComponent(segment); }
    catch { fail('INVALID_PATH', 'The API path has invalid encoding.'); }
    if (decoded === '.' || decoded === '..' || /[\/\\\x00-\x1f\x7f]/.test(decoded)) {
      fail('INVALID_PATH', 'API paths must not contain traversal or encoded separators.');
    }
  }
  const prefix = base.pathname.replace(/\/+$/, '');
  const url = new URL(base.href.replace(/\/+$/, '') + path);
  if (url.origin !== base.origin || !url.pathname.startsWith(`${prefix}/api/v1`)) {
    fail('INVALID_PATH', 'The API path must stay within the configured Talus server.');
  }
  if (!url.search) url.search = '';
  return url;
}

export function normalizeMethod(method = 'GET') {
  if (typeof method !== 'string') fail('INVALID_METHOD', 'A supported HTTP method is required.');
  const value = noNewlines(method).toUpperCase();
  if (!METHODS.has(value)) fail('INVALID_METHOD', 'The HTTP method is not supported.');
  return value;
}

/** Caller headers cannot set credentials, a different host, or HTTP framing. */
export function requestHeaders(input = {}) {
  try {
    const entries = input instanceof Headers ? [...input.entries()] : Array.isArray(input) ? input : Object.entries(input);
    const output = new Headers();
    for (const entry of entries) {
      if (!Array.isArray(entry) || entry.length !== 2 || typeof entry[0] !== 'string' || typeof entry[1] !== 'string') {
        fail('INVALID_HEADERS', 'Header names and values must be strings.');
      }
      const [name, value] = entry;
      const lower = noNewlines(name).toLowerCase();
      noNewlines(value);
      if (FORBIDDEN_HEADERS.has(lower) || lower.startsWith('x-talus-')) {
        fail('AUTH_HEADER_FORBIDDEN', 'Authentication and transport headers are managed by the Talus host.');
      }
      if (output.has(lower)) fail('DUPLICATE_HEADER', 'Duplicate request headers are not supported.');
      if (lower === 'accept-encoding' && value.trim().toLowerCase() !== 'identity') {
        fail('INVALID_HEADERS', 'Talus transfer requests require identity content encoding.');
      }
      output.set(name, value);
    }
    return output;
  } catch (error) {
    if (error instanceof TalusRequestError) throw error;
    fail('INVALID_HEADERS', 'Request headers are invalid.');
  }
}

function bodyBytes(body, maxBytes) {
  if (!Number.isSafeInteger(maxBytes) || maxBytes < 0 || maxBytes > MAX_BODY_BYTES) {
    fail('INVALID_LIMIT', 'The body limit must be between zero and 64 MiB.');
  }
  if (body === undefined || body === null) return Buffer.alloc(0);
  let bytes;
  if (typeof body === 'string') {
    if (Buffer.byteLength(body, 'utf8') > maxBytes) fail('BODY_TOO_LARGE', 'The request body exceeds its byte limit.');
    bytes = Buffer.from(body, 'utf8');
  } else if (ArrayBuffer.isView(body)) {
    if (body.byteLength > maxBytes) fail('BODY_TOO_LARGE', 'The request body exceeds its byte limit.');
    bytes = Buffer.from(new Uint8Array(body.buffer, body.byteOffset, body.byteLength));
  } else if (body instanceof ArrayBuffer) {
    if (body.byteLength > maxBytes) fail('BODY_TOO_LARGE', 'The request body exceeds its byte limit.');
    bytes = Buffer.from(new Uint8Array(body));
  } else fail('INVALID_BODY', 'The request body must be text or bytes.');
  return bytes;
}

async function readBounded(response, maxBytes, signal) {
  if (response.body === null) return Buffer.alloc(0);
  const reader = response.body.getReader();
  const chunks = [];
  let length = 0;
  try {
    while (true) {
      signal.throwIfAborted();
      const { done, value } = await reader.read();
      if (done) break;
      length += value.byteLength;
      if (length > maxBytes) fail('RESPONSE_TOO_LARGE', 'The Talus response exceeds its byte limit.');
      chunks.push(Buffer.from(value));
    }
    return Buffer.concat(chunks, length);
  } catch (error) {
    try { await reader.cancel(); } catch { /* Keep the original error. */ }
    throw error;
  } finally { reader.releaseLock(); }
}

/**
 * Perform one host-owned HTTPS request. The API key stays in host memory and
 * the TLS-protected authentication header. There are no redirects or retries.
 * Bodies are bounded to 64 MiB and the deadline includes complete body reads.
 * Node 24 proxy support can be enabled before startup with NODE_USE_ENV_PROXY=1.
 */
export async function request({ baseUrl, apiKey, method = 'GET', path, headers = {}, json, body, signal,
  timeoutMs = DEFAULT_TIMEOUT_MS, allowHttpLoopback = false, maxBodyBytes = MAX_BODY_BYTES }) {
  if (process.env.NODE_TLS_REJECT_UNAUTHORIZED === '0') {
    fail('UNSAFE_TLS_CONFIGURATION', 'Talus requests require TLS certificate verification.');
  }
  const url = resolveApiUrl(baseUrl, path, { allowHttpLoopback });
  const verb = normalizeMethod(method);
  if (typeof apiKey !== 'string' || !apiKey.length || /[^\x21-\x7e]/.test(apiKey)) {
    fail('INVALID_CREDENTIAL', 'A configured Talus API key is required.');
  }
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > 300_000) {
    fail('INVALID_TIMEOUT', 'The timeout must be between 1 and 300000 milliseconds.');
  }
  if (json !== undefined && body !== undefined) fail('INVALID_BODY', 'Choose either JSON or a raw body.');
  const outgoing = requestHeaders(headers);
  let data = body;
  if (json !== undefined) {
    try { data = JSON.stringify(json); }
    catch { fail('INVALID_JSON', 'The JSON request body cannot be serialized.'); }
    if (data === undefined) fail('INVALID_JSON', 'The JSON request body cannot be serialized.');
    if (!outgoing.has('content-type')) outgoing.set('content-type', 'application/json');
  }
  const bytes = bodyBytes(data, maxBodyBytes);
  if (['GET', 'HEAD'].includes(verb) && bytes.length) fail('INVALID_BODY', 'GET and HEAD requests cannot have a body.');
  outgoing.set('accept-encoding', 'identity');
  outgoing.set('X-API-Key', apiKey);
  const deadline = new AbortController();
  const timer = setTimeout(() => deadline.abort(), timeoutMs);
  timer.unref?.();
  const combined = signal ? AbortSignal.any([signal, deadline.signal]) : deadline.signal;
  try {
    combined.throwIfAborted();
    const response = await fetch(url, {
      method: verb, headers: outgoing, body: bytes.length || !['GET', 'HEAD'].includes(verb) ? bytes : undefined,
      redirect: 'manual', signal: combined,
    });
    if (REDIRECT_STATUSES.has(response.status)) {
      await response.body?.cancel();
      fail('REDIRECT_FORBIDDEN', 'Talus redirects are refused; configure the final HTTPS endpoint.');
    }
    const encoding = response.headers.get('content-encoding');
    if (encoding && encoding.toLowerCase() !== 'identity') {
      await response.body?.cancel();
      fail('RESPONSE_ENCODING_FORBIDDEN', 'The Talus server must honor identity content encoding.');
    }
    const data = await readBounded(response, maxBodyBytes, combined);
    if (data.includes(Buffer.from(apiKey, 'utf8'))) {
      fail('CREDENTIAL_IN_RESPONSE', 'The Talus server returned protected credential material.');
    }
    const visibleHeaders = new Headers();
    for (const [name, value] of response.headers) {
      if (!RESPONSE_PRIVATE_HEADERS.has(name.toLowerCase()) && !name.includes(apiKey.toLowerCase()) && !value.includes(apiKey)) visibleHeaders.set(name, value);
    }
    return new Response([204, 205, 304].includes(response.status) || verb === 'HEAD' ? null : data, {
      status: response.status, statusText: response.statusText.includes(apiKey) ? '' : response.statusText, headers: visibleHeaders,
    });
  } catch (error) {
    if (error instanceof TalusRequestError) throw error;
    if (signal?.aborted) fail('REQUEST_ABORTED', 'The Talus request was cancelled.');
    if (deadline.signal.aborted) fail('REQUEST_TIMEOUT', 'The Talus request timed out.');
    fail('REQUEST_FAILED', 'The Talus request failed; verify connectivity and TLS trust.');
  } finally { clearTimeout(timer); }
}
