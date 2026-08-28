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
	mu    sync.Mutex
	calls []contractControlCall
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
			exerciseDetailGroupVolumeControl(t, runContext, controls)

			if err := chromedp.Run(runContext,
				chromedp.Click(".zone-member-details > summary", chromedp.ByQuery),
				chromedp.WaitVisible(".zone-physical-members", chromedp.ByQuery),
			); err != nil {
				t.Fatalf("open member disclosure: %v", err)
			}
			assertBrowserMemberDisclosure(t, runContext)
			assertBrowserDetailLayout(t, runContext)
			assertNoHorizontalDocumentOverflow(t, runContext, "open member disclosure")
			exerciseLogicalMemberControls(t, runContext, controls)
		})
	}
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
		case serveBrowserContractControlResponse(w, r, controls):
		case r.URL.Path == "/api/announcements":
			writeBrowserContractJSON(w, map[string]interface{}{"announcements": []interface{}{}})
		case strings.HasSuffix(r.URL.Path, "/zone") || strings.HasSuffix(r.URL.Path, "/zone/"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
				"masterIp":            degradedZone.MasterControlID,
				"masterHwId":          degradedZone.MasterDeviceID,
				"masterName":          degradedZone.Members[0].Name,
				"master":              degradedZone.Members[0],
				"members":             degradedZone.Members[1:],
				"physicalMemberCount": degradedZone.PhysicalMemberCount,
				"isMaster":            true,
				"isSlave":             false,
				"isStandalone":        false,
			}})
		case strings.HasSuffix(r.URL.Path, "/stereo-pair/"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{"capable": false}})
		case strings.HasSuffix(r.URL.Path, "/recents"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: []interface{}{}})
		case strings.HasSuffix(r.URL.Path, "/settings/"):
			writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{}})
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

func serveBrowserContractControlResponse(w http.ResponseWriter, r *http.Request, recorder *contractControlRecorder) bool {
	if r.Method != http.MethodPost {
		return false
	}

	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 7 || parts[0] != "api" || parts[1] != "control" || parts[2] != "devices" {
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
	default:
		return false
	}

	value, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || ((call.kind == "balance") && (value < -7 || value > 7)) ||
		((call.kind != "balance") && (value < 0 || value > 100)) {
		writeBrowserContractError(w, "test control value outside fixture bounds", http.StatusBadRequest)
		return true
	}
	call.value = value
	recorder.record(call)

	switch call.kind {
	case "group-volume":
		writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
			"requested": value,
			"partial":   false,
			"members": []map[string]interface{}{
				{"controlId": contractDegradedHost, "actual": value},
				{"controlId": contractPairRightHost, "actual": value},
			},
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
		writeBrowserContractJSON(w, webtypes.APIResponse{Success: true, Data: map[string]interface{}{
			"requested": value,
			"target":    value,
			"actual":    value,
			"atTarget":  true,
		}})
	}

	return true
}

func writeBrowserContractError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(webtypes.APIResponse{Success: false, Error: message})
}

func (recorder *contractControlRecorder) reset() {
	recorder.mu.Lock()
	recorder.calls = nil
	recorder.mu.Unlock()
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
	assertBrowserExpression(t, ctx, "one enabled logical stereo balance control", fmt.Sprintf(`document.querySelectorAll('.zone-logical-member .stereo-balance-slider').length === 1 && (() => { const input = document.querySelector('.zone-logical-member .stereo-balance-slider'); return input.getAttribute('aria-label') === %q && !input.disabled && input.min === '-7' && input.max === '7' && input.value === '0' && input.closest('.stereo-balance-control').getAttribute('aria-disabled') === 'false'; })()`, contractPairName+" balance"))
	assertBrowserExpression(t, ctx, "logical stereo balance keeps a touch-sized target", `document.querySelector('.zone-logical-member .stereo-balance-slider').getBoundingClientRect().height >= 44`)
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
		".stereo-balance-row",
		".stereo-balance-slider",
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
}

func exerciseDetailGroupVolumeControl(t *testing.T, ctx context.Context, recorder *contractControlRecorder) {
	t.Helper()

	recorder.reset()
	input := ".device-detail > .controls .volume-row .volume-slider"
	dispatchBrowserSliderSequence(t, ctx, input, 34, 36)
	waitForBrowserControl(t, ctx, input, ".volume-row", ".volume-value", 36)
	assertContractControlCalls(t, recorder, "group-volume", []contractControlCall{
		{kind: "group-volume", controlID: contractDegradedHost, value: 34},
		{kind: "group-volume", controlID: contractDegradedHost, value: 36},
	})
}

func exerciseLogicalMemberControls(t *testing.T, ctx context.Context, recorder *contractControlRecorder) {
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

	balanceInput := fmt.Sprintf(`input.stereo-balance-slider[aria-label=%q]`, contractPairName+" balance")
	assertUntouchedControlBlurDoesNotWrite(t, ctx, recorder, balanceInput)
	dispatchBrowserSliderSequence(t, ctx, balanceInput, -2, 3)
	waitForBrowserControl(t, ctx, balanceInput, ".stereo-balance-control", ".stereo-balance-value", 3)
	assertContractControlCalls(t, recorder, "balance", []contractControlCall{
		{kind: "balance", controlID: contractPairRightHost, value: -2},
		{kind: "balance", controlID: contractPairRightHost, value: 3},
	})
}

func assertUntouchedControlBlurDoesNotWrite(t *testing.T, ctx context.Context, recorder *contractControlRecorder, selector string) {
	t.Helper()

	evaluateBrowserContract(t, ctx, fmt.Sprintf(`(() => {
        const input = document.querySelector(%q);
        input.focus();
        input.blur();
    })()`, selector), nil)
	time.Sleep(300 * time.Millisecond)
	assertContractControlCalls(t, recorder, "balance", nil)
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
	if len(beforeBlur) < 1 || len(beforeBlur) > 2 {
		t.Fatalf("member-volume calls before blur = %+v, want one coalesced final or one intermediate plus final", beforeBlur)
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
