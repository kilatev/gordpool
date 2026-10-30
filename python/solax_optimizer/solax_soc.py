"""Read the selected inverter's battery SOC from the SolaX Overview page."""
import argparse
import re
from time import monotonic, sleep
from urllib.parse import parse_qs, urlsplit

from .solax_charge import is_login_url, is_solax_url, login_with_pass

SOC = re.compile(r"(?:battery\s*)?(?:SOC|state of charge)\s*[:：]?\s*(\d+(?:[.,]\d+)?)\s*%", re.I)


def read_soc(cdp_url, serial, login=True, pass_vault="cli",
             pass_item="solaxcloud.com ", pass_login_field="email"):
    from playwright.sync_api import sync_playwright

    with sync_playwright() as playwright:
        browser = playwright.chromium.connect_over_cdp(cdp_url, timeout=15000)
        def solax_pages():
            return [page for context in browser.contexts for page in context.pages
                    if is_solax_url(page.url)]

        def app_pages():
            return [page for page in solax_pages() if urlsplit(page.url).path == "/green/"]

        deadline = monotonic() + 30
        pages = solax_pages()
        while not pages and monotonic() < deadline:
            sleep(0.25)
            pages = solax_pages()

        def read_pages():
            matches = []
            for page in pages:
                if urlsplit(page.url).path != "/green/":
                    continue
                page.wait_for_function("() => location.hash.includes('siteId=')", timeout=30000)
                parts = urlsplit(page.url)
                query = parts.fragment.partition("?")[2]
                site_id = parse_qs(query).get("siteId")
                if not site_id:
                    continue
                root = f"{parts.scheme}://{parts.netloc}{parts.path}"
                page.goto(f"{root}#/device?{query}")
                page.wait_for_function(
                    "siteId => location.hash.includes('/device') && "
                    "new URLSearchParams(location.hash.split('?')[1]).get('siteId') === siteId",
                    arg=site_id[0], timeout=30000)
                rows = page.locator(".arco-table-tr")
                rows.filter(has_text=serial).first.wait_for(timeout=30000)
                page.get_by_text("Loading", exact=True).wait_for(state="hidden")
                texts = rows.all_inner_texts()
                device_rows = [text for text in texts if re.search(r"\bInverter\b", text)]
                selected = [text for text in device_rows if serial in text]
                if len(selected) != 1 or len(device_rows) != 1:
                    raise RuntimeError("The selected site must contain exactly this one inverter")
                page.goto(f"{root}#/overview?{query}")
                page.wait_for_function(
                    "siteId => location.hash.includes('/overview') && "
                    "new URLSearchParams(location.hash.split('?')[1]).get('siteId') === siteId",
                    arg=site_id[0], timeout=30000)
                page.wait_for_function(
                    """() => {
                        const text = document.body.innerText;
                        return /(?:battery\\s*)?(?:SOC|state of charge)\\s*[:：]?\\s*\\d+(?:[.,]\\d+)?\\s*%/i.test(text);
                    }""",
                    timeout=30000)
                text = page.locator("body").inner_text()
                matches.extend(float(value.replace(",", "."))
                               for value in SOC.findall(text))
            return matches

        login_pages = [page for page in pages if is_login_url(page.url)]
        if login_pages:
            if len(login_pages) != 1:
                raise RuntimeError("Could not identify the SolaX login page")
            login_pages[0].wait_for_function(
                """() => (location.hostname === 'global.solaxcloud.com' &&
                            location.pathname.startsWith('/green/')) ||
                           (() => {
                             const visible = e => e.offsetWidth || e.offsetHeight || e.getClientRects().length;
                             return [...document.querySelectorAll('input[type="password"]')].some(visible) &&
                               [...document.querySelectorAll('input[type="text"],input[type="email"]')].some(visible);
                           })()""",
                timeout=15000)
            pages = solax_pages()
        matches = read_pages()
        login_pages = [page for page in pages if is_login_url(page.url)]
        if not matches and login_pages:
            if not login:
                raise RuntimeError("SolaX is not signed in")
            if len(login_pages) != 1:
                raise RuntimeError("Could not identify the SolaX login page")
            login_with_pass(login_pages[0], pass_vault, pass_item, pass_login_field)
            pages = app_pages()
            matches = read_pages()
        if len(matches) != 1 or not 0 <= matches[0] <= 100:
            raise RuntimeError("Could not identify one valid SOC on the SolaX Overview page")
        return matches[0]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cdp-url", default="http://127.0.0.1:9222")
    parser.add_argument("--serial", required=True)
    args = parser.parse_args()
    try:
        print(read_soc(args.cdp_url, args.serial))
    except Exception as error:
        parser.exit(1, f"Could not read SolaX SOC ({type(error).__name__}).\n")


if __name__ == "__main__":
    main()
