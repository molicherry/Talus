import { fileURLToPath } from 'node:url';
import * as skillFilesystem from '@deepseek-ai/dsh-skill-filesystem';
import { defineTool } from '@deepseek-ai/dsh-tools';
import z from '@deepseek-ai/schemastery';
import { credentialReference } from './credentials.mjs';
import { createBroker } from './skills/talus/scripts/broker.mjs';
import { request, validateBaseUrl } from './skills/talus/scripts/private-client.mjs';

export { credentialReference } from './credentials.mjs';
export const name = 'talus';
export const inject = ['skills', 'shellEnv', 'credentials', 'settings', 'tools'];

export const Config = z.object({
  // Legacy HTTP destinations remain editable; the UI and requests require HTTPS.
  url: z.string().pattern(/^(?!.*\/api\/v1\/*$)(?:https?:\/\/[^\s/?#@]+(?:\/[^\s?#]*)?)?$/i).default('')
    .description('Talus HTTPS base URL, without /api/v1.').volatile(),
  apiKey: z.string().pattern(/^[^\r\n]*$/).role('secret')
    .description('Legacy field; re-enter the key in Plugins → Talus to use the credential service.').volatile(),
});

function untilAborted(operation, signal) {
  return new Promise((resolve, reject) => {
    const cancel = () => {
      signal.removeEventListener('abort', cancel);
      reject(new Error('The Talus request was cancelled or timed out.'));
    };
    Promise.resolve(operation).then(value => {
      signal.removeEventListener('abort', cancel); resolve(value);
    }, error => {
      signal.removeEventListener('abort', cancel); reject(error);
    });
    if (signal.aborted) cancel();
    else signal.addEventListener('abort', cancel, { once: true });
  });
}

export async function apply(ctx, config) {
  const lifetime = new AbortController();
  const ns = ctx.fiber.entry?.options.id ?? 'talus';
  let clearing;

  // Never copy an old key: native references have no compare-and-set, so copying
  // could overwrite a concurrent UI save. Erase it only after native setup.
  async function clearLegacy(url) {
    if (clearing) return clearing;
    clearing = (async () => {
      const legacy = config.apiKey?.get();
      if (!legacy || config.url.get().trim() !== url) return;
      const view = ctx.settings.describe().find(row => row.ns === ns);
      if (!view || view.value?.apiKey !== legacy) return;
      const op = view.base?.apiKey
        ? { op: 'set', path: ['apiKey'], value: '' }
        : { op: 'unset', path: ['apiKey'] };
      try { await ctx.settings.mutate(ns, [op], view.revision); }
      catch { /* A stale revision or read-only profile must preserve newer edits. */ }
    })().finally(() => { clearing = undefined; });
    return clearing;
  }

  async function resolveConfig({ signal = lifetime.signal } = {}) {
    signal.throwIfAborted();
    const rawUrl = config.url.get().trim();
    if (!rawUrl) throw new Error('Configure the Talus HTTPS URL and API key in Plugins → Talus.');
    const url = validateBaseUrl(rawUrl).toString().replace(/\/+$/, '');
    let hit;
    try { hit = await untilAborted(ctx.credentials.resolve(credentialReference(url)), signal); }
    catch {
      throw new Error(signal.aborted
        ? 'The Talus request was cancelled or timed out.'
        : 'The Talus credential store could not be read. Check the host credential configuration.');
    }
    signal.throwIfAborted();
    if (!hit?.value) throw new Error('Enter the API key for this Talus address in Plugins → Talus. Legacy configuration and environment keys are no longer used.');
    signal.throwIfAborted();
    return { url, apiKey: hit.value };
  }

  const broker = await createBroker({ resolveConfig });
  ctx.effect(() => () => broker.close());
  ctx.shellEnv.register({
    name: 'talus',
    variables: {
      DSH_TALUS_BROKER: { description: 'Talus local broker descriptor path; contains no API key.' },
      DSH_TALUS_HELPER: { description: 'Bundled Talus request helper using host-managed authentication.' },
    },
    resolve: () => ({
      DSH_TALUS_BROKER: broker.descriptorPath,
      DSH_TALUS_HELPER: fileURLToPath(new URL('./skills/talus/scripts/request.mjs', import.meta.url)),
    }),
  });

  ctx.tools.register(defineTool({
    name: 'talus_request',
    description: 'Call the configured Talus management API over HTTPS using host-held credentials. Use relative /api/v1 paths. Returns bounded JSON/text; use the bundled broker helper in the shell for binary files or responses over 1 MiB. Talus permissions and operation confirmation requirements still apply.',
    parameters: {
      path: { type: 'string', required: true, description: 'Relative /api/v1/... path, optionally including a query string.' },
      method: { type: 'string', enum: ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'] },
      json: { type: 'json', description: 'Optional JSON body; never put authentication credentials here.' },
      headers: { type: 'object', additionalProperties: true, description: 'Optional string headers, such as Range or X-Upload-Token. Authentication, host, and transport headers are reserved.' },
      timeout_ms: { type: 'integer', description: 'Deadline including body reads; default 30000, maximum 300000 milliseconds.' },
    },
    output: {
      schema: {
        type: 'object', additionalProperties: false,
        properties: {
          status: { type: 'integer', required: true },
          headers: { type: 'object', additionalProperties: true, required: true },
          body: { type: 'string', required: true },
        },
      },
      render: (_args, value) => [{ type: 'text', text: JSON.stringify(value) }],
    },
    async execute(args, exec) {
      const timeoutMs = args.timeout_ms ?? 30_000;
      if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1 || timeoutMs > 300_000) {
        throw new Error('The request timeout must be between 1 and 300000 milliseconds.');
      }
      const deadline = AbortSignal.timeout(timeoutMs);
      const signal = AbortSignal.any([lifetime.signal, deadline, ...(exec.signal ? [exec.signal] : [])]);
      const connection = await resolveConfig({ signal });
      const response = await request({
        baseUrl: connection.url, apiKey: connection.apiKey,
        path: args.path, method: args.method, headers: args.headers, json: args.json,
        timeoutMs, maxBodyBytes: 1024 * 1024, signal,
      });
      const type = response.headers.get('content-type') ?? '';
      if (type && !/^(?:text\/|application\/(?:[\w.-]+\+)?json(?:\s*;|$))/i.test(type)) {
        throw new Error('Use the bundled Talus request helper with --output for binary responses.');
      }
      return { status: response.status, headers: Object.fromEntries(response.headers), body: await response.text() };
    },
  }));

  ctx.plugin(skillFilesystem, {
    providerName: 'talus', includeDefaultRoots: false,
    bundledSkillDir: fileURLToPath(new URL('./skills/', import.meta.url)), watch: false,
  });
  ctx.effect(() => () => lifetime.abort());
  ctx.effect(() => ctx.settings.configure({ auto: false }, ctx.fiber));

  // Settings can describe this plugin only after activation. Native credential
  // saves and live settings changes also trigger obsolete field cleanup.
  const cleanIfConfigured = async () => {
    try {
      const rawUrl = config.url.get().trim();
      if (!rawUrl || !config.apiKey?.get()) return;
      if ((await ctx.credentials.describe(credentialReference(rawUrl))).configured) await clearLegacy(rawUrl);
    } catch { /* Invalid legacy URLs remain editable; no request is made. */ }
  };
  ctx.on('internal/status', fiber => { if (fiber === ctx.fiber && fiber.state === 2) void cleanIfConfigured(); });
  ctx.on('credentials/reference-updated', ref => {
    try {
      if (ref === credentialReference(config.url.get().trim())) void cleanIfConfigured();
    } catch { /* Unconfigured destinations do not own a credential. */ }
  });
  ctx.on('loader/volatile-update', () => { void cleanIfConfigured(); });
}
