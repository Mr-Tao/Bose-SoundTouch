//go:build browsertest

package soundtouchweb

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/go-chi/chi/v5"
)

const libraryTabsFixture = `
import { h, render } from 'preact';
import { useState } from 'preact/hooks';
import { LibraryTabs } from '/app/static/js/components/LibraryTabs.js';
import { useFavorites } from '/app/static/js/useFavorites.js';
window.played = [];
window.unhandled = [];
window.addEventListener('unhandledrejection', e => window.unhandled.push(String(e.reason)));
function Fixture() {
  const [selectedTab, onTabChange] = useState('presets');
  const [revision, setRevision] = useState(1);
  const favorites = useFavorites();
  window.favorites = favorites;
  window.renderVolume = () => setRevision(v => v + 1);
  return h('div', { class: 'device-detail' }, h(LibraryTabs, {
    deviceId: 'speaker', device: { info: { device_id: 'physical-speaker' } },
    selectedTab, onTabChange, favorites,
    status: { revision, recentsRevision: 1, epoch: 'one', nowPlaying: { Source: 'STANDBY' },
      presets: { Preset: [{ ID: 1, ContentItem: { Source: 'TUNEIN', Location: 's1', ItemName: 'One' } }] } },
    onPlay: item => window.played.push(item),
  }));
}
render(h(Fixture), document.getElementById('fixture'));
`

func TestLibraryTabsKeyboardAndFavorites(t *testing.T) {
	var mu sync.Mutex
	entries := []map[string]any{{"id": "first", "content_item": map[string]any{
		"Source": "TUNEIN", "Location": "s1", "ItemName": "One",
	}}}
	reads, adds, removes := 0, 0, 0
	server := newPlayerFixtureServer(t, libraryTabsFixture, func(r chi.Router) {
		r.Get("/api/control/favorites", func(w http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			reads++
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"available": true, "entries": entries}})
		})
		r.Post("/api/control/favorites", func(w http.ResponseWriter, req *http.Request) {
			var item map[string]any
			_ = json.NewDecoder(req.Body).Decode(&item)
			mu.Lock()
			defer mu.Unlock()
			adds++
			entry := map[string]any{"id": "second", "content_item": item}
			entries = append(entries, entry)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": entry})
		})
		r.Delete("/api/control/favorites/{id}", func(w http.ResponseWriter, req *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			removes++
			for i, entry := range entries {
				if entry["id"] == chi.URLParam(req, "id") {
					entries = append(entries[:i], entries[i+1:]...)
					break
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true})
		})
		r.Get("/api/control/catalog", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
				"available": true, "entries": []map[string]any{{"source": "TUNEIN", "location": "s2", "name": "Two"}},
			}})
		})
		r.Get("/api/control/devices/speaker/recents", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"Items": []any{}}})
		})
	})
	ctx := newHeadlessChromeContext(t)
	var slots, playback, unhandled int
	var keyboard, hidden, overflow, nested bool
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(320, 700),
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.preset-slot`)),
		chromedp.Poll[chromedp.Void](`window.favorites?.available === true && !window.favorites.loading`),
		evalInto(`document.querySelectorAll('.preset-slot').length`, &slots),
		chromedp.Evaluate[chromedp.Void](`document.querySelector('[role=tab]').focus()`),
		chromedp.KeyEvent(kb.ArrowRight),
		evalInto(`document.activeElement.textContent === 'Favorites' && document.querySelector('[aria-selected=true]').textContent === 'Presets'`, &keyboard),
		chromedp.KeyEvent(kb.End),
		chromedp.KeyEvent(kb.Home),
		chromedp.KeyEvent(kb.ArrowRight),
		chromedp.KeyEvent(" "),
		chromedp.WaitVisible(chromedp.CSS(`.favorite-play`)),
		evalInto(`document.querySelectorAll('[role=tabpanel][hidden]').length === 2 && [...document.querySelectorAll('[role=tabpanel][hidden] button')].every(el => el.getClientRects().length === 0)`, &hidden),
		evalInto(`document.documentElement.scrollWidth > innerWidth + 1 || document.querySelector('.library-tabs').scrollWidth > document.querySelector('.library-tabs').clientWidth + 1`, &overflow),
		evalInto(`window.played.length`, &playback),
		chromedp.Evaluate[chromedp.Void](`window.renderVolume(); window.renderVolume()`),
		chromedp.Click(chromedp.CSS(`.favorites-add`)),
		chromedp.WaitVisible(chromedp.CSS(`.catalog-entry`)),
		chromedp.Click(chromedp.CSS(`.catalog-entry`)),
		chromedp.Poll[chromedp.Void](`document.querySelectorAll('.favorite-row').length === 2 && !window.favorites.loading && !window.favorites.busy`),
		chromedp.Click(chromedp.CSS(`.favorite-remove[aria-label="Remove Two from favorites"]`)),
		chromedp.Poll[chromedp.Void](`document.querySelectorAll('.favorite-row').length === 1 && !window.favorites.loading && !window.favorites.busy`),
		evalInto(`window.played.length`, &playback),
		evalInto(`!!document.querySelector('button button')`, &nested),
		evalInto(`window.unhandled.length`, &unhandled),
	); err != nil {
		t.Fatalf("library browser run: %v", err)
	}
	if slots != 6 || !keyboard || !hidden || overflow || nested || playback != 0 || unhandled != 0 {
		t.Errorf("slots=%d keyboard=%v hidden=%v overflow=%v nested=%v playback=%d unhandled=%d", slots, keyboard, hidden, overflow, nested, playback, unhandled)
	}
	mu.Lock()
	defer mu.Unlock()
	if adds != 1 || removes != 1 || reads != 3 {
		t.Errorf("requests: GET=%d POST=%d DELETE=%d; want one initial read and one read per mutation", reads, adds, removes)
	}
}

func TestFavoritesReadFailureRetainsLibraryAndRetries(t *testing.T) {
	const fixture = `
import { h, render } from 'preact';
import { useFavorites } from '/app/static/js/useFavorites.js';
import { Favorites } from '/app/static/js/components/Favorites.js';
import { api } from '/app/static/js/api.js';
window.mode = 'ready';
api.favorites = async () => {
  if (window.mode === 'error') throw new Error('Library temporarily unreachable');
  return { success: true, data: { available: window.mode !== 'unavailable', entries: window.mode === 'empty' || window.mode === 'unavailable' ? [] : [
    { id: 'kept', content_item: { Source: 'TUNEIN', Location: 's1', ItemName: 'Kept station' } }
  ] } };
};
function Fixture() {
  window.favorites = useFavorites();
  return h(Favorites, { deviceId: 'speaker', favorites: window.favorites, onPlay() {} });
}
render(h(Fixture), document.getElementById('fixture'));
`
	server := newMetadataFixtureServer(t, fixture)
	ctx := newHeadlessChromeContext(t)
	var retained, retried, empty, unavailable bool
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.favorite-row`)),
		chromedp.Evaluate[chromedp.Void](`window.mode = 'error'; window.favorites.refresh()`),
		chromedp.WaitVisible(chromedp.CSS(`.favorites-error`)),
		evalInto(`document.querySelectorAll('.favorite-row').length === 1 && !document.querySelector('.favorites-empty')`, &retained),
		chromedp.Evaluate[chromedp.Void](`window.mode = 'ready'`),
		chromedp.Click(chromedp.CSS(`.favorites-retry`)),
		chromedp.Poll[chromedp.Void](`!window.favorites.error && !window.favorites.loading`),
		evalInto(`!document.querySelector('.favorites-error') && document.querySelectorAll('.favorite-row').length === 1`, &retried),
		chromedp.Evaluate[chromedp.Void](`window.mode = 'empty'; window.favorites.refresh()`),
		chromedp.WaitVisible(chromedp.CSS(`.favorites-empty`)),
		evalInto(`document.querySelector('.favorites-empty').textContent.includes('No favorites yet')`, &empty),
		chromedp.Evaluate[chromedp.Void](`window.mode = 'unavailable'; window.favorites.refresh()`),
		chromedp.Poll[chromedp.Void](`window.favorites.available === false && !window.favorites.loading`),
		evalInto(`!document.querySelector('.favorites-empty') && document.querySelector('.favorites-section').textContent.includes('unavailable')`, &unavailable),
	); err != nil {
		t.Fatalf("favorites retry browser run: %v", err)
	}
	if !retained || !retried || !empty || !unavailable {
		t.Errorf("retained=%v retried=%v empty=%v unavailable=%v", retained, retried, empty, unavailable)
	}
}

// Deferred reads make the ordering deterministic: the first answer is older
// than the write, and the second stays pending while the old one is inspected.
func TestFavoritesIgnoresStaleReadAfterWrite(t *testing.T) {
	const fixture = `
import { h, render } from 'preact';
import { useState } from 'preact/hooks';
import { useFavorites } from '/app/static/js/useFavorites.js';
import { api } from '/app/static/js/api.js';
window.reads = [];
window.writes = 0;
window.unhandled = [];
window.addEventListener('unhandledrejection', e => window.unhandled.push(String(e.reason)));
api.favorites = options => new Promise(resolve => window.reads.push({ resolve, signal: options.signal }));
const entry = { id: 'saved', content_item: { Source: 'TUNEIN', Location: 's2', ItemName: 'Saved' } };
api.addFavorite = async () => { window.writes++; return { success: true, data: entry }; };
function Fixture() {
  const [token, setToken] = useState(0);
  window.invalidate = () => setToken(t => t + 1);
  window.favorites = useFavorites(token);
  return h('span', {}, window.favorites.entries.map(e => e.id).join(','));
}
window.unmount = () => render(null, document.getElementById('fixture'));
render(h(Fixture), document.getElementById('fixture'));
`
	server := newMetadataFixtureServer(t, fixture)
	ctx := newHeadlessChromeContext(t)
	var preserved, identity, aborted bool
	var writes, unhandled int
	if err := chromedp.Do(ctx,
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.Poll[chromedp.Void](`window.reads?.length === 1`),
		chromedp.Evaluate[chromedp.Void](`window.invalidate(); window.favorites.refresh(); window.favorites.refresh()`),
		chromedp.Evaluate[chromedp.Void](`window.favorites.add({ Source: 'TUNEIN', Location: 's2' }).catch(() => {})`),
		chromedp.Poll[chromedp.Void](`window.favorites.entries.length === 1`),
		chromedp.Evaluate[chromedp.Void](`window.reads[0].resolve({ success: true, data: { available: true, entries: [] } })`),
		chromedp.Poll[chromedp.Void](`window.reads.length === 2`),
		evalInto(`window.favorites.entries[0]?.id === 'saved'`, &preserved),
		evalInto(`window.favorites.has({ Source: 'TUNEIN', SourceAccount: 'TUNEIN', Location: 's2', ItemName: 'Changed' })`, &identity),
		evalInto(`window.writes`, &writes),
		chromedp.Evaluate[chromedp.Void](`window.unmount()`),
		chromedp.Poll[chromedp.Void](`window.reads[1].signal.aborted`),
		evalInto(`window.reads[1].signal.aborted`, &aborted),
		evalInto(`window.unhandled.length`, &unhandled),
	); err != nil {
		t.Fatalf("favorites ordering browser run: %v", err)
	}
	if !preserved || !identity || !aborted || writes != 1 || unhandled != 0 {
		t.Errorf("preserved=%v identity=%v aborted=%v writes=%d unhandled=%d", preserved, identity, aborted, writes, unhandled)
	}
}

// Exercise the real detail wiring: save actions never play, and a favorite
// uses the checked, reconciled content command on the currently selected device.
func TestDeviceLibrarySavingNavigationAndCheckedPlayback(t *testing.T) {
	const fixture = `
import { h, render } from 'preact';
import { useState } from 'preact/hooks';
import { DeviceDetail, mergeStatusUpdate } from '/app/static/js/app.js';
import { api } from '/app/static/js/api.js';
window.saved = []; window.playWrites = []; window.favoriteWrites = 0; window.readbacks = 0;
let revision = 1;
let selected = 'one';
const initial = { epoch: 1, revision: 1, nowPlayingRevision: 1,
 nowPlaying: { Source: 'TUNEIN', PlayStatus: 'PLAY_STATE', StationName: 'Current station',
  ContentItem: { Source: 'TUNEIN', SourceAccount: 'TUNEIN', Location: 's1', ItemName: 'Current station' } },
 volume: { ActualVolume: 0, MuteEnabled: false }, presets: { Preset: [] }, sources: { SourceItem: [] } };
api.favorites = async () => ({ success: true, data: { available: true, entries: window.saved } });
api.addFavorite = async item => {
 window.favoriteWrites++;
 const entry = { id: 'fav'+window.favoriteWrites, content_item: item };
 window.saved = [...window.saved, entry]; return { success: true, data: entry };
};
api.recents = async () => ({ success: true, data: { Items: [{ ID: 'recent', ContentItem: {
 Source: 'LOCAL_INTERNET_RADIO', Location: 'http://192.0.2.10:8769/ct24.aac', ItemName: 'Recent station', Type: 'station' } }] } });
api.playChecked = async (id, item) => { window.playWrites.push({ id, item }); return { success: true }; };
api.deviceNowPlaying = async id => {
 window.readbacks++;
 const item = window.playWrites.at(-1).item;
 const next = { ...initial, revision: ++revision, nowPlayingRevision: revision, nowPlaying: {
 Source: item.source, PlayStatus: 'PLAY_STATE', ContentItem: { Source: item.source,
 Location: item.location, SourceAccount: item.sourceAccount, Type: item.type } } };
 return { success: true, data: { info: { device_id: id.toUpperCase() }, status: next } };
};
function Fixture() {
 const [id, setId] = useState('one');
 const [tab, setTab] = useState('presets');
 const [devices, setDevices] = useState({
 one: { info: { name: 'Speaker one', device_id: 'ONE' }, status: initial },
 two: { info: { name: 'Speaker two', device_id: 'TWO' }, status: initial },
 });
 window.switchDevice = () => { selected = selected === 'one' ? 'two' : 'one'; setId(selected); };
 return h(DeviceDetail, { key: id, deviceId: id, devices, onBack() {},
 libraryTab: tab, onLibraryTabChange: setTab, commandReadbackDelays: [50, 150, 300],
 onStatusReadback: (id, status) => setDevices(old => mergeStatusUpdate(old, id, status)),
 });
}
render(h(Fixture), document.getElementById('fixture'));
`
	server := newPlayerFixtureServer(t, fixture, func(r chi.Router) {
		r.Get("/api/control/devices/*", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":true,"data":{}}`)) })
	})
	ctx := newHeadlessChromeContext(t)
	var pending, scoped, savedFields, selected, noNested bool
	var writesBeforePlay, writes, readbacks, favoriteWrites int
	if err := chromedp.Do(ctx,
		chromedp.EmulateViewport(390, 844),
		chromedp.Navigate(server.URL+"/fixture"),
		chromedp.WaitVisible(chromedp.CSS(`.now-playing .favorite-save`)),
		chromedp.Click(chromedp.CSS(`.now-playing .favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.saved.length===1`),
		chromedp.Click(chromedp.CSS(`[role=tab][id$="-recents"]`)),
		chromedp.WaitVisible(chromedp.CSS(`.recent-row .favorite-save`)),
		chromedp.Click(chromedp.CSS(`.recent-row .favorite-save`)),
		chromedp.Poll[chromedp.Void](`window.saved.length===2`),
		evalInto(`window.playWrites.length`, &writesBeforePlay),
		evalInto(`window.saved[1].content_item.Location==='http://192.0.2.10:8769/ct24.aac' && window.saved[1].content_item.Type==='station'`, &savedFields),
		chromedp.Click(chromedp.CSS(`[role=tab][id$="-favorites"]`)),
		chromedp.WaitVisible(chromedp.CSS(`.favorite-row`)),
		chromedp.Evaluate[chromedp.Void](`window.switchDevice()`),
		chromedp.Poll[chromedp.Void](`document.querySelector('.device-detail-name')?.textContent==='Speaker two' || document.body.textContent.includes('Speaker two')`),
		chromedp.WaitVisible(chromedp.CSS(`.favorite-row`)),
		evalInto(`document.querySelector('[role=tab][aria-selected=true]').textContent==='Favorites'`, &selected),
		chromedp.Click(chromedp.CSS(`.favorite-play[aria-label="Play Recent station"]`)),
		evalInto(`document.querySelector('.favorite-play').disabled`, &pending),
		chromedp.Poll[chromedp.Void](`document.querySelector('.discrete-command-status')?.textContent==='Favorite started'`),
		evalInto(`window.playWrites.length`, &writes),
		evalInto(`window.favoriteWrites`, &favoriteWrites),
		evalInto(`window.readbacks`, &readbacks),
		evalInto(`window.playWrites[0].id==='two' && window.playWrites[0].item.location==='http://192.0.2.10:8769/ct24.aac'`, &scoped),
		evalInto(`!document.querySelector('button button')`, &noNested),
	); err != nil {
		t.Fatalf("real library detail wiring: %v", err)
	}
	if writesBeforePlay != 0 || writes != 1 || favoriteWrites != 2 || readbacks != 3 || !pending || !scoped || !savedFields || !selected || !noNested {
		t.Errorf("before=%d play=%d saves=%d reads=%d pending=%v scoped=%v fields=%v selected=%v siblings=%v", writesBeforePlay, writes, favoriteWrites, readbacks, pending, scoped, savedFields, selected, noNested)
	}
}
