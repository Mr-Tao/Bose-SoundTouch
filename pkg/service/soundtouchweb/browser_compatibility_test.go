//go:build browsertest

// Package soundtouchweb browser-level regression tests for #649. These drive
// a real headless Chrome via chromedp (already a project dependency, used
// today for the doc-screenshot tool) instead of only asserting on the raw
// HTML/JS source. They are opt-in (build tag "browsertest", run via `make
// test-browser`) rather than part of the default `go test ./...`/`make
// check` path, since they require a Chrome/Chromium binary to be present --
// see CONTRIBUTING or the Makefile for how to run them locally or in CI.
package soundtouchweb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// newHeadlessChromeContext returns a context bound to a fresh headless
// Chrome instance, torn down automatically at the end of the test.
func newHeadlessChromeContext(t *testing.T) context.Context {
	t.Helper()

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			// CI runners commonly execute as a user without the namespace
			// permissions Chrome's sandbox needs; harmless to also set
			// locally.
			chromedp.Flag("no-sandbox", true),
		)...,
	)
	t.Cleanup(cancelAlloc)

	ctx, cancelCtx := chromedp.NewContext(allocCtx)
	t.Cleanup(cancelCtx)

	ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(cancelTimeout)

	return ctx
}

// TestPlayerRendersNatively confirms the shipped page (native import maps,
// es-module-shims left uninjected) still renders in an ordinary modern
// browser -- i.e. that restoring import maps for #649 didn't break the
// common case for the vast majority of users who never need the shim.
func TestPlayerRendersNatively(t *testing.T) {
	app := NewWebApp()
	r := chi.NewRouter()
	app.Mount(r, nil)

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	ctx := newHeadlessChromeContext(t)

	var shimInjected bool
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(`.nav-discover-icon`, chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelectorAll('script[src*="es-module-shims"]').length > 0`, &shimInjected),
	); err != nil {
		t.Fatalf("chromedp run: %v", err)
	}

	if shimInjected {
		t.Error("es-module-shims should not be injected on a browser with native import map support")
	}
}

// TestPlayerRendersUnderForcedShimMode exercises es-module-shims resolving the
// same import map and vendored files the real app uses. It does not emulate
// Safari or the production feature-detection loader; those require a target-
// browser canary. The test serves a page that forces es-module-shims into
// shimMode
// (see the library's README: shimMode is triggered by
// window.esmsInitOptions.shimMode or by using importmap-shim/module-shim
// script types), which routes every browser -- including this ordinary
// headless Chrome -- through the library's own polyfill resolution instead
// of native import map support.
func TestPlayerRendersUnderForcedShimMode(t *testing.T) {
	app := NewWebApp()
	r := chi.NewRouter()
	app.MountWeb(r, nil) // only need /app/static/* and /api/control/*

	const shimModePage = `<!doctype html>
<html lang="en">
<head>
<meta charset="UTF-8" />
<script>window.esmsInitOptions = { shimMode: true };</script>
<script src="/app/static/lib/es-module-shims.js"></script>
<script type="importmap-shim">
{
    "imports": {
        "preact": "/app/static/lib/preact.module.js",
        "preact/hooks": "/app/static/lib/preact-hooks.module.js",
        "htm": "/app/static/lib/htm.module.js"
    }
}
</script>
<link rel="stylesheet" href="/app/static/css/app.css" />
</head>
<body>
<div id="app"></div>
<script type="module-shim" src="/app/static/js/app.js"></script>
</body>
</html>`

	r.Get("/test-shim-mode", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(shimModePage))
	})

	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	ctx := newHeadlessChromeContext(t)

	var rendered bool
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL+"/test-shim-mode"),
		chromedp.WaitVisible(`.nav-discover-icon`, chromedp.ByQuery),
		chromedp.Evaluate(`document.getElementById('app').children.length > 0`, &rendered),
	); err != nil {
		t.Fatalf("chromedp run (forced shim mode): %v", err)
	}

	if !rendered {
		t.Error("app did not render under forced es-module-shims shim mode")
	}
}

func TestPlayerBrowserResumesAfterSuspendedWebSocket(t *testing.T) {
	app := newResumeBrowserApp(t)
	r := chi.NewRouter()
	app.Mount(r, nil)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	ctx := newHeadlessChromeContext(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(`.zone-card`, chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			window.__aftertouchNoReload = 'preserved';
			window.__resumeDeviceFetches = 0;
			window.__resumeDeviceFetchCaptured = false;
			window.__resumeDeviceFetchCaches = [];
			window.__originalFetch = window.fetch.bind(window);
			window.fetch = async (...args) => {
				const url = new URL(args[0], window.location.href);
				if (url.pathname !== '/api/control/devices') return window.__originalFetch(...args);

				window.__resumeDeviceFetches++;
				window.__resumeDeviceFetchCaches.push(args[1]?.cache);
				const response = await window.__originalFetch(...args);
				let body = await response.text();
				if (window.__resumeDeviceFetches === 1) {
					const payload = JSON.parse(body);
					payload.data['192.0.2.10'].info.name = 'STALE_RESUME_RESPONSE';
					body = JSON.stringify(payload);
					window.__resumeDeviceFetchCaptured = true;
					await new Promise(resolve => { window.__releaseResumeDeviceFetch = resolve; });
				}
				return new Response(body, {
					status: response.status,
					statusText: response.statusText,
					headers: response.headers,
				});
			};
		})()`, nil),
	); err != nil {
		t.Fatalf("load player before suspend: %v", err)
	}

	clients := app.globalWebSocketClients()
	if len(clients) != 1 {
		t.Fatalf("browser WebSocket clients = %d, want 1", len(clients))
	}
	oldClient := clients[0]

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			window.dispatchEvent(new Event('pageshow'));
			document.dispatchEvent(new Event('visibilitychange'));
			window.dispatchEvent(new Event('pageshow'));
		})()`, nil),
		chromedp.Poll(
			`window.__resumeDeviceFetchCaptured === true && window.__resumeDeviceFetches === 1 && window.__resumeDeviceFetchCaches[0] === 'no-store'`,
			nil,
			chromedp.WithPollingTimeout(time.Second),
		),
	); err != nil {
		t.Fatalf("visible-page API reconciliation was not coalesced: %v", err)
	}

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			window.__resumeZoneReappeared = false;
			window.__staleResumeResponseRendered = false;
			window.__resumeZoneObserver = new MutationObserver(() => {
				if (document.querySelector('.zone-card')) window.__resumeZoneReappeared = true;
				if (document.body.textContent.includes('STALE_RESUME_RESPONSE')) {
					window.__staleResumeResponseRendered = true;
				}
			});
			window.__resumeZoneObserver.observe(document.body, { childList: true, subtree: true });
		})()`, nil),
	); err != nil {
		t.Fatalf("watch resumed projection: %v", err)
	}

	// The first response represents a request suspended with the old page
	// lifecycle. A second resume must invalidate it and queue exactly one new
	// no-store reconciliation, even though several lifecycle events fire.
	clearResumeBrowserZone(t, app)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			window.dispatchEvent(new Event('pageshow'));
			document.dispatchEvent(new Event('visibilitychange'));
			window.dispatchEvent(new Event('pageshow'));
			window.__releaseResumeDeviceFetch();
		})()`, nil),
		chromedp.Poll(
			`window.__resumeDeviceFetches === 2 && !document.querySelector('.zone-card')`,
			nil,
			chromedp.WithPollingTimeout(time.Second),
		),
	); err != nil {
		t.Fatalf("post-resume reconciliation did not replace the stale request: %v", err)
	}

	_ = oldClient.Close()
	waitForReplacementBrowserWebSocket(t, app, oldClient, 4*time.Second)

	var staleResponseRejected bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(
		`!window.__resumeZoneReappeared &&
		 !window.__staleResumeResponseRendered &&
		 !document.body.textContent.includes('STALE_RESUME_RESPONSE') &&
		 !document.querySelector('.zone-card') &&
		 window.__aftertouchNoReload === 'preserved' &&
		 window.__resumeDeviceFetches === 2 &&
		 window.__resumeDeviceFetchCaches.every(value => value === 'no-store')`,
		&staleResponseRejected,
	)); err != nil {
		t.Fatalf("inspect resumed projection: %v", err)
	}
	if !staleResponseRejected {
		t.Fatal("stale resume response replaced the newer WebSocket projection")
	}

	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		window.__resumeZoneObserver.disconnect();
		window.fetch = window.__originalFetch;
	})()`, nil)); err != nil {
		t.Fatalf("clean up browser resume fixture: %v", err)
	}
}

func newResumeBrowserApp(t *testing.T) *WebApp {
	t.Helper()

	app := NewWebApp()
	zone := &models.ZoneInfo{
		Master:  "master-id",
		Members: []models.Member{{DeviceID: "member-id", IP: "192.0.2.20"}},
	}
	master := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "master-id",
		Name:     "Master",
		Type:     "SoundTouch 20",
	})
	master.SetStatus(&webtypes.DeviceStatus{
		NowPlaying:  &models.NowPlaying{Source: "STANDBY"},
		Zone:        zone,
		IsConnected: true,
	})
	member := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "member-id",
		Name:     "Member",
		Type:     "SoundTouch 20",
	})
	member.SetStatus(&webtypes.DeviceStatus{
		NowPlaying:  &models.NowPlaying{Source: "STANDBY"},
		IsConnected: true,
	})
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", member)

	if projected := app.deviceViewSnapshot()["192.0.2.10"].Zone; projected == nil {
		t.Fatal("resume browser fixture did not project its initial zone")
	}

	return app
}

func clearResumeBrowserZone(t *testing.T, app *WebApp) {
	t.Helper()

	master, ok := app.GetDevice("192.0.2.10")
	if !ok {
		t.Fatal("resume browser fixture lost its zone master")
	}
	master.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Zone = nil
	})
}

func waitForReplacementBrowserWebSocket(
	t *testing.T,
	app *WebApp,
	oldClient *websocket.Conn,
	timeout time.Duration,
) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, client := range app.globalWebSocketClients() {
			if client != oldClient {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("player did not replace its suspended WebSocket connection")
}
