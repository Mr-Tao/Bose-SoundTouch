//go:build browsertest

package soundtouchweb

import (
	"net/http/httptest"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/go-chi/chi/v5"
)

func TestSoundSettingsRendersReportedCacheBeforePendingRefresh(t *testing.T) {
	app := NewWebApp()
	router := chi.NewRouter()
	app.Mount(router, nil)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(chromedp.CSS(".nav-discover-icon")),
		chromedp.Evaluate[chromedp.Void](`(async () => {
   const [{SoundSettings},{api},{h,render}] = await Promise.all([
    import('/app/static/js/components/SoundSettings.js'), import('/app/static/js/api.js'),import('/app/static/lib/preact.module.js')]);
   const check=(ok,message)=>{if(!ok)throw new Error(message)};
   const wait=async pred=>{for(let n=0;n<200;n++){if(pred())return;await new Promise(r=>setTimeout(r,10));}throw new Error('Sound refresh did not settle')};
   const original={settings:api.settings,soundSettings:api.soundSettings};
   const root=document.createElement('div');document.body.append(root);
   let reads=[];
   api.settings=()=>{throw new Error('Acoustic controls must not request full device settings')};
   api.soundSettings=id=>new Promise(resolve=>reads.push({id,resolve}));
   const caps={DeviceID:'speaker',BassAvailable:true,BassMin:-9,BassMax:0,BassDefault:0};
   const target={controlId:'speaker.local',physicalId:'speaker',name:'Speaker',status:{epoch:1,revision:2,connectivity:'online',bass:{ActualBass:-3},bassCapabilities:caps}};
   const device={info:{device_id:'speaker'},settingsTargets:[target],status:target.status};
   const show=d=>render(h(SoundSettings,{deviceId:'speaker.local',device:d,nested:true,active:true}),root);
   try {
    show({...device,settingsTargets:[{...target,status:{...target.status,bass:{ActualBass:-3,DeviceID:'replacement'}}}]});
    check(!root.querySelector('.sound-control'),'Contradictory bass identity must not enable cached capabilities');
    show(device);
    check(root.querySelectorAll('.sound-control').length===1,'First render must use the reported cache without waiting for GET');
    check(!root.querySelector('.sound-control button').disabled,'Reported cached control should be usable');
    await wait(()=>reads.length===1);
    check(!root.querySelector('.sound-control button').disabled,'Pending refresh must not hide or disable valid cached sound');
    reads.shift().resolve({success:false,error:'Acoustic read unavailable'});
    await wait(()=>root.textContent.includes('Acoustic read unavailable')&&root.querySelector('.sound-control button').disabled);
    root.querySelector('.settings-error button').click();
    await wait(()=>reads.length===1);
    check(root.querySelector('.sound-control button').disabled,'Retry must not revive cache before a fresh answer');
    reads.shift().resolve({success:true,data:{targetIdentity:'speaker',statusEpoch:1,statusRevision:2,bassCapabilities:{...caps,BassAvailable:false}}});
    await wait(()=>!root.textContent.includes('Acoustic read unavailable'));
    check(root.querySelectorAll('.sound-control').length===1&&root.querySelector('.sound-control button').disabled,'Fresh unavailable result must override cache while retaining geometry');
    const replacement={...target,status:{...target.status,epoch:2,bassCapabilities:undefined}};
    show({...device,settingsTargets:[replacement],status:replacement.status});
    check(!root.querySelector('.sound-control'),'New connection must not inherit previous capabilities or geometry');
    await wait(()=>reads.length===1);
    reads.shift().resolve({success:true,data:{targetIdentity:'speaker',statusEpoch:1,bassCapabilities:caps,bass:{ActualBass:-1}}});
    await wait(()=>root.textContent.includes('physical speaker changed'));
    check(!root.querySelector('.sound-control'),'Old epoch response must not enable replacement connection');

    const group={ID:'pair',MasterDeviceID:'right',Roles:{Roles:[{DeviceID:'left',Role:'LEFT'},{DeviceID:'right',Role:'RIGHT'}]}};
    const physical=(id,role)=>({controlId:id+'.local',physicalId:id,role,status:{epoch:3,revision:1,connectivity:'online',group,bass:{ActualBass:0},bassCapabilities:{...caps,DeviceID:id}}});
    const left=physical('left','LEFT'),right=physical('right','RIGHT');
    const pair={info:{device_id:'right'},stereoPair:{id:'pair',masterDeviceId:'right',members:[{deviceId:'left',role:'LEFT'},{deviceId:'right',role:'RIGHT'}]},status:{epoch:3,group},settingsTargets:[right,left]};
    render(h(SoundSettings,{deviceId:'right.local',device:pair,nested:true,active:true}),root);
    check(root.querySelectorAll('.sound-control').length===1,'Cached stereo pair must expose one shared bass immediately');
    check(!root.querySelector('.sound-control button').disabled,'Both cached physical ranges must enable pair bass');
    await wait(()=>reads.length===2);
    check(reads.every(read=>['left.local','right.local'].includes(read.id)),'Refresh must verify both physical members');
    reads.splice(0).forEach(({id,resolve})=>resolve({success:true,data:{targetIdentity:id.split('.')[0],statusEpoch:3,bassCapabilities:{...caps,DeviceID:id.split('.')[0]},bass:{ActualBass:0}}}));
   } finally {render(null,root);root.remove();Object.assign(api,original);}
  })()`, chromedp.EvalAwaitPromise),
	); err != nil {
		t.Fatal(err)
	}
}
