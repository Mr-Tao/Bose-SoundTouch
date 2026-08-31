//go:build browser

package soundtouchweb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

const (
	contractUnavailableHost       = "192.168.101.9"
	contractUnavailableMemberHost = "192.168.101.10"
	contractDegradedHost          = "192.168.101.101"
	contractPairLeftHost          = "192.168.101.102"
	contractPairRightHost         = "192.168.101.103"
	contractHealthyControlID      = "atrium.local"
	contractHealthyHost           = "192.168.101.20"
	contractHealthyMemberHost     = "192.168.101.21"

	contractUnavailableName = "Gallery"
	contractDegradedName    = "Whole-house programme coordinator with an intentionally long display name"
	contractPairName        = "Living Room Stereo Pair With An Intentionally Long Logical Name"
	contractLeftName        = "Living Room window speaker with a deliberately long physical name"
	contractRightName       = "Living Room bookshelf speaker with a deliberately long physical name"

	contractMasterModel = "SoundTouch 30 Series III with extended model metadata"
	contractPairModel   = "SoundTouch 10 with extended model metadata"
)

type contractViewport struct {
	name   string
	width  int64
	height int64
	dpr    float64
	mobile bool
}

type contractControlCall struct {
	kind      string
	controlID string
	memberID  string
	value     int
}

type contractControlRecorder struct {
	mu                   sync.Mutex
	calls                []contractControlCall
	nextGroupVolumeDelay time.Duration
}

func TestPlayerBrowserContract(t *testing.T) {
	app := newBrowserContractApp(t)
	server, controls := newBrowserContractServer(t, app)

	browserPath := browserExecutable(t)
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("no-proxy-server", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	defer cancelAllocator()
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()

	viewports := []contractViewport{
		{name: "desktop-1440x900", width: 1440, height: 900, dpr: 1},
		{name: "iphone-390x844-dpr3", width: 390, height: 844, dpr: 3, mobile: true},
		{name: "ipad-834x1112-dpr2", width: 834, height: 1112, dpr: 2, mobile: true},
	}

	for _, viewport := range viewports {
		viewport := viewport
		t.Run(viewport.name, func(t *testing.T) {
			controls.reset()
			resetBrowserContractSoundSettings(t, app)
			targetContext, cancelTarget := chromedp.NewContext(browserContext)
			runContext, cancelTimeout := context.WithTimeout(targetContext, 30*time.Second)
			defer func() {
				if t.Failed() {
					captureBrowserContractFailure(t, targetContext, viewport.name)
				}
				cancelTimeout()
				cancelTarget()
			}()

			metrics := emulation.SetDeviceMetricsOverride(viewport.width, viewport.height, viewport.dpr, viewport.mobile).
				WithScreenWidth(viewport.width).
				WithScreenHeight(viewport.height)
			if err := chromedp.Run(runContext,
				storage.ClearDataForOrigin(server.URL, "local_storage"),
				metrics,
				chromedp.Navigate(server.URL+"/app"),
				chromedp.WaitVisible(contractZoneCardSelector(contractDegradedHost), chromedp.ByQuery),
			); err != nil {
				t.Fatalf("load projected device list: %v", err)
			}

			assertBrowserViewport(t, runContext, viewport)
			assertMountedWebSocketClient(t, app)
			assertBrowserDeviceCards(t, runContext)
			assertBrowserNameAndIPSort(t, runContext)
			assertBrowserListLayout(t, runContext)
			assertNoHorizontalDocumentOverflow(t, runContext, "device list")
			exerciseGroupVolumeControl(t, runContext, controls)

			if err := chromedp.Run(runContext,
				chromedp.Click(contractZoneCardSelector(contractDegradedHost)+" .zone-card-open", chromedp.ByQuery),
				chromedp.WaitVisible(".zone-member-details > summary", chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open degraded group detail: %v", err)
			}
			assertBrowserExpression(t, runContext, "member disclosure initially closed", `document.querySelector('.zone-member-details')?.open === false`)
			assertNoHorizontalDocumentOverflow(t, runContext, "closed member disclosure")
			if err := chromedp.Run(runContext,
				chromedp.Click(".zone-member-details > summary", chromedp.ByQuery),
				chromedp.WaitVisible(".zone-physical-members", chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open member disclosure: %v", err)
			}
			assertBrowserMemberDisclosure(t, runContext)
			assertBrowserMemberSettingsCollapsed(t, runContext)
			openBrowserMemberSettings(t, runContext)
			assertBrowserMemberSettings(t, runContext)
			if viewport.name == "desktop-1440x900" {
				assertBrowserEmbeddedSettingsGenerationFence(t, runContext)
				assertBrowserEmbeddedSettingsMutationGenerationFence(t, runContext)
				assertBrowserBluetoothClearConfirmation(t, runContext)
			}
			assertBrowserDetailLayout(t, runContext)
			assertNoHorizontalDocumentOverflow(t, runContext, "open member disclosure")
			exerciseDetailGroupVolumeControl(t, runContext, controls, app)
			exerciseLogicalMemberControls(t, runContext, controls, app)
			assertBrowserUnavailableMemberVolumeControl(t, runContext, controls)
			if viewport.name == "desktop-1440x900" {
				assertBrowserUnknownMemberVolumeControl(t, runContext)
			}
		})
	}
}

func TestPlayerBrowserResumesAfterSuspendedWebSocket(t *testing.T) {
	app := newBrowserContractApp(t)
	server, _ := newBrowserContractServer(t, app)

	browserPath := browserExecutable(t)
	allocatorOptions := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("no-proxy-server", true),
		chromedp.Flag("no-sandbox", true),
	)
	allocatorContext, cancelAllocator := chromedp.NewExecAllocator(context.Background(), allocatorOptions...)
	defer cancelAllocator()
	browserContext, cancelBrowser := chromedp.NewContext(allocatorContext)
	defer cancelBrowser()
	runContext, cancelTimeout := context.WithTimeout(browserContext, 15*time.Second)
	defer cancelTimeout()

	if err := chromedp.Run(runContext,
		chromedp.Navigate(server.URL+"/app"),
		chromedp.WaitVisible(contractZoneCardSelector(contractHealthyControlID), chromedp.ByQuery),
		chromedp.Evaluate(`(() => {
			window.__aftertouchNoReload = 'preserved';
			window.__resumeDeviceFetches = 0;
			window.__resumeDeviceFetchCaptured = false;
			window.__originalFetch = window.fetch.bind(window);
			window.fetch = async (...args) => {
				const url = new URL(args[0], window.location.href);
				if (url.pathname !== '/api/control/devices') return window.__originalFetch(...args);

				window.__resumeDeviceFetches++;
				window.__resumeDeviceFetchCache = args[1]?.cache;
				const response = await window.__originalFetch(...args);
				const body = await response.text();
				window.__resumeDeviceFetchCaptured = true;
				await new Promise(resolve => { window.__releaseResumeDeviceFetch = resolve; });
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

	if err := chromedp.Run(runContext,
		chromedp.Evaluate(`(() => {
			window.dispatchEvent(new Event('pageshow'));
			document.dispatchEvent(new Event('visibilitychange'));
			window.dispatchEvent(new Event('pageshow'));
		})()`, nil),
		chromedp.Poll(
			`window.__resumeDeviceFetchCaptured === true && window.__resumeDeviceFetches === 1 && window.__resumeDeviceFetchCache === 'no-store'`,
			nil,
			chromedp.WithPollingTimeout(time.Second),
		),
	); err != nil {
		t.Fatalf("visible-page API reconciliation was not coalesced: %v", err)
	}

	clearBrowserContractZone(t, app, contractHealthyControlID)
	_ = oldClient.Close()
	waitForReplacementBrowserWebSocket(t, app, oldClient, 4*time.Second)

	var resumed bool
	if err := chromedp.Run(runContext,
		chromedp.Poll(fmt.Sprintf(
			`!document.querySelector(%q) && window.__aftertouchNoReload === 'preserved'`,
			contractZoneCardSelector(contractHealthyControlID),
		), &resumed, chromedp.WithPollingTimeout(time.Second)),
		chromedp.Evaluate(fmt.Sprintf(`(() => {
			window.__resumeZoneReappeared = false;
			window.__resumeZoneObserver = new MutationObserver(() => {
				if (document.querySelector(%q)) window.__resumeZoneReappeared = true;
			});
			window.__resumeZoneObserver.observe(document.body, { childList: true, subtree: true });
			window.__releaseResumeDeviceFetch();
		})()`, contractZoneCardSelector(contractHealthyControlID)), nil),
		chromedp.Sleep(300*time.Millisecond),
	); err != nil {
		t.Fatalf("replacement WebSocket did not reconcile stale topology: %v", err)
	}
	assertBrowserExpression(t, runContext, "stale resume response stayed rejected",
		fmt.Sprintf(`!window.__resumeZoneReappeared && !document.querySelector(%q) && window.__resumeDeviceFetches === 1`,
			contractZoneCardSelector(contractHealthyControlID)))

	clearBrowserContractZone(t, app, contractUnavailableHost)
	app.BroadcastDeviceList()

	var reconnected bool
	if err := chromedp.Run(runContext,
		chromedp.Poll(fmt.Sprintf(
			`!document.querySelector(%q) && window.__aftertouchNoReload === 'preserved'`,
			contractZoneCardSelector(contractUnavailableHost),
		), &reconnected, chromedp.WithPollingTimeout(time.Second)),
	); err != nil {
		t.Fatalf("replacement WebSocket did not deliver the authoritative topology: %v", err)
	}

	if err := chromedp.Run(runContext, chromedp.Evaluate(`(() => {
		window.__resumeZoneObserver.disconnect();
		window.fetch = window.__originalFetch;
	})()`, nil)); err != nil {
		t.Fatalf("clean up browser resume fixture: %v", err)
	}
}

func clearBrowserContractZone(t *testing.T, app *WebApp, controlID string) {
	t.Helper()

	device, ok := app.GetDevice(controlID)
	if !ok {
		t.Fatalf("browser fixture lost zone master %q", controlID)
	}
	device.UpdateStatus(func(status *webtypes.DeviceStatus) {
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

func resetBrowserContractSoundSettings(t *testing.T, app *WebApp) {
	t.Helper()

	pairMaster, ok := app.GetDevice(contractPairRightHost)
	if !ok {
		t.Fatalf("browser fixture lost pair master %q", contractPairRightHost)
	}
	pairMaster.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Bass = &models.Bass{TargetBass: -3, ActualBass: -3}
		status.BassRevision++
		status.Balance = &models.Balance{
			BalanceAvailable: true,
			BalanceMin:       -7,
			BalanceMax:       7,
			BalanceDefault:   0,
			TargetBalance:    0,
			ActualBalance:    0,
			CapabilityKnown:  true,
		}
		status.BalanceRevision++
	})
}

func newBrowserContractApp(t *testing.T) *WebApp {
	t.Helper()

	app := NewWebApp()
	healthyZone := &models.ZoneInfo{
		Master: "healthy-master",
		Members: []models.Member{
			{DeviceID: "healthy-master", IP: contractHealthyHost},
			{DeviceID: "healthy-member", IP: contractHealthyMemberHost},
		},
	}
	unavailableZone := &models.ZoneInfo{
		Master: "unavailable-master",
		Members: []models.Member{
			{DeviceID: "unavailable-master", IP: contractUnavailableHost},
			{DeviceID: "unavailable-member", IP: contractUnavailableMemberHost},
		},
	}
	degradedZone := &models.ZoneInfo{
		Master: "degraded-master",
		Members: []models.Member{
			{DeviceID: "degraded-master", IP: contractDegradedHost},
			{DeviceID: "pair-left", IP: contractPairLeftHost},
		},
	}
	pair := &models.Group{
		ID:             "browser-contract-pair",
		Name:           contractPairName,
		MasterDeviceID: "pair-right",
		Status:         "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "pair-left", Role: "LEFT", IPAddress: contractPairLeftHost},
			{DeviceID: "pair-right", Role: "RIGHT", IPAddress: contractPairRightHost},
		}},
	}

	addBrowserContractDeviceAt(t, app, contractHealthyControlID, contractHealthyHost, "healthy-master", "Atrium", "SoundTouch 20", 19, webtypes.ConnectivityOnline, nil, healthyZone)
	addBrowserContractDevice(t, app, contractHealthyMemberHost, "healthy-member", "Breakfast Room", "SoundTouch 10", 27, webtypes.ConnectivityOnline, nil, nil)
	addBrowserContractDevice(t, app, contractUnavailableHost, "unavailable-master", contractUnavailableName, "SoundTouch 20", 22, webtypes.ConnectivityOnline, nil, unavailableZone)
	addBrowserContractDevice(t, app, contractUnavailableMemberHost, "unavailable-member", "Gallery Annex", "SoundTouch 10", 17, webtypes.ConnectivityOffline, nil, nil)
	addBrowserContractDevice(t, app, contractDegradedHost, "degraded-master", contractDegradedName,
		contractMasterModel, 33, webtypes.ConnectivityOnline, nil, degradedZone)
	addBrowserContractDevice(t, app, contractPairLeftHost, "pair-left", contractLeftName,
		contractPairModel, 41, webtypes.ConnectivityOffline, pair, nil)
	pairMaster := addBrowserContractDevice(t, app, contractPairRightHost, "pair-right", contractRightName,
		contractPairModel, 41, webtypes.ConnectivityOnline, pair, nil)
	pairMaster.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Bass = &models.Bass{
			TargetBass: -3,
			ActualBass: -3,
		}
		status.BassCapabilities = &models.BassCapabilities{
			BassAvailable: true,
			BassMin:       -9,
			BassMax:       0,
			BassDefault:   0,
		}
		status.Balance = &models.Balance{
			BalanceAvailable: true,
			BalanceMin:       -7,
			BalanceMax:       7,
			BalanceDefault:   0,
			TargetBalance:    0,
			ActualBalance:    0,
			CapabilityKnown:  true,
		}
	})

	projection := app.deviceViewSnapshot()
	for _, entry := range app.DeviceSnapshot() {
		if entry.Device.Client != nil {
			t.Fatalf("browser fixture %q unexpectedly has a physical speaker client", entry.ID)
		}
	}
	if len(projection) != 3 {
		t.Fatalf("fixture projected %d logical devices, want 3: %+v", len(projection), projection)
	}
	unavailable := projection[contractUnavailableHost]
	if unavailable.Zone == nil || !unavailable.Zone.Degraded || unavailable.Zone.AvailableMemberCount != 1 || unavailable.Zone.PhysicalMemberCount != 2 {
		t.Fatalf("fixture did not project the ordinary unavailable 1-of-2 group: %+v", unavailable)
	}
	degraded := projection[contractDegradedHost]
	if degraded.Zone == nil || !degraded.Zone.Degraded || degraded.Zone.AvailableMemberCount != 2 || degraded.Zone.PhysicalMemberCount != 3 {
		t.Fatalf("fixture did not project the degraded 2-logical/3-physical group: %+v", degraded)
	}

	return app
}

func addBrowserContractDevice(
	t *testing.T,
	app *WebApp,
	host, deviceID, name, model string,
	volume int,
	connectivity webtypes.Connectivity,
	group *models.Group,
	zone *models.ZoneInfo,
) *webtypes.DeviceConnection {
	return addBrowserContractDeviceAt(t, app, host, host, deviceID, name, model, volume, connectivity, group, zone)
}

func addBrowserContractDeviceAt(
	t *testing.T,
	app *WebApp,
	controlID, address, deviceID, name, model string,
	volume int,
	connectivity webtypes.Connectivity,
	group *models.Group,
	zone *models.ZoneInfo,
) *webtypes.DeviceConnection {
	t.Helper()

	connected := connectivity != webtypes.ConnectivityOffline
	connection := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID:  deviceID,
		Name:      name,
		Type:      model,
		IPAddress: address,
	})
	connection.SetStatus(&webtypes.DeviceStatus{
		NowPlaying:    &models.NowPlaying{Source: "STANDBY"},
		Volume:        &models.Volume{TargetVolume: volume, ActualVolume: volume},
		Group:         group,
		Zone:          zone,
		Connectivity:  connectivity,
		HTTPReachable: connected,
		IsConnected:   connected,
		LastActivity:  time.Unix(1_700_000_000, 0),
	})
	if !app.AddDevice(controlID, connection) {
		t.Fatalf("duplicate browser fixture control ID %q", controlID)
	}

	return connection
}

func newBrowserContractServer(t *testing.T, app *WebApp) (*httptest.Server, *contractControlRecorder) {
	t.Helper()

	projection := app.deviceViewSnapshot()
	degradedZone := projection[contractDegradedHost].Zone
	controls := &contractControlRecorder{}
	router := chi.NewRouter()
	app.MountWeb(router, nil)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case serveBrowserContractControlResponse(w, r, controls, app):
		case r.URL.Path == "/api/announcements":
			writeBrowserContractJSON(w, map[string]interface{}{"announcements": []interface{}{}})
		case strings.HasSuffix(r.URL.Path, "/zone") || strings.HasSuffix(r.URL.Path, "/zone/"):
			zoneProjection := degradedZone
			if strings.Contains(r.URL.Path, "/devices/"+contractUnavailableHost+"/zone") {
				zoneProjection = projection[contractUnavailableHost].Zone
			}
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
				"masterIp":            zoneProjection.MasterControlID,
				"masterHwId":          zoneProjection.MasterDeviceID,
				"masterName":          zoneProjection.Members[0].Name,
				"master":              zoneProjection.Members[0],
				"members":             zoneProjection.Members[1:],
				"physicalMemberCount": zoneProjection.PhysicalMemberCount,
				"isMaster":            true,
				"isSlave":             false,
				"isStandalone":        false,
			}})
		case strings.HasSuffix(r.URL.Path, "/stereo-pair/"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{"capable": false}})
		case strings.HasSuffix(r.URL.Path, "/recents"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: []interface{}{}})
		case strings.HasSuffix(r.URL.Path, "/settings/"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
				"support": map[string]interface{}{},
				"network": map[string]interface{}{"interfaces": []map[string]interface{}{
					{
						"type": "WIFI_INTERFACE", "name": "wlan0", "ipAddress": "192.168.101.103",
						"ssid": "Whole-house wireless network", "band": "5GHz",
						"state": "NETWORK_WIFI_CONNECTED", "firmwareNetworkQuality": "Marginal",
						"firmwareNetworkQualitySource": "netStats", "firmwareNetworkQualityState": "conflict",
						"networkInfoFirmwareQuality": "Poor",
					},
					{
						"type": "WIFI_INTERFACE", "name": "wlan1", "ipAddress": "192.168.101.104",
						"band": "2.4GHz", "state": "NETWORK_WIFI_DISCONNECTED",
					},
					{
						"type": "ETHERNET_INTERFACE", "name": "eth0", "macAddress": "AA:BB:CC:DD:EE:02",
						"state": "NETWORK_ETHERNET_DISCONNECTED",
					},
				}},
			}})
		default:
			router.ServeHTTP(w, r)
		}
	})

	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	return server, controls
}

func serveBrowserContractControlResponse(
	w http.ResponseWriter,
	r *http.Request,
	recorder *contractControlRecorder,
	app *WebApp,
) bool {
	if r.Method != http.MethodPost {
		return false
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 6 || parts[0] != "api" || parts[1] != "control" || parts[2] != "devices" {
		return false
	}

	controlID := parts[3]
	var call contractControlCall
	switch {
	case len(parts) == 7 && parts[4] == "zone" && parts[5] == "volume":
		call = contractControlCall{kind: "group-volume", controlID: controlID}
	case len(parts) == 9 && parts[4] == "zone" && parts[5] == "member" && parts[7] == "volume":
		call = contractControlCall{kind: "member-volume", controlID: controlID, memberID: parts[6]}
	case len(parts) == 7 && parts[4] == "stereo-pair" && parts[5] == "balance":
		call = contractControlCall{kind: "balance", controlID: controlID}
	case len(parts) == 6 && parts[4] == "action" && parts[5] == "bass":
		var request webtypes.BassRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			writeBrowserContractError(w, "decode test bass request", http.StatusBadRequest)
			return true
		}
		call = contractControlCall{kind: "bass", controlID: controlID, value: request.Level}
	default:
		return false
	}

	value := call.value
	var err error
	if call.kind != "bass" {
		value, err = strconv.Atoi(parts[len(parts)-1])
	}
	if err != nil || ((call.kind == "balance") && (value < -7 || value > 7)) ||
		((call.kind == "bass") && (value < -9 || value > 0)) ||
		((call.kind != "balance" && call.kind != "bass") && (value < 0 || value > 100)) {
		writeBrowserContractError(w, "test control value outside fixture bounds", http.StatusBadRequest)
		return true
	}
	call.value = value
	recorder.record(call)
	if call.kind == "member-volume" && value == 49 {
		writeBrowserContractError(w, "simulated member volume failure", http.StatusBadGateway)
		return true
	}

	switch call.kind {
	case "group-volume":
		if delay := recorder.takeNextGroupVolumeDelay(); delay > 0 {
			time.Sleep(delay)
		}
		members := applyBrowserContractGroupVolume(app, controlID, value)
		app.BroadcastDeviceList()
		writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
			"requested": value,
			"partial":   false,
			"members":   members,
		}})
	case "member-volume":
		partial := value == 47
		actual := value
		errorMessage := ""
		if partial {
			actual = value - 1
			errorMessage = fmt.Sprintf("readback volume %d does not match target %d", actual, value)
		}
		writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
			"requested": value,
			"controlId": call.memberID,
			"partial":   partial,
			"members": []map[string]interface{}{
				{
					"controlId": call.memberID,
					"name":      contractPairName,
					"actual":    actual,
					"error":     errorMessage,
				},
			},
		}})
	case "balance":
		projectionValue := value
		revisionAdvance := uint64(1)
		if value == 1 {
			projectionValue = 2
			revisionAdvance = 2
		}
		projectionRevision := uint64(0)
		if connection, ok := app.GetDevice(controlID); ok {
			connection.UpdateStatus(func(status *webtypes.DeviceStatus) {
				status.Balance = &models.Balance{
					BalanceAvailable: true,
					BalanceMin:       -7,
					BalanceMax:       7,
					BalanceDefault:   0,
					TargetBalance:    projectionValue,
					ActualBalance:    projectionValue,
					CapabilityKnown:  true,
				}
				status.BalanceRevision += revisionAdvance
				projectionRevision = status.BalanceRevision
			})
			app.BroadcastDeviceList()
		}
		responseRevision := projectionRevision
		if revisionAdvance > 1 {
			responseRevision--
			time.Sleep(50 * time.Millisecond)
		}
		writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
			"requested": value,
			"target":    value,
			"actual":    value,
			"revision":  responseRevision,
			"atTarget":  true,
		}})
	case "bass":
		projectionValue := value
		revisionAdvance := uint64(1)
		if value == -4 {
			projectionValue = -5
			revisionAdvance = 2
		}
		projectionRevision := uint64(0)
		if connection, ok := app.GetDevice(controlID); ok {
			connection.UpdateStatus(func(status *webtypes.DeviceStatus) {
				status.Bass = &models.Bass{TargetBass: projectionValue, ActualBass: projectionValue}
				status.BassRevision += revisionAdvance
				projectionRevision = status.BassRevision
			})
			app.BroadcastDeviceList()
		}
		responseRevision := projectionRevision
		if revisionAdvance > 1 {
			responseRevision--
			time.Sleep(50 * time.Millisecond)
		}
		writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
			"requested": value,
			"target":    value,
			"actual":    value,
			"revision":  responseRevision,
			"atTarget":  true,
		}})
	}

	return true
}

func applyBrowserContractGroupVolume(app *WebApp, controlID string, requested int) []map[string]interface{} {
	view := app.deviceViewSnapshot()[controlID]
	if view.Zone == nil {
		return nil
	}

	baseline := 0
	for _, member := range view.Zone.Members {
		if member.ActualVolume != nil && *member.ActualVolume > baseline {
			baseline = *member.ActualVolume
		}
	}

	delta := requested - baseline
	members := make([]map[string]interface{}, 0, len(view.Zone.Members))

	for _, member := range view.Zone.Members {
		if member.ActualVolume == nil {
			continue
		}

		actual := models.ClampVolumeLevel(*member.ActualVolume + delta)
		connection, ok := app.GetDevice(member.ControlID)
		if !ok {
			continue
		}

		connection.UpdateStatus(func(status *webtypes.DeviceStatus) {
			status.Volume = &models.Volume{TargetVolume: actual, ActualVolume: actual}
		})
		members = append(members, map[string]interface{}{
			"controlId": member.ControlID,
			"actual":    actual,
		})
	}

	return members
}

func writeBrowserContractError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(webtypes.APIResponse{Success: false, Error: message})
}

func (recorder *contractControlRecorder) reset() {
	recorder.mu.Lock()
	recorder.calls = nil
	recorder.nextGroupVolumeDelay = 0
	recorder.mu.Unlock()
}

func (recorder *contractControlRecorder) delayNextGroupVolume(delay time.Duration) {
	recorder.mu.Lock()
	recorder.nextGroupVolumeDelay = delay
	recorder.mu.Unlock()
}

func (recorder *contractControlRecorder) takeNextGroupVolumeDelay() time.Duration {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	delay := recorder.nextGroupVolumeDelay
	recorder.nextGroupVolumeDelay = 0

	return delay
}

func (recorder *contractControlRecorder) record(call contractControlCall) {
	recorder.mu.Lock()
	recorder.calls = append(recorder.calls, call)
	recorder.mu.Unlock()
}

func (recorder *contractControlRecorder) callsFor(kind string) []contractControlCall {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	var calls []contractControlCall
	for _, call := range recorder.calls {
		if call.kind == kind {
			calls = append(calls, call)
		}
	}

	return calls
}

func writeBrowserContractJSON(w http.ResponseWriter, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func browserExecutable(t *testing.T) string {
	t.Helper()

	if configured := os.Getenv("BROWSER_BIN"); configured != "" {
		if _, err := os.Stat(configured); err != nil {
			t.Fatalf("BROWSER_BIN %q is not usable: %v", configured, err)
		}
		return configured
	}
	for _, candidate := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
	}
	t.Fatal("browser contract requires Chrome or Chromium; set BROWSER_BIN to its executable")
	return ""
}

func assertBrowserViewport(t *testing.T, ctx context.Context, viewport contractViewport) {
	t.Helper()

	var actual struct {
		Width  int64   `json:"width"`
		Height int64   `json:"height"`
		DPR    float64 `json:"dpr"`
	}
	evaluateBrowserContract(t, ctx, `({width: window.innerWidth, height: window.innerHeight, dpr: window.devicePixelRatio})`, &actual)
	if actual.Width != viewport.width || actual.Height != viewport.height || actual.DPR != viewport.dpr {
		t.Fatalf("viewport = %dx%d DPR%g, want %dx%d DPR%g", actual.Width, actual.Height, actual.DPR, viewport.width, viewport.height, viewport.dpr)
	}
}

func assertMountedWebSocketClient(t *testing.T, app *WebApp) {
	t.Helper()

	app.WSMutex.RLock()
	clients := len(app.WSClients)
	app.WSMutex.RUnlock()
	if clients == 0 {
		t.Fatal("MountWeb HandleWebSocket did not register the browser before its initial devices frame rendered")
	}
}

func assertBrowserDeviceCards(t *testing.T, ctx context.Context) {
	t.Helper()

	healthyCard := contractZoneCardSelector(contractHealthyControlID)
	unavailableCard := contractZoneCardSelector(contractUnavailableHost)
	stereoCard := contractZoneCardSelector(contractDegradedHost)

	assertBrowserExpression(t, ctx, "three distinct logical group cards", `document.querySelectorAll('.zone-card').length === 3`)
	assertBrowserExpression(t, ctx, "healthy group count copy", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-badge')?.textContent.trim() === 'Group · 2'`, healthyCard))
	assertBrowserExpression(t, ctx, "healthy group omits degraded status copy", fmt.Sprintf(`!document.querySelector(%q)?.querySelector('.zone-card-availability')`, healthyCard))
	assertBrowserExpression(t, ctx, "healthy group logical status", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-indicator')?.getAttribute('aria-label') === 'Online'`, healthyCard))

	assertBrowserExpression(t, ctx, "ordinary unavailable group count copy", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-badge')?.textContent.trim() === 'Group · 2'`, unavailableCard))
	assertBrowserExpression(t, ctx, "ordinary unavailable logical availability copy", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-availability')?.textContent.trim() === '1/2 available'`, unavailableCard))
	assertBrowserExpression(t, ctx, "ordinary unavailable title", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-availability')?.title === '1 unavailable'`, unavailableCard))
	assertBrowserExpression(t, ctx, "ordinary unavailable master logical status", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-indicator')?.getAttribute('aria-label') === 'Online'`, unavailableCard))

	assertBrowserExpression(t, ctx, "degraded stereo group count copy", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-badge')?.textContent.trim() === 'Group · 2'`, stereoCard))
	assertBrowserExpression(t, ctx, "degraded stereo physical availability copy", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-availability')?.textContent.trim() === '2/3 speakers available'`, stereoCard))
	assertBrowserExpression(t, ctx, "degraded stereo availability title", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.zone-card-availability')?.title === '1 physical speaker unavailable'`, stereoCard))
	assertBrowserExpression(t, ctx, "degraded stereo master logical status", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-indicator')?.getAttribute('aria-label') === 'Online'`, stereoCard))
	assertBrowserExpression(t, ctx, "long logical name", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-name')?.textContent === %q`, stereoCard, contractDegradedName))

	assertBrowserExpression(t, ctx, "one logical status indicator per card", `Array.from(document.querySelectorAll('.zone-card')).every(card => card.querySelectorAll('.zone-card-open .device-indicator').length === 1)`)
	assertBrowserExpression(t, ctx, "group volume sliders", `document.querySelectorAll('.zone-volume-slider').length === 3`)
	assertBrowserExpression(t, ctx, "usable group volume sliders", `Array.from(document.querySelectorAll('.zone-volume-slider')).every(node => !node.disabled && node.getBoundingClientRect().width >= 120)`)
	assertBrowserExpression(t, ctx, "group volume labels", `Array.from(document.querySelectorAll('.zone-volume-label')).every(label => label.textContent.trim() === 'Group volume')`)
}

func assertBrowserNameAndIPSort(t *testing.T, ctx context.Context) {
	t.Helper()

	assertBrowserExpression(t, ctx, "name sort selected initially", `Array.from(document.querySelectorAll('.sort-btn')).some(button => button.textContent.trim() === 'Name' && button.classList.contains('active'))`)
	assertBrowserExpression(t, ctx, "name sort de-emphasizes IP by omitting it", `document.querySelectorAll('.device-ip').length === 0`)
	assertBrowserStrings(t, ctx, "name-sort card order", `Array.from(document.querySelectorAll('.zone-card .device-name')).map(node => node.textContent.trim())`, []string{
		"Atrium",
		contractUnavailableName,
		contractDegradedName,
	})

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`Array.from(document.querySelectorAll('.sort-btn')).find(button => button.textContent.trim() === 'IP').click()`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll('.sort-btn')).some(button => button.textContent.trim() === 'IP' && button.classList.contains('active'))`, nil),
	); err != nil {
		t.Fatalf("activate IP sort through the UI: %v", err)
	}

	assertBrowserStrings(t, ctx, "numeric IP-sort card order", `Array.from(document.querySelectorAll('.zone-card')).map(card => card.getAttribute('aria-labelledby'))`, []string{
		contractZoneNameID(contractUnavailableHost),
		contractZoneNameID(contractHealthyControlID),
		contractZoneNameID(contractDegradedHost),
	})
	assertBrowserExpression(t, ctx, "IP sort shows one IP per logical card", `document.querySelectorAll('.zone-card .device-ip').length === 3`)
	assertBrowserExpression(t, ctx, "hostname control uses resolved address for IP presentation", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-ip')?.title === %q`, contractZoneCardSelector(contractHealthyControlID), contractHealthyHost))

	card := contractZoneCardSelector(contractDegradedHost)
	assertBrowserExpression(t, ctx, "IP sort preserves full address in title", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-ip')?.title === %q`, card, contractDegradedHost))
	assertBrowserExpression(t, ctx, "IP prefix is de-emphasized", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-ip-prefix')?.textContent === '192.168.101.' && Number(getComputedStyle(document.querySelector(%q).querySelector('.device-ip-prefix')).opacity) < 1`, card, card))
	assertBrowserExpression(t, ctx, "IP last octet is emphasized", fmt.Sprintf(`document.querySelector(%q)?.querySelector('.device-ip-last')?.textContent === '101' && Number.parseInt(getComputedStyle(document.querySelector(%q).querySelector('.device-ip-last')).fontWeight, 10) >= 600`, card, card))
}

func assertBrowserMemberDisclosure(t *testing.T, ctx context.Context) {
	t.Helper()

	assertBrowserExpression(t, ctx, "member disclosure opened", `document.querySelector('.zone-member-details')?.open === true`)
	assertBrowserExpression(t, ctx, "logical and physical count summary", `document.querySelector('.zone-member-details > summary')?.textContent.trim() === '2 members · 3 speakers'`)
	assertBrowserStrings(t, ctx, "logical identity, model/type, IP, kind, and connectivity", `Array.from(document.querySelectorAll('.zone-logical-member')).map(row => [row.querySelector('.zone-logical-name').childNodes[0].textContent.trim(), ...Array.from(row.querySelectorAll('.zone-logical-metadata > span')).map(node => node.textContent.trim()), row.querySelector('.zone-logical-header > .device-indicator').getAttribute('aria-label'), row.querySelector('.zone-logical-header > .device-indicator').classList.contains('online') ? 'online' : 'not-online'].join('|'))`, []string{
		strings.Join([]string{contractDegradedName, contractMasterModel, contractDegradedHost, "Speaker", contractDegradedName + ": Online", "online"}, "|"),
		strings.Join([]string{contractPairName, contractPairModel, contractPairRightHost, "Stereo pair", contractPairName + ": Online", "online"}, "|"),
	})
	assertBrowserStrings(t, ctx, "physical LEFT/RIGHT identity, type, full IP, and connectivity", `Array.from(document.querySelectorAll('.zone-physical-member')).map(row => [row.querySelector('.zone-physical-role').textContent.trim(), row.querySelector('.zone-physical-name').textContent.trim(), ...Array.from(row.querySelectorAll('.zone-physical-metadata > span')).map(node => node.textContent.trim()), row.querySelector('.device-indicator').getAttribute('aria-label')].join('|'))`, []string{
		strings.Join([]string{"LEFT", contractLeftName, contractPairModel, contractPairLeftHost, contractLeftName + ": Offline"}, "|"),
		strings.Join([]string{"RIGHT", contractRightName, contractPairModel, contractPairRightHost, contractRightName + ": Online"}, "|"),
	})
	assertBrowserExpression(t, ctx, "logical member volume sliders", `document.querySelectorAll('.zone-member-volume-slider').length === 2`)
	assertBrowserExpression(t, ctx, "usable member volume sliders", `Array.from(document.querySelectorAll('.zone-member-volume-slider')).every(node => !node.disabled && node.getBoundingClientRect().width >= 120)`)
	assertBrowserStrings(t, ctx, "member volume labels", `Array.from(document.querySelectorAll('.zone-member-volume-slider')).map(node => node.getAttribute('aria-label'))`, []string{
		contractDegradedName + " volume",
		contractPairName + " volume",
	})
	assertBrowserExpression(t, ctx, "physical LEFT/RIGHT rows have no independent volume sliders", `document.querySelectorAll('.zone-physical-member input[type="range"]').length === 0`)
}

func assertBrowserMemberSettingsCollapsed(t *testing.T, ctx context.Context) {
	t.Helper()

	ordinaryMember := ".zone-logical-member:nth-child(1)"
	pairMember := ".zone-logical-member:nth-child(2)"
	assertBrowserExpression(t, ctx, "zone root has no implicit member settings", `document.querySelector('.device-detail > .member-settings-section') === null`)
	assertBrowserExpression(t, ctx, "every logical member owns one collapsed settings disclosure", `Array.from(document.querySelectorAll('.zone-logical-member')).every(row => {
		const disclosures = row.querySelectorAll(':scope > details');
		return disclosures.length === 1 && disclosures[0].classList.contains('member-settings-section') && disclosures[0].open === false;
	})`)
	assertBrowserStrings(t, ctx, "member settings disclosure titles", `Array.from(document.querySelectorAll('.zone-logical-member > .member-settings-section > .settings-summary .section-title')).map(node => node.textContent.trim())`, []string{"Settings", "Settings"})
	assertBrowserStrings(t, ctx, "member settings disclosure accessible names", `Array.from(document.querySelectorAll('.zone-logical-member > .member-settings-section > .settings-summary')).map(node => node.getAttribute('aria-label'))`, []string{
		"Settings for " + contractDegradedName,
		"Settings for " + contractPairName,
	})
	assertBrowserExpression(t, ctx, "peer sound and device disclosures are absent", `Array.from(document.querySelectorAll('.zone-logical-member')).every(row => row.querySelectorAll(':scope > .sound-settings-section, :scope > .settings-section').length === 0)`)
	assertBrowserExpression(t, ctx, "device settings stay lazy while collapsed", `document.querySelectorAll('.member-settings-section .settings-loading, .member-settings-section .settings-load-error').length === 0`)
	assertBrowserExpression(t, ctx, "collapsed member settings do not request physical device settings", `performance.getEntriesByType('resource').every(entry => !new URL(entry.name).pathname.includes('/settings/'))`)
	assertBrowserExpression(t, ctx, "old prominent acoustic controls are absent", `document.querySelectorAll('.device-card .stereo-balance-slider, .device-card .bass-row, .controls .stereo-balance-slider, .controls .bass-row').length === 0`)
	assertBrowserExpression(t, ctx, "ordinary member keeps one direct device section", fmt.Sprintf(`document.querySelectorAll(%q).length === 0 && document.querySelector(%q)?.textContent.trim() === %q`, ordinaryMember+" > .member-settings-section .device-settings-disclosure", ordinaryMember+" > .member-settings-section .device-settings-group > .member-settings-heading", "Device · "+contractDegradedName))
	assertBrowserStrings(t, ctx, "pair device sections identify both physical targets", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(node => node.textContent.trim())`, pairMember+" > .member-settings-section .device-settings-disclosure > .device-settings-summary > .member-settings-heading"), []string{
		"Device · " + contractLeftName,
		"Device · " + contractRightName,
	})
	assertBrowserStrings(t, ctx, "pair device sections retain deterministic roles", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(node => node.textContent.trim())`, pairMember+" > .member-settings-section .device-settings-disclosure > .device-settings-summary > .device-settings-role"), []string{"LEFT", "RIGHT"})
	assertBrowserStrings(t, ctx, "pair device sections have unique accessible targets", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(node => node.getAttribute('aria-label'))`, pairMember+" > .member-settings-section .device-settings-disclosure > .device-settings-summary"), []string{
		"Device · " + contractLeftName + " settings (LEFT)",
		"Device · " + contractRightName + " settings (RIGHT)",
	})
	assertBrowserExpression(t, ctx, "pair device sections are independently collapsed", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).length === 2 && Array.from(document.querySelectorAll(%q)).every(node => !node.open)`, pairMember+" > .member-settings-section .device-settings-disclosure", pairMember+" > .member-settings-section .device-settings-disclosure"))
}

func openBrowserMemberSettings(t *testing.T, ctx context.Context) {
	t.Helper()

	section := ".zone-logical-member:nth-child(2) > .member-settings-section"
	if err := chromedp.Run(ctx,
		chromedp.Click(section+" > .settings-summary", chromedp.ByQuery),
		chromedp.WaitVisible(section+" .sound-settings-group .stepped-setting-controls", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open pair member settings: %v", err)
	}
	assertBrowserExpression(t, ctx, "opening member settings keeps physical device settings lazy", `performance.getEntriesByType('resource').every(entry => !new URL(entry.name).pathname.includes('/settings/'))`)
	if err := chromedp.Run(ctx,
		chromedp.Click(section+" .device-settings-disclosure:nth-of-type(1) > .device-settings-summary", chromedp.ByQuery),
		chromedp.Poll(fmt.Sprintf(`performance.getEntriesByType('resource').filter(entry => new URL(entry.name).pathname === %q).length === 1`, "/api/control/devices/"+contractPairLeftHost+"/settings/"), nil),
		chromedp.Click(section+" .device-settings-disclosure:nth-of-type(2) > .device-settings-summary", chromedp.ByQuery),
		chromedp.Poll(fmt.Sprintf(`performance.getEntriesByType('resource').filter(entry => new URL(entry.name).pathname === %q).length === 1`, "/api/control/devices/"+contractPairRightHost+"/settings/"), nil),
	); err != nil {
		t.Fatalf("open physical pair device settings: %v", err)
	}
}

func assertBrowserMemberSettings(t *testing.T, ctx context.Context) {
	t.Helper()

	section := ".zone-logical-member:nth-child(2) > .member-settings-section"
	soundSection := section + " > .member-settings-content > .sound-settings-group"
	assertBrowserExpression(t, ctx, "member settings opened", fmt.Sprintf(`document.querySelector(%q)?.open === true`, section))
	assertBrowserStrings(t, ctx, "subordinate sound heading", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(node => node.textContent.trim())`, section+" > .member-settings-content > .sound-settings-group > .member-settings-heading"), []string{"Sound"})
	assertBrowserStrings(t, ctx, "subordinate physical device headings", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(node => node.textContent.trim())`, section+" > .member-settings-content > .device-settings-disclosure > .device-settings-summary > .member-settings-heading"), []string{
		"Device · " + contractLeftName,
		"Device · " + contractRightName,
	})
	assertBrowserExpression(t, ctx, "both physical device settings opened independently", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).length === 2 && Array.from(document.querySelectorAll(%q)).every(node => node.open)`, section+" > .member-settings-content > .device-settings-disclosure", section+" > .member-settings-content > .device-settings-disclosure"))
	assertBrowserExpression(t, ctx, "acoustic controls stay out of the physical device section", fmt.Sprintf(`document.querySelectorAll(%q).length === 0`, section+" .device-settings-group .stepped-setting"))
	assertBrowserStrings(t, ctx, "scoped acoustic settings", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(control => [control.querySelector('.stepped-setting-label').textContent.trim(), control.querySelector('.stepped-setting-scope').textContent.trim(), control.querySelector('.stepped-setting-value').textContent.trim(), control.querySelector('.stepped-setting-footer span').textContent.trim()].join('|'))`, soundSection+" .stepped-setting"), []string{
		strings.Join([]string{"Bass reduction", "Speaker · " + contractRightName, "-3", "Default 0"}, "|"),
		strings.Join([]string{"Balance", "Stereo pair · " + contractPairName, "Centered", "Default Centered"}, "|"),
	})
	assertBrowserExpression(t, ctx, "discrete controls have stable touch targets", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).length === 4 && Array.from(document.querySelectorAll(%q)).every(button => { const rect = button.getBoundingClientRect(); return rect.width >= 44 && rect.height >= 44; })`, soundSection+" .stepped-setting-step", soundSection+" .stepped-setting-step"))
	assertBrowserStrings(t, ctx, "scoped acoustic setting output labels", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(output => output.getAttribute('aria-label'))`, soundSection+" .stepped-setting-value"), []string{
		"Bass reduction for Speaker · " + contractRightName + ": -3",
		"Balance for Stereo pair · " + contractPairName + ": Centered",
	})
	assertBrowserStrings(t, ctx, "scoped reset labels", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(button => button.getAttribute('aria-label'))`, soundSection+" .stepped-setting-reset"), []string{
		"Reset bass reduction for Speaker · " + contractRightName + " to 0",
		"Reset balance for Stereo pair · " + contractPairName + " to Centered",
	})
	assertBrowserExpression(t, ctx, "range sliders are absent from sound settings", fmt.Sprintf(`document.querySelectorAll(%q).length === 0`, soundSection+" input[type=range]"))
	assertBrowserExpression(t, ctx, "each physical speaker has one connected network summary", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).length === 2 && Array.from(document.querySelectorAll(%q)).every(list => list.querySelectorAll(':scope > .settings-network-row').length === 1)`, section+" .settings-network-primary", section+" .settings-network-primary"))
	assertBrowserStrings(t, ctx, "connected network summary includes only type IP and band", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(row => [row.querySelector('strong').textContent.trim(), row.querySelector('span').textContent.trim()].join('|'))`, section+" .settings-network-primary > .settings-network-row"), []string{
		"Wi-Fi · Whole-house wireless network|192.168.101.103 · 5GHz",
		"Wi-Fi · Whole-house wireless network|192.168.101.103 · 5GHz",
	})
	assertBrowserStrings(t, ctx, "firmware quality is subordinate topology-sensitive conflict evidence", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).map(node => node.textContent.trim())`, section+" .settings-network-evidence"), []string{
		"Firmware network quality: Marginal (/netStats; /networkInfo: Poor; topology-sensitive)",
		"Firmware network quality: Marginal (/netStats; /networkInfo: Poor; topology-sensitive)",
	})
	assertBrowserExpression(t, ctx, "firmware quality is not presented as signal RSSI or dBm", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).every(node => !/signal|rssi|dbm/i.test(node.textContent))`, section+" .settings-network-evidence"))
	assertBrowserExpression(t, ctx, "disconnected interfaces stay in closed technical disclosures", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).length === 2 && Array.from(document.querySelectorAll(%q)).every(details => !details.open && details.querySelectorAll('.settings-network-row').length === 2 && details.querySelector('summary').textContent.trim() === 'Technical details · 2 disconnected interfaces')`, section+" .settings-network-technical", section+" .settings-network-technical"))
	assertBrowserExpression(t, ctx, "disconnected interface names are absent from primary summaries", fmt.Sprintf(`Array.from(document.querySelectorAll(%q)).every(list => !list.textContent.includes('wlan1') && !list.textContent.includes('eth0'))`, section+" .settings-network-primary"))
}

func assertBrowserEmbeddedSettingsGenerationFence(t *testing.T, ctx context.Context) {
	t.Helper()

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(async () => {
			const [{ Settings }, { api }, { h, render }] = await Promise.all([
				import('/app/static/js/components/Settings.js'),
				import('/app/static/js/api.js'),
				import('/app/static/js/dependencies.js'),
			]);
			const root = document.createElement('div');
			root.id = 'settings-generation-contract';
			document.body.append(root);
			const requests = [];
			const originalSettings = api.settings;
			api.settings = deviceId => new Promise((resolve, reject) => {
				requests.push({ deviceId, resolve, reject });
			});
			window.__settingsGenerationContract = {
				requests,
				renderDevice(deviceId) {
					render(h(Settings, { deviceId, targetName: deviceId, embedded: true, active: true }), root);
				},
				cleanup() {
					render(null, root);
					root.remove();
					api.settings = originalSettings;
					delete window.__settingsGenerationContract;
				},
			};
			window.__settingsGenerationContract.renderDevice('stale-success');
		})()`, nil),
		chromedp.Poll(`window.__settingsGenerationContract?.requests.length === 1`, nil),
		chromedp.Evaluate(`window.__settingsGenerationContract.renderDevice('current-success')`, nil),
		chromedp.Poll(`window.__settingsGenerationContract?.requests.length === 2`, nil),
		chromedp.Evaluate(`window.__settingsGenerationContract.requests[1].resolve({ success: true, data: { support: {}, errors: { network: 'current response' } } })`, nil),
		chromedp.Poll(`document.querySelector('#settings-generation-contract')?.textContent.includes('current response')`, nil),
		chromedp.Evaluate(`(async () => {
			window.__settingsGenerationContract.requests[0].resolve({ success: true, data: { support: {}, errors: { network: 'stale response' } } });
			await new Promise(resolve => setTimeout(resolve, 20));
		})()`, nil),
	); err != nil {
		t.Fatalf("exercise stale settings success: %v", err)
	}

	assertBrowserExpression(t, ctx, "stale settings success cannot replace current readback", `document.querySelector('#settings-generation-contract')?.textContent.includes('current response') && !document.querySelector('#settings-generation-contract')?.textContent.includes('stale response')`)

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__settingsGenerationContract.renderDevice('stale-error')`, nil),
		chromedp.Poll(`window.__settingsGenerationContract?.requests.length === 3`, nil),
		chromedp.Evaluate(`window.__settingsGenerationContract.renderDevice('current-after-error')`, nil),
		chromedp.Poll(`window.__settingsGenerationContract?.requests.length === 4`, nil),
		chromedp.Evaluate(`window.__settingsGenerationContract.requests[3].resolve({ success: true, data: { support: {}, errors: { network: 'current after error' } } })`, nil),
		chromedp.Poll(`document.querySelector('#settings-generation-contract')?.textContent.includes('current after error')`, nil),
		chromedp.Evaluate(`(async () => {
			window.__settingsGenerationContract.requests[2].reject(new Error('stale rejection'));
			await new Promise(resolve => setTimeout(resolve, 20));
		})()`, nil),
	); err != nil {
		t.Fatalf("exercise stale settings error: %v", err)
	}

	assertBrowserExpression(t, ctx, "stale settings error cannot replace current readback", `document.querySelector('#settings-generation-contract')?.textContent.includes('current after error') && !document.querySelector('#settings-generation-contract')?.textContent.includes('stale rejection') && !document.querySelector('#settings-generation-contract .settings-load-error')`)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__settingsGenerationContract.renderDevice('retry-target')`, nil),
		chromedp.Poll(`window.__settingsGenerationContract?.requests.length === 5`, nil),
		chromedp.Evaluate(`window.__settingsGenerationContract.requests[4].reject(new Error('retryable failure'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-generation-contract .settings-load-error')?.textContent.includes('retryable failure')`, nil),
		chromedp.Click("#settings-generation-contract .settings-load-error button", chromedp.ByQuery),
		chromedp.Poll(`window.__settingsGenerationContract?.requests.length === 6`, nil),
		chromedp.Evaluate(`window.__settingsGenerationContract.requests[5].resolve({ success: true, data: { support: {}, errors: { network: 'retry response' } } })`, nil),
		chromedp.Poll(`document.querySelector('#settings-generation-contract')?.textContent.includes('retry response') && !document.querySelector('#settings-generation-contract .settings-load-error')`, nil),
	); err != nil {
		t.Fatalf("retry current settings error: %v", err)
	}

	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__settingsGenerationContract.cleanup()`, nil)); err != nil {
		t.Fatalf("clean up settings generation contract: %v", err)
	}
}

func assertBrowserEmbeddedSettingsMutationGenerationFence(t *testing.T, ctx context.Context) {
	t.Helper()

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(async () => {
			const [{ Settings }, { api }, { h, render }] = await Promise.all([
				import('/app/static/js/components/Settings.js'),
				import('/app/static/js/api.js'),
				import('/app/static/js/dependencies.js'),
			]);
			const root = document.createElement('div');
			root.id = 'settings-mutation-generation-contract';
			document.body.append(root);
			const loads = [];
			const mutations = [];
			const originalSettings = api.settings;
			const originalSetSystemTimeout = api.setSystemTimeout;
			api.settings = deviceId => new Promise((resolve, reject) => {
				loads.push({ deviceId, resolve, reject });
			});
			api.setSystemTimeout = (deviceId, enabled) => new Promise((resolve, reject) => {
				mutations.push({ deviceId, enabled, resolve, reject });
			});
			const response = (marker, enabled = true) => ({
				success: true,
				data: {
					support: { systemTimeout: true },
					systemTimeout: { enabled },
					errors: { systemTimeout: marker },
				},
			});
			window.__settingsMutationGenerationContract = {
				loads,
				mutations,
				response,
				renderDevice(deviceId) {
					render(h(Settings, { deviceId, targetName: deviceId, embedded: true, active: true }), root);
				},
				startMutation() {
					const input = root.querySelector('.settings-toggle input');
					if (!input || input.disabled) throw new Error('standby toggle unavailable');
					input.click();
				},
				cleanup() {
					render(null, root);
					root.remove();
					api.settings = originalSettings;
					api.setSystemTimeout = originalSetSystemTimeout;
					delete window.__settingsMutationGenerationContract;
				},
			};
			window.__settingsMutationGenerationContract.renderDevice('stale-success');
		})()`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.loads.length === 1`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.loads[0].resolve(window.__settingsMutationGenerationContract.response('stale-success-loaded'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('stale-success-loaded')`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.startMutation()`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.mutations.length === 1`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.renderDevice('current-success')`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.loads.length === 2`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.loads[1].resolve(window.__settingsMutationGenerationContract.response('current-success-loaded'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('current-success-loaded')`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.startMutation()`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.mutations.length === 2`, nil),
		chromedp.Evaluate(`(async () => {
			window.__settingsMutationGenerationContract.mutations[0].resolve(window.__settingsMutationGenerationContract.response('stale mutation success'));
			await new Promise(resolve => setTimeout(resolve, 20));
		})()`, nil),
	); err != nil {
		t.Fatalf("exercise stale settings mutation success: %v", err)
	}

	assertBrowserExpression(t, ctx, "settings mutations retain their physical target IDs", `window.__settingsMutationGenerationContract.mutations[0]?.deviceId === 'stale-success' && window.__settingsMutationGenerationContract.mutations[1]?.deviceId === 'current-success'`)
	assertBrowserExpression(t, ctx, "stale mutation success cannot replace the current target or clear its busy state", `(() => {
		const root = document.querySelector('#settings-mutation-generation-contract');
		const input = root?.querySelector('.settings-toggle input');
		return root?.textContent.includes('current-success-loaded') &&
			!root?.textContent.includes('stale mutation success') && input?.disabled === true;
	})()`)

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.mutations[1].resolve(window.__settingsMutationGenerationContract.response('current mutation success', false))`, nil),
		chromedp.Poll(`(() => {
			const root = document.querySelector('#settings-mutation-generation-contract');
			return root?.textContent.includes('current mutation success') &&
				root?.querySelector('.settings-toggle input')?.disabled === false;
		})()`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.renderDevice('stale-error')`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.loads.length === 3`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.loads[2].resolve(window.__settingsMutationGenerationContract.response('stale-error-loaded'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('stale-error-loaded')`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.startMutation()`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.mutations.length === 3`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.renderDevice('current-after-error')`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.loads.length === 4`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.loads[3].resolve(window.__settingsMutationGenerationContract.response('current after stale error'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('current after stale error')`, nil),
		chromedp.Evaluate(`(async () => {
			window.__settingsMutationGenerationContract.mutations[2].reject(new Error('stale mutation rejection'));
			await new Promise(resolve => setTimeout(resolve, 20));
		})()`, nil),
	); err != nil {
		t.Fatalf("exercise stale settings mutation error: %v", err)
	}

	assertBrowserExpression(t, ctx, "stale rejection remains owned by its original target", `window.__settingsMutationGenerationContract.mutations[2]?.deviceId === 'stale-error'`)
	assertBrowserExpression(t, ctx, "stale mutation error cannot appear under the current target", `document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('current after stale error') && !document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('stale mutation rejection')`)

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.renderDevice('stale-unverified')`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.loads.length === 5`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.loads[4].resolve(window.__settingsMutationGenerationContract.response('stale-unverified-loaded'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('stale-unverified-loaded')`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.startMutation()`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.mutations.length === 4`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.renderDevice('current-after-unverified')`, nil),
		chromedp.Poll(`window.__settingsMutationGenerationContract?.loads.length === 6`, nil),
		chromedp.Evaluate(`window.__settingsMutationGenerationContract.loads[5].resolve(window.__settingsMutationGenerationContract.response('current after stale unverified'))`, nil),
		chromedp.Poll(`document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('current after stale unverified')`, nil),
		chromedp.Evaluate(`(async () => {
			window.__settingsMutationGenerationContract.mutations[3].resolve({
				success: false,
				outcome: 'unverified',
				error: 'stale unverified mutation',
				data: window.__settingsMutationGenerationContract.response('stale unverified data').data,
			});
			await new Promise(resolve => setTimeout(resolve, 20));
		})()`, nil),
	); err != nil {
		t.Fatalf("exercise stale unverified settings mutation: %v", err)
	}

	assertBrowserExpression(t, ctx, "stale unverified result remains owned by its original target", `window.__settingsMutationGenerationContract.mutations[3]?.deviceId === 'stale-unverified'`)
	assertBrowserExpression(t, ctx, "stale unverified mutation cannot replace or annotate the current target", `document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('current after stale unverified') && !document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('stale unverified mutation') && !document.querySelector('#settings-mutation-generation-contract')?.textContent.includes('stale unverified data')`)

	var staleControlVisible bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(() => {
		const contract = window.__settingsMutationGenerationContract;
		contract.renderDevice('ownership-target');
		const input = document.querySelector('#settings-mutation-generation-contract .settings-toggle input');
		if (!input || input.disabled) return false;
		input.click();
		return true;
	})()`, &staleControlVisible)); err != nil {
		t.Fatalf("exercise immediate settings target switch: %v", err)
	}
	if staleControlVisible {
		t.Fatal("old settings control remained actionable during an immediate target switch")
	}
	assertBrowserExpression(t, ctx, "immediate target switch sends no cross-target mutation", `window.__settingsMutationGenerationContract.mutations.length === 4`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__settingsMutationGenerationContract.cleanup()`, nil)); err != nil {
		t.Fatalf("clean up settings mutation generation contract: %v", err)
	}
}

func assertBrowserBluetoothClearConfirmation(t *testing.T, ctx context.Context) {
	t.Helper()

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(async () => {
			const [{ Settings }, { api }, { h, render }] = await Promise.all([
				import('/app/static/js/components/Settings.js'),
				import('/app/static/js/api.js'),
				import('/app/static/js/dependencies.js'),
			]);
			const root = document.createElement('div');
			root.id = 'settings-bluetooth-clear-contract';
			document.body.append(root);
			const snapshot = {
				support: { bluetooth: true, bluetoothClear: true },
				bluetooth: { connectionStatus: 'READY' },
				errors: {},
			};
			const originalSettings = api.settings;
			const originalClear = api.clearBluetoothPairings;
			const originalConfirm = window.confirm;
			let calls = 0;
			api.settings = async () => ({ success: true, data: snapshot });
			api.clearBluetoothPairings = async () => {
				calls += 1;
				return {
					success: false,
					outcome: 'unverified',
					error: 'Pairing-list readback is unavailable.',
					data: snapshot,
				};
			};
			window.__settingsBluetoothClearContract = {
				get calls() { return calls; },
				setConfirmation(value) { window.confirm = () => value; },
				click() {
					const button = Array.from(root.querySelectorAll('button'))
						.find(candidate => candidate.textContent.trim() === 'Clear all pairings');
					if (!button) throw new Error('Bluetooth clear button unavailable');
					button.click();
				},
				cleanup() {
					render(null, root);
					root.remove();
					api.settings = originalSettings;
					api.clearBluetoothPairings = originalClear;
					window.confirm = originalConfirm;
					delete window.__settingsBluetoothClearContract;
				},
			};
			render(h(Settings, {
				deviceId: 'bluetooth-clear-target',
				targetName: 'Bluetooth clear target',
				embedded: true,
				active: true,
			}), root);
		})()`, nil),
		chromedp.Poll(`Array.from(document.querySelectorAll('#settings-bluetooth-clear-contract button')).some(button => button.textContent.trim() === 'Clear all pairings')`, nil),
		chromedp.Evaluate(`window.__settingsBluetoothClearContract.setConfirmation(false)`, nil),
		chromedp.Evaluate(`window.__settingsBluetoothClearContract.click()`, nil),
		chromedp.Evaluate(`new Promise(resolve => setTimeout(resolve, 20))`, nil),
	); err != nil {
		t.Fatalf("exercise cancelled Bluetooth clear confirmation: %v", err)
	}
	assertBrowserExpression(t, ctx, "cancelled Bluetooth clear sends no request", `window.__settingsBluetoothClearContract.calls === 0`)

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__settingsBluetoothClearContract.setConfirmation(true)`, nil),
		chromedp.Evaluate(`window.__settingsBluetoothClearContract.click()`, nil),
		chromedp.Poll(`window.__settingsBluetoothClearContract.calls === 1`, nil),
	); err != nil {
		t.Fatalf("exercise confirmed Bluetooth clear: %v", err)
	}
	assertBrowserExpression(t, ctx, "confirmed Bluetooth clear sends exactly one request", `window.__settingsBluetoothClearContract.calls === 1`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__settingsBluetoothClearContract.cleanup()`, nil)); err != nil {
		t.Fatalf("clean up Bluetooth clear confirmation contract: %v", err)
	}
}

func assertBrowserListLayout(t *testing.T, ctx context.Context) {
	t.Helper()

	assertBrowserElementLayout(t, ctx, "device list", []string{
		".device-sort",
		".device-grid",
		".zone-card",
		".zone-card-open",
		".device-header",
		".device-type",
		".zone-card-summary",
		".zone-volume-row",
		".zone-volume-slider",
	}, []string{
		contractZoneCardSelector(contractDegradedHost) + " .device-name",
	})
}

func assertBrowserDetailLayout(t *testing.T, ctx context.Context) {
	t.Helper()

	assertBrowserElementLayout(t, ctx, "expanded group detail", []string{
		".device-detail",
		".zone-member-details",
		".zone-logical-member",
		".zone-logical-identity",
		".zone-logical-name",
		".zone-logical-metadata",
		".zone-member-volume-row",
		".zone-member-volume-slider",
		".member-settings-section",
		".member-settings-content",
		".member-settings-group",
		".member-settings-heading",
		".stepped-setting",
		".stepped-setting-controls",
		".stepped-setting-step",
		".stepped-setting-indicator",
		".settings-network-primary",
		".settings-network-primary > .settings-network-row",
		".settings-network-evidence",
		".settings-network-technical > summary",
		".zone-physical-member",
		".zone-physical-identity",
		".zone-physical-metadata",
	}, []string{
		".zone-logical-member:nth-child(1) .zone-logical-name",
	})
	assertBrowserExpression(t, ctx, "long logical and physical names retain wrapping policy", `[
        document.querySelector('.zone-logical-member:nth-child(2) .zone-logical-name'),
        ...document.querySelectorAll('.zone-physical-name'),
    ].every(element => getComputedStyle(element).overflowWrap === 'anywhere')`)
}

func assertBrowserElementLayout(t *testing.T, ctx context.Context, stage string, selectors, wrappingSelectors []string) {
	t.Helper()

	selectorJSON, err := json.Marshal(selectors)
	if err != nil {
		t.Fatalf("encode %s layout selectors: %v", stage, err)
	}
	wrappingJSON, err := json.Marshal(wrappingSelectors)
	if err != nil {
		t.Fatalf("encode %s wrapping selectors: %v", stage, err)
	}

	expression := fmt.Sprintf(`(() => {
        const failures = [];
        const tolerance = 1;
        const viewportWidth = document.documentElement.clientWidth;
        for (const selector of %s) {
            const elements = Array.from(document.querySelectorAll(selector));
            if (elements.length === 0) {
                failures.push(selector + ': missing');
                continue;
            }
            elements.forEach((element, index) => {
                const rect = element.getBoundingClientRect();
                if (rect.width <= 0 || rect.height <= 0) failures.push(selector + '[' + index + ']: empty bounds');
                if (rect.left < -tolerance || rect.right > viewportWidth + tolerance) failures.push(selector + '[' + index + ']: outside viewport');
                if (element.clientWidth > 0 && element.scrollWidth > element.clientWidth + tolerance) failures.push(selector + '[' + index + ']: horizontally clipped');
                if (element.clientHeight > 0 && element.scrollHeight > element.clientHeight + tolerance) failures.push(selector + '[' + index + ']: vertically clipped');
                const boundary = element.closest('.zone-physical-member, .zone-logical-member, .zone-card, .zone-member-details, .device-detail, .main-content, body');
                if (boundary && boundary !== element) {
                    const parentRect = boundary.getBoundingClientRect();
                    if (rect.left < parentRect.left - tolerance || rect.right > parentRect.right + tolerance) failures.push(selector + '[' + index + ']: outside container');
                }
            });
        }
        for (const selector of %s) {
            const element = document.querySelector(selector);
            if (!element) {
                failures.push(selector + ': missing wrapping target');
                continue;
            }
            const range = document.createRange();
            range.selectNodeContents(element);
            const lineTops = new Set(Array.from(range.getClientRects()).filter(rect => rect.width > 0 && rect.height > 0).map(rect => Math.round(rect.top)));
            if (lineTops.size < 2) failures.push(selector + ': long text did not wrap');
        }
        return failures;
    })()`, selectorJSON, wrappingJSON)

	var failures []string
	evaluateBrowserContract(t, ctx, expression, &failures)
	if len(failures) != 0 {
		t.Fatalf("browser contract failed: %s element layout: %s", stage, strings.Join(failures, "; "))
	}
}

func exerciseGroupVolumeControl(t *testing.T, ctx context.Context, recorder *contractControlRecorder) {
	t.Helper()

	input := contractZoneCardSelector(contractDegradedHost) + " .zone-volume-slider"
	dispatchBrowserSliderSequence(t, ctx, input, 48, 52)
	waitForBrowserControl(t, ctx, input, ".zone-card", ".zone-volume-value", 52)
	assertContractControlCalls(t, recorder, "group-volume", []contractControlCall{
		{kind: "group-volume", controlID: contractDegradedHost, value: 48},
		{kind: "group-volume", controlID: contractDegradedHost, value: 52},
	})

	recorder.reset()
	dispatchBrowserSliderSequence(t, ctx, input, 53, 53)
	waitForBrowserControl(t, ctx, input, ".zone-card", ".zone-volume-value", 53)
	assertContractControlCalls(t, recorder, "group-volume", []contractControlCall{
		{kind: "group-volume", controlID: contractDegradedHost, value: 53},
	})

	recorder.reset()
	evaluateBrowserContract(t, ctx, fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        if (!input) throw new Error('slider is missing');
        const pointer = {bubbles: true, pointerId: 3, pointerType: 'touch', isPrimary: true};
        input.dispatchEvent(new PointerEvent('pointerdown', pointer));
        input.value = '54';
        input.dispatchEvent(new Event('input', {bubbles: true}));
    })()`, input), nil)
	waitForBrowserControl(t, ctx, input, ".zone-card", ".zone-volume-value", 54)
	evaluateBrowserContract(t, ctx, fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        const pointer = {bubbles: true, pointerId: 3, pointerType: 'touch', isPrimary: true};
        input.dispatchEvent(new PointerEvent('pointerup', pointer));
        input.dispatchEvent(new Event('change', {bubbles: true}));
    })()`, input), nil)
	time.Sleep(300 * time.Millisecond)
	assertContractControlCalls(t, recorder, "group-volume", []contractControlCall{
		{kind: "group-volume", controlID: contractDegradedHost, value: 54},
	})
}

func exerciseDetailGroupVolumeControl(
	t *testing.T,
	ctx context.Context,
	recorder *contractControlRecorder,
	app *WebApp,
) {
	t.Helper()

	recorder.reset()
	input := ".device-detail > .controls .volume-row .volume-slider"
	recorder.delayNextGroupVolume(750 * time.Millisecond)
	dispatchBrowserSliderSequence(t, ctx, input, 34, 36)
	assertOptimisticMemberVolumePreview(t, ctx, []string{"28", "36"})
	waitForBrowserControl(t, ctx, input, ".volume-row", ".volume-value", 36)
	assertContractControlCalls(t, recorder, "group-volume", []contractControlCall{
		{kind: "group-volume", controlID: contractDegradedHost, value: 34},
		{kind: "group-volume", controlID: contractDegradedHost, value: 36},
	})
	assertContractControlCalls(t, recorder, "balance", nil)
	assertBrowserMemberVolumeReadback(t, ctx, app)
}

func assertOptimisticMemberVolumePreview(t *testing.T, ctx context.Context, want []string) {
	t.Helper()

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("encode optimistic member-volume expectation: %v", err)
	}
	expression := fmt.Sprintf(
		`JSON.stringify(Array.from(document.querySelectorAll('.zone-member-volume-slider')).map(node => node.value)) === %q`,
		string(encoded),
	)
	var matched bool
	if err := chromedp.Run(ctx, chromedp.Poll(expression, &matched,
		chromedp.WithPollingInterval(10*time.Millisecond),
		chromedp.WithPollingTimeout(300*time.Millisecond))); err != nil {
		t.Fatalf("member sliders did not follow the group slider before its delayed response: %v", err)
	}
}

func assertBrowserMemberVolumeReadback(t *testing.T, ctx context.Context, app *WebApp) {
	t.Helper()

	zone := app.deviceViewSnapshot()[contractDegradedHost].Zone
	if zone == nil {
		t.Fatal("degraded browser fixture lost its projected zone")
	}

	expected := make([]string, 0, len(zone.Members))
	for _, member := range zone.Members {
		if member.ActualVolume == nil {
			continue
		}
		expected = append(expected, fmt.Sprintf("%s=%d", member.Name, *member.ActualVolume))
	}

	assertBrowserStrings(t, ctx, "member volume readback after detail group drag",
		`Array.from(document.querySelectorAll('.zone-logical-member')).map(row => {
			const name = row.querySelector('.zone-logical-name').childNodes[0].textContent.trim();
			const value = row.querySelector('.zone-member-volume-slider').value;
			return name + '=' + value;
		})`, expected)
}

func exerciseLogicalMemberControls(t *testing.T, ctx context.Context, recorder *contractControlRecorder, app *WebApp) {
	t.Helper()

	memberInput := fmt.Sprintf(`input.zone-member-volume-slider[aria-label=%q]`, contractPairName+" volume")
	dispatchBrowserSliderSequence(t, ctx, memberInput, 43, 46)
	waitForBrowserControl(t, ctx, memberInput, ".zone-member-volume-control", ".zone-member-volume-value", 46)
	assertContractControlCalls(t, recorder, "member-volume", []contractControlCall{
		{kind: "member-volume", controlID: contractDegradedHost, memberID: contractPairRightHost, value: 43},
		{kind: "member-volume", controlID: contractDegradedHost, memberID: contractPairRightHost, value: 46},
	})
	recorder.reset()
	exerciseRetainedFinalVolumeFailure(t, ctx, recorder, memberInput)
	recorder.reset()
	exerciseKeyboardFinalVolumeFailure(t, ctx, recorder, app, memberInput)

	recorder.reset()
	pairSoundSettings := ".zone-logical-member:nth-child(2) > .member-settings-section .sound-settings-group"
	bassControl := pairSoundSettings + " .stepped-setting:nth-child(1)"
	balanceControl := pairSoundSettings + " .stepped-setting:nth-child(2)"
	assertUntouchedButtonBlurDoesNotWrite(t, ctx, recorder, balanceControl+` button[aria-label="Move balance one step right"]`)

	clickBrowserSettingAndWait(t, ctx, bassControl, `button[aria-label="Reduce bass one step"]`, "-5")
	clickBrowserSettingAndWait(t, ctx, bassControl, ".stepped-setting-reset", "0")
	assertContractControlCalls(t, recorder, "bass", []contractControlCall{
		{kind: "bass", controlID: contractPairRightHost, value: -4},
		{kind: "bass", controlID: contractPairRightHost, value: 0},
	})

	clickBrowserSettingAndWait(t, ctx, balanceControl, `button[aria-label="Move balance one step right"]`, "+2 Right")
	clickBrowserSettingAndWait(t, ctx, balanceControl, ".stepped-setting-reset", "Centered")
	assertContractControlCalls(t, recorder, "balance", []contractControlCall{
		{kind: "balance", controlID: contractPairRightHost, value: 1},
		{kind: "balance", controlID: contractPairRightHost, value: 0},
	})
}

func assertBrowserUnavailableMemberVolumeControl(t *testing.T, ctx context.Context, recorder *contractControlRecorder) {
	t.Helper()

	recorder.reset()
	card := contractZoneCardSelector(contractUnavailableHost)
	if err := chromedp.Run(ctx,
		chromedp.Click(".device-detail .back-btn", chromedp.ByQuery),
		chromedp.WaitVisible(card, chromedp.ByQuery),
		chromedp.Click(card+" .zone-card-open", chromedp.ByQuery),
		chromedp.WaitVisible(".zone-member-details > summary", chromedp.ByQuery),
		chromedp.Click(".zone-member-details > summary", chromedp.ByQuery),
		chromedp.WaitVisible(".zone-member-volume-slider", chromedp.ByQuery),
	); err != nil {
		t.Fatalf("open ordinary unavailable group detail: %v", err)
	}

	assertBrowserExpression(t, ctx, "one member-volume control per ordinary logical member", `document.querySelectorAll('.zone-logical-member').length === 2 && document.querySelectorAll('.zone-member-volume-slider').length === 2`)
	assertBrowserExpression(t, ctx, "available ordinary member volume remains enabled", `document.querySelector('input.zone-member-volume-slider[aria-label="Gallery volume"]')?.disabled === false`)
	assertBrowserExpression(t, ctx, "unavailable ordinary member keeps a disabled described control", `(() => {
		const input = document.querySelector('input.zone-member-volume-slider[aria-label="Gallery Annex volume"]');
		const description = document.getElementById(input?.getAttribute('aria-describedby'));
		const output = input?.closest('.zone-member-volume-control')?.querySelector('.zone-member-volume-value');
		return input?.disabled === true && description?.textContent.trim() === 'Volume unavailable.' &&
			output?.textContent.trim() === '17';
	})()`)

	evaluateBrowserContract(t, ctx, `(() => {
		const input = document.querySelector('input.zone-member-volume-slider[aria-label="Gallery Annex volume"]');
		input.value = '50';
		input.dispatchEvent(new PointerEvent('pointerdown', {bubbles: true, pointerId: 9, pointerType: 'touch', isPrimary: true}));
		input.dispatchEvent(new Event('input', {bubbles: true}));
		input.dispatchEvent(new PointerEvent('pointerup', {bubbles: true, pointerId: 9, pointerType: 'touch', isPrimary: true}));
		input.dispatchEvent(new Event('change', {bubbles: true}));
	})()`, nil)
	time.Sleep(300 * time.Millisecond)
	assertContractControlCalls(t, recorder, "member-volume", nil)
}

func assertBrowserUnknownMemberVolumeControl(t *testing.T, ctx context.Context) {
	t.Helper()

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(async () => {
			const [{ ZoneMemberVolumeControl }, { api }, { h, render }] = await Promise.all([
				import('/app/static/js/components/ZoneMemberVolumeControl.js'),
				import('/app/static/js/api.js'),
				import('/app/static/js/dependencies.js'),
			]);
			const root = document.createElement('div');
			root.id = 'unknown-member-volume-contract';
			document.body.append(root);
			const originalMemberVolume = api.zoneMemberVolume;
			let calls = 0;
			let resolveFirst;
			api.zoneMemberVolume = (_zoneMasterId, memberId, level) => {
				calls += 1;
				const response = {
					success: true,
					data: {
						requested: level,
						controlId: memberId,
						members: [{ controlId: memberId, actual: level }],
					},
				};
				if (calls === 1) {
					return new Promise(resolve => { resolveFirst = () => resolve(response); });
				}
				return Promise.resolve(response);
			};
			window.__unknownMemberVolumeContract = {
				get calls() { return calls; },
				renderVolume(volume) {
					render(h(ZoneMemberVolumeControl, {
						zoneMasterId: 'unknown-zone-master',
						memberId: 'unknown-member',
						ariaLabel: 'Unknown member volume',
						available: true,
						volume,
					}), root);
				},
				resolveFirst() {
					if (!resolveFirst) throw new Error('first member-volume request is not pending');
					resolveFirst();
				},
				cleanup() {
					render(null, root);
					root.remove();
					api.zoneMemberVolume = originalMemberVolume;
					delete window.__unknownMemberVolumeContract;
				},
			};
			window.__unknownMemberVolumeContract.renderVolume(20);
		})()`, nil),
		chromedp.Poll(`document.querySelector('#unknown-member-volume-contract input.zone-member-volume-slider')?.disabled === false`, nil),
	); err != nil {
		t.Fatalf("render initial known member-volume control: %v", err)
	}

	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`(() => {
			const input = document.querySelector('#unknown-member-volume-contract input.zone-member-volume-slider');
			input.dispatchEvent(new PointerEvent('pointerdown', {bubbles: true, pointerId: 10, pointerType: 'touch', isPrimary: true}));
			input.value = '21';
			input.dispatchEvent(new Event('input', {bubbles: true}));
			input.value = '22';
			input.dispatchEvent(new Event('input', {bubbles: true}));
		})()`, nil),
		chromedp.Poll(`window.__unknownMemberVolumeContract.calls === 1`, nil),
		chromedp.Evaluate(`window.__unknownMemberVolumeContract.renderVolume(null)`, nil),
		chromedp.Poll(`document.querySelector('#unknown-member-volume-contract .zone-member-volume-slider')?.tagName === 'DIV'`, nil),
	); err != nil {
		t.Fatalf("queue member volume before unknown readback: %v", err)
	}

	assertBrowserExpression(t, ctx, "unknown member readback is explicit and does not invent a numeric slider value", `(() => {
		const root = document.querySelector('#unknown-member-volume-contract');
		const group = root?.querySelector('.zone-member-volume-control');
		const description = document.getElementById(group?.getAttribute('aria-describedby'));
		const output = root?.querySelector('.zone-member-volume-value');
		const visual = root?.querySelector('.zone-member-volume-slider-unknown');
		return root?.querySelector('input[type="range"]') === null &&
			root?.querySelector('[aria-valuenow]') === null &&
			group?.getAttribute('role') === 'group' &&
			group?.getAttribute('aria-label') === 'Unknown member volume' &&
			description?.textContent.trim() === 'Volume readback unknown.' &&
			visual?.getAttribute('aria-hidden') === 'true' && output?.textContent.trim() === 'Unknown';
	})()`)
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`window.__unknownMemberVolumeContract.resolveFirst()`, nil),
		chromedp.Evaluate(`new Promise(resolve => setTimeout(resolve, 350))`, nil),
	); err != nil {
		t.Fatalf("settle queued member volume after unknown readback: %v", err)
	}
	assertBrowserExpression(t, ctx, "unknown transition suppresses the delayed queued request", `window.__unknownMemberVolumeContract.calls === 1`)
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.__unknownMemberVolumeContract.cleanup()`, nil)); err != nil {
		t.Fatalf("clean up unknown member-volume contract: %v", err)
	}
}

func assertUntouchedButtonBlurDoesNotWrite(t *testing.T, ctx context.Context, recorder *contractControlRecorder, selector string) {
	t.Helper()

	evaluateBrowserContract(t, ctx, fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        input.focus();
        input.blur();
	})()`, selector), nil)
	time.Sleep(300 * time.Millisecond)
	assertContractControlCalls(t, recorder, "balance", nil)
	assertContractControlCalls(t, recorder, "bass", nil)
}

func clickBrowserSettingAndWait(t *testing.T, ctx context.Context, controlSelector, buttonSelector, want string) {
	t.Helper()

	selector := controlSelector + " " + buttonSelector
	if err := chromedp.Run(ctx, chromedp.Click(selector, chromedp.ByQuery)); err != nil {
		t.Fatalf("click setting control %s: %v", selector, err)
	}
	expression := fmt.Sprintf(`(() => {
		const control = document.querySelector(%q);
		return control?.getAttribute('aria-busy') === 'false' &&
			control?.querySelector('.stepped-setting-value')?.textContent.trim() === %q;
	})()`, controlSelector, want)
	var settled bool
	if err := chromedp.Run(ctx, chromedp.Poll(expression, &settled, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("wait for setting %s to report %q: %v", controlSelector, want, err)
	}
}

func exerciseRetainedFinalVolumeFailure(t *testing.T, ctx context.Context, recorder *contractControlRecorder, selector string) {
	t.Helper()

	expression := fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        if (!input) throw new Error('slider is missing');
        const pointer = {bubbles: true, pointerId: 2, pointerType: 'touch', isPrimary: true};
        input.dispatchEvent(new PointerEvent('pointerdown', pointer));
        input.value = '47';
        input.dispatchEvent(new Event('input', {bubbles: true}));
        input.dispatchEvent(new PointerEvent('pointerup', pointer));
        input.dispatchEvent(new Event('change', {bubbles: true}));
    })()`, selector)
	evaluateBrowserContract(t, ctx, expression, nil)

	failureExpression := fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        const control = input?.closest('.zone-member-volume-control');
        return input?.value === '46' && control?.getAttribute('aria-busy') === 'false' &&
            control?.querySelector('.zone-member-volume-failure')?.textContent.trim() === %q;
    })()`, selector, "1 member failed: "+contractPairName)
	var failed bool
	if err := chromedp.Run(ctx, chromedp.Poll(failureExpression, &failed, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("wait for retained member-volume failure: %v", err)
	}

	beforeBlur := recorder.callsFor("member-volume")
	if len(beforeBlur) != 1 {
		t.Fatalf("member-volume calls before blur = %+v, want one promoted final request", beforeBlur)
	}
	for _, call := range beforeBlur {
		if call != (contractControlCall{kind: "member-volume", controlID: contractDegradedHost, memberID: contractPairRightHost, value: 47}) {
			t.Fatalf("unexpected member-volume call before blur: %+v", call)
		}
	}

	evaluateBrowserContract(t, ctx, fmt.Sprintf(`document.querySelector(%q).dispatchEvent(new FocusEvent('blur', {bubbles: true}))`, selector), nil)
	time.Sleep(300 * time.Millisecond)
	if afterBlur := recorder.callsFor("member-volume"); !reflect.DeepEqual(afterBlur, beforeBlur) {
		t.Fatalf("blur added a member-volume write: before=%+v after=%+v", beforeBlur, afterBlur)
	}
	assertBrowserExpression(t, ctx, "final member-volume failure survives blur", fmt.Sprintf(
		`document.querySelector(%q)?.closest('.zone-member-volume-control')?.querySelector('.zone-member-volume-failure')?.textContent.trim() === %q`,
		selector,
		"1 member failed: "+contractPairName,
	))
}

func exerciseKeyboardFinalVolumeFailure(
	t *testing.T,
	ctx context.Context,
	recorder *contractControlRecorder,
	app *WebApp,
	selector string,
) {
	t.Helper()

	connection, ok := app.GetDevice(contractPairRightHost)
	if !ok || connection.Status().Volume == nil {
		t.Fatal("keyboard failure fixture is missing the authoritative pair volume")
	}
	authoritative := strconv.Itoa(connection.Status().Volume.ActualVolume)
	evaluateBrowserContract(t, ctx, fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        if (!input) throw new Error('slider is missing');
        input.focus();
        input.value = '49';
        input.dispatchEvent(new Event('input', {bubbles: true}));
    })()`, selector), nil)

	var retained bool
	retainedExpression := fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        const control = input?.closest('.zone-member-volume-control');
        return input?.value === '49' && control?.getAttribute('aria-busy') === 'false' &&
            !control?.querySelector('.zone-member-volume-failure');
    })()`, selector)
	if err := chromedp.Run(ctx, chromedp.Poll(retainedExpression, &retained, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("keyboard volume intent rolled back before finalization: %v", err)
	}

	assertContractControlCalls(t, recorder, "member-volume", []contractControlCall{
		{kind: "member-volume", controlID: contractDegradedHost, memberID: contractPairRightHost, value: 49},
	})
	evaluateBrowserContract(t, ctx, fmt.Sprintf(
		`document.querySelector(%q).dispatchEvent(new Event('change', {bubbles: true}))`, selector), nil)

	var finalized bool
	finalizedExpression := fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        const control = input?.closest('.zone-member-volume-control');
        return input?.value === %q && control?.getAttribute('aria-busy') === 'false' &&
            control?.querySelector('.zone-member-volume-failure')?.textContent.trim() === 'simulated member volume failure';
    })()`, selector, authoritative)
	if err := chromedp.Run(ctx, chromedp.Poll(finalizedExpression, &finalized, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("keyboard volume failure did not finalize with authoritative rollback: %v", err)
	}
	assertContractControlCalls(t, recorder, "member-volume", []contractControlCall{
		{kind: "member-volume", controlID: contractDegradedHost, memberID: contractPairRightHost, value: 49},
	})
}

func dispatchBrowserSliderSequence(t *testing.T, ctx context.Context, selector string, inputValue, finalValue int) {
	t.Helper()

	var bounds struct {
		Min int `json:"min"`
		Max int `json:"max"`
	}
	expression := fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        if (!input) throw new Error('slider is missing');
        if (input.disabled) throw new Error('slider is disabled');
        const min = Number(input.min);
        const max = Number(input.max);
        if (%d < min || %d > max || %d < min || %d > max) throw new Error('synthetic slider value is outside UI bounds');
        const pointer = {bubbles: true, pointerId: 1, pointerType: 'touch', isPrimary: true};
        input.dispatchEvent(new PointerEvent('pointerdown', pointer));
        input.value = String(%d);
        input.dispatchEvent(new Event('input', {bubbles: true}));
        input.value = String(%d);
        input.dispatchEvent(new Event('input', {bubbles: true}));
        input.dispatchEvent(new PointerEvent('pointerup', pointer));
        input.dispatchEvent(new Event('change', {bubbles: true}));
        return {min, max};
    })()`, selector, inputValue, inputValue, finalValue, finalValue, inputValue, finalValue)
	evaluateBrowserContract(t, ctx, expression, &bounds)
	if inputValue < bounds.Min || inputValue > bounds.Max || finalValue < bounds.Min || finalValue > bounds.Max {
		t.Fatalf("browser contract attempted out-of-bounds synthetic values %d/%d for %s range %d..%d", inputValue, finalValue, selector, bounds.Min, bounds.Max)
	}
}

func waitForBrowserControl(t *testing.T, ctx context.Context, inputSelector, controlSelector, outputSelector string, value int) {
	t.Helper()

	expression := fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        const control = input?.closest(%q);
        const output = control?.querySelector(%q);
        return input?.value === %q && control?.getAttribute('aria-busy') === 'false' && output?.textContent.trim() === %q;
    })()`, inputSelector, controlSelector, outputSelector, strconv.Itoa(value), strconv.Itoa(value))
	var settled bool
	if err := chromedp.Run(ctx, chromedp.Poll(expression, &settled, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Fatalf("wait for deterministic fake control response on %s: %v", inputSelector, err)
	}
}

func assertContractControlCalls(t *testing.T, recorder *contractControlRecorder, kind string, want []contractControlCall) {
	t.Helper()

	got := recorder.callsFor(kind)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fake API calls = %+v, want %+v", kind, got, want)
	}
}

func contractZoneNameID(host string) string {
	return "zone-name-" + strings.NewReplacer(".", "-", ":", "-").Replace(host)
}

func contractZoneCardSelector(host string) string {
	return fmt.Sprintf(`section.zone-card[aria-labelledby=%q]`, contractZoneNameID(host))
}

func assertNoHorizontalDocumentOverflow(t *testing.T, ctx context.Context, state string) {
	t.Helper()
	assertBrowserExpression(t, ctx, state+" has no horizontal document overflow", `Math.max(document.documentElement.scrollWidth, document.body.scrollWidth) <= document.documentElement.clientWidth`)
}

func assertBrowserExpression(t *testing.T, ctx context.Context, description, expression string) {
	t.Helper()

	var matched bool
	evaluateBrowserContract(t, ctx, expression, &matched)
	if !matched {
		t.Fatalf("browser contract failed: %s", description)
	}
}

func assertBrowserStrings(t *testing.T, ctx context.Context, description, expression string, want []string) {
	t.Helper()

	var got []string
	evaluateBrowserContract(t, ctx, expression, &got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("browser contract failed: %s = %q, want %q", description, got, want)
	}
}

func evaluateBrowserContract(t *testing.T, ctx context.Context, expression string, result interface{}) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Evaluate(expression, result)); err != nil {
		t.Fatalf("evaluate browser contract expression %q: %v", expression, err)
	}
}

func captureBrowserContractFailure(t *testing.T, targetContext context.Context, name string) {
	t.Helper()

	directory := os.Getenv("BROWSER_TEST_ARTIFACTS_DIR")
	if directory == "" {
		directory = filepath.Join(os.TempDir(), "player-browser-contract-artifacts")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Logf("create browser failure artifact directory: %v", err)
		return
	}

	base := filepath.Join(directory, name)
	// The stage run context may have reached its deadline. Derive a fresh,
	// bounded context from the still-live target so timeout failures can retain
	// the current page rather than immediately failing capture with DeadlineExceeded.
	captureContext, cancelCapture := context.WithTimeout(targetContext, 8*time.Second)
	defer cancelCapture()

	var screenshot []byte
	if err := chromedp.Run(captureContext, chromedp.FullScreenshot(&screenshot, 100)); err != nil {
		t.Logf("capture browser failure screenshot: %v", err)
	} else if err := os.WriteFile(base+".png", screenshot, 0o644); err != nil {
		t.Logf("write browser failure screenshot: %v", err)
	}

	var document string
	if err := chromedp.Run(captureContext, chromedp.OuterHTML("html", &document, chromedp.ByQuery)); err != nil {
		t.Logf("capture browser failure DOM: %v", err)
	} else if err := os.WriteFile(base+".html", []byte(document), 0o644); err != nil {
		t.Logf("write browser failure DOM: %v", err)
	}
	t.Logf("browser failure artifacts: %s.{png,html}", base)
}
