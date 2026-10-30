//go:build browsertest

package soundtouchweb

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/go-chi/chi/v5"
)

// The complete detail view exercises its scoped styles and keeps all speaker
// operations mocked. The existing metadata-only fixture is not detail-scoped.
const deviceHeadingFixture = `
import {h,render} from 'preact';
import {DeviceDetail} from '/app/static/js/app.js';
import {api} from '/app/static/js/api.js';
api.favorites=async()=>({success:true,data:{available:true,entries:[]}});
api.recents=async()=>({success:true,data:{Items:[]}});
window.headingWrites=[];
api.control=(...args)=>headingWrites.push(args);
api.storePresetContent=(...args)=>headingWrites.push(args);
const long='A long speaker name without breakpoints '+ 'LongName'.repeat(20);
window.renderHeading=(mode='playing',name='ložnice')=>{
 const standby=mode==='standby';
 const source=mode==='airplay'?'AIRPLAY':'TUNEIN';
 const nowPlaying=standby?{Source:'STANDBY'}:{Source:source,PlayStatus:mode==='buffering'?'BUFFERING_STATE':mode==='paused'?'PAUSE_STATE':'PLAY_STATE',Track:'News '+ 'VeryLongUnbrokenTitle'.repeat(8),Artist:'Artist',Album:'Album',Time:{Position:25,Total:200},ContentItem:{Source:source,Location:'station',ItemName:'Station',IsPresetable:true}};
 render(h(DeviceDetail,{deviceId:'speaker',onBack(){},devices:{speaker:{info:{name:name==='long'?long:name,type:'SoundTouch 10',device_id:'PHYSICAL'},status:{epoch:1,revision:1,nowPlaying,presets:{Preset:[]},sources:{SourceItem:[]},volume:{ActualVolume:10,MuteEnabled:false}}}}}),document.getElementById('fixture'));
};
renderHeading();
`

func TestDeviceHeadingAndCompactNowPlaying(t *testing.T) {
	server := newPlayerFixtureServer(t, deviceHeadingFixture, func(r chi.Router) {
		r.Get("/api/control/devices/*", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":true,"data":{}}`)) })
	})
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(320, 844),
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.device-detail-heading`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.device-detail-name').textContent==='ložnice' && document.querySelector('.device-detail-model').textContent==='SoundTouch 10' && document.querySelector('.now-playing-label').textContent==='Now playing'`),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing .favorite-save`)),
		chromedp.Click(chromedp.CSS(`.now-playing-actions .preset-picker-btn`)),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions .preset-picker-overlay`)),
		chromedp.Poll[chromedp.Void](`document.documentElement.scrollWidth<=320 && [...document.querySelectorAll('.now-playing-actions .preset-picker-overlay .preset-picker-slot')].every(b=>{const r=b.getBoundingClientRect();return r.left>=0 && r.right<=320 && r.width>=44 && r.height>=44}) && window.headingWrites.length===0`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`renderHeading('airplay','long')`),
		chromedp.WaitVisible(chromedp.CSS(`.track-details`)),
		chromedp.Poll[chromedp.Void](`document.documentElement.scrollWidth<=320 && document.querySelector('.device-detail-name').textContent.startsWith('A long speaker') && document.querySelector('.track-title').title.length>80 && !!document.querySelector('.progress-row') && !document.querySelector('.now-playing .favorite-save')`),
		chromedp.Evaluate[chromedp.Void](`renderHeading('buffering','obývák stereo')`),
		chromedp.WaitVisible(chromedp.CSS(`.buffering-badge`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.device-detail-name').textContent==='obývák stereo' && document.documentElement.scrollWidth<=320`),
		chromedp.Evaluate[chromedp.Void](`renderHeading('standby')`),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing.standby`)),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing .favorite-save') && !document.querySelector('.now-playing-actions .preset-picker-btn') && !document.querySelector('.now-playing-label') && window.headingWrites.length===0`),
	); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{390, 768} {
		if err := chromedp.Do(ctx,
			chromedp.EmulateViewport(int64(width), 900),
			chromedp.Evaluate[chromedp.Void](`renderHeading('playing','long')`),
			chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
			chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
			chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions .preset-picker-btn`)),
			chromedp.Click(chromedp.CSS(`.now-playing-actions .preset-picker-btn`)),
			chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions .preset-picker-overlay`)),
			chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.documentElement.scrollWidth<=%d && document.querySelector('.now-playing-actions .preset-picker-overlay').getBoundingClientRect().left>=0 && document.querySelector('.now-playing-actions .preset-picker-overlay').getBoundingClientRect().right<=%d`, width, width)),
			chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		); err != nil {
			t.Fatalf("viewport %d: %v", width, err)
		}
	}
}
