import { createHash } from 'node:crypto';
import { credentialRef } from '@deepseek-ai/dsh-credentials';

// Bind the key to the destination, even if a separate public URL save fails.
// HTTP is accepted here only to describe/delete pre-upgrade credentials.
export function credentialReference(value) {
  const url = new URL(value.trim());
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new Error('Use a Talus HTTPS base URL without credentials, a query, or a fragment.');
  }
  const canonical = url.toString().replace(/\/+$/, '');
  return credentialRef(`TALUS_${createHash('sha256').update(canonical).digest('hex').toUpperCase()}`);
}
