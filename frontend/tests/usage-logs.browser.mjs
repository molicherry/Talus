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
 const state = { lists: [], details: [], first: row(A), detail: row(A), forbid: false, holdList: null, detailStatus: null, optionRequests: [], optionStatus: null, optionCustom: null };
 await context.route('**/api/v1/**', async route => {
  const url = new URL(route.request().url()), respond = (data, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(status >= 400 ? data : { data }) });
  if (url.pathname === '/api/v1/usage-logs') { state.lists.push(url); if (state.holdList) await state.holdList; const query = Object.fromEntries(url.searchParams); delete query.cursor; query.page_size = Number(query.page_size); const next = url.searchParams.get('cursor'); return respond({ items: [next ? row(B) : state.first], query, id_upper_bound: '9007199254742000', next_cursor: next ? null : 'opaque-one', has_more: !next, consistency: 'bounded_keyset' }); }
  if (url.pathname === '/api/v1/usage-logs/filter-options') {
   state.optionRequests.push(url);
   if (state.optionCustom && await state.optionCustom({ url, respond })) return;
   if (state.optionStatus) return respond({ error: { code: state.optionStatus, reason: state.optionStatus === 403 ? 'forbidden' : 'internal_error' } }, state.optionStatus);
   const kind = url.searchParams.get('kind'), q = (url.searchParams.get('q') || '').toLowerCase();
   const data = kind === 'server' ? [{ id: '1', name: 'Alpha server', deleted: false }, { id: '9007199254741999', name: 'Retired server', deleted: true }] : kind === 'user' ? [{ id: '2', name: 'Alice', deleted: false }, { id: '3', name: 'Former user', deleted: true }] : [{ id: '42', name: 'automation', prefix: 'tal_demo', deleted: false }, { id: '43', name: 'release automation', prefix: 'tal_release', deleted: false }];
   if (q === '007') return respond({ items: [{ id: '5', name: '007', deleted: false }], has_more: false });
   if (q === 'many') return respond({ items: Array.from({ length: 50 }, (_, i) => ({ id: String(i + 1), name: `Many server ${i + 1}`, deleted: false })), has_more: true });
   return respond({ items: data.filter(item => `${item.name} ${item.id} ${item.prefix || ''}`.toLowerCase().includes(q)), has_more: false });
  }
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
 {
  const { page, context, state, errors } = await fixture(browser); const largeID = '9007199254741999';
  await page.goto(`${base}/usage-logs?server_id=${largeID}&user_id=2&api_key_id=42`);
  const serverInput = page.getByRole('combobox', { name: 'Server', exact: true }), userInput = page.getByRole('combobox', { name: 'User', exact: true }), keyInput = page.getByRole('combobox', { name: 'API key', exact: true });
  await until(async () => (await serverInput.inputValue()).includes('Retired server') && (await userInput.inputValue()).includes('Alice') && (await keyInput.inputValue()).includes('tal_demo'), 'URL selected labels');
  assert.ok((await serverInput.inputValue()).includes(largeID)); assert.ok((await serverInput.inputValue()).includes('Deleted'));
  assert.equal(state.optionRequests.length, 3); for (const [kind, value] of [['server', largeID], ['user', '2'], ['api_key', '42']]) assert.ok(state.optionRequests.some(url => url.searchParams.get('kind') === kind && url.searchParams.get('q') === value));
  await page.getByRole('button', { name: 'Clear Server filter', exact: true }).click(); await page.getByRole('button', { name: 'Apply filters', exact: true }).click();
  await until(() => state.lists.length === 2, 'clear server'); assert.equal(state.lists[1].searchParams.get('server_id'), null); assert.equal(state.lists[1].searchParams.get('api_key_id'), '42');
  await serverInput.click(); await serverInput.fill('Alpha'); await page.getByRole('option', { name: 'Alpha server · #1', exact: true }).waitFor();
  await serverInput.press('ArrowDown'); await serverInput.press('ArrowDown'); await serverInput.press('Enter'); assert.ok((await serverInput.inputValue()).includes('Alpha server'));
  await serverInput.click(); assert.equal(await serverInput.getAttribute('aria-expanded'), 'true'); assert.equal(await serverInput.inputValue(), '', 'repeat click starts a new search'); await serverInput.press('Escape');
  await serverInput.press('A'); assert.equal(await serverInput.inputValue(), 'A', 'typing on focused closed selector does not append the selected label'); await serverInput.press('Escape');
  await serverInput.press('ArrowDown'); await delay(400); assert.ok(await serverInput.getAttribute('aria-activedescendant')); await serverInput.press('Enter'); assert.equal(await serverInput.inputValue(), '', 'Down+Enter All clears selection');
  await keyInput.click(); await keyInput.fill('release'); await page.getByRole('option', { name: 'release automation · tal_release · #43', exact: true }).click();
  assert.ok((await keyInput.inputValue()).includes('tal_release')); await page.getByRole('button', { name: 'Apply filters', exact: true }).click(); await until(() => state.lists.length === 3, 'named key filter'); assert.equal(state.lists[2].searchParams.get('api_key_id'), '43');
  assert.equal(await page.getByText('Correlates logs from the same HTTP request for troubleshooting.', { exact: true }).isVisible(), true); assert.equal(errors.length, 0); await context.close(); console.log('  ok: URL label restoration, exact large IDs, deleted/prefix choices, clear and repeated keyboard selection');
 }
 {
  const { page, context, state, errors } = await fixture(browser); await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); const input = page.getByRole('combobox', { name: 'Server', exact: true });
  await input.click(); await input.fill('9007199254741997'); await input.press('Enter'); assert.equal(await input.inputValue(), 'Historical ID #9007199254741997'); await page.getByRole('button', { name: 'Apply filters', exact: true }).click(); await until(() => state.lists.length === 2, 'manual historical ID'); assert.equal(state.lists[1].searchParams.get('server_id'), '9007199254741997');
  await input.click(); await input.fill('9223372036854775808'); await page.getByText('IDs must be between 1 and 9223372036854775807; name search still works.', { exact: true }).waitFor(); assert.equal(await page.getByRole('option', { name: /^Use historical ID/ }).count(), 0); await input.press('Enter'); assert.equal(await input.getAttribute('aria-expanded'), 'true');
  await input.fill('007'); await page.getByRole('option', { name: '007 · #5', exact: true }).click(); assert.equal(await input.inputValue(), '007 · #5', 'numeric names remain searchable even when not valid manual IDs'); await input.click(); await input.fill('9223372036854775807'); await input.press('Enter'); await page.getByRole('button', { name: 'Apply filters', exact: true }).click(); await until(() => state.lists.length === 3, 'signed maximum ID'); assert.equal(state.lists[2].searchParams.get('server_id'), '9223372036854775807');
  await input.click(); await input.fill('missing-name'); await page.getByText('No matching names. You can still use a historical ID.', { exact: true }).waitFor();
  await input.fill('many'); await page.getByText('More than 50 matches. Narrow your search.', { exact: true }).waitFor(); assert.equal(await page.getByRole('listbox').getByRole('option').count(), 51);
  await input.press('Escape'); await page.getByRole('button', { name: 'Clear Server filter', exact: true }).click(); state.optionStatus = 500; await input.click(); await input.fill('Alpha'); const retry = page.getByRole('button', { name: 'Retry', exact: true }); await retry.waitFor();
  await input.press('Tab'); await page.keyboard.press('Tab'); assert.equal(await retry.evaluate(el => el === document.activeElement), true, 'Retry is keyboard reachable'); state.optionStatus = null; await page.keyboard.press('Enter'); await page.getByRole('option', { name: 'Alpha server · #1', exact: true }).waitFor(); assert.equal(await input.evaluate(el => el === document.activeElement), true, 'Retry preserves focus');
  assert.equal(errors.length, 0); await context.close(); console.log('  ok: manual/history IDs, signed bigint bounds, no-results/50-match hint and keyboard retry');
 }
 {
  const { page, context, state, errors } = await fixture(browser); let release, finished = false;
  state.optionCustom = async ({ url, respond }) => { if (url.searchParams.get('q') !== 'slow') return false; await new Promise(resolve => { release = resolve; }); try { await respond({ items: [{ id: '4', name: 'Stale candidate', deleted: false }], has_more: false }); } catch {} finished = true; return true; };
  await page.goto(`${base}/usage-logs`); await detailButton(page, A).waitFor(); const input = page.getByRole('combobox', { name: 'Server', exact: true }); await input.click(); await input.fill('slow'); await until(() => !!release, 'slow search started'); await input.fill('Alpha'); await page.getByRole('option', { name: 'Alpha server · #1', exact: true }).waitFor(); release(); await until(() => finished, 'late search resolved'); assert.equal(await page.getByRole('option', { name: /Stale candidate/ }).count(), 0);
  await page.getByRole('option', { name: 'Alpha server · #1', exact: true }).click(); assert.ok((await input.inputValue()).includes('Alpha server')); finished = false; release = null; await input.click(); await input.fill('slow'); await until(() => !!release, 'old-account request');
  await page.evaluate(() => { const oldValue = localStorage.getItem('auth_token'); const newValue = `test.${btoa(JSON.stringify({ uid: 2, username: 'new-admin', role: 'admin', exp: Date.now() / 1000 + 3600 }))}.test`; localStorage.setItem('auth_token', newValue); window.dispatchEvent(new StorageEvent('storage', { key: 'auth_token', oldValue, newValue, storageArea: localStorage })); });
  await until(async () => (await input.inputValue()) === '', 'new epoch clears selected name'); assert.equal(await page.getByRole('listbox').count(), 0); release(); await until(() => finished, 'old account response'); assert.equal(await input.inputValue(), ''); assert.equal(await page.getByText('Stale candidate', { exact: true }).count(), 0); assert.equal(errors.length, 0); await context.close(); console.log('  ok: stale search results and account-switched candidates/selections cannot reappear');
 }
 {
  const { page, context, state, errors } = await fixture(browser); let release, finished = false;
  state.optionCustom = async ({ url, respond }) => { if (url.searchParams.get('kind') !== 'user') return false; if (url.searchParams.get('q') === '2') { await new Promise(resolve => { release = resolve; }); try { await respond({ items: [{ id: '2', name: 'Old Alice', deleted: false }], has_more: false }); } catch {} finished = true; return true; } await respond({ items: [{ id: '2', name: 'New Alice', deleted: false }], has_more: false }); return true; };
  await page.goto(`${base}/usage-logs?user_id=2`); await until(() => !!release, 'URL label lookup'); const input = page.getByRole('combobox', { name: 'User', exact: true }); await input.click(); await page.getByRole('option', { name: 'New Alice · #2', exact: true }).click(); assert.equal(await input.inputValue(), 'New Alice · #2'); release(); await until(() => finished, 'old label resolved'); assert.equal(await input.inputValue(), 'New Alice · #2', 'explicit same-ID selection wins over pending URL label');
  assert.equal(errors.length, 0); await context.close(); console.log('  ok: same-ID selection supersedes a pending URL label lookup');
 }
 console.log('\nusage-logs-browser: ALL PASS');
} finally { await browser?.close(); server.kill('SIGTERM'); }
