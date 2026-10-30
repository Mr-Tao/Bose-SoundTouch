//go:build browsertest

package soundtouchweb

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/go-chi/chi/v5"
)

const recentActionsFixture = `
import {h,render} from 'preact';
import {useState} from 'preact/hooks';
import {api} from '/app/static/js/api.js';
import {Recents} from '/app/static/js/components/Recents.js';
import {useFavorites} from '/app/static/js/useFavorites.js';
import {usePresetMutations} from '/app/static/js/usePresetMutations.js';
import {PresetMutationDialog} from '/app/static/js/components/PresetMutationDialog.js';
const item=name=>({Source:'TUNEIN',Location:'recent-'+name,ItemName:name,IsPresetable:true,ContainerArt:''});
let rows=[{ID:'first',ContentItem:item('Historical A')},{ID:'second',ContentItem:item('Historical B')}];
let bank=[item('Existing'),null,item('Occupied'),null,null,null],saved=[],revision=1,physical='PHYSICAL';
window.plays=[];window.favoriteWrites=[];window.presetWrites=[];
api.recents=async()=>({success:true,data:{Items:structuredClone(rows)}});
api.favorites=async()=>({success:true,data:{available:true,entries:structuredClone(saved)}});
api.addFavorite=async content=>{
 window.favoriteWrites.push({action:'add',content});
 if(window.failFavorite)throw Error('Favorite write failed');
 const entry={id:'favorite-'+saved.length,content_item:content};saved.push(entry);return {success:true,data:entry};
};
api.removeFavorite=async id=>{window.favoriteWrites.push({action:'remove',id});saved=saved.filter(e=>e.id!==id);return {success:true}};
const status=()=>({epoch:1,revision:++revision,presets:{Preset:bank.flatMap((ContentItem,index)=>ContentItem?[{ID:index+1,ContentItem}]:[])},nowPlaying:{Source:'TUNEIN',ContentItem:item('Currently playing')}});
api.devicePresets=async()=>({success:true,data:{info:{device_id:physical},status:status()}});
api.storePresetContent=async(id,slot,content)=>{window.presetWrites.push({id,slot,content});bank[slot-1]={Source:content.source,Location:content.location,SourceAccount:content.sourceAccount,ItemName:content.itemName,Type:content.type,ContainerArt:content.containerArt};return {success:true}};
function Fixture(){
 const [id,setId]=useState('speaker'),[rev,setRev]=useState(1),[currentStatus,setStatus]=useState(status);
 const favorites=useFavorites();
 const mutations=usePresetMutations({deviceId:id,device:{info:{device_id:physical},status:currentStatus},onStatusReadback:(_id,next)=>setStatus(next)});
 window.changeTarget=()=>{physical='OTHER';setId('other');};
 window.replaceRecent=()=>{rows=[{ID:'first',ContentItem:item('Replacement')},rows[1]];setRev(v=>v+1);};
 window.reorderRecents=()=>{rows=[rows[1],rows[0]];setRev(v=>v+1);};
 window.removeRecent=()=>{rows=rows.slice(1);setRev(v=>v+1);};
 return h('div',null,h('div',{id:'recent-host'},h(Recents,{deviceId:id,physicalId:physical,epoch:1,revision:rev,presets:currentStatus.presets,favorites,presetMutations:mutations,onPlay:row=>window.plays.push({id,item:row.ContentItem})})),h(PresetMutationDialog,{mutations}));
}
render(h(Fixture),document.getElementById('fixture'));
`

func recentActionsURL(t *testing.T) string {
	t.Helper()
	return newPlayerFixtureServer(t, recentActionsFixture, func(chi.Router) {}).URL + "/fixture"
}

func recentPointer(index int, typ input.DispatchMouseEventType) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		var pt struct{ X, Y float64 }
		if err := chromedp.Do(ctx, evalInto(fmt.Sprintf(`(()=>{const r=document.querySelectorAll('.recent-item')[%d].getBoundingClientRect();return {X:r.x+25,Y:r.y+r.height/2};})()`, index), &pt)); err != nil {
			return err
		}
		return chromedp.Do(ctx, chromedp.MouseEvent(typ, pt.X, pt.Y, chromedp.ButtonLeft, chromedp.ClickCount(1)))
	})
}

func TestRecentActionsFavoriteAndHistoricalPreset(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(recentActionsURL(t)),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Focus(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-menu .favorite-save`)),
		chromedp.Click(chromedp.CSS(`.recent-actions-menu .favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===1 && window.favoriteWrites[0].content.ItemName==='Historical A' && window.plays.length===0 && window.presetWrites.length===0`),
		chromedp.Click(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-menu .favorite-save`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.recent-actions-menu .favorite-save').textContent.includes('Remove')`),
		chromedp.Click(chromedp.CSS(`.recent-actions-menu .favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===2 && window.favoriteWrites[1].action==='remove' && window.plays.length===0`),
		chromedp.Click(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.recent-actions-menu .preset-picker-btn`)),
		chromedp.Click(chromedp.CSS(`.recent-actions-menu .preset-picker-slot:nth-child(3)`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.preset-outcome').textContent.includes('Historical A') && document.querySelector('.preset-outcome').textContent.includes('Occupied') && window.presetWrites.length===0`),
		chromedp.Click(chromedp.CSS(`.preset-confirm`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
		chromedp.Poll[chromedp.Void](`window.presetWrites.length===1 && window.presetWrites[0].content.itemName==='Historical A' && window.presetWrites[0].content.location==='recent-Historical A' && window.plays.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestRecentActionsHoldAndChangedRows(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(recentActionsURL(t)),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-toggle`)),
		recentPointer(0, chromedp.MousePressed),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-menu`)),
		recentPointer(0, chromedp.MouseReleased),
		chromedp.Poll[chromedp.Void](`window.plays.length===0 && window.presetWrites.length===0 && window.favoriteWrites.length===0`),
		chromedp.Evaluate[chromedp.Void](`window.reorderRecents()`),
		chromedp.Poll[chromedp.Void](`document.querySelector('.recent-name').textContent==='Historical B'`),
		chromedp.Click(chromedp.CSS(`.recent-actions-menu .favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===1 && window.favoriteWrites[0].content.ItemName==='Historical A'`),
		chromedp.Click(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.changeTarget()`),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.recent-actions-menu')`),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.removeRecent()`),
		chromedp.Poll[chromedp.Void](`document.querySelectorAll('.recent-item').length===1 && !document.querySelector('.recent-actions-menu') && window.favoriteWrites.length===1`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestRecentActionsCancelAndFailures(t *testing.T) {
	for _, cancellation := range []string{
		`window.dispatchEvent(new Event('blur'))`,
		`document.getElementById('recent-host').hidden=true`,
		`window.replaceRecent()`,
	} {
		ctx := newHeadlessChromeContext(t)
		if err := chromedp.Do(ctx,
			chromedp.Navigate(recentActionsURL(t)),
			chromedp.WaitVisible(chromedp.CSS(`.recent-actions-toggle`)),
			recentPointer(0, chromedp.MousePressed),
			chromedp.Evaluate[chromedp.Void](cancellation),
			chromedp.Sleep(650*time.Millisecond),
			chromedp.Evaluate[chromedp.Void](`document.getElementById('recent-host').hidden=false`),
			recentPointer(0, chromedp.MouseReleased),
			chromedp.Poll[chromedp.Void](`!document.querySelector('.recent-actions-menu') && window.plays.length===0 && window.favoriteWrites.length===0 && window.presetWrites.length===0`),
		); err != nil {
			t.Fatalf("cancel %s: %v", cancellation, err)
		}
	}
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(recentActionsURL(t)),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.failFavorite=true`),
		chromedp.Click(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.recent-actions-menu .favorite-save`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('[role=alert]')?.textContent.includes('Favorite write failed') && window.favoriteWrites.length===1 && window.plays.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestRecentActionsTouchScrollAndNarrowLayout(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			if _, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 320, Height: 844, DeviceScaleFactor: 1, Mobile: true}); err != nil {
				return err
			}
			_, err := chromedp.Call(ctx, emulation.SetTouchEmulationEnabled, emulation.SetTouchEmulationEnabledParams{Enabled: true})
			return err
		}),
		chromedp.Navigate(recentActionsURL(t)),
		chromedp.WaitVisible(chromedp.CSS(`.recent-actions-toggle`)),
		chromedp.Poll[chromedp.Void](`document.documentElement.scrollWidth<=320 && [...document.querySelectorAll('.recent-actions-toggle')].every(b=>b.getBoundingClientRect().width>=44 && b.getBoundingClientRect().height>=44)`),
		chromedp.Evaluate[chromedp.Void](`document.body.style.minHeight='2400px'`),
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			var pt struct{ X, Y float64 }
			if err := chromedp.Do(ctx, evalInto(`(()=>{const r=document.querySelector('.recent-item').getBoundingClientRect();return {X:r.x+25,Y:r.y+r.height/2};})()`, &pt)); err != nil {
				return err
			}
			for _, e := range []struct {
				typ input.DispatchTouchEventType
				dy  float64
			}{{input.DispatchTouchEventTypeTouchStart, 0}, {input.DispatchTouchEventTypeTouchMove, -35}, {input.DispatchTouchEventTypeTouchMove, -100}, {input.DispatchTouchEventTypeTouchEnd, 0}} {
				points := []*input.TouchPoint{{X: pt.X, Y: pt.Y + e.dy}}
				if e.typ == input.DispatchTouchEventTypeTouchEnd {
					points = []*input.TouchPoint{}
				}
				if _, err := chromedp.Call(ctx, input.DispatchTouchEvent, input.DispatchTouchEventParams{Type: e.typ, TouchPoints: points}); err != nil {
					return err
				}
			}
			return nil
		}),
		chromedp.Poll[chromedp.Void](`window.scrollY>0 && !document.querySelector('.recent-actions-menu') && window.plays.length===0 && window.favoriteWrites.length===0 && window.presetWrites.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}
