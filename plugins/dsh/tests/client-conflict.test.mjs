import assert from 'node:assert/strict';
import { createHash, webcrypto } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';
import test from 'node:test';
import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';

const refFor = url => `TALUS_${createHash('sha256').update(new URL(url).toString().replace(/\/+$/, '')).digest('hex').toUpperCase()}`;
const pause = ms => new Promise(resolve => setTimeout(resolve, ms));
const source = await readFile(new URL('../client.js', import.meta.url), 'utf8');

function setup({ targetInitiallySet = false, setReplyLost = false, failRecheck = false } = {}) {
  let module, component, props;
  vm.runInNewContext(source, { window: { __ModuleLoader__: { load(value) { module = value; } } },
    AbortController, DOMException, URL, TextEncoder, Uint8Array, crypto: webcrypto, setTimeout, clearTimeout });
  const plugin = module.factory(name => { assert.equal(name, 'react'); return React; });
  const oldAddress = 'https://old.example.test';
  const newAddress = 'https://new.example.test';
  const targetRef = refFor(newAddress);
  const values = new Map(targetInitiallySet ? [[targetRef, 'existing-fixture-key']] : []);
  const calls = [];
  let nextDescribeFails = false, mutateCount = 0;
  let snapshot = { status: 'ready', writable: true, revision: 1, value: { url: oldAddress } };
  const subscribers = new Set();
  const form = {
    subscribe(fn) { subscribers.add(fn); return () => subscribers.delete(fn); },
    getSnapshot() { return snapshot; },
    async mutate(ops, expectedRevision) {
      mutateCount++;
      assert.equal(expectedRevision, 1);
      assert.equal(ops[0].value, newAddress);
      snapshot = { ...snapshot, revision: 2, value: { url: 'https://concurrent.example.test' } };
      for (const listener of subscribers) listener();
      if (failRecheck) nextDescribeFails = true;
      return false;
    },
  };
  const description = { view: { namespaces: [] } };
  const descriptionStore = { subscribe() { return () => {}; }, getSnapshot() { return description; } };
  const native = {
    async describe(refs) {
      calls.push('describe');
      if (nextDescribeFails) { nextDescribeFails = false; return { ok: false }; }
      return { ok: true, value: Object.fromEntries(refs.map(ref => [ref, { configured: values.has(ref), writable: true }])) };
    },
    async set(ref, value) {
      calls.push('set'); values.set(ref, value);
      return setReplyLost ? { ok: false } : { ok: true };
    },
    async unset(ref) { calls.push('unset'); values.delete(ref); return { ok: true }; },
  };
  plugin.apply({
    effect(fn) { fn(); }, locale: { register() { return () => {}; } },
    remote: { credentials: native },
    configForms: { get() { return form; }, describe() { return descriptionStore; } },
    slots: {
      inject(_name, fn) { fn(); },
      register(options, view) { component = view; props = { ...options.inject(), t: key => key }; },
    },
  });
  return { component, props, targetRef, values, calls, newAddress, get mutateCount() { return mutateCount; } };
}

async function mount(s) {
  let renderer;
  await act(async () => { renderer = TestRenderer.create(React.createElement(s.component, s.props)); });
  return renderer;
}
const input = (r, name) => r.root.findByProps({ name });
const visible = (r, value) => r.root.findAll(node => node.children?.includes(value)).length > 0;
async function editAddress(renderer, address) {
  await act(async () => { input(renderer, 'talus-url').props.onChange({ target: { value: address } }); });
  await act(async () => { await pause(230); });
}
async function submit(renderer) {
  await act(async () => { await renderer.root.findByType('form').props.onSubmit({ preventDefault() {} }); });
}

test('credential changes are reported accurately after a conflicting URL save', async t => {
  await t.test('a saved target key is disclosed as configured and the draft is preserved', async () => {
    const s = setup(); const renderer = await mount(s);
    try {
      await editAddress(renderer, s.newAddress);
      await act(async () => { input(renderer, 'talus-api-key').props.onChange({ target: { value: 'draft-fixture-key' } }); });
      const before = s.calls.filter(x => x === 'describe').length;
      await submit(renderer);
      assert.equal(s.values.get(s.targetRef), 'draft-fixture-key');
      assert.equal(s.mutateCount, 1);
      assert.ok(s.calls.filter(x => x === 'describe').length >= before + 2, 'recheck the target after conflict');
      assert.ok(visible(renderer, 'failedKeyPresent'));
      assert.equal(input(renderer, 'talus-url').props.value, s.newAddress);
      assert.equal(input(renderer, 'talus-api-key').props.value, 'draft-fixture-key');
      assert.ok(!visible(renderer, 'saved'));
    } finally { await act(async () => renderer.unmount()); }
  });
  await t.test('a cleared target key is reported missing after conflict', async () => {
    const s = setup({ targetInitiallySet: true }); const renderer = await mount(s);
    try {
      await editAddress(renderer, s.newAddress);
      const clear = renderer.root.findAllByType('button').find(node => node.children.includes('clear'));
      await act(async () => clear.props.onClick());
      await submit(renderer);
      assert.equal(s.values.has(s.targetRef), false);
      assert.ok(visible(renderer, 'failedKeyMissing'));
      assert.ok(visible(renderer, 'clearPending'));
      assert.equal(input(renderer, 'talus-url').props.value, s.newAddress);
    } finally { await act(async () => renderer.unmount()); }
  });
  await t.test('a credential write whose reply was lost is rechecked before reporting failure', async () => {
    const s = setup({ setReplyLost: true }); const renderer = await mount(s);
    try {
      await editAddress(renderer, s.newAddress);
      await act(async () => input(renderer, 'talus-api-key').props.onChange({ target: { value: 'draft-fixture-key' } }));
      await submit(renderer);
      assert.equal(s.values.get(s.targetRef), 'draft-fixture-key');
      assert.equal(s.mutateCount, 0);
      assert.ok(visible(renderer, 'failedKeyPresent'));
      assert.equal(input(renderer, 'talus-api-key').props.value, 'draft-fixture-key');
    } finally { await act(async () => renderer.unmount()); }
  });
  await t.test('an unavailable recheck reports an unknown key status', async () => {
    const s = setup({ failRecheck: true }); const renderer = await mount(s);
    try {
      await editAddress(renderer, s.newAddress);
      await act(async () => input(renderer, 'talus-api-key').props.onChange({ target: { value: 'draft-fixture-key' } }));
      await submit(renderer);
      assert.ok(visible(renderer, 'failedKeyUnknown'));
      assert.equal(input(renderer, 'talus-api-key').props.value, 'draft-fixture-key');
    } finally { await act(async () => renderer.unmount()); }
  });
});
