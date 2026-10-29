//go:build browsertest

package soundtouchweb

import (
	"context"
	"fmt"
	"testing"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
	"github.com/go-chi/chi/v5"
)

// Existing isolated-component fixtures retain their HTTP write/payload
// assertions while gaining the same controller and fresh firmware-bank model.
const presetFixtureHelpers = `
import { useState as usePresetFixtureState } from 'preact/hooks';
import { api as presetFixtureAPI } from '/app/static/js/api.js';
import { usePresetMutations as useFixturePresetMutations } from '/app/static/js/usePresetMutations.js';
import { PresetMutationDialog as FixturePresetDialog } from '/app/static/js/components/PresetMutationDialog.js';
function preparePresetBank(presets, physicalId='fixture-speaker') {
  let bank=structuredClone(presets || {Preset:[]}),revision=1;
  const store=presetFixtureAPI.storePresetContent,clear=presetFixtureAPI.removePreset;
  presetFixtureAPI.devicePresets=async()=>({success:true,data:{info:{device_id:physicalId},status:{epoch:1,revision:++revision,presets:structuredClone(bank)}}});
  presetFixtureAPI.storePresetContent=async(id,slot,item,options)=>{
    const result=await store(id,slot,item,options);
    if(result.success){
      bank.Preset=(bank.Preset||[]).filter(p=>p.ID!==slot && !(p.ContentItem?.Source===item.source && p.ContentItem?.Location===item.location));
      bank.Preset.push({ID:slot,ContentItem:{Source:item.source,SourceAccount:item.sourceAccount||'',Location:item.location,Type:item.type||'',ItemName:item.itemName||'',ContainerArt:item.containerArt||''}});
    }
    return result;
  };
  presetFixtureAPI.removePreset=async(id,slot,options)=>{const result=await clear(id,slot,options);if(result.success)bank.Preset=(bank.Preset||[]).filter(p=>p.ID!==slot);return result;};
}
function renderSafePresetComponent(Component, props) {
  const initial={...props.status,epoch:1,presets:props.status?.presets||props.presets||{Preset:[]}};
  preparePresetBank(initial.presets);
  function Fixture(){
    const [status,setStatus]=usePresetFixtureState(initial);
    const mutations=useFixturePresetMutations({deviceId:props.deviceId,device:{info:{device_id:'fixture-speaker'},status},
      onStatusReadback:(_id,next)=>setStatus(previous=>({...previous,...next}))});
    return h('section',{id:'presets'},h(Component,{...props,status,presets:status.presets,presetMutations:mutations}),h(FixturePresetDialog,{mutations}));
  }
  render(h(Fixture),document.getElementById('fixture'));
}
`

// All hardware APIs are replaced inside the fixture. These tests never call
// a speaker, and deliberately emit no WebSocket notification after a store.
const presetFeedbackFixture = `
import { h, render } from 'preact';
import { useState } from 'preact/hooks';
import { api } from '/app/static/js/api.js';
import { usePresetMutations } from '/app/static/js/usePresetMutations.js';
import { PresetMutationDialog } from '/app/static/js/components/PresetMutationDialog.js';
import { Presets } from '/app/static/js/components/Presets.js';
import { CatalogPicker } from '/app/static/js/components/CatalogPicker.js';
const item = name => ({Source:'TUNEIN',SourceAccount:'',Location:name,ItemName:name,Type:'',ContainerArt:''});
let bank = [item('Station A'),null,item('Station B'),null,null,null];
let revision = 1;
window.writes = []; window.reads = []; window.picks = [];
const status = () => ({epoch:1, revision:++revision, nowPlaying:{Source:'TUNEIN',ContentItem:item('Station A')},
  presets:{Preset:bank.flatMap((ContentItem,index) => ContentItem ? [{ID:index+1,ContentItem}] : [])}});
api.storedPresets = async () => ({success:true,data:{disagrees:false}});
api.catalog = async () => ({success:true,data:{available:true,entries:[{source:'TUNEIN',location:'Catalog Station',name:'Catalog Station'}]}});
api.devicePresets = async (id,options) => {
  window.reads.push({id,headers:options.headers});
  if(window.hold) await new Promise(resolve => window.release = resolve);
  if(window.writes.length && window.readFail) throw Error('Read failed');
  if(window.drift){bank[5]=item('External station');window.drift=false;}
  return {success:true,data:{info:{device_id:'physical'},status:status()}};
};
api.storePresetContent = async (id,slot,content,options) => {
  window.writes.push({id,slot,content,headers:options.headers});
  if(!window.mismatch){
    if(!window.copy)bank=bank.map(entry=>entry?.Location===content.location?null:entry);
    bank[slot-1]={Source:content.source,SourceAccount:content.sourceAccount,Location:content.location,
      ItemName:content.itemName,Type:content.type,ContainerArt:content.containerArt};
  }
  return {success:true};
};
api.removePreset=async(id,slot)=>{window.writes.push({id,slot,method:'DELETE'});if(window.failClear)return{success:false};bank[slot-1]=null;return{success:true};};
function Fixture(){
  const [deviceId,setDeviceId]=useState('speaker');
  const [currentStatus,setStatus]=useState(status);
  window.changeDevice=()=>setDeviceId('other');
  const device={info:{device_id:'physical'},status:currentStatus};
  const mutations=usePresetMutations({deviceId,device,onStatusReadback:(_id,next)=>setStatus(next)});
  return h('div',null,
    h(Presets,{deviceId,status:currentStatus,presetMutations:mutations}),
    h(PresetMutationDialog,{mutations}),
    h('div',{id:'favorite-catalog'},h(CatalogPicker,{deviceId,onPick:ci=>window.picks.push(ci)})));
}
render(h(Fixture),document.getElementById('fixture'));
`

func presetFixture(t *testing.T) string {
	t.Helper()
	return newPlayerFixtureServer(t, presetFeedbackFixture, func(chi.Router) {}).URL + "/fixture"
}

func TestPresetFeedbackConfirmationMoveAndNoop(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	var warning string
	var writes int
	if err := chromedp.Do(ctx,
		chromedp.Navigate(presetFixture(t)),
		chromedp.WaitVisible(chromedp.CSS(`[aria-label="Edit preset 3"]`)),
		chromedp.Click(chromedp.CSS(`[aria-label="Edit preset 3"]`)),
		chromedp.Click(chromedp.CSS(`.preset-save-current`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
		textInto(chromedp.CSS(`.preset-outcome`), &warning),
		evalInto(`window.writes.length`, &writes),
	); err != nil {
		t.Fatal(err)
	}
	if writes != 0 || warning != "Move “Station A” from preset 1 to preset 3. The speaker may empty the old slot. Replace “Station B” in preset 3." {
		t.Fatalf("preconfirmation writes=%d warning=%q", writes, warning)
	}
	if err := chromedp.Do(ctx,
		chromedp.Click(chromedp.CSS(`.preset-cancel`)),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.preset-safety-dialog') && window.writes.length===0`),
		chromedp.Click(chromedp.CSS(`.preset-save-current`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
		chromedp.Evaluate[chromedp.Void](`{const button=document.querySelector('.preset-confirm');button.click();button.click();}`),
		chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
		chromedp.Poll[chromedp.Void](`window.writes.length===1 && window.writes[0].headers['X-AfterTouch-Preset-Target']==='physical' &&
            document.querySelectorAll('.preset-name')[0].textContent.includes('Empty') &&
            document.querySelectorAll('.preset-name')[2].textContent==='Station A'`),
		chromedp.Click(chromedp.CSS(`.preset-cancel`)),
		chromedp.Click(chromedp.CSS(`.preset-save-current`)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.unchanged`)),
		chromedp.Poll[chromedp.Void](`window.writes.length===1`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPresetFeedbackDriftAndReadbackFailures(t *testing.T) {
	for _, failure := range []string{"mismatch", "readFail"} {
		t.Run(failure, func(t *testing.T) {
			ctx := newHeadlessChromeContext(t)
			if err := chromedp.Do(ctx,
				chromedp.Navigate(presetFixture(t)),
				chromedp.WaitVisible(chromedp.CSS(`[aria-label="Edit preset 3"]`)),
				chromedp.Click(chromedp.CSS(`[aria-label="Edit preset 3"]`)),
				chromedp.Click(chromedp.CSS(`.preset-save-current`)),
				chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
				chromedp.Evaluate[chromedp.Void](`window.drift=true`),
				chromedp.Click(chromedp.CSS(`.preset-confirm`)),
				chromedp.Poll[chromedp.Void](`document.querySelector('.preset-outcome')?.textContent.includes('Presets changed') && window.writes.length===0`),
				chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`window.%s=true`, failure)),
				chromedp.Click(chromedp.CSS(`.preset-confirm`)),
				chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.unverified`)),
				chromedp.Poll[chromedp.Void](`window.writes.length===1 && window.reads.length===6 && !document.querySelector('.preset-outcome.confirmed')`),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPresetFeedbackDeviceChangeAndFavoriteSelector(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(presetFixture(t)),
		chromedp.WaitVisible(chromedp.CSS(`[aria-label="Edit preset 3"]`)),
		chromedp.Click(chromedp.CSS(`#favorite-catalog .catalog-entry`)),
		chromedp.Poll[chromedp.Void](`window.picks.length===1 && window.writes.length===0 && window.reads.length===0`),
		chromedp.Click(chromedp.CSS(`[aria-label="Edit preset 3"]`)),
		chromedp.Evaluate[chromedp.Void](`window.hold=true`),
		chromedp.Click(chromedp.CSS(`.preset-save-current`)),
		chromedp.Poll[chromedp.Void](`typeof window.release==='function'`),
		chromedp.Evaluate[chromedp.Void](`window.changeDevice()`),
		chromedp.Poll[chromedp.Void](`!document.querySelector('.preset-safety-dialog')`),
		chromedp.Evaluate[chromedp.Void](`window.release()`),
		chromedp.Poll[chromedp.Void](`window.writes.length===0 && !document.querySelector('.preset-confirm')`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPresetFeedbackTouchControlsLightAndDark(t *testing.T) {
	for _, theme := range []string{"light", "dark"} {
		t.Run(theme, func(t *testing.T) {
			ctx := newHeadlessChromeContext(t)
			var readable bool
			if err := chromedp.Do(ctx,
				chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
					if _, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 390, Height: 844, DeviceScaleFactor: 1, Mobile: true}); err != nil {
						return err
					}
					if _, err := chromedp.Call(ctx, emulation.SetTouchEmulationEnabled, emulation.SetTouchEmulationEnabledParams{Enabled: true}); err != nil {
						return err
					}
					_, err := chromedp.Call(ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: theme}}})
					return err
				}),
				chromedp.Navigate(presetFixture(t)),
				chromedp.WaitVisible(chromedp.CSS(`.preset-edit-control`)),
				evalInto(`Array.from(document.querySelectorAll('.preset-edit-control')).every(button=>{
                  const box=button.getBoundingClientRect(),style=getComputedStyle(button);
                  return box.width>=44 && box.height>=44 && style.opacity==='1' && style.color!==style.backgroundColor;
                }) && document.documentElement.scrollWidth<=390 && window.writes.length===0`, &readable),
			); err != nil {
				t.Fatal(err)
			}
			if !readable {
				t.Fatal("Edit controls must be visible, touch-sized and fit the viewport")
			}
		})
	}
}

func TestPresetFeedbackExplicitMoveClearsOnlyRetainedCopy(t *testing.T) {
	for _, copy := range []bool{false, true} {
		t.Run(fmt.Sprint(copy), func(t *testing.T) {
			ctx := newHeadlessChromeContext(t)
			if err := chromedp.Do(ctx,
				chromedp.Navigate(presetFixture(t)),
				chromedp.WaitVisible(chromedp.CSS(`[aria-label="Edit preset 1"]`)),
				chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`window.copy=%t`, copy)),
				chromedp.Click(chromedp.CSS(`[aria-label="Edit preset 1"]`)),
				chromedp.Click(chromedp.CSS(`.catalog-move-slot:nth-child(2)`)),
				chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
				chromedp.Poll[chromedp.Void](`window.writes.length===0 && document.querySelector('.preset-outcome').textContent.includes('Preset 1 will be cleared')`),
				chromedp.Click(chromedp.CSS(`.preset-confirm`)),
				chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
				chromedp.Poll[chromedp.Void](fmt.Sprintf(`window.writes.length===%d &&
                    document.querySelectorAll('.preset-name')[0].textContent.includes('Empty') &&
                    document.querySelectorAll('.preset-name')[2].textContent==='Station A'`, map[bool]int{false: 1, true: 2}[copy])),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}
