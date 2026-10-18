const { DEFAULT_SETTINGS, originPattern } = window.GordpoolSettings;
const form = document.getElementById('form');
const status = document.getElementById('status');

chrome.storage.sync.get('settings').then(({ settings }) => {
  const s = Object.assign({}, DEFAULT_SETTINGS, settings || {});
  for (const [k, v] of Object.entries(s)) if (form.elements[k]) form.elements[k].value = v;
});

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  const settings = {};
  for (const k of Object.keys(DEFAULT_SETTINGS)) settings[k] = form.elements[k].value.trim();
  settings.apiBase = settings.apiBase.replace(/\/+$/, '');
  try {
    // fetch() from the service worker needs host access to the API origin
    const granted = await chrome.permissions.request({ origins: [originPattern(settings.apiBase)] });
    await chrome.storage.sync.set({ settings });
    status.textContent = granted ? 'Сохранено' : 'Сохранено, но без доступа к API план не загрузится';
  } catch (err) {
    status.textContent = 'Ошибка: ' + err.message;
  }
});
