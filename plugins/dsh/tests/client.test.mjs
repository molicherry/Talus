import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';
import { Context } from '@deepseek-ai/cordis';

test('browser credential bridge uses DSH context injection and native argument signatures', async t => {
  let module, bridge;
  vm.runInNewContext(await readFile(new URL('../client.js', import.meta.url), 'utf8'), {
    window: { __ModuleLoader__: { load(value) { module = value; } } },
    AbortController, DOMException,
  });
  const plugin = module.factory(name => {
    assert.equal(name, 'react');
    return { createElement() {} };
  });
  const calls = [];
  let stored;
  const native = {
    describe(...args) {
      assert.equal(args.length, 1);
      calls.push('describe');
      return { ok: true, value: Object.fromEntries(args[0].map(ref => [ref, {
        configured: stored !== undefined, writable: true,
      }])) };
    },
    set(...args) {
      assert.equal(args.length, 2, 'the native method does not accept an AbortSignal');
      calls.push('set'); stored = args[1];
      return { ok: true, value: undefined };
    },
    unset(...args) {
      assert.equal(args.length, 1);
      calls.push('unset'); stored = undefined;
      return { ok: true, value: undefined };
    },
  };
  const ctx = new Context();
  t.after(() => ctx.fiber.dispose());
  ctx.provide('remote', { credentials: native });
  ctx.provide('remote.credentials', native);
  ctx.provide('locale', { register() { return () => {}; } });
  ctx.provide('configForms', { get() {}, describe() {} });
  ctx.provide('slots', {
    inject(_name, callback) { callback(); },
    register(options) { bridge = options.inject().credentials; },
  });
  await ctx.plugin(plugin).await();
  assert.ok(bridge);
  const signal = new AbortController().signal;
  assert.deepEqual(structuredClone(await bridge.describe('TALUS_TEST', signal)), { configured: false, writable: true });
  await bridge.set('TALUS_TEST', 'synthetic-private-key', signal);
  assert.equal(stored, 'synthetic-private-key');
  const status = await bridge.describe('TALUS_TEST', signal);
  assert.equal(status.configured, true);
  assert.ok(!JSON.stringify(status).includes('synthetic-private-key'));
  await bridge.unset('TALUS_TEST', signal);
  assert.equal(stored, undefined);
  assert.deepEqual(calls, ['describe', 'set', 'describe', 'unset']);
});
