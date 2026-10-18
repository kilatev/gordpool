// Macro engine for filling SolaX Smart Scenes from a gordpool plan.
//
// The Smart Scene editor is a third-party SPA whose markup we do not control,
// so instead of hard-coded selectors the user records one scene by hand. Each
// recorded step keeps a locator for the element plus a template where values
// from the sample plan window were replaced by placeholders like {start}.
// Replaying renders the templates for every window of the plan.
//
// Works as a classic script in the page (window.GordpoolMacro) and as a
// CommonJS module in Node tests.
(function (root) {
  'use strict';

  // ---------- plan values ----------

  const PLACEHOLDERS = [
    { key: 'name', label: 'Название сцены' },
    { key: 'date', label: 'Дата YYYY-MM-DD' },
    { key: 'date_dmy', label: 'Дата DD/MM/YYYY' },
    { key: 'date_dmy_dot', label: 'Дата DD.MM.YYYY' },
    { key: 'date_mdy', label: 'Дата MM/DD/YYYY' },
    { key: 'day', label: 'День месяца', short: true },
    { key: 'start', label: 'Начало HH:MM' },
    { key: 'end', label: 'Конец HH:MM (24:00 в конце дня)' },
    { key: 'end_2359', label: 'Конец HH:MM (23:59 вместо 24:00)' },
    { key: 'start_hms', label: 'Начало HH:MM:SS' },
    { key: 'end_hms', label: 'Конец HH:MM:SS' },
    { key: 'action', label: 'Действие (текст из настроек)' },
    { key: 'power_kw', label: 'Мощность объекта, кВт' },
    { key: 'per_inverter_kw', label: 'Мощность на инвертор, кВт' },
    { key: 'power_w', label: 'Мощность объекта, Вт' },
    { key: 'per_inverter_w', label: 'Мощность на инвертор, Вт' },
    { key: 'target_soc', label: 'Целевой SOC, %' },
    { key: 'start_soc', label: 'SOC в начале, %' },
    { key: 'energy_kwh', label: 'Энергия, кВт·ч' },
    { key: 'duration_min', label: 'Длительность, мин' },
  ];

  const DEFAULT_ACTION_LABELS = { charge: 'Charge', discharge: 'Discharge' };

  function num(x) {
    // 8 → "8", 8.25 → "8.25", no float noise
    return String(Math.round(x * 1000) / 1000);
  }

  function sceneName(w) {
    const verb = w.action === 'discharge' ? 'Discharge' : 'Charge';
    return `gordpool ${w.date} ${w.start_local}-${w.end_local} ${verb}`;
  }

  function windowKey(w) {
    return `${w.date} ${w.start_local}-${w.end_local} ${w.action}`;
  }

  // windowValues returns the string for every placeholder of one plan window.
  function windowValues(w, actionLabels) {
    const labels = Object.assign({}, DEFAULT_ACTION_LABELS, actionLabels || {});
    const [y, m, d] = w.date.split('-');
    const end2359 = w.end_local === '24:00' ? '23:59' : w.end_local;
    const start = Date.parse(w.start);
    const end = Date.parse(w.end);
    return {
      name: sceneName(w),
      date: w.date,
      date_dmy: `${d}/${m}/${y}`,
      date_dmy_dot: `${d}.${m}.${y}`,
      date_mdy: `${m}/${d}/${y}`,
      day: String(Number(d)),
      start: w.start_local,
      end: w.end_local,
      end_2359: end2359,
      start_hms: `${w.start_local}:00`,
      end_hms: w.end_local === '24:00' ? '23:59:59' : `${w.end_local}:00`,
      action: labels[w.action] || w.action,
      power_kw: num(w.power_kw),
      per_inverter_kw: num(w.per_inverter_kw),
      power_w: String(Math.abs(w.modbus.active_power_w)),
      per_inverter_w: String(Math.abs(w.modbus.per_inverter_power_w)),
      target_soc: num(w.target_soc_percent),
      start_soc: num(w.start_soc_percent),
      energy_kwh: num(w.energy_kwh),
      duration_min: num((end - start) / 60000),
    };
  }

  // ---------- templates ----------

  function escapeRe(s) {
    return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  }

  // detectTemplate replaces values of the sample window found in a recorded
  // string with placeholders. Longer values win, so "2025-12-08" is not eaten
  // by "8". Values shorter than 2 chars only match the whole string, and only
  // for placeholders marked short, to avoid turning every "1" into {day}.
  function detectTemplate(recorded, values) {
    if (recorded == null || recorded === '') return { template: recorded || '', placeholders: [] };
    const text = String(recorded);
    const shortKeys = new Set(PLACEHOLDERS.filter((p) => p.short).map((p) => p.key));
    const order = PLACEHOLDERS.map((p) => p.key);

    for (const key of order) {
      if (values[key] === text && (text.length >= 2 || shortKeys.has(key))) {
        return { template: `{${key}}`, placeholders: [key] };
      }
    }

    const candidates = order
      .filter((k) => values[k] && values[k].length >= 2)
      .sort((a, b) => values[b].length - values[a].length || order.indexOf(a) - order.indexOf(b));

    let template = text;
    const used = [];
    for (const key of candidates) {
      const v = values[key];
      // whole tokens only: "18:00" must not match inside "118:000"
      const re = new RegExp(`(^|[^0-9A-Za-z{])${escapeRe(v)}(?![0-9A-Za-z}])`, 'g');
      if (re.test(template)) {
        template = template.replace(re, (_, pre) => `${pre}{${key}}`);
        used.push(key);
      }
    }
    return { template, placeholders: used };
  }

  function render(template, values) {
    if (template == null) return template;
    return String(template).replace(/\{([a-z0-9_]+)\}/g, (all, key) =>
      Object.prototype.hasOwnProperty.call(values, key) ? values[key] : all,
    );
  }

  // retemplate applies detectTemplate to every value/text of recorded steps.
  function retemplate(steps, values) {
    return steps.map((s) => {
      const out = Object.assign({}, s);
      if (s.type === 'input') out.template = detectTemplate(s.value, values).template;
      if (s.type === 'click' && s.target && s.target.text) out.template = detectTemplate(s.target.text, values).template;
      return out;
    });
  }

  // ---------- DOM: describing elements ----------

  const CLICKABLE =
    'button,a,[role=button],[role=option],[role=menuitem],[role=tab],[role=checkbox],[role=radio],[role=switch],li,label,option,input,textarea,select';

  function normText(s) {
    return String(s || '').replace(/\s+/g, ' ').trim();
  }

  function ownText(el) {
    const t = normText(el.innerText !== undefined ? el.innerText : el.textContent);
    return t.length <= 80 ? t : '';
  }

  function stableClass(c) {
    return (
      c &&
      !/^(is-|has-|hover|focus|active|selected|checked|disabled|ng-|v-|css-|sc-|jsx-)/.test(c) &&
      !/\d{3,}/.test(c) &&
      !/[^A-Za-z0-9_-]/.test(c)
    );
  }

  function stableId(id) {
    return id && !/\d{3,}/.test(id) && !/^el-id-/.test(id) && /^[A-Za-z][\w-]*$/.test(id);
  }

  function simpleSelector(el) {
    const classes = Array.from(el.classList || []).filter(stableClass).slice(0, 3);
    return el.tagName.toLowerCase() + classes.map((c) => '.' + c).join('');
  }

  function cssPath(el, doc) {
    const parts = [];
    let cur = el;
    for (let depth = 0; cur && cur.nodeType === 1 && cur !== doc.body && depth < 8; depth++) {
      if (stableId(cur.id)) {
        parts.unshift('#' + cur.id);
        break;
      }
      let sel = simpleSelector(cur);
      const parent = cur.parentElement;
      if (parent) {
        const same = Array.from(parent.children).filter((c) => c.tagName === cur.tagName);
        if (same.length > 1) sel += `:nth-of-type(${same.indexOf(cur) + 1})`;
      }
      parts.unshift(sel);
      cur = cur.parentElement;
    }
    return parts.join(' > ');
  }

  function attrSelector(el, doc) {
    const tag = el.tagName.toLowerCase();
    for (const a of ['name', 'placeholder', 'aria-label', 'data-testid', 'title']) {
      const v = el.getAttribute(a);
      if (!v || /\d{3,}/.test(v)) continue;
      const sel = `${tag}[${a}="${v.replace(/"/g, '\\"')}"]`;
      if (visibleAll(doc.querySelectorAll(sel)).length === 1) return sel;
    }
    return '';
  }

  // describe picks the meaningful element for an event target and returns a
  // serialisable locator for it.
  function describe(target, doc) {
    doc = doc || target.ownerDocument;
    const el = (target.closest && target.closest(CLICKABLE)) || target;
    const isField = /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName);
    const css = attrSelector(el, doc) || cssPath(el, doc);
    const matches = visibleAll(doc.querySelectorAll(css));
    return {
      tag: el.tagName.toLowerCase(),
      css,
      simple: simpleSelector(el),
      index: Math.max(0, matches.indexOf(el)),
      text: isField ? '' : ownText(el),
      placeholder: el.getAttribute('placeholder') || '',
      label: el.getAttribute('aria-label') || '',
    };
  }

  // ---------- DOM: finding elements ----------

  function isVisible(el) {
    if (!el || !el.isConnected) return false;
    const view = el.ownerDocument.defaultView;
    const st = view.getComputedStyle(el);
    if (st.display === 'none' || st.visibility === 'hidden' || Number(st.opacity) === 0) return false;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  }

  function visibleAll(list) {
    return Array.from(list).filter(isVisible);
  }

  function safeQuery(doc, sel) {
    try {
      return Array.from(doc.querySelectorAll(sel));
    } catch (e) {
      return [];
    }
  }

  // findElement resolves a locator. Elements with text (buttons, dropdown
  // options) are matched by their rendered text first, because popups are
  // re-created and their positions shift; fields fall back to CSS.
  function findElement(desc, text, doc) {
    if (text) {
      const byText = (els) => visibleAll(els).filter((e) => ownText(e) === text);
      let found = byText(safeQuery(doc, desc.css));
      if (!found.length) found = byText(safeQuery(doc, desc.simple));
      if (!found.length) {
        // deepest element whose text matches, e.g. a span inside the option
        found = byText(safeQuery(doc, CLICKABLE));
        if (!found.length) found = byText(safeQuery(doc, 'span,div,p'));
        found = found.filter((e) => !found.some((o) => o !== e && e.contains(o)));
      }
      if (found.length) return found[Math.min(desc.index || 0, found.length - 1)];
      return null;
    }
    let found = visibleAll(safeQuery(doc, desc.css));
    if (!found.length && desc.placeholder) {
      found = visibleAll(safeQuery(doc, `${desc.tag}[placeholder="${desc.placeholder.replace(/"/g, '\\"')}"]`));
    }
    if (!found.length && desc.label) {
      found = visibleAll(safeQuery(doc, `${desc.tag}[aria-label="${desc.label.replace(/"/g, '\\"')}"]`));
    }
    if (!found.length) return null;
    return found[Math.min(desc.index || 0, found.length - 1)];
  }

  function sleep(ms) {
    return new Promise((r) => setTimeout(r, ms));
  }

  async function waitFor(desc, text, doc, timeoutMs) {
    const deadline = Date.now() + (timeoutMs || 5000);
    for (;;) {
      const el = findElement(desc, text, doc);
      if (el) return el;
      if (Date.now() > deadline) return null;
      await sleep(100);
    }
  }

  // ---------- DOM: acting ----------

  function fire(el, type, init) {
    const view = el.ownerDocument.defaultView;
    const Ctor = /^(pointer)/.test(type)
      ? view.PointerEvent || view.MouseEvent
      : /^(mouse|click)/.test(type)
        ? view.MouseEvent
        : /^key/.test(type)
          ? view.KeyboardEvent
          : view.Event;
    el.dispatchEvent(new Ctor(type, Object.assign({ bubbles: true, cancelable: true, composed: true, view }, init)));
  }

  function click(el) {
    el.scrollIntoView({ block: 'center', inline: 'center' });
    for (const t of ['pointerdown', 'mousedown', 'pointerup', 'mouseup']) fire(el, t, { button: 0 });
    if (typeof el.focus === 'function') el.focus();
    el.click();
  }

  // setValue writes through the native setter so React/Vue see the change,
  // then fires the events frameworks listen to.
  function setValue(el, value) {
    const view = el.ownerDocument.defaultView;
    el.focus();
    if (el.isContentEditable) {
      el.textContent = value;
    } else {
      const proto =
        el.tagName === 'TEXTAREA'
          ? view.HTMLTextAreaElement.prototype
          : el.tagName === 'SELECT'
            ? view.HTMLSelectElement.prototype
            : view.HTMLInputElement.prototype;
      const setter = Object.getOwnPropertyDescriptor(proto, 'value').set;
      setter.call(el, value);
    }
    fire(el, 'input', {});
    fire(el, 'change', {});
  }

  function pressKey(el, key) {
    const code = key === 'Enter' ? 13 : key === 'Tab' ? 9 : key === 'Escape' ? 27 : 0;
    for (const t of ['keydown', 'keypress', 'keyup']) fire(el, t, { key, code: key, keyCode: code, which: code });
  }

  // runSteps replays steps for one window. hooks:
  //   onStep(i)                 before step i
  //   shouldStop() -> bool      checked between steps
  //   beforeStep(i) -> Promise  awaited before step i (used to pause before saving)
  async function runSteps(steps, values, opts) {
    const o = Object.assign({ delay: 300, timeout: 5000, startAt: 0 }, opts || {});
    const doc = o.doc || root.document;
    for (let i = o.startAt; i < steps.length; i++) {
      if (o.shouldStop && o.shouldStop()) return { stopped: true, at: i };
      if (o.beforeStep) await o.beforeStep(i);
      if (o.shouldStop && o.shouldStop()) return { stopped: true, at: i };
      if (o.onStep) await o.onStep(i);

      const s = steps[i];
      if (s.type === 'wait') {
        await sleep(Number(s.ms) || 500);
        continue;
      }
      const text = s.type === 'click' && s.target.text ? render(s.template || s.target.text, values) : '';
      const el = await waitFor(s.target, text, doc, o.timeout);
      if (!el) {
        const what = text ? `"${text}"` : s.target.css;
        throw new Error(`шаг ${i + 1}: не найден элемент ${what}`);
      }
      if (s.type === 'click') {
        click(el);
      } else if (s.type === 'input') {
        setValue(el, render(s.template != null ? s.template : s.value, values));
        const next = steps[i + 1];
        if (!next || next.type !== 'key') el.blur();
      } else if (s.type === 'key') {
        pressKey(el, s.key);
      }
      await sleep(o.delay);
    }
    return { stopped: false, at: steps.length };
  }

  // ---------- recording ----------

  // Recorder turns DOM events into steps. `ignore(el)` filters out our own UI.
  function Recorder(doc, ignore, onChange) {
    this.doc = doc;
    this.ignore = ignore || (() => false);
    this.onChange = onChange || (() => {});
    this.steps = [];
    this.handlers = null;
  }

  Recorder.prototype.start = function (steps) {
    if (this.handlers) return;
    this.steps = steps ? steps.slice() : [];
    const rec = this;
    const isTextField = (el) =>
      (el.tagName === 'INPUT' && !/^(checkbox|radio|button|submit|reset|file)$/i.test(el.type)) ||
      el.tagName === 'TEXTAREA' ||
      el.tagName === 'SELECT' ||
      el.isContentEditable;

    const onClick = (e) => {
      const t = e.target;
      if (!(t instanceof rec.doc.defaultView.Element) || rec.ignore(t)) return;
      const target = describe(t, rec.doc);
      const last = rec.steps[rec.steps.length - 1];
      // a second click on the same field adds nothing
      if (last && last.type === 'click' && last.target.css === target.css && !target.text && isTextField(t)) return;
      rec.push({ type: 'click', target });
    };
    const onInput = (e) => {
      const t = e.target;
      if (!(t instanceof rec.doc.defaultView.Element) || rec.ignore(t) || !isTextField(t)) return;
      const value = t.isContentEditable ? t.textContent : t.value;
      const target = describe(t, rec.doc);
      const last = rec.steps[rec.steps.length - 1];
      if (last && last.type === 'input' && last.target.css === target.css) {
        last.value = value;
        rec.onChange(rec.steps);
        return;
      }
      rec.push({ type: 'input', target, value });
    };
    const onKey = (e) => {
      const t = e.target;
      if (!(t instanceof rec.doc.defaultView.Element) || rec.ignore(t)) return;
      if (e.key !== 'Enter' || !isTextField(t)) return;
      rec.push({ type: 'key', key: 'Enter', target: describe(t, rec.doc) });
    };
    this.handlers = { click: onClick, input: onInput, change: onInput, keydown: onKey };
    for (const [type, fn] of Object.entries(this.handlers)) this.doc.addEventListener(type, fn, true);
  };

  Recorder.prototype.push = function (step) {
    this.steps.push(step);
    this.onChange(this.steps);
  };

  Recorder.prototype.stop = function () {
    if (!this.handlers) return this.steps;
    for (const [type, fn] of Object.entries(this.handlers)) this.doc.removeEventListener(type, fn, true);
    this.handlers = null;
    return this.steps;
  };

  Recorder.prototype.recording = function () {
    return !!this.handlers;
  };

  const api = {
    PLACEHOLDERS,
    DEFAULT_ACTION_LABELS,
    sceneName,
    windowKey,
    windowValues,
    detectTemplate,
    render,
    retemplate,
    describe,
    findElement,
    waitFor,
    setValue,
    click,
    pressKey,
    runSteps,
    Recorder,
  };

  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.GordpoolMacro = api;
})(typeof window !== 'undefined' ? window : globalThis);
