//go:build browsertest

package soundtouchweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
	"github.com/go-chi/chi/v5"
)

const presetHoldFixture = `
import { h, render } from 'preact';
import { Presets } from '/app/static/js/components/Presets.js';
const item = (location, name) => ({Source:'TUNEIN', Location:location, ItemName:name});
const initialPresets = {Preset:[
  {ID:1, ContentItem:item('s10001', 'Station A')},
  {ID:3, ContentItem:item('s10003', 'Station B')},
]};
const root = document.getElementById('fixture');
let deviceId = 'speaker', physicalId = 'SPEAKER', epoch = 1;
let nowPlaying = {Source:'TUNEIN', ContentItem:item('s10001', 'Station A')};
window.plays = [];
function showPresets() {
  render(h(Presets, {
    deviceId, physicalId,
    status: {epoch, revision: 1, nowPlaying, presets:initialPresets},
    onSelect: preset => window.plays.push(preset.ID),
  }), root);
}
window.changeDevice = () => { deviceId = 'other'; showPresets(); };
window.changePhysicalId = () => { physicalId = 'OTHER'; showPresets(); };
window.changeEpoch = () => { epoch++; showPresets(); };
window.changePlayback = () => {
  nowPlaying = {Source:'TUNEIN', ContentItem:item('s10002', 'New station')};
  showPresets();
};
showPresets();
`

const holdStationA = `{"ID":1,"ContentItem":{"Source":"TUNEIN","Location":"s10001","ItemName":"Station A"}}`
const holdStationB = `{"ID":3,"ContentItem":{"Source":"TUNEIN","Location":"s10003","ItemName":"Station B"}}`

type presetHoldRequest struct {
	method, slot, name string
}

type presetHoldBank struct {
	mu    sync.Mutex
	calls []presetHoldRequest
	drift bool
	saved string
}

func (b *presetHoldBank) checkCalls(calls ...presetHoldRequest) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(_ context.Context, _ *chromedp.Target) error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !slices.Equal(b.calls, calls) {
			return fmt.Errorf("preset requests = %v, want %v", b.calls, calls)
		}
		return nil
	})
}

func presetHoldPage(t *testing.T, bank *presetHoldBank) string {
	t.Helper()
	server := newPlayerFixtureServer(t, presetHoldFixture, func(r chi.Router) {
		checkTarget := func(w http.ResponseWriter, req *http.Request) bool {
			if chi.URLParam(req, "id") == "speaker" && req.Header.Get("X-AfterTouch-Preset-Target") == "SPEAKER" &&
				req.Header.Get("X-AfterTouch-Preset-Epoch") == "1" {
				return true
			}
			t.Errorf("%s %s did not identify the speaker and epoch", req.Method, req.URL.Path)
			w.WriteHeader(http.StatusConflict)
			return false
		}
		r.Get("/api/control/catalog", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"success":true,"data":{"available":false,"entries":[]}}`))
		})
		r.Get("/api/control/devices/{id}/presets", func(w http.ResponseWriter, req *http.Request) {
			if !checkTarget(w, req) {
				return
			}
			if req.Header.Get("Cache-Control") != "no-cache" && req.Header.Get("Cache-Control") != "no-store" {
				t.Errorf("fresh bank read used Cache-Control %q", req.Header.Get("Cache-Control"))
			}
			bank.mu.Lock()
			defer bank.mu.Unlock()
			bank.calls = append(bank.calls, presetHoldRequest{method: http.MethodGet})
			entries := []string{holdStationA, holdStationB}
			switch {
			case bank.saved != "":
				entries = append(entries, bank.saved)
			case bank.drift && len(bank.calls) > 1:
				entries = append(entries, `{"ID":2,"ContentItem":{"Source":"TUNEIN","Location":"s10005","ItemName":"External station"}}`)
			}
			writePresetFixtureBank(w, entries...)
		})
		r.Post("/api/control/devices/{id}/preset/{slot}", func(w http.ResponseWriter, req *http.Request) {
			if !checkTarget(w, req) {
				return
			}
			slot := chi.URLParam(req, "slot")
			var content struct{ Source, Location, ItemName string }
			if err := json.NewDecoder(req.Body).Decode(&content); err != nil {
				t.Errorf("decode stored preset content: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if slot != "2" || content.Source != "TUNEIN" || content.Location != "s10002" || content.ItemName != "New station" {
				t.Errorf("unexpected preset write to slot %s: %+v", slot, content)
				w.WriteHeader(http.StatusConflict)
				return
			}
			bank.mu.Lock()
			defer bank.mu.Unlock()
			bank.calls = append(bank.calls, presetHoldRequest{method: http.MethodPost, slot: slot, name: content.ItemName})
			bank.saved = `{"ID":2,"ContentItem":{"Source":"TUNEIN","Location":"s10002","ItemName":"New station"}}`
			_, _ = w.Write([]byte(`{"success":true}`))
		})
	})
	return server.URL + "/fixture"
}

func presetSlotSelector(slot int) string {
	return fmt.Sprintf(`.preset-slot[data-preset-slot="%d"]`, slot)
}

func openPresetEditor(slot int) chromedp.Action[chromedp.Void] {
	const picker = `select[aria-label="Preset to edit"]`
	return chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		return chromedp.Do(ctx,
			chromedp.WaitVisible(chromedp.CSS(`.preset-edit-control[aria-label="Edit presets"]`)),
			chromedp.Evaluate[chromedp.Void](`{
				const button = document.querySelector('.preset-edit-control');
				if (button.getAttribute('aria-expanded') !== 'true') button.click();
			}`),
			chromedp.WaitVisible(chromedp.CSS(picker)),
			chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`{
				const select = document.querySelector(%q);
				select.value = %s;
				select.dispatchEvent(new Event('change', { bubbles: true }));
			}`, picker, strconv.Quote(strconv.Itoa(slot)))),
			chromedp.Poll[chromedp.Void](fmt.Sprintf(`document.querySelector(%q).value === %q`, picker, strconv.Itoa(slot))),
		)
	})
}

type presetPointerPoint struct{ X, Y float64 }

func locatePresetPointer(slot int, point *presetPointerPoint) chromedp.Action[chromedp.Void] {
	return evalInto(fmt.Sprintf(`(() => {
		const r = document.querySelector(%q).getBoundingClientRect();
		return {X:r.x + 30, Y:r.y + r.height / 2};
	})()`, presetSlotSelector(slot)), point)
}

// CDP pointer input exercises pointer capture and browser-generated clicks.
func presetPointer(point *presetPointerPoint, typ input.DispatchMouseEventType) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		return chromedp.Do(ctx, chromedp.MouseEvent(typ, point.X, point.Y, chromedp.ButtonLeft, chromedp.ClickCount(1)))
	})
}

func TestPresetGridTapAndKeyboardUseSharedEditor(t *testing.T) {
	bank := &presetHoldBank{}
	ctx := newHeadlessChromeContext(t)
	read := presetHoldRequest{method: http.MethodGet}
	if err := chromedp.Do(ctx,
		chromedp.Navigate(presetHoldPage(t, bank)),
		chromedp.WaitVisible(chromedp.CSS(presetSlotSelector(1))),
		chromedp.Poll[chromedp.Void](`document.querySelectorAll('.preset-slot').length === 6 &&
			document.querySelectorAll('.preset-edit-control').length === 1 &&
			!document.querySelector('.preset-edit-btn, .preset-save-btn')`),
		chromedp.Click(chromedp.CSS(presetSlotSelector(1))),
		chromedp.Poll[chromedp.Void](`window.plays.join(',') === '1'`),
		chromedp.Focus(chromedp.CSS(presetSlotSelector(3))),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Poll[chromedp.Void](`window.plays.join(',') === '1,3'`),
		chromedp.Click(chromedp.CSS(presetSlotSelector(2))),
		chromedp.WaitVisible(chromedp.CSS(`select[aria-label="Preset to edit"]`)),
		chromedp.Poll[chromedp.Void](`document.querySelectorAll('select[aria-label="Preset to edit"]').length === 1 &&
			document.querySelector('select[aria-label="Preset to edit"]').value === '2' &&
			!document.querySelector('.preset-save-current').disabled && window.plays.join(',') === '1,3'`),
		bank.checkCalls(),
		chromedp.Evaluate[chromedp.Void](`window.changePlayback()`),
		chromedp.Focus(chromedp.CSS(`.preset-save-current`)),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
		bank.checkCalls(read),
		chromedp.Focus(chromedp.CSS(`.preset-confirm`)),
		chromedp.KeyEvent(kb.Enter),
		chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
		bank.checkCalls(read, read, presetHoldRequest{method: http.MethodPost, slot: "2", name: "New station"}, read),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPresetHoldReleaseChecksBankBeforeConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, warning string
		slot          int
		change        bool
	}{
		{name: "occupied", slot: 3, change: true, warning: `text.includes('Station B') && text.includes('New station')`},
		{name: "duplicate", slot: 2, warning: `text.includes('Station A') && text.includes('preset 1')`},
		{name: "occupied and duplicate", slot: 3, warning: `text.includes('Station B') && text.includes('Station A') && text.includes('preset 1')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := &presetHoldBank{}
			ctx := newHeadlessChromeContext(t)
			var point presetPointerPoint
			if err := chromedp.Do(ctx,
				chromedp.Navigate(presetHoldPage(t, bank)),
				chromedp.WaitVisible(chromedp.CSS(presetSlotSelector(tc.slot))),
				chromedp.Evaluate[chromedp.Void](fmt.Sprintf(`if (%t) window.changePlayback();`, tc.change)),
				locatePresetPointer(tc.slot, &point),
				presetPointer(&point, chromedp.MousePressed),
				chromedp.WaitVisible(chromedp.CSS(`.preset-slot.preset-hold-ready`)),
				bank.checkCalls(),
				presetPointer(&point, chromedp.MouseReleased),
				chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
				chromedp.Poll[chromedp.Void](`(() => {
					const text = document.querySelector('.preset-outcome').textContent;
					return window.plays.length === 0 && `+tc.warning+`;
				})()`),
				bank.checkCalls(presetHoldRequest{method: http.MethodGet}),
				chromedp.Click(chromedp.CSS(`.preset-cancel`)),
				chromedp.Poll[chromedp.Void](`!document.querySelector('.preset-confirm') && window.plays.length === 0`),
				bank.checkCalls(presetHoldRequest{method: http.MethodGet}),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPresetHoldSavesOnlyAfterTwoFreshReads(t *testing.T) {
	for _, tc := range []struct {
		name  string
		drift bool
	}{{name: "empty"}, {name: "changed after preflight", drift: true}} {
		t.Run(tc.name, func(t *testing.T) {
			bank := &presetHoldBank{drift: tc.drift}
			ctx := newHeadlessChromeContext(t)
			var point presetPointerPoint
			read := presetHoldRequest{method: http.MethodGet}
			if err := chromedp.Do(ctx,
				chromedp.Navigate(presetHoldPage(t, bank)),
				chromedp.WaitVisible(chromedp.CSS(presetSlotSelector(2))),
				chromedp.Evaluate[chromedp.Void](`window.changePlayback()`),
				locatePresetPointer(2, &point),
				presetPointer(&point, chromedp.MousePressed),
				chromedp.WaitVisible(chromedp.CSS(`.preset-slot.preset-hold-ready`)),
				bank.checkCalls(),
				presetPointer(&point, chromedp.MouseReleased),
			); err != nil {
				t.Fatal(err)
			}
			if tc.drift {
				if err := chromedp.Do(ctx,
					chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
					chromedp.Poll[chromedp.Void](`document.querySelector('.preset-outcome').textContent.includes('External station')`),
					bank.checkCalls(read, read),
					chromedp.Click(chromedp.CSS(`.preset-confirm`)),
				); err != nil {
					t.Fatal(err)
				}
			}
			calls := []presetHoldRequest{read, read}
			if tc.drift {
				calls = append(calls, read)
			}
			calls = append(calls, presetHoldRequest{method: http.MethodPost, slot: "2", name: "New station"}, read)
			if err := chromedp.Do(ctx,
				chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
				chromedp.Poll[chromedp.Void](`document.querySelector('.preset-outcome.confirmed').textContent.includes('Preset 2 saved.') &&
					!document.querySelector('.preset-confirm') && window.plays.length === 0`),
				bank.checkCalls(calls...),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPresetHoldCancellationAndTargetFences(t *testing.T) {
	for _, tc := range []struct{ name, cancel string }{
		{"early release", ""},
		{"pointer cancel", `document.querySelector('.preset-holding').dispatchEvent(new PointerEvent('pointercancel', {bubbles:true, pointerId:1, isPrimary:true}))`},
		{"blur", `window.dispatchEvent(new Event('blur'))`},
		{"multitouch", `document.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, pointerId:22, pointerType:'touch', isPrimary:false}))`},
		{"device", `window.changeDevice()`},
		{"physical ID", `window.changePhysicalId()`},
		{"epoch", `window.changeEpoch()`},
		{"playback", `window.changePlayback()`},
		{"hidden panel", `document.getElementById('fixture').hidden = true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bank := &presetHoldBank{}
			ctx := newHeadlessChromeContext(t)
			var point presetPointerPoint
			if err := chromedp.Do(ctx,
				chromedp.Navigate(presetHoldPage(t, bank)),
				chromedp.WaitVisible(chromedp.CSS(presetSlotSelector(3))),
				locatePresetPointer(3, &point),
				presetPointer(&point, chromedp.MousePressed),
				chromedp.WaitVisible(chromedp.CSS(`.preset-slot.preset-holding`)),
			); err != nil {
				t.Fatal(err)
			}
			if tc.cancel != "" {
				if err := chromedp.Do(ctx,
					chromedp.Evaluate[chromedp.Void](tc.cancel),
					chromedp.Poll[chromedp.Void](`!document.querySelector('.preset-holding, .preset-hold-ready')`),
				); err != nil {
					t.Fatal(err)
				}
			}
			if err := chromedp.Do(ctx,
				presetPointer(&point, chromedp.MouseReleased),
				chromedp.Evaluate[chromedp.Void](`document.getElementById('fixture').hidden = false`),
				chromedp.Sleep(100*time.Millisecond),
				chromedp.Poll[chromedp.Void](`window.plays.length === 0 &&
					!document.querySelector('.preset-holding, .preset-hold-ready, .preset-confirm')`),
				bank.checkCalls(),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPresetHoldTouchScrollCancelsWithoutPlaybackOrWrites(t *testing.T) {
	bank := &presetHoldBank{}
	ctx := newHeadlessChromeContext(t)
	var point presetPointerPoint
	if err := chromedp.Do(ctx,
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			if _, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 390, Height: 844, DeviceScaleFactor: 1, Mobile: true}); err != nil {
				return err
			}
			_, err := chromedp.Call(ctx, emulation.SetTouchEmulationEnabled, emulation.SetTouchEmulationEnabledParams{Enabled: true})
			return err
		}),
		chromedp.Navigate(presetHoldPage(t, bank)),
		chromedp.WaitVisible(chromedp.CSS(presetSlotSelector(3))),
		chromedp.Poll[chromedp.Void](`(() => {
			const edit = document.querySelector('.preset-edit-control').getBoundingClientRect();
			const grid = document.querySelector('.preset-grid').getBoundingClientRect();
			return edit.top >= grid.bottom && edit.width >= 44 && edit.height >= 44 &&
				document.documentElement.scrollWidth <= 390;
		})()`),
		chromedp.Evaluate[chromedp.Void](`document.body.style.minHeight = '2400px'`),
		locatePresetPointer(3, &point),
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			for _, event := range []struct {
				typ input.DispatchTouchEventType
				dy  float64
			}{
				{input.DispatchTouchEventTypeTouchStart, 0},
				{input.DispatchTouchEventTypeTouchMove, -35},
				{input.DispatchTouchEventTypeTouchMove, -100},
				{input.DispatchTouchEventTypeTouchEnd, 0},
			} {
				var points []*input.TouchPoint
				if event.typ != input.DispatchTouchEventTypeTouchEnd {
					points = []*input.TouchPoint{{X: point.X, Y: point.Y + event.dy}}
				}
				if _, err := chromedp.Call(ctx, input.DispatchTouchEvent, input.DispatchTouchEventParams{Type: event.typ, TouchPoints: points}); err != nil {
					return err
				}
			}
			return nil
		}),
		chromedp.Poll[chromedp.Void](`window.scrollY > 0 && window.plays.length === 0 &&
			!document.querySelector('.preset-holding, .preset-hold-ready, .preset-confirm')`),
		bank.checkCalls(),
	); err != nil {
		t.Fatal(err)
	}
}
