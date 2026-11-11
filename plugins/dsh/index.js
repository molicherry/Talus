import { fileURLToPath } from 'node:url';
import * as skillFilesystem from '@deepseek-ai/dsh-skill-filesystem';

export const name = 'talus';
export const inject = ['skills'];

// Use DSH's parser and resource handling without changing its existing roots.
export function apply(ctx) {
  ctx.plugin(skillFilesystem, {
    providerName: 'talus',
    includeDefaultRoots: false,
    bundledSkillDir: fileURLToPath(new URL('./skills/', import.meta.url)),
    watch: false,
  });
}
