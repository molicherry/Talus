import assert from 'node:assert/strict';
import { cp, mkdir, readdir, readFile, rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';

const source = fileURLToPath(new URL('../../../skills/talus/', import.meta.url));
const target = fileURLToPath(new URL('../skills/talus/', import.meta.url));
const sourceLicense = fileURLToPath(new URL('../../../LICENSE', import.meta.url));
const targetLicense = fileURLToPath(new URL('../LICENSE', import.meta.url));
const check = process.argv[2] === '--check';
if (process.argv.length > (check ? 3 : 2)) {
  throw new Error('Usage: node scripts/sync-skills.mjs [--check]');
}

async function files(directory, prefix = '') {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const relative = join(prefix, entry.name);
    if (entry.isDirectory()) result.push(...await files(join(directory, entry.name), relative));
    else if (entry.isFile()) result.push(relative);
    else throw new Error(`Only regular files and directories may be bundled: ${relative}`);
  }
  return result.sort();
}

// Verify the source before replacing the generated copy, including symlinks.
const sourceFiles = await files(source);
await readFile(sourceLicense);
if (check) {
  assert.deepEqual(await files(target), sourceFiles, 'Bundled skill file list differs; run npm run sync-skills');
  for (const file of sourceFiles) {
    assert.deepEqual(await readFile(join(target, file)), await readFile(join(source, file)),
      `${file} differs; run npm run sync-skills`);
  }
  assert.deepEqual(await readFile(targetLicense), await readFile(sourceLicense),
    'LICENSE differs; run npm run sync-skills');
  console.log('Bundled Talus skill and license match the repository sources.');
} else {
  await rm(target, { recursive: true, force: true });
  await mkdir(target, { recursive: true });
  await cp(source, target, { recursive: true });
  await cp(sourceLicense, targetLicense);
  console.log('Synced the complete Talus skill and license.');
}
