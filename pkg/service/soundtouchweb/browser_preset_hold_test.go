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
)

// Real browser pointer input, rather than direct handler calls, exercises
// pointer capture, compatibility clicks and touch scrolling together.
func presetPointer(slot int, typ input.DispatchMouseEventType) chromedp.Action[chromedp.Void] {
	return chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
		var point struct{ X, Y float64 }
		if err := chromedp.Do(ctx, evalInto(fmt.Sprintf(`(()=>{const r=document.querySelectorAll('.preset-slot')[%d].getBoundingClientRect();return {X:r.x+30,Y:r.y+r.height/2};})()`, slot-1), &point)); err != nil {
			return err
		}
		return chromedp.Do(ctx, chromedp.MouseEvent(typ, point.X, point.Y, chromedp.ButtonLeft, chromedp.ClickCount(1)))
	})
}

func TestPresetHoldReleaseUsesExistingConfirmation(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(presetFixture(t)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-slot`)),
		presetPointer(3, chromedp.MousePressed),
		chromedp.Poll[chromedp.Void](`document.querySelector('.preset-hold-ready')!==null`),
		chromedp.Poll[chromedp.Void](`window.reads.length===0 && window.writes.length===0 && window.plays.length===0`),
		presetPointer(3, chromedp.MouseReleased),
		chromedp.WaitVisible(chromedp.CSS(`.preset-confirm`)),
		chromedp.Poll[chromedp.Void](`window.writes.length===0 && window.plays.length===0 && document.querySelector('.preset-outcome').textContent.includes('Replace “Station B”') && document.querySelector('.preset-outcome').textContent.includes('from preset 1')`),
		chromedp.Click(chromedp.CSS(`.preset-cancel`)),
		chromedp.Click(chromedp.CSS(`.preset-slot`)),
		chromedp.Poll[chromedp.Void](`window.plays.length===1 && window.plays[0]===1 && window.writes.length===0`),
		chromedp.Focus(chromedp.CSS(`.preset-slot`)),
		chromedp.KeyEvent(kb.Enter),
		chromedp.Poll[chromedp.Void](`window.plays.length===2 && window.writes.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPresetHoldCancellationAndTargetFences(t *testing.T) {
	cases := []struct{ name, cancel string }{
		{"early-release", ""},
		{"pointer-cancel", `document.querySelector('.preset-holding').dispatchEvent(new PointerEvent('pointercancel',{bubbles:true,pointerId:1,isPrimary:true}))`},
		{"blur", `window.dispatchEvent(new Event('blur'))`},
		{"multitouch", `document.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,pointerId:22,pointerType:'touch',isPrimary:false}))`},
		{"device", `window.changeDevice()`},
		{"epoch", `window.changeEpoch()`},
		{"playback", `window.changePlayback('External station')`},
		{"hidden-panel", `document.querySelector('.presets-section').closest('#fixture').hidden=true`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newHeadlessChromeContext(t)
			if err := chromedp.Do(ctx,
				chromedp.Navigate(presetFixture(t)),
				chromedp.WaitVisible(chromedp.CSS(`.preset-slot`)),
				presetPointer(3, chromedp.MousePressed),
				chromedp.Poll[chromedp.Void](`document.querySelector('.preset-holding')!==null`),
			); err != nil {
				t.Fatal(err)
			}
			if tc.cancel != "" {
				if err := chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](tc.cancel)); err != nil {
					t.Fatal(err)
				}
			}
			if err := chromedp.Do(ctx,
				presetPointer(3, chromedp.MouseReleased),
				chromedp.Evaluate[chromedp.Void](`document.getElementById('fixture').hidden=false`),
				chromedp.Sleep(100*time.Millisecond),
				chromedp.Poll[chromedp.Void](`window.reads.length===0 && window.writes.length===0 && window.plays.length===0 && !document.querySelector('.preset-holding')`),
			); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPresetHoldSavesNewContentOnlyAfterRelease(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Navigate(presetFixture(t)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-slot`)),
		chromedp.Evaluate[chromedp.Void](`window.changePlayback('New station')`),
		chromedp.Sleep(30*time.Millisecond),
		presetPointer(2, chromedp.MousePressed),
		chromedp.Poll[chromedp.Void](`document.querySelector('.preset-hold-ready')!==null`),
		chromedp.Poll[chromedp.Void](`window.writes.length===0 && window.reads.length===0`),
		presetPointer(2, chromedp.MouseReleased),
		chromedp.WaitVisible(chromedp.CSS(`.preset-outcome.confirmed`)),
		chromedp.Poll[chromedp.Void](`window.writes.length===1 && window.writes[0].slot===2 && window.writes[0].content.itemName==='New station' && document.querySelectorAll('.preset-name')[1].textContent==='New station' && window.plays.length===0`),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPresetHoldTouchScrollCancelsWithoutPlaybackOrWrites(t *testing.T) {
	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Do(ctx,
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			if _, err := chromedp.Call(ctx, emulation.SetDeviceMetricsOverride, emulation.SetDeviceMetricsOverrideParams{Width: 390, Height: 844, DeviceScaleFactor: 1, Mobile: true}); err != nil {
				return err
			}
			_, err := chromedp.Call(ctx, emulation.SetTouchEmulationEnabled, emulation.SetTouchEmulationEnabledParams{Enabled: true})
			return err
		}),
		chromedp.Navigate(presetFixture(t)),
		chromedp.WaitVisible(chromedp.CSS(`.preset-slot`)),
		chromedp.Evaluate[chromedp.Void](`document.body.style.minHeight='2400px'`),
		chromedp.Func(func(ctx context.Context, _ *chromedp.Target) error {
			var point struct{ X, Y float64 }
			if err := chromedp.Do(ctx, evalInto(`(()=>{const r=document.querySelectorAll('.preset-slot')[2].getBoundingClientRect();return {X:r.x+30,Y:r.y+r.height/2};})()`, &point)); err != nil {
				return err
			}
			for _, event := range []struct {
				typ input.DispatchTouchEventType
				dy  float64
			}{{input.DispatchTouchEventTypeTouchStart, 0}, {input.DispatchTouchEventTypeTouchMove, -35}, {input.DispatchTouchEventTypeTouchMove, -100}, {input.DispatchTouchEventTypeTouchEnd, 0}} {
				points := []*input.TouchPoint{{X: point.X, Y: point.Y + event.dy}}
				if event.typ == input.DispatchTouchEventTypeTouchEnd {
					points = []*input.TouchPoint{}
				}
				if _, err := chromedp.Call(ctx, input.DispatchTouchEvent, input.DispatchTouchEventParams{Type: event.typ, TouchPoints: points}); err != nil {
					return err
				}
			}
			return nil
		}),
		chromedp.Poll[chromedp.Void](`window.scrollY>0 && window.writes.length===0 && window.reads.length===0 && window.plays.length===0 && !document.querySelector('.preset-holding')`),
	); err != nil {
		t.Fatal(err)
	}
}
