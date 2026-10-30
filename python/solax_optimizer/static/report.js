'use strict';
const $ = id => document.getElementById(id);
const zone = 'Europe/Riga';
const date = value => new Intl.DateTimeFormat('ru-RU', {timeZone: zone, day:'2-digit', month:'2-digit', hour:'2-digit', minute:'2-digit',timeZoneName:'shortOffset'}).format(new Date(value));
const number = value => new Intl.NumberFormat('ru-RU', {maximumFractionDigits:2}).format(value);
const actions = {CHARGE:'Заряд', DISCHARGE:'Использование дома', HOLD:'Ожидание'};
let demo = false, snapshot = null, lastReport = null;

function element(tag, text, className) {
  const node = document.createElement(tag);
  if (text !== undefined) node.textContent = text;
  if (className) node.className = className;
  return node;
}
async function request(url, body) {
  const response = await fetch(url, body === undefined ? {} : {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
  const data = await response.json();
  if (!response.ok) throw new Error(typeof data.detail === 'string' ? data.detail : 'Проверьте параметры батареи и доступность цен.');
  return data;
}
function battery() {
  return Object.fromEntries([...new FormData($('settings'))].map(([key,value]) => [key, value === '' ? null : Number(value)]));
}
async function busy(work) {
  document.querySelectorAll('button').forEach(b => b.disabled = true);
  $('error').hidden = true;
  $('status').textContent = 'Выполняется расчёт…';
  try { await work(); }
  catch (error) {
    $('error').textContent = 'Не удалось обновить отчёт: ' + error.message;
    $('error').hidden = false;
    $('status').textContent = lastReport ? 'Показан предыдущий отчёт; новые параметры ещё не применены.' : 'Отчёт не построен. Можно открыть демо без сети.';
  } finally { document.querySelectorAll('button').forEach(b => b.disabled = false); }
}
async function calculate() {
  const settings = battery();
  const body = {battery:settings};
  if (demo) { body.prices = snapshot.prices; body.now = snapshot.prices[0].start; }
  const endpoint = $('planner').value === 'periods' ? '/v1/plan/periods' : '/v1/plan';
  const plan = await request(endpoint, body);
  const report = {mode:demo ? 'demo' : 'nordpool', snapshot, battery:settings, plan};
  render(report);
  lastReport = report;
  $('status').textContent = 'Отчёт построен. Расчёт не управляет оборудованием.';
}
function chart(prices, slots) {
  const svg = $('chart'); svg.replaceChildren();
  const start = Date.parse(prices[0].start), end = Date.parse(prices.at(-1).end);
  const low = Math.min(0, ...prices.map(p => p.eur_mwh)), high = Math.max(low + 1, ...prices.map(p => p.eur_mwh));
  const x = value => 58 + (Date.parse(value)-start)/(end-start)*920;
  const y = value => 230 - (value-low)/(high-low)*205;
  function draw(tag, attrs, text) {
    const node = document.createElementNS('http://www.w3.org/2000/svg', tag);
    Object.entries(attrs).forEach(([key,value]) => node.setAttribute(key,value));
    if (text !== undefined) node.textContent = text;
    svg.append(node); return node;
  }
  const fill = {CHARGE:'#bce3d4', DISCHARGE:'#f8dcc1', HOLD:'#edf0f2'};
  slots.forEach(s => draw('rect', {x:x(s.start), y:20, width:x(s.end)-x(s.start), height:210, fill:fill[s.action]}));
  [low, (low+high)/2, high].forEach(v => {
    draw('line', {x1:58,x2:978,y1:y(v),y2:y(v),stroke:'#d7dfd5'});
    draw('text', {x:48,y:y(v)+4,'text-anchor':'end',fill:'#657366','font-size':12},number(v));
  });
  const points = prices.flatMap(p => [x(p.start)+','+y(p.eur_mwh), x(p.end)+','+y(p.eur_mwh)]).join(' ');
  draw('polyline',{points,fill:'none',stroke:'#244f3d','stroke-width':2});
  [0,.25,.5,.75,1].forEach(f => {
    const stamp = new Date(start + f*(end-start)).toISOString();
    draw('text',{x:x(stamp),y:257,'text-anchor':f === 1 ? 'end' : 'start',fill:'#657366','font-size':12},date(stamp));
  });
}
function render(report) {
  const {plan,snapshot,mode} = report, summary = plan.summary;
  const limits = plan.planning_constraints;
  $('constraints').textContent = limits ? 'До '+limits.horizon_hours+' ч по опубликованным ценам · не более '+limits.max_charge_windows+' окон заряда и '+limits.max_discharge_windows+' окон использования · заряд из сети ≤ '+number(limits.max_grid_charge_kw)+' кВт с учётом потерь. Периоды заданы абсолютным временем, не ежедневным расписанием оборудования.' : 'Весь опубликованный горизонт без ограничения количества окон. Только ценовые возможности.';
  $('source').textContent = mode === 'demo' ? 'ДЕМО · синтетические цены' : 'Nord Pool LV · загрузка ' + date(snapshot.fetched_at);
  $('horizon').textContent = date(snapshot.prices[0].start) + ' — ' + date(snapshot.prices.at(-1).end) + ' · ' + snapshot.prices.length + ' ценовых интервалов · ' + plan.slots.length + ' интервалов плана · заряд ' + plan.initial_soc_grid_percent + '% → ' + plan.final_soc_percent + '%';
  $('warnings').textContent = (snapshot.warnings || []).join(' · ');
  $('warnings').hidden = !snapshot.warnings?.length;
  $('metrics').replaceChildren();
  [ ['Денежный эффект',summary.potential_cash_benefit_eur,'€','Условный, с учётом износа'],
    ['Оценка остатка',summary.terminal_value_change_eur,'€','Не денежный доход'],
    ['Итог модели',summary.objective_benefit_eur,'€','Эффект + оценка остатка'],
    ['Заряд из сети',summary.charge_from_grid_kwh,'кВт·ч','Использование дома: '+number(summary.potential_use_kwh)+' кВт·ч']
  ].forEach(([title,value,unit,note]) => {
    const card = element('div',undefined,'metric'); card.append(element('span',title),element('strong',number(value)+' '+unit,value<0?'negative':''),element('span',note)); $('metrics').append(card);
  });
  chart(snapshot.prices, plan.slots);
  $('windows').replaceChildren();
  plan.windows.forEach(w => {
    const slots = plan.slots.filter(s => s.start >= w.start && s.end <= w.end);
    const duration = slots.reduce((n,s) => n + Date.parse(s.end)-Date.parse(s.start),0);
    const average = slots.reduce((n,s) => n + s.price_eur_mwh*(Date.parse(s.end)-Date.parse(s.start)),0)/duration;
    const detail = element('details',undefined,'window');
    detail.append(element('summary', actions[w.action]+' · '+date(w.start)+' — '+date(w.end)+' · '+w.soc_start_percent+'% → '+w.soc_end_percent+'%', w.action.toLowerCase()));
    detail.append(element('p',w.reason));
    detail.append(element('p','Средняя цена: '+number(average)+' €/MWh · из сети: '+number(w.charge_from_grid_kwh)+' кВт·ч · использование дома: '+number(w.potential_use_kwh)+' кВт·ч. Мощность может различаться внутри периода — см. интервалы ниже.'));
    const wrap = element('div',undefined,'table-wrap'), table = element('table'), head = element('thead'), row = element('tr');
    ['Начало','Конец','€/MWh','Мощность батареи, кВт','Заряд, %'].forEach(t => row.append(element('th',t))); head.append(row); table.append(head);
    const tbody = element('tbody'); slots.forEach(s => { const tr = element('tr'); [date(s.start),date(s.end),number(s.price_eur_mwh),number(s.power_battery_kw),s.soc_start_percent+' → '+s.soc_end_percent].forEach(t => tr.append(element('td',t))); tbody.append(tr); });
    table.append(tbody); wrap.append(table); detail.append(wrap); $('windows').append(detail);
  });
  $('report').hidden = false;
}
$('settings').addEventListener('submit', event => { event.preventDefault(); busy(calculate); });
$('refresh').addEventListener('click', () => busy(async () => {
  const fresh = await request('/v1/prices/refresh', {});
  snapshot = fresh; demo = false; await calculate();
}));
$('demo').addEventListener('click', () => busy(async () => {
  const prices = [100,70,40,10,50,80,150,250].map((price,i) => ({start:new Date(Date.UTC(2026,9,28,10,i*15)).toISOString(), end:new Date(Date.UTC(2026,9,28,10,(i+1)*15)).toISOString(), eur_mwh:price}));
  snapshot = {prices,warnings:[]}; demo = true; await calculate();
}));
$('download').addEventListener('click', () => {
  if (!lastReport) return;
  const url = URL.createObjectURL(new Blob([JSON.stringify(lastReport,null,2)],{type:'application/json'}));
  const link = element('a'); link.href=url; link.download='gordpool-report.json'; link.click(); setTimeout(() => URL.revokeObjectURL(url),1000);
});
busy(async () => { snapshot = await request('/v1/prices'); demo = false; await calculate(); });
