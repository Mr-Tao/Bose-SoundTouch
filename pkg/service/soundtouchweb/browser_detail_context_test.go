//go:build browsertest

package soundtouchweb

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/go-chi/chi/v5"
)

const detailContextFixture = `
import {h,render} from 'preact';
import {DeviceDetail} from '/app/static/js/app.js';
import {api} from '/app/static/js/api.js';
window.contextWrites=[];
api.favorites=async()=>({success:true,data:{available:true,entries:[]}});
api.recents=async()=>({success:true,data:{Items:[]}});
api.control=(...args)=>contextWrites.push(args);
api.storePresetContent=(...args)=>contextWrites.push(args);
let revision=0;
window.renderContext=(kind='single',connectivity='online')=>{
 const device={info:{device_id:'LEFT',name:'Bedroom',type:'SoundTouch 10'},status:{epoch:1,revision:++revision,connectivity,isConnected:connectivity==='online',nowPlaying:{Source:'TUNEIN',StationName:'ČRo Plus',PlayStatus:'PLAY_STATE',ContentItem:{Source:'TUNEIN',Location:'s1',ItemName:'ČRo Plus',IsPresetable:true}},presets:{Preset:['ČRo Plus','Radiožurnál','WDR 4','ČT24','Proglas','Jazz'].map((name,n)=>({ID:n+1,ContentItem:{Source:'TUNEIN',Location:'s'+(n+1),ItemName:name}}))},sources:{SourceItem:[{Source:'TUNEIN',DisplayName:'TuneIn',Status:'READY'}]},volume:{ActualVolume:20,TargetVolume:20,MuteEnabled:false}}};
 if(kind==='pair'||kind==='member') {device.info.name='Living pair';device.stereoPair={id:'pair',name:'Living pair',masterDeviceId:'LEFT',members:[{deviceId:'LEFT',name:'Living left',role:'LEFT',controlId:'speaker'},{deviceId:'RIGHT',name:'Living right',role:'RIGHT',controlId:'right'}]};}
 if(kind==='group') device.zone={masterControlId:'speaker',memberCount:2,physicalMemberCount:3,members:[{controlId:'speaker',name:'Bedroom',deviceIds:['LEFT'],available:true},{controlId:'pair',name:'Living pair',kind:'stereoPair',deviceIds:['PAIRLEFT','PAIRRIGHT'],available:true}]};
 if(kind==='member') device.zoneMembership={masterControlId:'master',masterName:'Kitchen'};
 render(h(DeviceDetail,{deviceId:'speaker',devices:{speaker:device},onBack(){},onStatusReadback(){}}),document.getElementById('fixture'));
};
renderContext();
`

func TestDetailContextReflectsTargetAndFreshness(t *testing.T) {
	server := newPlayerFixtureServer(t, detailContextFixture, func(r chi.Router) {
		r.Get("/api/control/devices/*", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":true,"data":{}}`)) })
	})
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(320, 844), chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(".device-detail-name")),
		chromedp.Poll[chromedp.Void](`document.querySelector('.device-detail-name').textContent==='Bedroom' && document.querySelector('.device-detail-model').textContent==='SoundTouch 10' && document.querySelector('.volume-label label').textContent==='Volume'`),
		chromedp.Evaluate[chromedp.Void](`renderContext('group')`),
		chromedp.Poll[chromedp.Void](`document.querySelector('.device-detail-model').textContent==='Group · 2 members · 3 speakers' && document.querySelector('.volume-label label').textContent==='Group volume' && document.querySelector('.library-owner').textContent==='Presets · Bedroom'`),
		chromedp.Evaluate[chromedp.Void](`renderContext('member')`),
		chromedp.Poll[chromedp.Void](`document.querySelector('.device-detail-name').textContent==='Living pair' && document.querySelector('.device-detail-model').textContent==='Stereo pair · 2 speakers' && document.querySelector('.device-detail-membership').textContent==='In group · Kitchen' && document.querySelector('.volume-label label').textContent==='Member volume' && document.querySelector('.library-owner').textContent==='Presets · Living left'`),
		chromedp.Click(chromedp.CSS(`[role=tab][id$=favorites]`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.library-owner').textContent==='Shared library'`),
		chromedp.Click(chromedp.CSS(`[role=tab][id$=recents]`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.library-owner').textContent==='Recents · Living left'`),
		chromedp.Evaluate[chromedp.Void](`renderContext('pair','offline')`),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.device-detail-membership') && document.querySelector('.volume-label label').textContent==='Volume' && document.querySelector('.device-detail-status').textContent==='Offline' && document.querySelector('.device-detail-freshness').textContent==='Last known'`),
		chromedp.Evaluate[chromedp.Void](`renderContext('single','online')`),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.device-detail-freshness') && document.querySelector('.device-detail-status').textContent==='Online' && !document.querySelector('.sources-section').open && document.documentElement.scrollWidth<=320 && contextWrites.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestDetailNavigationKeepsGlobalIdentity(t *testing.T) {
	app := newZoneMemberNavigationApp(t)
	server, mutations := newZoneMemberNavigationServer(t, app)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(390, 844), chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".zone-card")), chromedp.Click(chromedp.CSS(".zone-card")),
		chromedp.WaitVisible(chromedp.CSS(".device-detail-heading")),
		chromedp.Poll[chromedp.Void](`document.querySelector('.page-title').textContent==='Devices' && !document.querySelector('.page-title .sub-title') && document.querySelector('.device-detail-name').textContent==='Atrium' && document.querySelector('.device-detail-model').textContent==='Group · 4 members · 5 speakers' && document.querySelector('.brand img').getAttribute('src').endsWith('/logo.svg') && document.documentElement.scrollWidth<=390`),
	); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{320, 390, 768} {
		if err := chromedp.Do(ctx,
			chromedp.EmulateViewport(int64(width), 844),
			chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.documentElement.scrollWidth<=%d && document.querySelector('.navbar').getBoundingClientRect().height<=60`, width)),
			chromedp.Click(chromedp.CSS(`.detail-nav-menu > summary`)),
			chromedp.WaitVisible(chromedp.CSS(`.detail-nav-options`)),
			chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector('.detail-nav-options').getBoundingClientRect().left>=0 && document.querySelector('.detail-nav-options').getBoundingClientRect().right<=%d && document.querySelectorAll('.detail-nav-options a').length===6`, width)),
			chromedp.KeyEvent("\u001b"),
			chromedp.Poll[chromedp.Void](`!document.querySelector('.detail-navigation details[open]') && document.activeElement.matches('.detail-nav-menu > summary')`),
		); err != nil {
			t.Fatalf("navigation %d: %v", width, err)
		}
	}

	if mutations.Load() != 0 {
		t.Fatalf("view sent %d topology writes", mutations.Load())
	}
}
