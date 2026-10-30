//go:build browsertest

package soundtouchweb

import (
	"github.com/chromedp/chromedp"
	"testing"
)

func TestZoneMemberBackRestoresExpandedViewAfterDelayedReads(t *testing.T) {
	app := newZoneMemberNavigationApp(t)
	// This ST10's late capability response removes its provisional stereo
	// section above Zone, after the zone/settings responses have completed.
	master, _ := app.GetDevice(zoneNavigationMaster)
	master.DeviceInfo.Type = "SoundTouch 10"
	server, mutations := newZoneMemberNavigationServer(t, app)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(390, 640),
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".zone-card")),
		chromedp.Click(chromedp.CSS(".zone-card")),
		chromedp.WaitVisible(chromedp.CSS(".zone-member-details > summary")),
		chromedp.Evaluate[chromedp.Void](`(async () => {
            const check = (ok, message) => { if (!ok) throw new Error(message); };
            const wait = async predicate => {
                for (let n = 0; n < 500; n++) {
                    if (predicate()) return;
                    await new Promise(resolve => setTimeout(resolve, 10));
                }
                throw new Error('Detail return did not settle');
            };
            const tick = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
            const style = document.createElement('style');
            style.textContent = '.device-detail { padding-bottom: 900px; }';
            document.head.append(style);
            window.__detailWrites = [];
            const original = window.fetch;
            window.fetch = async function(input, init) {
                const method = String(init?.method || input?.method || 'GET').toUpperCase();
                if (!['GET', 'HEAD'].includes(method)) window.__detailWrites.push(method);
                let response = await original.apply(this, arguments);
                if (!window.__detailReturnDelay) return response;
                const path = new URL(typeof input === 'string' ? input : input.url, location.href).pathname.replace(/\/+$/, '');
                const delay = path.endsWith('/stereo-pair') ? 1100 : path.endsWith('/settings') ? 650 : 250;
                await new Promise(resolve => setTimeout(resolve, delay));
                if (path.endsWith('/settings')) {
                    const body = await response.json();
                    body.data.network.interfaces[0].ssid = 'Fresh Wi-Fi';
                    response = new Response(JSON.stringify(body), {status: 200, headers: {'Content-Type': 'application/json'}});
                }
                return response;
            };
            const header = () => document.querySelector('button[aria-label="Settings for Living Pair"]');
            const row = () => header().closest('.zone-logical-member');
            const link = () => row().querySelector('.member-details-link');
            document.querySelector('.zone-member-details').open = true;
            await wait(header);
            header().click();
            await wait(() => link().checkVisibility());
            const physical = row().querySelector('.member-settings-content > details');
            physical.open = true;
            await wait(() => physical.querySelector('.settings-network-technical'));
            physical.querySelector('.settings-network-technical').open = true;
            await wait(() => !document.querySelector('.stereo-pair-section') &&
                !row().querySelector('[data-detail-loading="true"]:not(details:not([open]))'));
            link().focus({preventScroll: true});
            window.scrollTo(0, window.scrollY + link().getBoundingClientRect().top - 180);
            await tick();
            const before = link().getBoundingClientRect().top;
            const firstKey = physical.dataset.detailKey;
            window.__firstKey = firstKey;
            window.__capturedView = (await import('/app/static/js/detailViewState.mjs')).captureDetailView(document.querySelector('.device-detail'), {info: {device_id: 'ZONE-MASTER'}}, link());
            window.__detailReturnDelay = true;
            // Pointer activation need not focus a button on every browser.
            link().blur();
            link().click();
            await wait(() => document.querySelector('.device-detail-name')?.textContent.includes('Living Pair') &&
                document.querySelectorAll('.stereo-pair-member').length === 2);
            document.querySelector('.device-detail .back-btn').click();
            await wait(() => document.querySelector('.device-detail-name')?.textContent.includes('Atrium') && header());
            await wait(() => header().getAttribute('aria-expanded') === 'true' &&
                [...row().querySelectorAll('details')].some(node => node.dataset.detailKey === firstKey && node.open &&
                    node.querySelector('.settings-network-technical')?.open) &&
                row().textContent.includes('Fresh Wi-Fi') && !document.querySelector('.stereo-pair-section'));
            await tick();
            check(document.querySelector('.zone-member-details').open, 'Group members must stay expanded');
            check(Math.abs(link().getBoundingClientRect().top - before) < 3, 'Return must preserve the link viewport position after delayed layout');
            check(document.activeElement === link(), 'Return must restore focus to the origin link');
            check(window.__detailWrites.length === 0, 'View restoration must not mutate speakers');
        })()`, chromedp.EvalAwaitPromise),
	); err != nil {
		var state string
		_ = chromedp.Do(ctx, evalInto(`JSON.stringify({captured: window.__capturedView, firstKey: window.__firstKey,
            title: document.querySelector('.page-title')?.textContent,
            headers: [...document.querySelectorAll('.zone-member-settings-toggle')].map(n=>[n.getAttribute('aria-label'),n.getAttribute('aria-expanded')]),
            details: [...document.querySelectorAll('details[data-detail-key]')].map(n=>[n.dataset.detailKey,n.open]),
            loading: [...document.querySelectorAll('[data-detail-loading]')].map(n=>[n.className,n.dataset.detailLoading]),
            text: document.querySelector('.zone-logical-members')?.textContent,
            stereo: document.querySelector('.stereo-pair-section')?.textContent})`, &state))
		t.Fatalf("restore delayed origin view: %v; state: %s", err, state)
	}
	if mutations.Load() != 0 {
		t.Fatalf("view restoration sent %d topology writes", mutations.Load())
	}
}

func TestDetailViewRestorationCancelsOnInputAndUnmount(t *testing.T) {
	app := newZoneMemberNavigationApp(t)
	server, _ := newZoneMemberNavigationServer(t, app)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".nav-discover-icon")),
		chromedp.Evaluate[chromedp.Void](`(async () => {
            const { captureDetailView, restoreDetailView } = await import('/app/static/js/detailViewState.mjs');
            const check = (ok, why) => { if (!ok) throw new Error(why); };
            const tick = () => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
            const root = document.createElement('div');
            root.innerHTML = '<details data-detail-key="group"><summary>Members</summary><button type="button" data-detail-key="member" aria-expanded="false">Member settings</button><div hidden><button data-detail-focus="member:full-details">Full device details</button></div></details>';
            const filler = document.createElement('div'); filler.style.height = '2500px';
            document.body.append(root, filler);
            const group = root.querySelector('details');
            const header = root.querySelector('[aria-expanded]');
            const content = header.nextElementSibling;
            const link = content.firstElementChild;
            header.onclick = () => { const open = header.getAttribute('aria-expanded') !== 'true'; header.setAttribute('aria-expanded', String(open)); content.hidden = !open; };
            group.open = true; header.click();
            const device = { info: { device_id: 'physical-A' } };
            const snapshot = captureDetailView(root, device, link);
            group.open = false; header.click();
            const mismatchCleanup = restoreDetailView(root, snapshot, { info: { device_id: 'replacement' } });
            await tick(); check(!group.open && header.getAttribute('aria-expanded') === 'false', 'Identity replacement must discard the view'); mismatchCleanup();
            const loading = document.createElement('div'); loading.dataset.detailLoading = 'true'; content.append(loading);
            const cancel = restoreDetailView(root, snapshot, device);
            await tick(); check(group.open && header.getAttribute('aria-expanded') === 'true', 'Saved disclosures should open before delayed reads finish');
            window.dispatchEvent(new WheelEvent('wheel', {bubbles: true}));
            header.click(); window.scrollTo(0, 90); const afterInput = window.scrollY;
            loading.remove(); await tick();
            check(window.scrollY === afterInput && header.getAttribute('aria-expanded') === 'false', 'Late layout must not override user input'); cancel();
            content.append(loading);
            const unmount = restoreDetailView(root, snapshot, device);
            root.remove(); window.scrollTo(0, 130); const afterUnmount = window.scrollY;
            await tick(); check(window.scrollY === afterUnmount, 'Old callbacks must not scroll a new page'); unmount(); filler.remove();
        })()`, chromedp.EvalAwaitPromise),
	); err != nil {
		t.Fatalf("cancel view restoration: %v", err)
	}
}
