const msg = document.getElementById('msg');

async function activeTab() {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  return tab;
}

document.getElementById('open').addEventListener('click', async () => {
  try {
    const tab = await activeTab();
    // activeTab grants access to the current page after the user clicks the action
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['lib/macro.js', 'content.js'] });
    window.close();
  } catch (err) {
    msg.textContent = 'Не удалось открыть панель: ' + err.message;
  }
});

document.getElementById('auto').addEventListener('click', async () => {
  try {
    const tab = await activeTab();
    const pattern = window.GordpoolSettings.originPattern(tab.url);
    if (!(await chrome.permissions.request({ origins: [pattern] }))) {
      msg.textContent = 'Доступ к сайту не выдан';
      return;
    }
    const { sites = [] } = await chrome.storage.sync.get('sites');
    if (!sites.includes(pattern)) sites.push(pattern);
    await chrome.storage.sync.set({ sites });
    const res = await chrome.runtime.sendMessage({ type: 'syncSites' });
    if (!res.ok) throw new Error(res.error);
    await chrome.scripting.executeScript({ target: { tabId: tab.id }, files: ['lib/macro.js', 'content.js'] });
    msg.style.color = '#067647';
    msg.textContent = 'Панель будет открываться на ' + pattern;
  } catch (err) {
    msg.textContent = 'Ошибка: ' + err.message;
  }
});

document.getElementById('options').addEventListener('click', () => chrome.runtime.openOptionsPage());
