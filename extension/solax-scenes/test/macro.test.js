const { test } = require('node:test');
const assert = require('node:assert/strict');
const M = require('../lib/macro.js');
const S = require('../settings.js');
const plan = require('./fixtures/plan.json');

const charge = plan.windows[0]; // 03:00-04:00 charge 5.2 kW → 59 %

test('windowValues formats every placeholder', () => {
  const v = M.windowValues(charge);
  assert.equal(v.name, 'gordpool 2025-12-08 03:00-04:00 Charge');
  assert.equal(v.date, '2025-12-08');
  assert.equal(v.date_dmy, '08/12/2025');
  assert.equal(v.date_dmy_dot, '08.12.2025');
  assert.equal(v.date_mdy, '12/08/2025');
  assert.equal(v.day, '8');
  assert.equal(v.start, '03:00');
  assert.equal(v.end, '04:00');
  assert.equal(v.start_hms, '03:00:00');
  assert.equal(v.action, 'Charge');
  assert.equal(v.power_kw, '5.2');
  assert.equal(v.per_inverter_kw, '2.6');
  assert.equal(v.power_w, '5200');
  assert.equal(v.per_inverter_w, '2600');
  assert.equal(v.target_soc, '59');
  assert.equal(v.duration_min, '60');
  // every declared placeholder has a value
  for (const p of M.PLACEHOLDERS) assert.ok(p.key in v, p.key);
});

test('windowValues uses custom action labels and handles 24:00', () => {
  const w = Object.assign({}, plan.windows[2], { end_local: '24:00' });
  const v = M.windowValues(w, { discharge: 'Battery discharge' });
  assert.equal(v.action, 'Battery discharge');
  assert.equal(v.end, '24:00');
  assert.equal(v.end_2359, '23:59');
  assert.equal(v.end_hms, '23:59:59');
  // discharge power is reported as a positive number
  assert.equal(v.power_w, '2800');
});

test('detectTemplate replaces sample values with placeholders', () => {
  const v = M.windowValues(charge);
  const cases = [
    ['03:00', '{start}'],
    ['04:00', '{end}'],
    ['2025-12-08', '{date}'],
    ['08/12/2025', '{date_dmy}'],
    ['Charge', '{action}'],
    ['5.2', '{power_kw}'],
    ['59', '{target_soc}'],
    [v.name, '{name}'],
    ['2025-12-08 03:00', '{date} {start}'],
    ['from 03:00 to 04:00', 'from {start} to {end}'],
    ['8', '{day}'], // short values only as a whole string
    ['Save', 'Save'],
    ['18:00', '18:00'],
    ['', ''],
  ];
  for (const [input, want] of cases) assert.equal(M.detectTemplate(input, v).template, want, input);
});

test('detectTemplate does not match inside longer tokens', () => {
  const v = M.windowValues(charge);
  assert.equal(M.detectTemplate('1595', v).template, '1595');
  assert.equal(M.detectTemplate('103:000', v).template, '103:000');
});

test('render round-trips through another window', () => {
  const sample = M.windowValues(charge);
  const other = M.windowValues(plan.windows[3]);
  for (const s of ['03:00', 'Charge', '5.2', '59', '2025-12-08 03:00', sample.name]) {
    const t = M.detectTemplate(s, sample).template;
    const out = M.render(t, other);
    assert.notEqual(out, s, `${s} should change for another window`);
    assert.equal(M.detectTemplate(out, other).template, t);
  }
  assert.equal(M.render('{unknown} {start}', other), '{unknown} 07:00');
});

test('retemplate maps input values and click texts', () => {
  const v = M.windowValues(charge);
  const steps = M.retemplate(
    [
      { type: 'click', target: { text: '+' } },
      { type: 'input', target: {}, value: '03:00' },
      { type: 'click', target: { text: 'Charge' } },
      { type: 'click', target: { text: '' } },
    ],
    v,
  );
  assert.deepEqual(
    steps.map((s) => s.template),
    ['+', '{start}', '{action}', undefined],
  );
});

test('windowKey is stable and unique per window', () => {
  const keys = plan.windows.map(M.windowKey);
  assert.equal(new Set(keys).size, keys.length);
  assert.equal(keys[0], '2025-12-08 03:00-04:00 charge');
});

test('planURL builds the /v1/battery/solax query', () => {
  const url = new URL(S.planURL({ apiBase: 'https://api.example.com', max_charge_kw: '', initial_soc_percent: ' 42 ' }));
  assert.equal(url.origin + url.pathname, 'https://api.example.com/v1/battery/solax');
  assert.equal(url.searchParams.get('initial_soc_percent'), '42');
  assert.equal(url.searchParams.get('area'), 'LV');
  assert.equal(url.searchParams.get('timezone'), 'Europe/Riga');
  assert.equal(url.searchParams.has('max_charge_kw'), false);
  assert.equal(url.searchParams.has('apiBase'), false);
  assert.equal(S.originPattern('http://192.168.1.5:8080/x'), 'http://192.168.1.5/*');
});
