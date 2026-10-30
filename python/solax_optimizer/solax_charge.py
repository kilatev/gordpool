"""Set an inverter charge period through an authenticated SolaX browser."""
import argparse
import re
import shutil
import subprocess
from urllib.parse import urlsplit

START = "Forced Charge Period Start Time"
END = "Forced Charge Period End Time"
DISC_START = "Allowed Disc Period Start Time"
DISC_END = "Allowed Disc Period End Time"


def time_value(value):
    if not re.fullmatch(r"(?:[01][0-9]|2[0-3]):[0-5][0-9]", value):
        raise argparse.ArgumentTypeError("Expected HH:MM, from 00:00 to 23:59")
    return value


def is_settings_url(url):
    parts = urlsplit(url)
    return (parts.scheme == "https" and parts.hostname == "global.solaxcloud.com"
            and parts.path == "/green/remoteSetting/"
            and parts.fragment.split("?", 1)[0] in ("/inverter", "/x3hybridg4basic"))


def is_solax_url(url):
    parts = urlsplit(url)
    return is_login_url(url) or (parts.scheme == "https"
                                 and parts.hostname == "global.solaxcloud.com"
                                 and parts.path.startswith("/green/"))


def is_login_url(url):
    parts = urlsplit(url)
    return (parts.scheme == "https" and parts.hostname == "solaxcloud.com"
            and parts.path == "/user-center/")


def pass_field(vault, item, name):
    cli = shutil.which("pass-cli")
    if not cli:
        raise RuntimeError("pass-cli is not installed or not in PATH")
    result = subprocess.run(
        [cli, "item", "view", "--vault-name", vault, "--item-title", item,
         "--field", name], capture_output=True, text=True)
    if result.returncode:
        raise RuntimeError(f"Could not read the {name} field from Proton Pass")
    return result.stdout.rstrip("\n")


def login_with_pass(page, vault, item, login_field):
    username = pass_field(vault, item, login_field)
    password = pass_field(vault, item, "password")
    if not username or not password:
        raise RuntimeError("The SolaX login fields in Proton Pass must not be empty")
    user_input = page.locator('input[type="text"]:visible, input[type="email"]:visible')
    password_input = page.locator('input[type="password"]:visible')
    if user_input.count() != 1 or password_input.count() != 1:
        raise RuntimeError("Could not identify the SolaX login fields")
    user_input.fill(username)
    password_input.fill(password)
    print("Credentials filled. In Chromium, review the terms, check the agreement, and click Login. "
          "The script is waiting for sign-in.", flush=True)
    page.wait_for_function(
        "() => location.hostname === 'global.solaxcloud.com' "
        "&& location.pathname.startsWith('/green/')", timeout=180000)


def open_user_settings(browser, serial, login=False, pass_vault="cli",
                       pass_item="solaxcloud.com ", pass_login_field="email",
                       settings_password="0000"):
    from playwright.sync_api import expect

    pages = [page for context in browser.contexts for page in context.pages
             if is_solax_url(page.url)]
    if not pages and login:
        blank_pages = [page for context in browser.contexts for page in context.pages
                       if page.url in ("about:blank", "chrome://newtab/", "chrome://new-tab-page/")]
        if len(blank_pages) == 1:
            blank_pages[0].goto("https://solaxcloud.com/user-center/")
            pages = [blank_pages[0]]
    ready = [page for page in pages if is_settings_url(page.url)
             and page.get_by_text("User Settings", exact=True).count()]
    if ready:
        page = ready[0]
        open_period(page, serial)
        return page
    remote = [page for page in pages if is_settings_url(page.url)]
    page = remote[0] if remote else next((page for page in pages
                                          if page.get_by_text("Overview", exact=True).count()
                                          or page.get_by_text("Devices", exact=True).count()), None)
    if page is None and len(pages) == 1:
        page = pages[0]
    if page is None:
        raise RuntimeError("Keep the SolaX Overview open in the Chromium profile")
    page.set_default_timeout(30000)
    if is_login_url(page.url):
        if not login:
            raise RuntimeError("SolaX is not signed in; rerun with --login to use Proton Pass")
        login_with_pass(page, pass_vault, pass_item, pass_login_field)
    if not is_settings_url(page.url):
        row = page.locator(".arco-table-tr").filter(has_text=serial)
        if not row.count():
            devices = page.locator(".arco-menu-item").filter(has_text="Devices")
            expect(devices).to_have_count(1)
            devices.click()
            row = page.locator(".arco-table-tr").filter(has_text=serial)
            expect(row).to_have_count(1)
        row.locator(".icon-yunwei").click()
        try:
            with page.expect_popup(timeout=15000) as popup:
                page.get_by_text("Remote Settings", exact=True).click()
            page = popup.value
        except Exception as error:
            raise RuntimeError("SolaX did not open Remote Settings; check Network abnormality") from error
        page.set_default_timeout(30000)

    try:
        page.get_by_text("Work Mode", exact=True).wait_for(timeout=30000)
    except Exception as error:
        raise RuntimeError("Remote Settings did not load. Check Network abnormality and retry.") from error

    if not page.get_by_text("User Settings", exact=True).is_visible():
        page.get_by_text("Settings", exact=True).last.click()
    password = page.locator('input[type="tel"]:visible')
    if password.count():
        password.fill(settings_password)
        page.get_by_text("Confirm", exact=True).click()
    try:
        page.get_by_text("User Settings", exact=True).wait_for(timeout=30000)
        open_period(page, serial)
    except Exception as error:
        raise RuntimeError("User Settings did not load. Check the Remote Settings tab and network.") from error
    return page


def field(page, label):
    return page.get_by_text(label, exact=True).locator("..")


def open_period(page, serial):
    from playwright.sync_api import expect
    expect(page.get_by_text("User Settings", exact=True)).to_be_visible()
    expect(page.get_by_text(serial, exact=True)).to_be_visible()
    page.get_by_text("Loading", exact=True).wait_for(state="hidden")
    if not page.get_by_text(START, exact=True).is_visible():
        page.get_by_role("button", name=re.compile(r"^Chrg&Dischrg Period\s")).click()
    for label in (START, END, DISC_START, DISC_END):
        expect(field(page, label).get_by_role("textbox")).to_have_value(
            re.compile(r"^\d{2}:\d{2}$"))


def set_time(page, label, value):
    from playwright.sync_api import expect
    row = field(page, label)
    if row.get_by_role("textbox").input_value() == value:
        return False
    row.get_by_role("textbox").click()
    picker = page.locator(".van-picker:visible")
    expect(picker).to_have_count(1)
    columns = picker.locator(".van-picker-column")
    expect(columns).to_have_count(2)
    for index, number in enumerate(value.split(":")):
        # Vant clips wheel items; dispatch the item's normal click handler.
        columns.nth(index).get_by_role("button", name=number, exact=True).dispatch_event("click")
    picker.get_by_role("button", name="Confirm", exact=True).click()
    expect(row.get_by_role("textbox")).to_have_value(value)
    row.get_by_role("button", name="Save", exact=True).click()
    # Observed UI shows Loading for each write; do not reload mid-request.
    loading = page.get_by_text("Loading", exact=True)
    loading.wait_for(state="visible")
    loading.wait_for(state="hidden")
    return True


def apply_period(page, serial, start, end, discharge_start=None, discharge_end=None):
    from playwright.sync_api import expect
    if (discharge_start is None) != (discharge_end is None):
        raise ValueError("Both discharge start and end are required")
    for value in (start, end, discharge_start, discharge_end):
        if value is None:
            continue
        time_value(value)
    open_period(page, serial)
    pairs = ((START, start), (END, end), (DISC_START, discharge_start), (DISC_END, discharge_end))
    pairs = tuple((label, value) for label, value in pairs if value is not None)
    original = {label: field(page, label).get_by_role("textbox").input_value()
                for label, _ in pairs}
    print(f"Current charge: {original[START]}–{original[END]}; requested: {start}–{end}")
    if discharge_start is not None:
        print(f"Current discharge: {original[DISC_START]}–{original[DISC_END]}; "
              f"requested: {discharge_start}–{discharge_end}")
    for label, value in pairs:
        if set_time(page, label, value):
            page.reload()
            open_period(page, serial)
        expect(field(page, label).get_by_role("textbox")).to_have_value(value)
    print(f"Verified saved charge period: {start}–{end}")
    if discharge_start is not None:
        print(f"Verified saved discharge period: {discharge_start}–{discharge_end}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cdp-url", default="http://127.0.0.1:9222")
    parser.add_argument("--serial", required=True, help="Expected inverter serial number")
    parser.add_argument("--start", required=True, type=time_value)
    parser.add_argument("--end", required=True, type=time_value)
    parser.add_argument("--discharge-start", type=time_value)
    parser.add_argument("--discharge-end", type=time_value)
    parser.add_argument("--login", action="store_true", help="Fill SolaX credentials from Proton Pass")
    parser.add_argument("--pass-vault", default="cli")
    parser.add_argument("--pass-item", default="solaxcloud.com ")
    parser.add_argument("--pass-login-field", choices=("username", "email"), default="email")
    parser.add_argument("--settings-password", default="0000")
    args = parser.parse_args()
    if (args.discharge_start is None) != (args.discharge_end is None):
        parser.error("--discharge-start and --discharge-end must be provided together")
    from playwright.sync_api import sync_playwright
    try:
        with sync_playwright() as playwright:
            browser = playwright.chromium.connect_over_cdp(args.cdp_url, timeout=15000)
            page = open_user_settings(browser, args.serial, args.login, args.pass_vault,
                                      args.pass_item, args.pass_login_field, args.settings_password)
            apply_period(page, args.serial, args.start, args.end,
                         args.discharge_start, args.discharge_end)
            # Stop Playwright's connection; do not close the user's browser.
    except RuntimeError as error:
        parser.exit(1, f"{error}\n")
    except Exception as error:
        # Playwright errors may embed authenticated URLs. Do not print those.
        parser.exit(1, f"Failed ({type(error).__name__}). Check the browser and saved values; "
                    "one field may already have been saved. No rollback was attempted.\n")


if __name__ == "__main__":
    main()
