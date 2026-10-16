//go:build browsertest

package soundtouchweb

import (
	"net/http/httptest"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/go-chi/chi/v5"
)

func TestMemberDetailsLinkWithoutPhysicalSettings(t *testing.T) {
	app := NewWebApp()
	router := chi.NewRouter()
	app.MountWeb(router, nil)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(`.nav-discover-icon`)),
		chromedp.Evaluate[chromedp.Void](`(async () => {
            const [{ MemberSettings }, { h, render }] = await Promise.all([
                import('/app/static/js/components/MemberSettings.js'),
                import('/app/static/lib/preact.module.js'),
            ]);
            const root = document.createElement('div');
            document.body.append(root);
            let opens = 0;
            const props = { deviceId: 'member.local', device: { info: { name: 'Unavailable member' } },
                onOpenDetails: () => { opens += 1; } };
            const check = (condition, message) => { if (!condition) throw new Error(message); };
            render(h(MemberSettings, props), root);
            const disclosure = root.querySelector('.member-settings');
            const link = root.querySelector('.member-details-link');
            check(disclosure && !disclosure.open, 'Missing settings must not remove the collapsed detail route');
            check(link?.checkVisibility() === false, 'Full details must be subordinate to Settings');
            disclosure.querySelector(':scope > summary').click();
            check(link.getBoundingClientRect().height >= 44, 'Detail link must retain a touch target');
            check(link.getAttribute('aria-label') === 'Full device details for Unavailable member', 'The target needs an accessible name');
            link.click();
            check(opens === 1, 'Details must use the provided navigation callback once');
            check(!root.querySelector('.sound-settings, .device-settings'), 'No physical settings should be invented');
            render(h(MemberSettings, { ...props, onOpenDetails: undefined }), root);
            check(!root.querySelector('.member-settings'), 'No empty Settings when there is no route or physical target');
            render(h(MemberSettings, { ...props, nested: false }), root);
            check(!root.querySelector('.member-details-link'), 'Standalone Settings must not gain a member navigation route');
            render(null, root);
            root.remove();
        })()`),
	); err != nil {
		t.Fatalf("member details fallback: %v", err)
	}
}
