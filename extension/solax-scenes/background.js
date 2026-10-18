// Service worker: fetches plans (extension host permissions bypass CORS) and
// registers the content script on sites the user enabled.
importScripts('settings.js');

const { DEFAULT_SETTINGS, planURL } = self.GordpoolSettings;

async function getSettings() {
  const { settings } = await chrome.storage.sync.get('settings');
  return Object.assign({}, DEFAULT_SETTINGS, settings || {});
}

async function fetchPlan(overrides) {
  const settings = Object.assign(await getSettings(), overrides || {});
  const url = planURL(settings);
  const resp = await fetch(url, { headers: { Accept: 'application/json' } });
  const body = await resp.json().catch(() => ({}));
  if (!resp.ok) throw new Error(body.error || `HTTP ${resp.status}`);
  if (body.format !== 'solax-datahub1000/v1') throw new Error(`неожиданный формат ответа: ${body.format}`);
  await chrome.storage.local.set({ plan: { fetchedAt: new Date().toISOString(), url, data: body } });
  return body;
}

const SCRIPT_ID = 'gordpool-solax-panel';

async function syncRegisteredSites() {
  const { sites = [] } = await chrome.storage.sync.get('sites');
  const existing = await chrome.scripting.getRegisteredContentScripts({ ids: [SCRIPT_ID] });
  if (existing.length) await chrome.scripting.unregisterContentScripts({ ids: [SCRIPT_ID] });
  if (!sites.length) return;
  await chrome.scripting.registerContentScripts([
    { id: SCRIPT_ID, matches: sites, js: ['lib/macro.js', 'content.js'], runAt: 'document_idle', persistAcrossSessions: true },
  ]);
}

chrome.runtime.onInstalled.addListener(() => syncRegisteredSites().catch(console.error));
chrome.runtime.onStartup.addListener(() => syncRegisteredSites().catch(console.error));

chrome.runtime.onMessage.addListener((msg, _sender, sendResponse) => {
  const reply = (p) =>
    p.then(
      (data) => sendResponse({ ok: true, data }),
      (err) => sendResponse({ ok: false, error: String((err && err.message) || err) }),
    );
  switch (msg && msg.type) {
    case 'fetchPlan':
      reply(fetchPlan(msg.overrides));
      return true;
    case 'syncSites':
      reply(syncRegisteredSites());
      return true;
    case 'openOptions':
      chrome.runtime.openOptionsPage();
      return false;
  }
  return false;
});
