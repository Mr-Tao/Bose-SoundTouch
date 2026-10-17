//go:build browsertest

package soundtouchweb

import (
	"fmt"
	"testing"

	"github.com/chromedp/chromedp"
)

func TestZoneMemberHeadersDiscloseSettingsWithoutWrites(t *testing.T) {
	app := newZoneMemberNavigationApp(t)
	server, mutations := newZoneMemberNavigationServer(t, app)
	ctx := newHeadlessChromeContext(t)
	const ordinary = `button.zone-member-settings-toggle[aria-label="Settings for Breakfast Room"]`
	const pair = `button.zone-member-settings-toggle[aria-label="Settings for Living Pair"]`
	const left = `.zone-logical-member:has(` + pair + `) .member-settings-content > details.settings-section:nth-of-type(1)`
	const right = `.zone-logical-member:has(` + pair + `) .member-settings-content > details.settings-section:nth-of-type(2)`

	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".zone-card")),
		chromedp.Click(chromedp.CSS(".zone-card")),
		chromedp.WaitVisible(chromedp.CSS(".zone-member-details > summary")),
		chromedp.Click(chromedp.CSS(".zone-member-details > summary")),
		chromedp.WaitVisible(chromedp.CSS(ordinary)),
		chromedp.Evaluate[chromedp.Void](`(() => {
            const check = (condition, message) => { if (!condition) throw new Error(message); };
            const headers = [...document.querySelectorAll('.zone-member-settings-toggle')];
            check(headers.length === 4, 'Each logical member needs one settings header');
            check(!document.querySelector('.member-settings > summary'), 'A separate Settings row must not remain');
            check(!document.querySelector('.member-details-link[aria-label="Full device details for Atrium"]'), 'No self navigation');
            const ids = headers.map(header => header.getAttribute('aria-controls'));
            check(new Set(ids).size === headers.length, 'Settings content ids must be unique');
            for (const header of headers) {
                const content = document.getElementById(header.getAttribute('aria-controls'));
                check(header.type === 'button' && header.getAttribute('aria-expanded') === 'false', 'Headers start collapsed');
                check(content?.hidden && !content.checkVisibility(), 'aria-controls must identify hidden settings');
                check(header.closest('.zone-logical-member').querySelector('.zone-member-volume-control').checkVisibility(), 'Member volume must remain visible');
            }
            window.__memberHeaderWrites = [];
            const originalFetch = window.fetch;
            window.fetch = function(input, init) {
                const method = String(init?.method || input?.method || 'GET').toUpperCase();
                if (!['GET', 'HEAD'].includes(method)) window.__memberHeaderWrites.push(method);
                return originalFetch.apply(this, arguments);
            };
        })()`),
		chromedp.Click(chromedp.CSS(ordinary)),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q)?.getAttribute('aria-expanded') === 'true' &&
            document.querySelector('.member-details-link[aria-label="Full device details for Breakfast Room"]')?.checkVisibility()`, ordinary)),
		chromedp.Focus(chromedp.CSS(ordinary)),
		chromedp.KeyEvent("\r"),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q)?.getAttribute('aria-expanded') === 'false' &&
            !document.querySelector('.member-details-link[aria-label="Full device details for Breakfast Room"]')?.checkVisibility()`, ordinary)),
		chromedp.KeyEvent("\r"),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q)?.getAttribute('aria-expanded') === 'true'`, ordinary)),
		chromedp.Click(chromedp.CSS(pair)),
		chromedp.WaitVisible(chromedp.CSS(left+" > summary")),
		chromedp.Click(chromedp.CSS(left+" > summary")),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q)?.open && !document.querySelector(%q)?.open &&
            document.querySelector(%q)?.textContent.includes('LEFT') && document.querySelector(%q)?.textContent.includes('RIGHT')`, left, right, left+" > summary", right+" > summary")),
		chromedp.Click(chromedp.CSS(pair)),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q)?.getAttribute('aria-expanded') === 'false' &&
            document.querySelector(%q).closest('.zone-logical-member').querySelector('.zone-member-volume-control').checkVisibility()`, pair, pair)),
		chromedp.Click(chromedp.CSS(pair)),
		chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q)?.open && !document.querySelector(%q)?.open &&
            document.querySelector('.member-details-link[aria-label="Full device details for Living Pair"]')?.checkVisibility()`, left, right)),
	); err != nil {
		t.Fatalf("member header disclosure: %v", err)
	}

	for _, width := range []int64{320, 390, 768, 1280} {
		if err := chromedp.Do(ctx,
			chromedp.EmulateViewport(width, 900),
			chromedp.Evaluate[chromedp.Void](`(() => {
                const check = (condition, message) => { if (!condition) throw new Error(message); };
                for (const header of document.querySelectorAll('.zone-member-settings-toggle')) {
                    const rect = header.getBoundingClientRect();
                    check(rect.height >= 44, 'Member headers need a 44px touch target');
                    check(rect.left >= 0 && rect.right <= innerWidth, 'Header must fit the viewport');
                    const chevron = header.querySelector('.settings-chevron').getBoundingClientRect();
                    check(chevron.left >= rect.left && chevron.right <= rect.right, 'Chevron must fit its header');
                    const row = header.closest('.zone-logical-member');
                    check(row.querySelector('.zone-member-volume-control').checkVisibility(), 'Expanded settings must not hide volume');
                }
                check(window.__memberHeaderWrites.length === 0, 'Disclosure must not send mutation requests');
            })()`),
		); err != nil {
			t.Fatalf("member header at %dpx: %v", width, err)
		}
	}
	if got := mutations.Load(); got != 0 {
		t.Fatalf("settings disclosure sent %d zone mutation request(s)", got)
	}
}
