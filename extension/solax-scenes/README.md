# gordpool → SolaX Smart Scene (Chrome extension)

Fetches the battery plan from the gordpool API (`/v1/battery/solax`) and fills SolaX DataHub 1000 **Smart Scenes**
in the SolaX web UI for you.

The Smart Scene editor is SolaX's own app, and its markup can change with any release, so the extension does not
hard-code selectors. Instead you **record one scene by hand**. The extension remembers the steps, spots where you typed
the plan values (date, start/end time, action, power, SOC, name) and replays the same steps for every other window of
the plan.

## Install

1. `chrome://extensions` → enable *Developer mode* → *Load unpacked* → pick this `extension/solax-scenes` folder.
2. Open the extension *Options*: set the gordpool API address (e.g. your Cloud Run URL) and the battery parameters,
   then *Save*. Chrome asks for access to the API address, which the extension needs to download the plan.

## Use

1. Open the Smart Scene list in SolaX Cloud, click the extension icon → **Открыть панель на этой вкладке**.
   (**Открывать автоматически на этом сайте** asks for access to that site and then opens the panel by itself, also
   after reloads. Without it the panel only gets access to the current tab when you click the icon.)
2. **План**: enter the current SOC and click **Загрузить план**. Uncheck windows you do not want.
3. **Макрос** (once per site): check the action texts SolaX uses (`Charge`/`Discharge` by default), click
   **● Записать**, then create one scene by hand **starting from the scene list**, typing exactly the sample values
   the panel shows. Save the scene, then click **■ Остановить запись**.
   Each recorded step shows its template, for example `{start}` or `{action}`. You can edit a template if SolaX needs
   another format, e.g. `{end_2359}` instead of `{end}`, or `{date_dmy}`. Use **+ пауза** if a dialog animates slowly.
4. **Заполнение**: click **Заполнить выбранные**. By default it stops before the last step (Save) of every scene so you
   can check it; untick that once you trust the macro. Windows that are already filled are marked ✓ and skipped next time.

If a step fails (element not found), the log names the step. Fix its template or re-record. If the page reloads while
recording or filling, the panel continues where it stopped (with "open automatically" enabled).

## Develop

```sh
npm install   # Playwright for the e2e tests; uses the preinstalled Chromium
npm test      # unit tests + e2e against test/fixtures/mock-scene.html
```

`test/fixtures/mock-scene.html` imitates the hard parts of the SolaX SPA: random ids, state updated only from input
events, dropdowns rendered into `<body>`, a date field that commits on Enter, and an optional reload on save. It is not
a copy of the real page. Check the macro on a real SolaX account before relying on it.
`test/fixtures/plan.json` is a real response from `/v1/battery/solax`.
