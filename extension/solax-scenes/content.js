// Panel injected into the Smart Scene page: loads the plan, records a macro
// while the user creates one scene by hand, and replays it for every window.
(function () {
  'use strict';
  if (window.__gordpoolSolaxPanel) {
    window.__gordpoolSolaxPanel.show();
    return;
  }

  const M = window.GordpoolMacro;
  const ORIGIN = location.origin;
  const KEY_MACRO = `macro:${ORIGIN}`;
  const KEY_FILLED = `filled:${ORIGIN}`;
  const KEY_RECORDING = `recording:${ORIGIN}`;

  const state = {
    plan: null, // SolaxDataHubPlan
    selected: new Set(), // window keys
    filled: new Set(),
    macro: { steps: [], actionLabels: Object.assign({}, M.DEFAULT_ACTION_LABELS), sampleKey: '' },
    prefs: { delay: 400, pauseBeforeLast: true, soc: '' },
    recorder: null,
    running: false,
    stopRequested: false,
    resume: null, // resolves a pause
    log: [],
  };

  // ---------- storage ----------

  const store = chrome.storage.local;
  const save = (obj) => store.set(obj).catch((e) => addLog('Ошибка сохранения: ' + e.message));
  const saveMacro = () => save({ [KEY_MACRO]: state.macro });
  const saveFilled = () => save({ [KEY_FILLED]: Array.from(state.filled) });
  const savePrefs = () => save({ panelPrefs: state.prefs });
  const saveRun = (run) => save({ run });

  // ---------- DOM helpers (no innerHTML with page data) ----------

  function h(tag, props, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
      if (k === 'class') el.className = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (k === 'checked' || k === 'disabled' || k === 'value') el[k] = v;
      else el.setAttribute(k, v);
    }
    for (const c of children.flat()) if (c != null && c !== false) el.append(c instanceof Node ? c : String(c));
    return el;
  }

  const host = document.createElement('div');
  host.id = 'gordpool-solax-host';
  host.style.cssText = 'all: initial; position: fixed; right: 16px; bottom: 16px; z-index: 2147483647;';
  const shadow = host.attachShadow({ mode: 'open' });
  shadow.append(
    h(
      'style',
      {},
      `
      .panel { font: 13px/1.4 system-ui, sans-serif; color: #1d2433; width: 420px; max-height: 80vh; display: flex; flex-direction: column; background: #fff;
               border: 1px solid #c4cad6; border-radius: 10px; box-shadow: 0 8px 28px rgba(0,0,0,.18); overflow: hidden; }
      .head { display: flex; align-items: center; gap: 8px; padding: 8px 12px; background: #1f6feb; color: #fff; }
      .head b { flex: 1; }
      .head button { background: transparent; border: 0; color: #fff; cursor: pointer; font-size: 16px; }
      .tabs { display: flex; border-bottom: 1px solid #e3e7ee; }
      .tabs button { flex: 1; padding: 8px; border: 0; background: #f6f8fa; cursor: pointer; font: inherit; }
      .tabs button.on { background: #fff; font-weight: 600; box-shadow: inset 0 -2px 0 #1f6feb; }
      .body { padding: 10px 12px; overflow: auto; }
      .row { display: flex; gap: 6px; align-items: center; margin: 6px 0; flex-wrap: wrap; }
      button.b { padding: 6px 10px; border: 1px solid #c4cad6; border-radius: 6px; background: #fff; cursor: pointer; font: inherit; }
      button.p { background: #1f6feb; border-color: #1f6feb; color: #fff; }
      button.d { color: #b42318; }
      button:disabled { opacity: .5; cursor: default; }
      button, input, textarea { font: inherit; color: inherit; }
      input[type=text], input[type=number], textarea { padding: 4px 6px; border: 1px solid #c4cad6; border-radius: 5px; }
      table { width: 100%; border-collapse: collapse; }
      td { padding: 3px 4px; border-bottom: 1px solid #eef1f5; vertical-align: middle; }
      .charge { color: #067647; } .discharge { color: #b54708; }
      .muted { color: #5b6475; } .err { color: #b42318; } .ok { color: #067647; }
      .sample { background: #f6f8fa; border-radius: 6px; padding: 6px 8px; }
      .sample code { background: #fff; border: 1px solid #e3e7ee; border-radius: 4px; padding: 0 4px; user-select: all; }
      .step td:nth-child(3) { max-width: 120px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
      .step input { width: 120px; }
      .log { font: 12px ui-monospace, monospace; background: #0d1117; color: #c9d1d9; border-radius: 6px; padding: 6px 8px;
             max-height: 140px; overflow: auto; white-space: pre-wrap; }
      .banner { background: #fff8e6; border: 1px solid #f5c26b; border-radius: 6px; padding: 6px 8px; margin: 6px 0; }
      .rec { color: #b42318; font-weight: 600; }
      details summary { cursor: pointer; color: #5b6475; }
    `,
    ),
  );
  const root = h('div', { class: 'panel' });
  shadow.append(root);
  document.documentElement.append(host);

  let tab = 'plan';
  let collapsed = false;

  function addLog(line) {
    const t = new Date().toLocaleTimeString();
    state.log.push(`${t} ${line}`);
    if (state.log.length > 200) state.log.shift();
    render();
  }

  // ---------- plan ----------

  function windows() {
    return (state.plan && state.plan.windows) || [];
  }

  function sampleWindow() {
    const ws = windows();
    return ws.find((w) => M.windowKey(w) === state.macro.sampleKey) || ws.find((w) => state.selected.has(M.windowKey(w))) || ws[0];
  }

  function setPlan(plan, origin) {
    if (Array.isArray(plan)) plan = { format: 'solax-datahub1000/v1', windows: plan };
    if (!plan || !Array.isArray(plan.windows)) throw new Error('в JSON нет массива windows');
    state.plan = plan;
    state.selected = new Set(plan.windows.map(M.windowKey).filter((k) => !state.filled.has(k)));
    addLog(`План (${origin}): окон ${plan.windows.length}, новых ${state.selected.size}`);
  }

  async function loadPlan() {
    const overrides = {};
    if (String(state.prefs.soc).trim() !== '') overrides.initial_soc_percent = String(state.prefs.soc).trim();
    addLog('Загружаю план из gordpool API…');
    const res = await chrome.runtime.sendMessage({ type: 'fetchPlan', overrides });
    if (!res || !res.ok) {
      addLog('Ошибка загрузки плана: ' + ((res && res.error) || 'нет ответа'));
      return;
    }
    setPlan(res.data, 'API');
  }

  function pastePlan() {
    const text = prompt('Вставьте JSON из /v1/battery/solax');
    if (!text) return;
    try {
      setPlan(JSON.parse(text), 'вставка');
      save({ plan: { fetchedAt: new Date().toISOString(), url: 'paste', data: state.plan } });
    } catch (e) {
      addLog('Не удалось разобрать JSON: ' + e.message);
    }
  }

  // ---------- macro ----------

  function sampleValues() {
    const w = sampleWindow();
    return w ? M.windowValues(w, state.macro.actionLabels) : {};
  }

  // Recompute templates of steps the user did not edit by hand.
  function refreshTemplates() {
    const values = sampleValues();
    const auto = M.retemplate(state.macro.steps, values);
    state.macro.steps = state.macro.steps.map((s, i) => (s.manual ? s : auto[i]));
  }

  function startRecording(resumed) {
    const w = sampleWindow();
    if (!w) {
      addLog('Сначала загрузите план: значения образца берутся из него');
      return;
    }
    state.macro.sampleKey = M.windowKey(w);
    // survives page reloads until the user presses stop
    save({ [KEY_RECORDING]: true });
    state.recorder = new M.Recorder(
      document,
      (el) => el === host || host.contains(el),
      (steps) => {
        state.macro.steps = steps;
        refreshTemplates();
        saveMacro();
        render();
      },
    );
    state.recorder.start(state.macro.steps);
    addLog(
      resumed
        ? 'Страница перезагрузилась — запись продолжается'
        : 'Запись началась. Создайте одну сцену вручную, вводя значения образца, и сохраните её.',
    );
  }

  function stopRecording() {
    if (!state.recorder) return;
    state.macro.steps = state.recorder.stop();
    state.recorder = null;
    save({ [KEY_RECORDING]: false });
    refreshTemplates();
    saveMacro();
    // the scene created while recording is the sample window: mark it done
    if (state.macro.sampleKey) {
      state.filled.add(state.macro.sampleKey);
      state.selected.delete(state.macro.sampleKey);
      saveFilled();
    }
    addLog(`Запись остановлена: шагов ${state.macro.steps.length}`);
  }

  // ---------- run ----------

  function pause(text) {
    return new Promise((resolve) => {
      state.resume = { text, resolve };
      render();
    });
  }

  async function runWindows(keys, startWindow, startStep) {
    const steps = state.macro.steps;
    if (!steps.length) {
      addLog('Макрос пуст — сначала запишите создание одной сцены');
      return;
    }
    const byKey = new Map(windows().map((w) => [M.windowKey(w), w]));
    state.running = true;
    state.stopRequested = false;
    render();
    try {
      for (let wi = startWindow || 0; wi < keys.length; wi++) {
        const w = byKey.get(keys[wi]);
        if (!w) continue;
        const values = M.windowValues(w, state.macro.actionLabels);
        addLog(`Сцена ${wi + 1}/${keys.length}: ${values.name}`);
        const res = await M.runSteps(steps, values, {
          delay: Number(state.prefs.delay) || 400,
          startAt: wi === startWindow ? startStep || 0 : 0,
          shouldStop: () => state.stopRequested,
          beforeStep: async (i) => {
            // persisted first, so a navigating click resumes on the next step
            await saveRun({ active: true, origin: ORIGIN, keys, windowIdx: wi, stepIdx: i + 1 });
            if (state.prefs.pauseBeforeLast && i === steps.length - 1) {
              await pause(`Проверьте сцену «${values.name}» и нажмите «Продолжить», чтобы сохранить`);
            }
          },
        });
        if (res.stopped) {
          addLog('Остановлено');
          break;
        }
        state.filled.add(keys[wi]);
        state.selected.delete(keys[wi]);
        await saveFilled();
        addLog('Готово: ' + values.name);
      }
    } catch (e) {
      addLog('Ошибка: ' + e.message + '. Исправьте шаг на вкладке «Макрос» или повторите.');
    } finally {
      state.running = false;
      state.resume = null;
      await saveRun(null);
      render();
    }
  }

  function startRun() {
    const keys = windows().map(M.windowKey).filter((k) => state.selected.has(k));
    if (!keys.length) {
      addLog('Не выбрано ни одного окна');
      return;
    }
    runWindows(keys, 0, 0);
  }

  function stopRun() {
    state.stopRequested = true;
    if (state.resume) {
      const r = state.resume;
      state.resume = null;
      r.resolve();
    }
  }

  // ---------- rendering ----------

  function fmtWindow(w) {
    return `${w.date} ${w.start_local}–${w.end_local}`;
  }

  function renderPlan() {
    const ws = windows();
    const table = h(
      'table',
      {},
      ws.map((w) => {
        const key = M.windowKey(w);
        return h(
          'tr',
          {},
          h(
            'td',
            {},
            h('input', {
              type: 'checkbox',
              checked: state.selected.has(key),
              onchange: (e) => {
                if (e.target.checked) state.selected.add(key);
                else state.selected.delete(key);
              },
            }),
          ),
          h('td', {}, fmtWindow(w)),
          h('td', { class: w.action }, w.action === 'charge' ? 'заряд' : 'разряд'),
          h('td', {}, `${w.power_kw} кВт`),
          h('td', {}, `→ ${w.target_soc_percent}%`),
          h('td', { class: 'ok', title: 'уже заполнено' }, state.filled.has(key) ? '✓' : ''),
        );
      }),
    );
    return [
      h(
        'div',
        { class: 'row' },
        'SOC сейчас, %',
        h('input', {
          type: 'number',
          min: '0',
          max: '100',
          style: 'width:90px',
          value: state.prefs.soc,
          placeholder: 'из настроек',
          onchange: (e) => {
            state.prefs.soc = e.target.value;
            savePrefs();
          },
        }),
        h('button', { class: 'b p', onclick: loadPlan }, 'Загрузить план'),
        h('button', { class: 'b', onclick: pastePlan }, 'Вставить JSON'),
      ),
      state.plan
        ? h(
            'div',
            { class: 'muted' },
            `Пояс ${state.plan.timezone || '?'}, инверторов ${state.plan.inverters || 1}` +
              (state.plan.summary ? `, выгода ≈ ${(state.plan.summary.net_benefit_cents / 100).toFixed(2)} €` : ''),
          )
        : h('div', { class: 'muted' }, 'План ещё не загружен'),
      ws.length ? table : null,
      ws.length
        ? h(
            'div',
            { class: 'row' },
            h('button', { class: 'b', onclick: () => ((state.selected = new Set(ws.map(M.windowKey))), render()) }, 'Выбрать все'),
            h('button', { class: 'b', onclick: () => ((state.selected = new Set()), render()) }, 'Снять все'),
            h(
              'button',
              {
                class: 'b',
                onclick: () => {
                  state.filled = new Set();
                  saveFilled();
                  render();
                },
              },
              'Забыть отметки ✓',
            ),
          )
        : null,
    ];
  }

  function stepTarget(s) {
    if (s.type === 'wait') return `${s.ms} мс`;
    const t = s.target;
    return t.text || t.placeholder || t.label || t.css;
  }

  function renderMacro() {
    const values = sampleValues();
    const w = sampleWindow();
    const recording = !!state.recorder;
    const typeName = { click: 'клик', input: 'ввод', key: 'клавиша', wait: 'пауза' };
    const labels = state.macro.actionLabels;

    const sample = w
      ? h(
          'div',
          { class: 'sample' },
          h('div', {}, 'Создайте сцену для окна ', h('b', {}, fmtWindow(w)), ' и вводите ровно эти значения:'),
          ...[
            ['Название', values.name],
            ['Дата', values.date],
            ['Начало', values.start],
            ['Конец', values.end],
            ['Действие', values.action],
            ['Мощность, кВт', values.power_kw],
            ['На инвертор, кВт', values.per_inverter_kw],
            ['SOC до, %', values.target_soc],
          ].map(([k, v]) => h('div', {}, `${k}: `, h('code', {}, v))),
          h('div', { class: 'muted' }, 'Если поле в SolaX требует другой формат (например 23:59 вместо 24:00) — поправьте шаблон шага ниже.'),
        )
      : h('div', { class: 'muted' }, 'Загрузите план, чтобы получить значения образца.');

    const steps = h(
      'table',
      {},
      state.macro.steps.map((s, i) =>
        h(
          'tr',
          { class: 'step' },
          h('td', { class: 'muted' }, String(i + 1)),
          h('td', {}, s.type === 'key' ? `${typeName.key} ${s.key}` : typeName[s.type]),
          h('td', { title: s.type === 'wait' ? '' : s.target.css }, stepTarget(s)),
          h(
            'td',
            {},
            s.type === 'input' || (s.type === 'click' && s.target.text)
              ? h('input', {
                  type: 'text',
                  value: s.template != null ? s.template : s.value || s.target.text,
                  title: 'Шаблон: {start}, {end}, {date}, {action}, {power_kw}, {target_soc}…',
                  onchange: (e) => {
                    s.template = e.target.value;
                    s.manual = true;
                    saveMacro();
                  },
                })
              : s.type === 'wait'
                ? h('input', {
                    type: 'number',
                    value: s.ms,
                    style: 'width:70px',
                    onchange: (e) => {
                      s.ms = Number(e.target.value) || 500;
                      saveMacro();
                    },
                  })
                : null,
          ),
          h(
            'td',
            {},
            h(
              'button',
              {
                class: 'b d',
                title: 'удалить шаг',
                disabled: recording,
                onclick: () => {
                  state.macro.steps.splice(i, 1);
                  saveMacro();
                  render();
                },
              },
              '✕',
            ),
          ),
        ),
      ),
    );

    return [
      h(
        'div',
        { class: 'row' },
        'Текст действия в SolaX: заряд',
        h('input', {
          type: 'text',
          style: 'width:90px',
          value: labels.charge,
          onchange: (e) => {
            labels.charge = e.target.value;
            refreshTemplates();
            saveMacro();
            render();
          },
        }),
        'разряд',
        h('input', {
          type: 'text',
          style: 'width:90px',
          value: labels.discharge,
          onchange: (e) => {
            labels.discharge = e.target.value;
            refreshTemplates();
            saveMacro();
            render();
          },
        }),
      ),
      sample,
      h(
        'div',
        { class: 'row' },
        recording
          ? h('button', { class: 'b p', onclick: () => (stopRecording(), render()) }, '■ Остановить запись')
          : h('button', { class: 'b p', disabled: state.running, onclick: () => (startRecording(false), render()) }, '● Записать'),
        recording ? h('span', { class: 'rec' }, 'идёт запись…') : null,
        h(
          'button',
          {
            class: 'b',
            disabled: recording,
            onclick: () => {
              state.macro.steps.push({ type: 'wait', ms: 1000 });
              saveMacro();
              render();
            },
          },
          '+ пауза',
        ),
        h(
          'button',
          {
            class: 'b d',
            disabled: recording || !state.macro.steps.length,
            onclick: () => {
              if (!confirm('Удалить записанный макрос?')) return;
              state.macro.steps = [];
              saveMacro();
              render();
            },
          },
          'Очистить',
        ),
      ),
      state.macro.steps.length ? steps : h('div', { class: 'muted' }, 'Шагов пока нет'),
      h(
        'details',
        {},
        h('summary', {}, 'Подстановки'),
        M.PLACEHOLDERS.map((p) => h('div', {}, h('code', {}, `{${p.key}}`), ` — ${p.label}`)),
      ),
    ];
  }

  function renderRun() {
    const n = windows().filter((w) => state.selected.has(M.windowKey(w))).length;
    return [
      h(
        'div',
        { class: 'row' },
        'Пауза между шагами, мс',
        h('input', {
          type: 'number',
          style: 'width:70px',
          value: state.prefs.delay,
          onchange: (e) => {
            state.prefs.delay = Number(e.target.value) || 400;
            savePrefs();
          },
        }),
      ),
      h(
        'label',
        { class: 'row' },
        h('input', {
          type: 'checkbox',
          checked: state.prefs.pauseBeforeLast,
          onchange: (e) => {
            state.prefs.pauseBeforeLast = e.target.checked;
            savePrefs();
          },
        }),
        'Останавливаться перед последним шагом (сохранением) каждой сцены',
      ),
      state.resume
        ? h(
            'div',
            { class: 'banner' },
            state.resume.text,
            h(
              'div',
              { class: 'row' },
              h(
                'button',
                {
                  class: 'b p',
                  onclick: () => {
                    const r = state.resume;
                    state.resume = null;
                    r.resolve();
                    render();
                  },
                },
                'Продолжить',
              ),
            ),
          )
        : null,
      h(
        'div',
        { class: 'row' },
        h(
          'button',
          { class: 'b p', disabled: state.running || !!state.recorder || !n || !state.macro.steps.length, onclick: startRun },
          `Заполнить выбранные (${n})`,
        ),
        h('button', { class: 'b d', disabled: !state.running, onclick: stopRun }, 'Стоп'),
      ),
      !state.macro.steps.length ? h('div', { class: 'muted' }, 'Сначала запишите макрос на вкладке «Макрос».') : null,
    ];
  }

  function render() {
    root.replaceChildren();
    root.append(
      h(
        'div',
        { class: 'head' },
        h('b', {}, 'gordpool → Smart Scene'),
        h('button', { title: 'свернуть', onclick: () => ((collapsed = !collapsed), render()) }, collapsed ? '▢' : '—'),
        h('button', { title: 'закрыть', onclick: () => (host.style.display = 'none') }, '✕'),
      ),
    );
    if (collapsed) return;
    root.append(
      h(
        'div',
        { class: 'tabs' },
        [
          ['plan', 'План'],
          ['macro', 'Макрос'],
          ['run', 'Заполнение'],
        ].map(([id, label]) => h('button', { class: tab === id ? 'on' : '', onclick: () => ((tab = id), render()) }, label)),
      ),
    );
    const body = h('div', { class: 'body' }, tab === 'plan' ? renderPlan() : tab === 'macro' ? renderMacro() : renderRun());
    if (state.log.length) {
      const log = h('div', { class: 'log' }, state.log.slice(-30).join('\n'));
      body.append(log);
      queueMicrotask(() => (log.scrollTop = log.scrollHeight));
    }
    root.append(body);
  }

  // ---------- init ----------

  window.__gordpoolSolaxPanel = {
    show: () => {
      host.style.display = '';
      render();
    },
    state,
  };

  store.get(['plan', KEY_MACRO, KEY_FILLED, KEY_RECORDING, 'panelPrefs', 'run']).then((got) => {
    state.filled = new Set(got[KEY_FILLED] || []);
    if (got[KEY_MACRO]) state.macro = Object.assign(state.macro, got[KEY_MACRO]);
    if (got.panelPrefs) state.prefs = Object.assign(state.prefs, got.panelPrefs);
    if (got.plan && got.plan.data) {
      try {
        setPlan(got.plan.data, 'сохранённый');
      } catch (e) {
        /* ignore stale plan */
      }
    }
    if (got[KEY_RECORDING]) {
      tab = 'macro';
      startRecording(true);
    }
    render();
    const run = got.run;
    if (run && run.active && run.origin === ORIGIN) {
      tab = 'run';
      addLog('Страница перезагрузилась во время заполнения — продолжаю');
      setTimeout(() => runWindows(run.keys, run.windowIdx, run.stepIdx), 1000);
    }
  });
  render();
})();
