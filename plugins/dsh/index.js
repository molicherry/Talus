import { fileURLToPath } from 'node:url';
import * as skillFilesystem from '@deepseek-ai/dsh-skill-filesystem';
import z from '@deepseek-ai/schemastery';

export const name = 'talus';
export const inject = ['skills', 'shellEnv'];

export const Config = z.object({
  url: z.string().pattern(/^(?!.*\/api\/v1\/*$)(?:https?:\/\/[^\s/?#@]+(?:\/[^\s?#]*)?)?$/i).default('')
    .description('Talus base URL, without /api/v1.').volatile(),
  apiKey: z.string().pattern(/^[^\r\n]*$/).default('').role('secret')
    .description('Talus API key.').volatile(),
});

// Use DSH's parser and resource handling without changing its existing roots.
export function apply(ctx, config) {
  ctx.shellEnv.register({
    name: 'talus',
    variables: {
      DSH_TALUS_URL: { description: 'Configured Talus base URL; use as TALUS_URL in the talus skill.' },
      DSH_TALUS_API_KEY: { description: 'Configured Talus API key; use as TALUS_API_KEY without printing it.' },
    },
    resolve() {
      return {
        DSH_TALUS_URL: (config.url.get().trim() || process.env.TALUS_URL || 'http://localhost:8080').replace(/\/+$/, ''),
        DSH_TALUS_API_KEY: config.apiKey.get().trim() || process.env.TALUS_API_KEY || '',
      };
    },
  });
  ctx.plugin(skillFilesystem, {
    providerName: 'talus',
    includeDefaultRoots: false,
    bundledSkillDir: fileURLToPath(new URL('./skills/', import.meta.url)),
    watch: false,
  });
}
