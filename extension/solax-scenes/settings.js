// Shared settings helpers for background, popup and options pages.
(function (root) {
  'use strict';

  const DEFAULT_SETTINGS = {
    apiBase: 'http://localhost:8080',
    area: 'LV',
    timezone: 'Europe/Riga',
    inverters: '1',
    min_power_kw: '0.5',
    capacity_kwh: '10',
    initial_soc_percent: '50',
    min_soc_percent: '10',
    max_soc_percent: '90',
    max_charge_kw: '',
    max_discharge_kw: '',
    round_trip_efficiency: '',
    degradation_cost_cents: '2',
    max_cycles_per_day: '1',
  };

  // Query parameters of /v1/battery/solax; empty values are left to API defaults.
  const QUERY_KEYS = Object.keys(DEFAULT_SETTINGS).filter((k) => k !== 'apiBase');

  function planURL(settings) {
    const s = Object.assign({}, DEFAULT_SETTINGS, settings || {});
    const url = new URL('/v1/battery/solax', s.apiBase);
    for (const k of QUERY_KEYS) {
      const v = String(s[k] == null ? '' : s[k]).trim();
      if (v !== '') url.searchParams.set(k, v);
    }
    return url.toString();
  }

  function originPattern(urlString) {
    const u = new URL(urlString);
    return `${u.protocol}//${u.hostname}/*`;
  }

  const api = { DEFAULT_SETTINGS, QUERY_KEYS, planURL, originPattern };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.GordpoolSettings = api;
})(typeof self !== 'undefined' ? self : globalThis);
