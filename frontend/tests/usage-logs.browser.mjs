// Production-build UI tests with synthetic API data; no backend is required.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE ?? 'playwright');
const port = Number(process.env.UI_TEST_PORT ?? 4178), base = `http://127.0.0.1:${port}`;
const server = spawn(process.execPath, ['node_modules/vite/bin/vite.js', 'preview', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], { cwd: fileURLToPath(new URL('../', import.meta.url)), stdio: 'ignore' });
async function until(check, label, timeout = 5000) { const deadline = Date.now() + timeout; while (Date.now() < deadline) { if (await check()) return; await delay(20); } throw new Error(`Timed out: ${label}`); }
const A = '9007199254741009', B = '9007199254741008';
const row = (id, extra = {}) => ({ id, operation_id: `op-${id}`, started_at: new Date(Date.now() - 10000).toISOString(), action: 'server.exec', outcome: 'failed', phase: 'closed', auth_type: 'jwt', username_snapshot: 'test-admin', resource_type: 'server', resource_id: 1, resource_name_snapshot: 'Historical server', source: 'operation', request_id: `request-${id}`, http_status: 200, exit_code: 1, duration_ms: 25, ...extra });
async function fixture(browser, role = 'admin', mobile = false) {
 const context = await browser.newContext({ viewport: mobile ? { width: 390, height: 844 } : { width: 1280, height: 900 }, isMobile: mobile, hasTouch: mobile, locale: 'en-US' });
 await context.addInitScript(role => { localStorage.setItem('auth_token', `test.${btoa(JSON.stringify({ uid: 1, username: 'test-admin', role, exp: Date.now() / 1000 + 3600 }))}.test`); localStorage.setItem('i18nextLng', 'en'); }, role);
 const state = { lists: [], details: [], first: row(A), detail: row(A), forbid: false, holdList: null, detailStatus: null };
 await context.route('**/api/v1/**', async route => {
  const url = new URL(route.request().url()), respond = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(status >= 400 ? data : { data }) });
  if (url.pathname === '/api/v1/usage-logs') { state.lists.push(url); if (state.holdList) await state.holdList; const query = Object.fromEntries(url.searchParams); delete query.cursor; query.page_size = Number(query.page_size); const next = url.searchParams.get('cursor'); return respond({ items: [next ? row(B) : state.first], query, id_upper_bound: '9007199254742000', next_cursor: next ? null : 'opaque-one', has_more: !next, consistency: 'bounded_keyset' }); }
  if (url.pathname.startsWith('/api/v1/usage-logs/')) { state.details.push(url.pathname); if (state.forbid) return respond({ error: { code: 403, reason: 'forbidden' } }, 403); if (state.detailStatus) return respond({ error: { code: state.detailStatus, reason: 'internal_error' } }, state.detailStatus); return respond({ ...state.detail, id: url.pathname.split('/').at(-1) }); }
  return respond(url.pathname === '/api/v1/setup/status' ? { needs_setup: false } : []);
 });
 const page = await context.newPage(), errors = []; page.on('pageerror', error => errors.push(error));
 return { context, page, state, errors };
}
const detailButton = (page, id) => page.getByRole('button', { name: `View details #${id}`, exact: true });
const denied = 'Only JWT administrators can view usage logs. Your current session does not have access.';
let browser;
try {
 await until(async () => { try { return (await fetch(base)).ok; } catch { return false; } }, 'preview', 10000);
 browser = await chromium.launch({ headless: true, ...(process.env.CHROMIUM_EXECUTABLE ? { executablePath: process.env.CHROMIUM_EXECUTABLE } : {}), args: JSON.parse(process.env.CHROMIUM_ARGS ?? '[]') });
 {
  const { page, context, state, errors } = await fixture(browser);
  await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); assert.equal(state.details.length, 0);
  await page.getByRole('button', { name: 'Next page', exact: true }).click(); await detailButton(page, B).waitFor();
  for (const key of ['from', 'to']) assert.equal(state.lists[1].searchParams.get(key), state.lists[0].searchParams.get(key));
  assert.equal(state.lists[1].searchParams.get('cursor'), 'opaque-one');
  await page.getByRole('button', { name: 'Previous page', exact: true }).click(); await detailButton(page, A).waitFor(); assert.equal(state.lists.length, 2);
  await detailButton(page, A).click(); const drawer = page.getByRole('dialog'); await drawer.getByText('Command exit code', { exact: true }).waitFor();
  assert.equal(new URL(page.url()).searchParams.get('detail'), A); await drawer.getByText('200', { exact: true }).waitFor(); await drawer.getByText('1', { exact: true }).waitFor();
  await delay(2200); assert.equal(state.details.length, 1); await page.keyboard.press('Escape'); await drawer.waitFor({ state: 'detached' }); assert.equal(new URL(page.url()).searchParams.has('detail'), false); assert.equal(state.lists.length, 2);
  await page.getByRole('button', { name: `Filter by request ID: request-${A}`, exact: true }).click(); await until(() => state.lists.length === 3, 'request filter'); assert.equal(state.lists[2].searchParams.get('request_id'), `request-${A}`);
  assert.equal(errors.length, 0); await context.close(); console.log('  ok: frozen pages, exact IDs, on-demand details, URL, HTTP/exit and request filter');
 }
 {
  const { page, context, state, errors } = await fixture(browser); state.detail = row(A, { outcome: 'running', phase: 'ready', duration_ms: null });
  await page.clock.install({ time: new Date() });
  await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); await detailButton(page, A).click(); await until(() => state.details.length === 1, 'detail');
  const elapsed = page.getByRole('dialog').getByText(/^\d+ s elapsed$/); await elapsed.waitFor(); const initialElapsed = await elapsed.textContent();
  await page.clock.fastForward(1100); assert.notEqual(await elapsed.textContent(), initialElapsed, 'running detail time advances when list has no running rows');
  state.detail = { ...state.detail, outcome: 'succeeded', phase: 'closed', duration_ms: 2000, finished_at: new Date().toISOString() };
  await page.clock.fastForward(2100); await until(() => state.details.length === 2, 'running detail poll', 6000); await page.getByRole('dialog').getByText('Succeeded', { exact: true }).waitFor(); await page.clock.fastForward(10000); assert.equal(state.details.length, 2);
  await page.keyboard.press('Escape'); await page.getByLabel('Check for updates', { exact: true }).check(); state.first = { ...state.first, resource_name_snapshot: 'Updated server' };
  await page.clock.fastForward(15100); await until(() => state.lists.length === 2, 'first page probe', 5000); await page.getByText('The first page has updates. Refresh to view them.', { exact: true }).waitFor(); assert.equal(await page.getByRole('table').getByText('Historical server', { exact: true }).isVisible(), true);
  state.first = { ...state.first, resource_name_snapshot: 'Historical server' };
  await page.clock.fastForward(15100); await until(() => state.lists.length === 3, 'second probe');
  assert.equal(await page.getByText('The first page has updates. Refresh to view them.', { exact: true }).isVisible(), true, 'update notice is latched');
  await page.evaluate(() => { Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' }); document.dispatchEvent(new Event('visibilitychange')); });
  await page.clock.fastForward(31000); assert.equal(state.lists.length, 3, 'hidden page stops probes');
  assert.equal(errors.length, 0); await context.close(); console.log('  ok: running detail stops at terminal, update probes preserve rows, latch and stop while hidden');
 }
 {
  const { page, context, state, errors } = await fixture(browser); await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); state.forbid = true; await detailButton(page, A).click(); await page.getByText(denied, { exact: true }).waitFor();
  assert.equal(await page.getByRole('dialog').count(), 0); assert.equal(await detailButton(page, A).count(), 0); assert.equal(await page.getByRole('link', { name: 'Usage logs', exact: true }).count(), 0); assert.ok(await page.evaluate(() => localStorage.getItem('auth_token'))); assert.equal(errors.length, 0); await context.close(); console.log('  ok: current-session 403 clears rows, drawer and navigation, keeps token');
 }
 {
  const { page, context, state } = await fixture(browser, 'user'); await page.goto(`${base}/usage-logs`); await page.getByText(denied, { exact: true }).waitFor(); assert.equal(state.lists.length, 0); assert.equal(await page.getByRole('link', { name: 'Usage logs', exact: true }).count(), 0); await context.close(); console.log('  ok: non-admin denied before queries');
 }
 {
  const { page, context, errors } = await fixture(browser, 'admin', true); await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true); await detailButton(page, A).click(); await page.getByRole('dialog').getByText('Command exit code', { exact: true }).waitFor(); await page.keyboard.press('Escape'); assert.equal(errors.length, 0); await context.close(); console.log('  ok: mobile rows and drawer fit viewport');
 }
 {
  const { page, context, state, errors } = await fixture(browser); let release; state.holdList = new Promise(resolve => { release = resolve; });
  await page.clock.install({ time: new Date() }); await page.goto(`${base}/usage-logs`); await until(() => state.lists.length === 1, 'pending first-page fetch');
  await page.clock.fastForward(61000); await delay(100); assert.equal(state.lists.length, 1, 'minute cache cleanup preserves active uncached page fetch');
  release(); await detailButton(page, A).waitFor(); assert.equal(state.lists.length, 1); assert.equal(errors.length, 0); await context.close(); console.log('  ok: minute cache cleanup retains the current in-flight page');
 }
 {
  const { page, context, state, errors } = await fixture(browser); await page.clock.install({ time: new Date() });
  await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); await detailButton(page, A).click(); const drawer = page.getByRole('dialog'); await drawer.getByText('Historical server', { exact: true }).waitFor(); await page.keyboard.press('Escape');
  await page.clock.setSystemTime(new Date(Date.now() + 11 * 60000)); state.detailStatus = 500;
  await detailButton(page, A).click(); await drawer.getByRole('alert').waitFor(); assert.equal(await drawer.getByText('Historical server', { exact: true }).count(), 0, 'expired idle detail does not reappear when refresh fails');
  state.detailStatus = null; state.detail = { ...state.detail, resource_name_snapshot: 'After TTL' }; await drawer.getByRole('button', { name: 'Retry', exact: true }).click(); await drawer.getByText('After TTL', { exact: true }).waitFor(); assert.equal(errors.length, 0); await context.close(); console.log('  ok: idle detail TTL survives throttled timers; failed reopens never revive stale data');
 }
 console.log('\nusage-logs-browser: ALL PASS');
} finally { await browser?.close(); server.kill('SIGTERM'); }
