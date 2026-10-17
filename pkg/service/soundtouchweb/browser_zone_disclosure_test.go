//go:build browsertest

package soundtouchweb

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestOpenZoneKeepsDisclosuresDuringExternalMemberChanges(t *testing.T) {
	app := newZoneMemberNavigationApp(t)
	server, mutations := newZoneMemberNavigationServer(t, app)
	master, _ := app.GetDevice(zoneNavigationMaster)
	initial := master.Status().Zone
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".zone-card")),
		chromedp.Click(chromedp.CSS(".zone-card")),
		chromedp.WaitVisible(chromedp.CSS(".zone-member-details > summary")),
		chromedp.Click(chromedp.CSS(".zone-member-details > summary")),
		chromedp.Click(chromedp.CSS(`button[aria-label="Settings for Living Pair"]`)),
		chromedp.Click(chromedp.CSS(`.zone-logical-member:has(button[aria-label="Settings for Living Pair"]) .member-settings-content > details:first-of-type > summary`)),
		chromedp.Evaluate[chromedp.Void](`{
   window.__zoneDisclosure = document.querySelector('.zone-member-details');
   window.__pairHeader = document.querySelector('button[aria-label="Settings for Living Pair"]');
   window.__leftSettings = window.__pairHeader.closest('.zone-logical-member').querySelector('.member-settings-content > details');
   window.__pendingZoneReads = [];
   window.__zoneWrites = [];
   const original = window.fetch;
   window.fetch = async function(input, init) {
    const method = String(init?.method || input?.method || 'GET').toUpperCase();
    if (!['GET','HEAD'].includes(method)) window.__zoneWrites.push(method);
    const response = await original.apply(this, arguments);
    const path = new URL(typeof input === 'string' ? input : input.url, location.href).pathname.replace(/\/+$/, '');
    if (path.endsWith('/zone') || path.endsWith('/zone/candidates')) await new Promise(resolve => window.__pendingZoneReads.push(resolve));
    return response;
   };
  }`),
	); err != nil {
		t.Fatal(err)
	}
	joined := *initial
	joined.Members = append(append([]models.Member(nil), initial.Members...), models.Member{DeviceID: "CANDIDATE", IP: zoneNavigationCandidate})
	for _, state := range []*models.ZoneInfo{&joined, initial} {
		generation := master.BeginZoneRefresh()
		if !master.ApplyPolledZone(generation, "ZONE-MASTER", state) {
			t.Fatal("external membership did not change")
		}
		app.BroadcastDeviceList()
		if err := chromedp.Do(ctx,
			chromedp.Poll[chromedp.Void](`window.__pendingZoneReads.length === 2`),
			chromedp.Evaluate[chromedp.Void](`(() => {
    if (!window.__zoneDisclosure.isConnected || !window.__zoneDisclosure.open ||
     document.querySelector('.zone-member-details') !== window.__zoneDisclosure ||
     !window.__pairHeader.isConnected || window.__pairHeader.getAttribute('aria-expanded') !== 'true' ||
     !window.__leftSettings.isConnected || !window.__leftSettings.open) {
      throw new Error('External membership refresh discarded expanded zone/member/physical settings');
    }
    window.__pendingZoneReads.splice(0).forEach(resolve => resolve());
   })()`),
			chromedp.Poll[chromedp.Void](`document.querySelector('.zone-section')?.getAttribute('aria-busy') !== 'true'`),
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](`(() => {
  if (!window.__zoneDisclosure.open || !window.__leftSettings.open || window.__zoneWrites.length) throw new Error('Disclosure state or no-write contract changed');
 })()`)); err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 0 {
		t.Fatal("presentation refresh mutated topology")
	}
}

func TestRejoinedZoneMemberStartsCollapsedWithoutClosingSurvivors(t *testing.T) {
	app := newZoneMemberNavigationApp(t)
	server, mutations := newZoneMemberNavigationServer(t, app)
	master, _ := app.GetDevice(zoneNavigationMaster)
	initial := master.Status().Zone
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".zone-card")),
		chromedp.Click(chromedp.CSS(".zone-card")),
		chromedp.WaitVisible(chromedp.CSS(".zone-member-details > summary")),
		chromedp.Click(chromedp.CSS(".zone-member-details > summary")),
		chromedp.Click(chromedp.CSS(`button[aria-label="Settings for Living Pair"]`)),
		chromedp.Click(chromedp.CSS(`.zone-logical-member:has(button[aria-label="Settings for Living Pair"]) .member-settings-content > details:first-of-type > summary`)),
		chromedp.Click(chromedp.CSS(`button[aria-label="Settings for Breakfast Room"]`)),
		chromedp.Click(chromedp.CSS(`.zone-logical-member:has(button[aria-label="Settings for Breakfast Room"]) .member-settings-content > details:first-of-type > summary`)),
		chromedp.Evaluate[chromedp.Void](`{
   window.__survivingZone=document.querySelector('.zone-member-details');
   window.__survivingHeader=document.querySelector('button[aria-label="Settings for Breakfast Room"]');
   window.__survivingPhysical=window.__survivingHeader.closest('.zone-logical-member').querySelector('.member-settings-content > details');
   window.__removedHeader=document.querySelector('button[aria-label="Settings for Living Pair"]');
  }`),
	); err != nil {
		t.Fatal(err)
	}
	removed := *initial
	removed.Members = nil
	for _, member := range initial.Members {
		if member.DeviceID != "PAIR-LEFT" && member.DeviceID != "PAIR-RIGHT" {
			removed.Members = append(removed.Members, member)
		}
	}
	generation := master.BeginZoneRefresh()
	if !master.ApplyPolledZone(generation, "ZONE-MASTER", &removed) {
		t.Fatal("pair removal did not change topology")
	}
	app.BroadcastDeviceList()
	if err := chromedp.Do(ctx, chromedp.Poll[chromedp.Void](`!document.querySelector('button[aria-label="Settings for Living Pair"]') && !window.__removedHeader.isConnected`)); err != nil {
		t.Fatal(err)
	}
	generation = master.BeginZoneRefresh()
	if !master.ApplyPolledZone(generation, "ZONE-MASTER", initial) {
		t.Fatal("pair rejoin did not change topology")
	}
	app.BroadcastDeviceList()
	if err := chromedp.Do(ctx,
		chromedp.Poll[chromedp.Void](`document.querySelector('button[aria-label="Settings for Living Pair"]') !== null`),
		chromedp.Evaluate[chromedp.Void](`(() => {
   const rejoined=document.querySelector('button[aria-label="Settings for Living Pair"]');
   if(rejoined.getAttribute('aria-expanded')!=='false')throw new Error('Rejoined member must start collapsed');
   const settings=rejoined.closest('.zone-logical-member').querySelector('.member-settings');
   if(!settings.hidden || [...settings.querySelectorAll('details')].some(n=>n.open))throw new Error('Rejoined physical settings must start collapsed');
   if(document.querySelector('.zone-member-details')!==window.__survivingZone || !window.__survivingZone.open ||
    !window.__survivingHeader.isConnected || window.__survivingHeader.getAttribute('aria-expanded')!=='true' ||
    !window.__survivingPhysical.isConnected || !window.__survivingPhysical.open)throw new Error('Surviving disclosures must retain their mounted expanded view');
  })()`),
	); err != nil {
		t.Fatal(err)
	}
	if mutations.Load() != 0 {
		t.Fatal("presentation lifecycle mutated topology")
	}
}
