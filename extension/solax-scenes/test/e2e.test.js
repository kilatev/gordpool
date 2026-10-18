// End-to-end tests in real Chromium against test/fixtures/mock-scene.html.
//
// The panel tests run content.js in the page with a small chrome.* stub (backed
// by sessionStorage so it survives reloads); the last test loads the unpacked
// extension itself to check the manifest, options page and background worker.
const { test, before, after } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const http = require('node:http');
const os = require('node:os');
const path = require('node:path');
const { chromium } = require('playwright');

const ROOT = path.join(__dirname, '..');
const PLAN = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures/plan.json'), 'utf8'));
const MOCK = fs.readFileSync(path.join(__dirname, 'fixtures/mock-scene.html'), 'utf8');
const SCRIPTS = ['lib/macro.js', 'content.js'].map((f) => fs.readFileSync(path.join(ROOT, f), 'utf8'));

let server;
let base;
let browser;

before(async () => {
  server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://x');
    if (url.pathname === '/v1/battery/solax') {
      res.writeHead(200, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' });
      res.end(JSON.stringify(Object.assign({}, PLAN, { echo: Object.fromEntries(url.searchParams) })));
      return;
    }
    res.writeHead(200, { 'Content-Type': 'text/html' });
    res.end(MOCK);
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${server.address().port}`;
  browser = await chromium.launch();
});

after(async () => {
  if (browser) await browser.close();
  if (server) server.close();
});

// installPanel injects the chrome stub and the content scripts on every load.
async function openPage(query, prefs) {
  const page = await browser.newPage();
  page.on('dialog', (d) => d.dismiss());
  await page.addInitScript(
    ({ plan, scripts, prefs }) => {
      const KEY = '__ext_store';
      const read = () => JSON.parse(sessionStorage.getItem(KEY) || '{}');
      if (!sessionStorage.getItem(KEY)) sessionStorage.setItem(KEY, JSON.stringify({ panelPrefs: prefs }));
      window.chrome = {
        storage: {
          local: {
            get: async (keys) => {
              const d = read();
              const out = {};
              for (const k of [].concat(keys)) if (k in d) out[k] = d[k];
              return out;
            },
            set: async (obj) => sessionStorage.setItem(KEY, JSON.stringify(Object.assign(read(), obj))),
          },
        },
        runtime: {
          // like background.js: the fetched plan is also stored
          sendMessage: async (m) => {
            if (m.type !== 'fetchPlan') return { ok: false, error: 'unknown' };
            await window.chrome.storage.local.set({ plan: { url: 'stub', data: plan } });
            return { ok: true, data: plan };
          },
        },
      };
      document.addEventListener('DOMContentLoaded', () => scripts.forEach((s) => (0, eval)(s)));
    },
    { plan: PLAN, scripts: SCRIPTS, prefs },
  );
  await page.goto(base + '/' + (query || ''));
  await page.getByRole('button', { name: 'Загрузить план' }).waitFor();
  return page;
}

const panelState = (page) =>
  page.evaluate(() => {
    const s = window.__gordpoolSolaxPanel.state;
    return { steps: s.macro.steps, filled: Array.from(s.filled), running: s.running, log: s.log };
  });

const scenes = (page) => page.evaluate(() => window.__scenes);

// recordSample does what a user does: create one scene by hand with the sample values.
async function recordSample(page) {
  await page.getByRole('button', { name: 'Загрузить план' }).click();
  await page.getByRole('button', { name: 'Макрос' }).click();
  await page.getByRole('button', { name: '● Записать' }).click();

  const w = PLAN.windows[0];
  const visibleOption = (text) => page.locator('.dropdown:visible li').getByText(text, { exact: true });
  await page.click('button.add');
  await page.fill('[placeholder="Please enter the title"]', `gordpool ${w.date} ${w.start_local}-${w.end_local} Charge`);
  await page.click('text=Add condition');
  await visibleOption('Date & Time').click();
  await page.fill('[placeholder="Select date"]', w.date);
  await page.press('[placeholder="Select date"]', 'Enter');
  await page.fill('[placeholder="Start time"]', w.start_local);
  await page.fill('[placeholder="End time"]', w.end_local);
  await page.click('text=Add action');
  await visibleOption('Inverter & Battery').click();
  await page.click('[placeholder="Select mode"]');
  await visibleOption('Charge').click();
  await page.fill('[placeholder="Power (kW)"]', String(w.power_kw));
  await page.fill('[placeholder="Till SOC (%)"]', String(w.target_soc_percent));
  await page.click('.dialog >> text=Save');

  await page.getByRole('button', { name: '■ Остановить запись' }).click();
}

function expectedScene(w) {
  return {
    title: `gordpool ${w.date} ${w.start_local}-${w.end_local} ${w.action === 'charge' ? 'Charge' : 'Discharge'}`,
    date: w.date,
    start: w.start_local,
    end: w.end_local,
    mode: w.action === 'charge' ? 'Charge' : 'Discharge',
    power: String(w.power_kw),
    soc: String(w.target_soc_percent),
  };
}

async function waitIdle(page, timeout = 60000) {
  await page.waitForFunction(() => !window.__gordpoolSolaxPanel.state.running, null, { timeout });
}

test('records one scene and fills the rest of the plan', async () => {
  const page = await openPage('', { delay: 30, pauseBeforeLast: false });
  await recordSample(page);

  const { steps, filled } = await panelState(page);
  const templates = steps.filter((s) => s.template != null).map((s) => s.template);
  for (const t of ['{name}', '{date}', '{start}', '{end}', '{action}', '{power_kw}', '{target_soc}']) {
    assert.ok(templates.includes(t), `missing ${t} in ${JSON.stringify(templates)}`);
  }
  assert.ok(steps.some((s) => s.type === 'key' && s.key === 'Enter'));
  assert.deepEqual(filled, ['2025-12-08 03:00-04:00 charge']);

  await page.getByRole('button', { name: 'Заполнение' }).click();
  await page.getByRole('button', { name: `Заполнить выбранные (${PLAN.windows.length - 1})` }).click();
  await waitIdle(page);

  const got = await scenes(page);
  assert.deepEqual(got, PLAN.windows.map(expectedScene), JSON.stringify((await panelState(page)).log, null, 1));
  assert.equal((await panelState(page)).filled.length, PLAN.windows.length);
  // nothing left to fill
  await page.getByRole('button', { name: 'Заполнить выбранные (0)' }).waitFor();
  await page.close();
});

test('pauses before saving each scene until confirmed', async () => {
  const page = await openPage('', { delay: 30, pauseBeforeLast: true });
  await recordSample(page);
  await page.getByRole('button', { name: 'План' }).click();
  await page.getByRole('button', { name: 'Снять все' }).click();
  await page.locator('#gordpool-solax-host tr', { hasText: '06:00–07:00' }).locator('input[type=checkbox]').check();

  await page.getByRole('button', { name: 'Заполнение' }).click();
  await page.getByRole('button', { name: 'Заполнить выбранные (1)' }).click();
  await page.getByRole('button', { name: 'Продолжить' }).waitFor();
  // the dialog is filled but not saved yet
  assert.equal((await scenes(page)).length, 1);
  assert.equal(await page.inputValue('.dialog [placeholder="Start time"]'), '06:00');

  await page.getByRole('button', { name: 'Продолжить' }).click();
  await waitIdle(page);
  assert.deepEqual((await scenes(page))[1], expectedScene(PLAN.windows[2]));
  await page.close();
});

test('resumes after the page reloads on save', async () => {
  const page = await openPage('?reload=1', { delay: 30, pauseBeforeLast: false });
  await recordSample(page);
  // recording itself reloaded the page; the panel comes back with the macro
  await page.getByRole('button', { name: 'Заполнение' }).waitFor();
  const { steps } = await panelState(page);
  assert.ok(steps.length >= 10, `macro survived reload: ${steps.length} steps`);

  await page.getByRole('button', { name: 'План' }).click();
  await page.getByRole('button', { name: 'Снять все' }).click();
  for (const t of ['04:00–05:00', '07:00–08:00']) {
    await page.locator('#gordpool-solax-host tr', { hasText: t }).locator('input[type=checkbox]').check();
  }
  await page.getByRole('button', { name: 'Заполнение' }).click();
  await page.getByRole('button', { name: 'Заполнить выбранные (2)' }).click();

  await page.waitForFunction(() => window.__scenes.length === 3, null, { timeout: 30000 });
  await page.waitForFunction(() => window.__gordpoolSolaxPanel && window.__gordpoolSolaxPanel.state.filled.size === 3);
  await waitIdle(page);
  assert.deepEqual(await scenes(page), [PLAN.windows[0], PLAN.windows[1], PLAN.windows[3]].map(expectedScene));
  await page.close();
});

test('reports a step whose element is missing', async () => {
  const page = await openPage('', { delay: 30, pauseBeforeLast: false });
  const err = await page.evaluate(async () => {
    const M = window.GordpoolMacro;
    const steps = [{ type: 'click', target: { tag: 'li', css: 'li.nope', simple: 'li', index: 0, text: 'Nope' }, template: '{action}' }];
    try {
      await M.runSteps(steps, { action: 'Turbo' }, { timeout: 300, delay: 0 });
      return '';
    } catch (e) {
      return e.message;
    }
  });
  assert.equal(err, 'шаг 1: не найден элемент "Turbo"');
  await page.close();
});

test('setValue reaches framework state, a plain value write does not', async () => {
  const page = await openPage('', { delay: 30, pauseBeforeLast: false });
  await page.click('button.add');
  const res = await page.evaluate(() => {
    const title = document.querySelector('[placeholder="Please enter the title"]');
    title.value = 'plain'; // ignored by the mock's controlled state
    window.GordpoolMacro.setValue(title, 'via setter');
    return title.value;
  });
  assert.equal(res, 'via setter');
  await page.close();
});

test('unpacked extension loads: options page and background plan fetch', async () => {
  const userDir = fs.mkdtempSync(path.join(os.tmpdir(), 'gordpool-ext-'));
  const ctx = await chromium.launchPersistentContext(userDir, {
    channel: 'chromium',
    args: [`--disable-extensions-except=${ROOT}`, `--load-extension=${ROOT}`],
  });
  try {
    let [worker] = ctx.serviceWorkers();
    if (!worker) worker = await ctx.waitForEvent('serviceworker');
    const id = new URL(worker.url()).host;

    const options = await ctx.newPage();
    await options.goto(`chrome-extension://${id}/options.html`);
    assert.equal(await options.inputValue('[name=apiBase]'), 'http://localhost:8080');
    assert.equal(await options.inputValue('[name=timezone]'), 'Europe/Riga');

    const popup = await ctx.newPage();
    await popup.goto(`chrome-extension://${id}/popup.html`);
    await popup.getByRole('button', { name: 'Открыть панель на этой вкладке' }).waitFor();

    // fetchPlan through the real background worker against the local mock API
    const res = await options.evaluate(async (apiBase) => {
      await chrome.storage.sync.set({ settings: { apiBase, initial_soc_percent: '33' } });
      return chrome.runtime.sendMessage({ type: 'fetchPlan', overrides: { area: 'EE' } });
    }, base);
    assert.equal(res.ok, true, res.error);
    assert.equal(res.data.windows.length, PLAN.windows.length);
    assert.equal(res.data.echo.initial_soc_percent, '33');
    assert.equal(res.data.echo.area, 'EE');
    assert.equal(res.data.echo.timezone, 'Europe/Riga');
    const stored = await options.evaluate(() => chrome.storage.local.get('plan'));
    assert.equal(stored.plan.data.windows.length, PLAN.windows.length);
  } finally {
    await ctx.close();
  }
});
