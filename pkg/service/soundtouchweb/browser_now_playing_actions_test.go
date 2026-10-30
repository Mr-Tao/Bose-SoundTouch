//go:build browsertest

package soundtouchweb

import (
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const nowPlayingActionsFixture = `
import {h,render} from 'preact';
import {useState} from 'preact/hooks';
import {api} from '/app/static/js/api.js';
import {NowPlaying} from '/app/static/js/components/NowPlaying.js';
import {useFavorites} from '/app/static/js/useFavorites.js';
import {usePresetMutations} from '/app/static/js/usePresetMutations.js';
import {PresetMutationDialog} from '/app/static/js/components/PresetMutationDialog.js';
const item=name=>({Source:'TUNEIN',Location:'station-'+name,ItemName:name,IsPresetable:true});
const playing=name=>({Source:'TUNEIN',Track:name,Artist:'Artist',Album:'Album',ContentItem:item(name),Time:{Position:20,Total:120}});
let bank=[null,null,item('Occupied'),null,null,null],saved=[],physical='PHYSICAL';
window.favoriteWrites=[];window.presetWrites=[];
api.favorites=async()=>({success:true,data:{available:true,entries:structuredClone(saved)}});
api.addFavorite=async content=>{
 window.favoriteWrites.push({action:'add',content});
 if(window.deferFavorite)await new Promise(resolve=>window.releaseFavorite=resolve);
 if(window.failFavorite)throw Error('Favorite write failed');
 const entry={id:'saved',content_item:content};saved.push(entry);return {success:true,data:entry};
};
api.removeFavorite=async id=>{window.favoriteWrites.push({action:'remove',id});saved=saved.filter(e=>e.id!==id);return {success:true}};
const presets=()=>({Preset:bank.flatMap((ContentItem,index)=>ContentItem?[{ID:index+1,ContentItem}]:[])});
api.devicePresets=async()=>({success:true,data:{info:{device_id:physical},status:{epoch:1,presets:presets()}}});
api.storePresetContent=async(id,slot,content)=>{window.presetWrites.push({id,slot,content});bank[slot-1]={Source:content.source,Location:content.location,ItemName:content.itemName};return {success:true}};
function Fixture(){
 const [id,setId]=useState('speaker'),[nowPlaying,setNowPlaying]=useState(playing('Original')),[bankView,setBankView]=useState(presets),[favoritesAvailable,setFavoritesAvailable]=useState(true);
 const favorites=useFavorites();
 const mutations=usePresetMutations({deviceId:id,device:{info:{device_id:physical},status:{epoch:1}},onStatusReadback:(_id,next)=>setBankView(next.presets)});
 window.changeContent=()=>setNowPlaying(playing('Replacement'));
 window.changeTarget=()=>{physical='OTHER';setId('other');};
 window.standby=()=>setNowPlaying({Source:'STANDBY'});
 window.refreshContent=()=>setNowPlaying(structuredClone(nowPlaying));
 window.favoriteOnly=()=>setNowPlaying({...nowPlaying,ContentItem:{...nowPlaying.ContentItem,IsPresetable:false}});
 window.setFavoritesAvailable=setFavoritesAvailable;
 return h('div',null,h('button',{id:'outside'},'Outside'),h('div',{id:'now-playing-host'},h(NowPlaying,{nowPlaying,deviceId:id,presets:bankView,favorites:{...favorites,available:favoritesAvailable && favorites.available},presetMutations:mutations})),h(PresetMutationDialog,{mutations}));
}
render(h(Fixture),document.getElementById('fixture'));
`

func TestNowPlayingActionsFavoritesAndPresetConfirmation(t *testing.T) {
	server := newMetadataFixtureServer(t, nowPlayingActionsFixture)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.favorite-save') && !document.querySelector('.preset-picker-btn') && document.querySelector('.track-title').textContent==='Original' && document.querySelector('.progress-time').textContent==='0:20 / 2:00'`),
		chromedp.Focus(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-menu .favorite-save`)),
		chromedp.Poll[chromedp.Void](`document.activeElement.matches('.favorite-save') && window.favoriteWrites.length===0 && window.presetWrites.length===0`),
		chromedp.Click(chromedp.CSS(`.favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===1 && window.favoriteWrites[0].content.ItemName==='Original' && !document.querySelector('.now-playing-actions-menu')`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.favorite-save')?.textContent==='Remove from Favorites'`),
		chromedp.Click(chromedp.CSS(`.favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===2 && window.favoriteWrites[1].action==='remove'`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.refreshContent()`),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-menu .preset-picker-btn`)),
		chromedp.Click(chromedp.CSS(`.preset-picker-btn`)),
		chromedp.Click(chromedp.CSS(`.preset-picker-slot:nth-child(3)`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-menu') && document.querySelector('.preset-outcome').textContent.includes('Original') && document.querySelector('.preset-outcome').textContent.includes('Occupied') && window.presetWrites.length===0`),
		chromedp.Click(chromedp.CSS(`.preset-confirm`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
		chromedp.Poll[chromedp.Void](`window.presetWrites.length===1 && window.presetWrites[0].id==='speaker' && window.presetWrites[0].content.itemName==='Original'`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestNowPlayingActionsCancelStaleActions(t *testing.T) {
	server := newMetadataFixtureServer(t, nowPlayingActionsFixture)
	for name, cancel := range map[string]string{
		"content": `window.changeContent()`,
		"target":  `window.changeTarget()`,
		"standby": `window.standby()`,
		"hidden":  `document.getElementById('now-playing-host').hidden=true`,
		"blur":    `window.dispatchEvent(new Event('blur'))`,
		"focus":   `document.getElementById('outside').focus()`,
		"scroll":  `document.dispatchEvent(new Event('scroll'))`,
		"outside": `document.getElementById('outside').click()`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := newHeadlessChromeContext(t)
			if err := chromedp.Do(ctx,
				chromedp.Navigate(server.URL+"/fixture"),
				chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
				chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
				chromedp.Click(chromedp.CSS(`.preset-picker-btn`)),
				chromedp.Evaluate[chromedp.Void](`window.oldFavorite=document.querySelector('.favorite-save');window.oldSlot=document.querySelector('.preset-picker-slot')`),
				chromedp.Evaluate[chromedp.Void](cancel),
				chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-menu')`),
				chromedp.Evaluate[chromedp.Void](`window.oldFavorite.click();window.oldSlot.click()`),
				chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===0 && window.presetWrites.length===0 && !document.querySelector('.preset-confirm')`),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-menu') && document.activeElement.matches('.now-playing-actions-toggle')`),
		chromedp.Evaluate[chromedp.Void](`window.deferFavorite=true;window.failFavorite=true`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.favoriteWrites.length===1 && !!window.releaseFavorite`),
		chromedp.Evaluate[chromedp.Void](`window.changeContent();window.releaseFavorite()`),
		chromedp.Poll[chromedp.Void](`document.querySelector('.track-title').textContent==='Replacement' && !document.querySelector('.now-playing-actions-menu') && !document.querySelector('[role=alert]') && window.favoriteWrites[0].content.ItemName==='Original'`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestNowPlayingActionsCapabilityLossDoesNotRestoreMenu(t *testing.T) {
	server := newMetadataFixtureServer(t, nowPlayingActionsFixture)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.favoriteOnly()`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.WaitVisible(chromedp.CSS(`.favorite-save`)),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.preset-picker-btn')`),
		chromedp.Evaluate[chromedp.Void](`window.oldFavorite=document.querySelector('.favorite-save');window.setFavoritesAvailable(false)`),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-toggle')`),
		chromedp.Evaluate[chromedp.Void](`window.setFavoritesAvailable(true)`),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.refreshContent();window.oldFavorite.click()`),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-menu') && document.querySelector('.now-playing-actions-toggle').getAttribute('aria-expanded')==='false' && window.favoriteWrites.length===0 && window.presetWrites.length===0`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.WaitVisible(chromedp.CSS(`.favorite-save`)),
		chromedp.KeyEvent(kb.Escape),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-menu') && document.activeElement.matches('.now-playing-actions-toggle')`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestNowPlayingActionsFavoriteFailureKeepsRetry(t *testing.T) {
	server := newMetadataFixtureServer(t, nowPlayingActionsFixture)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Evaluate[chromedp.Void](`window.failFavorite=true`),
		chromedp.Click(chromedp.CSS(`.now-playing-actions-toggle`)),
		chromedp.Click(chromedp.CSS(`.favorite-save`)),
		chromedp.Poll[chromedp.Void](`document.querySelector('.now-playing-actions-menu [role=alert]')?.textContent==='Favorite write failed' && !document.querySelector('.favorite-save').disabled && window.favoriteWrites.length===1 && window.presetWrites.length===0`),
		chromedp.Evaluate[chromedp.Void](`window.failFavorite=false`),
		chromedp.Click(chromedp.CSS(`.favorite-save`)),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.now-playing-actions-menu') && !document.querySelector('[role=alert]') && window.favoriteWrites.length===2 && window.favoriteWrites.every(write=>write.content.ItemName==='Original') && window.presetWrites.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}
