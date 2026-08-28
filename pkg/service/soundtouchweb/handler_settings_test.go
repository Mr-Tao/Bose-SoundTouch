package soundtouchweb

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

type settingsSpeakerFixture struct {
	server *httptest.Server

	mu                 sync.Mutex
	clockEnabled       bool
	clockFormat        string
	clockTimeZone      string
	clockHasFormat     bool
	clockHasZone       bool
	clockHasTime       bool
	timeoutEnabled     bool
	language           int
	syncMode           string
	sourceName         string
	discoverable       bool
	netStatsAdvertised bool
	netStatsStatus     int
	netStatsBody       string
	networkInfoSignal  string

	clockPosts     atomic.Int32
	clockTimePosts atomic.Int32
	clearGets      atomic.Int32
	netStatsGets   atomic.Int32
}

func newSettingsSpeakerFixture(t *testing.T, advertiseSettings bool) *settingsSpeakerFixture {
	t.Helper()

	fixture := &settingsSpeakerFixture{
		clockEnabled:       false,
		clockFormat:        "TIME_FORMAT_24HOUR_ID",
		clockTimeZone:      "Europe/Prague",
		clockHasFormat:     true,
		clockHasZone:       true,
		clockHasTime:       true,
		timeoutEnabled:     true,
		language:           15,
		syncMode:           "SYNC_TO_ZONE",
		sourceName:         "Line in",
		netStatsAdvertised: advertiseSettings,
		netStatsStatus:     http.StatusOK,
		networkInfoSignal:  "POOR_SIGNAL",
		netStatsBody: `<network-data><devices><device id="radio"><interfaces>` +
			`<interface><name>eth0</name><mac-addr>11:22:33:44:55:66</mac-addr>` +
			`<bindings><ipv4address>192.0.2.10</ipv4address></bindings><running>true</running>` +
			`<kind>Wireless</kind><ssid>Test WiFi</ssid><rssi>Good</rssi>` +
			`<frequencyKHz>5200000</frequencyKHz></interface>` +
			`</interfaces></device></devices></network-data>`,
	}

	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()

		w.Header().Set("Content-Type", "application/xml")

		switch r.URL.Path {
		case "/capabilities":
			if advertiseSettings {
				_, _ = io.WriteString(w, `<capabilities><networkConfig>`+
					`<hostedWifiConfigWebPage hostedBy="BCO" generation="1" port="80">true</hostedWifiConfigWebPage>`+
					`</networkConfig><clockDisplay>true</clockDisplay>`+
					`<capability name="systemtimeout" url="/systemtimeout"/>`+
					`<capability name="rebroadcastlatencymode" url="/rebroadcastlatencymode"/>`+
					`</capabilities>`)
			} else {
				_, _ = io.WriteString(w, `<capabilities><clockDisplay>false</clockDisplay></capabilities>`)
			}
		case "/supportedURLs":
			locations := []string{"/sources"}
			if advertiseSettings {
				locations = append(locations,
					"/clockDisplay", "/clockTime", "/systemtimeout", "/language",
					"/rebroadcastlatencymode", "/bluetoothInfo", "/enterPairingMode",
					"/clearPairedList", "/nameSource", "/networkInfo",
				)
				if fixture.netStatsAdvertised {
					locations = append(locations, "/netStats")
				}
			}

			_, _ = io.WriteString(w, `<supportedURLs>`)
			for _, location := range locations {
				_, _ = fmt.Fprintf(w, `<URL location="%s"/>`, location)
			}
			_, _ = io.WriteString(w, `</supportedURLs>`)
		case "/sources":
			_, _ = fmt.Fprintf(w, `<sources><sourceItem source="BLUETOOTH" status="READY">Bluetooth</sourceItem>`+
				`<sourceItem source="AUX" sourceAccount="AUX1" status="READY" isLocal="true">%s</sourceItem></sources>`, fixture.sourceName)
		case "/clockDisplay":
			if r.Method == http.MethodPatch || r.Method == http.MethodPost {
				fixture.clockPosts.Add(1)
				body, _ := io.ReadAll(r.Body)
				text := string(body)
				if strings.Contains(text, `userEnable="true"`) {
					fixture.clockEnabled = true
				}
				if strings.Contains(text, `userEnable="false"`) {
					fixture.clockEnabled = false
				}
				if strings.Contains(text, `timeFormat="TIME_FORMAT_12HOUR_ID"`) {
					fixture.clockFormat = "TIME_FORMAT_12HOUR_ID"
				}
				if strings.Contains(text, `timeFormat="TIME_FORMAT_24HOUR_ID"`) {
					fixture.clockFormat = "TIME_FORMAT_24HOUR_ID"
				}
			}

			attributes := []string{fmt.Sprintf(`userEnable="%t"`, fixture.clockEnabled), `brightnessLevel="70"`}
			if fixture.clockHasZone {
				attributes = append(attributes, fmt.Sprintf(`timezoneInfo="%s"`, fixture.clockTimeZone))
			}
			if fixture.clockHasFormat {
				attributes = append(attributes, fmt.Sprintf(`timeFormat="%s"`, fixture.clockFormat))
			}
			_, _ = fmt.Fprintf(w, `<clockDisplay><clockConfig %s/></clockDisplay>`, strings.Join(attributes, " "))
		case "/clockTime":
			if r.Method == http.MethodPost {
				fixture.clockTimePosts.Add(1)
			}
			if fixture.clockHasTime {
				_, _ = fmt.Fprintf(w, `<clockTime utcTime="%d"/>`, time.Now().Unix())
			} else {
				_, _ = io.WriteString(w, `<clockTime/>`)
			}
		case "/systemtimeout":
			if r.Method == http.MethodPost {
				body, _ := io.ReadAll(r.Body)
				fixture.timeoutEnabled = strings.Contains(string(body), `<powersaving_enabled>true</powersaving_enabled>`)
			}
			_, _ = fmt.Fprintf(w, `<systemtimeout><powersaving_enabled>%t</powersaving_enabled></systemtimeout>`, fixture.timeoutEnabled)
		case "/language":
			if r.Method == http.MethodPost {
				body, _ := io.ReadAll(r.Body)
				_, _ = fmt.Sscanf(string(body), `<sysLanguage>%d</sysLanguage>`, &fixture.language)
			}
			_, _ = fmt.Fprintf(w, `<sysLanguage>%d</sysLanguage>`, fixture.language)
		case "/rebroadcastlatencymode":
			if r.Method == http.MethodPost {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), `SYNC_TO_ROOM`) {
					fixture.syncMode = "SYNC_TO_ROOM"
				} else {
					fixture.syncMode = "SYNC_TO_ZONE"
				}
			}
			_, _ = fmt.Fprintf(w, `<rebroadcastlatencymode mode="%s" controllable="true"/>`, fixture.syncMode)
		case "/bluetoothInfo":
			_, _ = io.WriteString(w, `<BluetoothInfo BluetoothMACAddress="AA:BB:CC:DD:EE:FF"/>`)
		case "/enterPairingMode":
			fixture.discoverable = true
			_, _ = io.WriteString(w, `<status>/enterPairingMode</status>`)
		case "/clearPairedList":
			fixture.clearGets.Add(1)
			_, _ = io.WriteString(w, `<status>/clearPairedList</status>`)
		case "/now_playing":
			status := "DISCONNECTED"
			if fixture.discoverable {
				status = "DISCOVERABLE"
			}
			_, _ = fmt.Fprintf(w, `<nowPlaying source="BLUETOOTH"><connectionStatusInfo status="%s" deviceName="Test phone"/></nowPlaying>`, status)
		case "/nameSource":
			body, _ := io.ReadAll(r.Body)
			var request models.SourceRenameRequest
			if err := xml.Unmarshal(body, &request); err != nil {
				http.Error(w, "invalid nameSource body", http.StatusBadRequest)

				return
			}
			fixture.sourceName = request.ItemName
			_, _ = io.WriteString(w, `<status>/nameSource</status>`)
		case "/networkInfo":
			_, _ = fmt.Fprintf(w, `<networkInfo><interfaces>`+
				`<interface type="WIFI_INTERFACE" name="wlan0" macAddress="AA:BB:CC:DD:EE:FF" ipAddress="192.0.2.10" ssid="Test WiFi" frequencyKHz="5200000" state="NETWORK_WIFI_CONNECTED" signal="%s"/>`+
				`<interface type="WIFI_INTERFACE" name="wlan1" macAddress="AA:BB:CC:DD:EE:01" state="NETWORK_WIFI_DISCONNECTED" signal="POOR_SIGNAL"/>`+
				`<interface type="ETHERNET_INTERFACE" name="eth0" macAddress="AA:BB:CC:DD:EE:02" state="NETWORK_ETHERNET_DISCONNECTED"/>`+
				`</interfaces></networkInfo>`, fixture.networkInfoSignal)
		case "/netStats":
			fixture.netStatsGets.Add(1)
			w.WriteHeader(fixture.netStatsStatus)
			_, _ = io.WriteString(w, fixture.netStatsBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	t.Cleanup(fixture.server.Close)

	return fixture
}

func settingsTestApp(fixture *settingsSpeakerFixture) *WebApp {
	app := NewWebApp()
	info := &models.DeviceInfo{
		DeviceID: "AABBCCDDEEFF",
		Name:     "Test speaker",
		Type:     "SoundTouch 20",
		NetworkInfo: []models.NetworkInfo{
			{IPAddress: "192.0.2.10"},
		},
	}
	connection := webtypes.NewDeviceConnection(client.NewClient(&client.Config{Host: fixture.server.URL}), info)
	connection.SetStatus(&webtypes.DeviceStatus{NowPlaying: &models.NowPlaying{Source: "STANDBY"}})
	app.AddDevice("speaker", connection)
	app.OnboardingURL = "/setup/"

	return app
}

func settingsRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	return withChiParams(request, map[string]string{"id": "speaker"})
}

func decodeSettingsResponse(t *testing.T, recorder *httptest.ResponseRecorder) struct {
	Success bool                   `json:"success"`
	Outcome string                 `json:"outcome"`
	Error   string                 `json:"error"`
	Data    deviceSettingsSnapshot `json:"data"`
} {
	t.Helper()

	var response struct {
		Success bool                   `json:"success"`
		Outcome string                 `json:"outcome"`
		Error   string                 `json:"error"`
		Data    deviceSettingsSnapshot `json:"data"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode settings response: %v", err)
	}

	return response
}

func TestHandleGetDeviceSettingsProjectsOnlyAdvertisedControls(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet, "/api/control/devices/speaker/settings", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	response := decodeSettingsResponse(t, recorder)
	if !response.Success {
		t.Fatalf("settings response failed: %s", response.Error)
	}

	support := response.Data.Support
	if !support.ClockDisplay || !support.ClockTime || !support.SystemTimeout || !support.Language || !support.Sync {
		t.Fatalf("missing supported system controls: %+v", support)
	}
	if response.Data.ClockDisplay == nil || response.Data.ClockDisplay.Enabled ||
		response.Data.ClockDisplay.Format != "24" || response.Data.ClockDisplay.TimeZone != "Europe/Prague" {
		t.Fatalf("unexpected clock-display projection: %+v", response.Data.ClockDisplay)
	}
	if response.Data.ClockTime == nil || response.Data.ClockTime.UTC == 0 {
		t.Fatalf("unexpected clock-time projection: %+v", response.Data.ClockTime)
	}
	if !support.Bluetooth || !support.BluetoothPair || !support.BluetoothClear || !support.SourceNaming {
		t.Fatalf("missing supported source controls: %+v", support)
	}
	if response.Data.Language == nil || response.Data.Language.Code != 15 || len(response.Data.Language.Options) != 24 {
		t.Fatalf("unexpected language projection: %+v", response.Data.Language)
	}
	if response.Data.Sync == nil || response.Data.Sync.Mode != "SYNC_TO_ZONE" {
		t.Fatalf("unexpected sync projection: %+v", response.Data.Sync)
	}
	if response.Data.Bluetooth == nil || response.Data.Bluetooth.MACAddress != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("unexpected Bluetooth projection: %+v", response.Data.Bluetooth)
	}
	if len(response.Data.Sources) != 1 || response.Data.Sources[0].SourceAccount != "AUX1" {
		t.Fatalf("unexpected renameable sources: %+v", response.Data.Sources)
	}
	if response.Data.Network == nil || len(response.Data.Network.Interfaces) != 3 {
		t.Fatalf("unexpected network projection: %+v", response.Data.Network)
	}
	connected := response.Data.Network.Interfaces[0]
	if connected.FirmwareNetworkQuality != "Good" || connected.FirmwareNetworkQualitySource != "netStats" ||
		connected.FirmwareNetworkQualityState != "conflict" || connected.NetworkInfoFirmwareQuality != "Poor" || connected.Band != "5GHz" {
		t.Fatalf("firmware network quality conflict was not retained: %+v", connected)
	}
	if connected.Name != "wlan0" || connected.MACAddress != "AA:BB:CC:DD:EE:FF" {
		t.Fatalf("/netStats identity replaced the physical /networkInfo interface: %+v", connected)
	}
	if fixture.netStatsGets.Load() != 1 {
		t.Fatalf("/netStats GET count = %d, want 1", fixture.netStatsGets.Load())
	}
	if !support.WiFiOnboarding || response.Data.OnboardingURL != "/setup/" {
		t.Fatalf("unexpected Wi-Fi onboarding projection: support=%+v url=%q",
			support, response.Data.OnboardingURL)
	}
}

func TestHandleGetDeviceSettingsNetworkStatsFallback(t *testing.T) {
	tests := []struct {
		name               string
		advertised         bool
		status             int
		body               string
		networkInfoQuality string
		wantStatsRequests  int32
		wantQuality        string
		wantSource         string
		wantState          string
	}{
		{
			name:        "endpoint absent keeps networkInfo report",
			advertised:  false,
			status:      http.StatusOK,
			wantQuality: "Poor",
			wantSource:  "networkInfo",
			wantState:   "reported",
		},
		{
			name:              "temporary endpoint failure uses fallback",
			advertised:        true,
			status:            http.StatusServiceUnavailable,
			body:              `<error>busy</error>`,
			wantStatsRequests: 1,
			wantQuality:       "Poor",
			wantSource:        "networkInfo",
			wantState:         "fallback",
		},
		{
			name:              "malformed endpoint response uses fallback",
			advertised:        true,
			status:            http.StatusOK,
			body:              `<network-data>`,
			wantStatsRequests: 1,
			wantQuality:       "Poor",
			wantSource:        "networkInfo",
			wantState:         "fallback",
		},
		{
			name:       "conflicting identity uses fallback",
			advertised: true,
			status:     http.StatusOK,
			body: `<network-data><devices><device><interfaces><interface>` +
				`<running>true</running><kind>Wireless</kind><bindings><ipv4address>192.0.2.10</ipv4address></bindings>` +
				`<ssid>Other WiFi</ssid><rssi>Excellent</rssi></interface></interfaces></device></devices></network-data>`,
			wantStatsRequests: 1,
			wantQuality:       "Poor",
			wantSource:        "networkInfo",
			wantState:         "fallback",
		},
		{
			name:       "ambiguous identity uses fallback",
			advertised: true,
			status:     http.StatusOK,
			body: `<network-data><devices><device><interfaces>` +
				`<interface><running>true</running><kind>Wireless</kind><bindings><ipv4address>192.0.2.10</ipv4address></bindings><ssid>Test WiFi</ssid><rssi>Good</rssi></interface>` +
				`<interface><running>true</running><kind>Wireless</kind><bindings><ipv4address>192.0.2.10</ipv4address></bindings><ssid>Test WiFi</ssid><rssi>Excellent</rssi></interface>` +
				`</interfaces></device></devices></network-data>`,
			wantStatsRequests: 1,
			wantQuality:       "Poor",
			wantSource:        "networkInfo",
			wantState:         "fallback",
		},
		{
			name:       "invalid nonempty RSSI uses fallback",
			advertised: true,
			status:     http.StatusOK,
			body: `<network-data><devices><device><interfaces><interface>` +
				`<running>true</running><kind>Wireless</kind><bindings><ipv4address>192.0.2.10</ipv4address></bindings>` +
				`<ssid>Test WiFi</ssid><rssi>-54 dBm</rssi></interface></interfaces></device></devices></network-data>`,
			wantStatsRequests: 1,
			wantQuality:       "Poor",
			wantSource:        "networkInfo",
			wantState:         "fallback",
		},
		{
			name:               "unknown RSSI without valid fallback is unavailable",
			advertised:         true,
			status:             http.StatusOK,
			networkInfoQuality: "UNKNOWN_SIGNAL",
			body: `<network-data><devices><device><interfaces><interface>` +
				`<running>true</running><kind>Wireless</kind><bindings><ipv4address>192.0.2.10</ipv4address></bindings>` +
				`<ssid>Test WiFi</ssid><rssi>Unknown</rssi></interface></interfaces></device></devices></network-data>`,
			wantStatsRequests: 1,
			wantState:         "unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettingsSpeakerFixture(t, true)
			fixture.netStatsAdvertised = test.advertised
			fixture.netStatsStatus = test.status
			fixture.netStatsBody = test.body
			if test.networkInfoQuality != "" {
				fixture.networkInfoSignal = test.networkInfoQuality
			}
			app := settingsTestApp(fixture)
			recorder := httptest.NewRecorder()

			app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet,
				"/api/control/devices/speaker/settings", ""))
			response := decodeSettingsResponse(t, recorder)
			if recorder.Code != http.StatusOK || !response.Success || response.Data.Network == nil {
				t.Fatalf("network fallback lost /networkInfo: status=%d response=%+v", recorder.Code, response)
			}

			connected := response.Data.Network.Interfaces[0]
			if connected.FirmwareNetworkQuality != test.wantQuality ||
				connected.FirmwareNetworkQualitySource != test.wantSource ||
				connected.FirmwareNetworkQualityState != test.wantState ||
				connected.NetworkInfoFirmwareQuality != "" {
				t.Fatalf("firmware network quality state = %+v", connected)
			}
			if response.Data.Errors["networkStats"] != "" {
				t.Fatalf("expected telemetry state became a section error: %+v", response.Data.Errors)
			}
			if fixture.netStatsGets.Load() != test.wantStatsRequests {
				t.Fatalf("/netStats GET count = %d, want %d", fixture.netStatsGets.Load(), test.wantStatsRequests)
			}
		})
	}
}

func TestCanonicalFirmwareNetworkQuality(t *testing.T) {
	tests := []struct {
		input string
		want  string
		valid bool
	}{
		{input: "FAIR_SIGNAL", want: "Fair", valid: true},
		{input: "marginal_signal", want: "Marginal", valid: true},
		{input: "Excellent", want: "Excellent", valid: true},
		{input: "UNKNOWN_SIGNAL"},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, valid := canonicalFirmwareNetworkQuality(test.input)
			if got != test.want || valid != test.valid {
				t.Fatalf("canonicalFirmwareNetworkQuality(%q) = (%q, %t), want (%q, %t)", test.input, got, valid, test.want, test.valid)
			}
		})
	}
}

func TestHandleGetDeviceSettingsNetworkStatsAgreementKeepsPrecedence(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.networkInfoSignal = "FAIR_SIGNAL"
	fixture.netStatsBody = strings.Replace(fixture.netStatsBody, "<rssi>Good</rssi>", "<rssi>fair</rssi>", 1)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet,
		"/api/control/devices/speaker/settings", ""))
	response := decodeSettingsResponse(t, recorder)
	if recorder.Code != http.StatusOK || !response.Success || response.Data.Network == nil {
		t.Fatalf("network response = status %d %+v", recorder.Code, response)
	}

	connected := response.Data.Network.Interfaces[0]
	if connected.FirmwareNetworkQuality != "Fair" || connected.FirmwareNetworkQualitySource != "netStats" ||
		connected.FirmwareNetworkQualityState != "reported" || connected.NetworkInfoFirmwareQuality != "" {
		t.Fatalf("agreeing firmware quality did not retain /netStats precedence: %+v", connected)
	}
	if response.Data.Errors["networkStats"] != "" {
		t.Fatalf("agreeing firmware quality reported an error: %+v", response.Data.Errors)
	}
}

func TestHandleGetDeviceSettingsPreservesUnknownCurrentLanguage(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.language = 99
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet,
		"/api/control/devices/speaker/settings", ""))
	response := decodeSettingsResponse(t, recorder)
	if recorder.Code != http.StatusOK || !response.Success || response.Data.Language == nil {
		t.Fatalf("response = status %d %+v", recorder.Code, response)
	}
	options := response.Data.Language.Options
	last := options[len(options)-1]
	if response.Data.Language.Code != 99 || last.Code != 99 || last.Name != "Unknown (99)" {
		t.Fatalf("unknown current language was not preserved: %+v", response.Data.Language)
	}
}

func TestHandleSetClockDisplayRequiresCapabilityAndReadback(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		fixture := newSettingsSpeakerFixture(t, true)
		app := settingsTestApp(fixture)
		recorder := httptest.NewRecorder()

		app.HandleSetClockDisplay(recorder, settingsRequest(http.MethodPatch,
			"/api/control/devices/speaker/settings/clock-display", `{"enabled":true,"format":"12"}`))

		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		response := decodeSettingsResponse(t, recorder)
		if response.Data.ClockDisplay == nil || !response.Data.ClockDisplay.Enabled || response.Data.ClockDisplay.Format != "12" {
			t.Fatalf("unexpected readback: %+v", response.Data.ClockDisplay)
		}
		if fixture.clockPosts.Load() != 1 {
			t.Fatalf("clock POST count = %d, want 1", fixture.clockPosts.Load())
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		fixture := newSettingsSpeakerFixture(t, false)
		app := settingsTestApp(fixture)
		recorder := httptest.NewRecorder()

		app.HandleSetClockDisplay(recorder, settingsRequest(http.MethodPatch,
			"/api/control/devices/speaker/settings/clock-display", `{"enabled":true}`))

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if fixture.clockPosts.Load() != 0 {
			t.Fatalf("unsupported clock control sent %d POSTs", fixture.clockPosts.Load())
		}
	})

	t.Run("format omitted from readback", func(t *testing.T) {
		fixture := newSettingsSpeakerFixture(t, true)
		fixture.clockHasFormat = false
		app := settingsTestApp(fixture)
		recorder := httptest.NewRecorder()

		app.HandleSetClockDisplay(recorder, settingsRequest(http.MethodPatch,
			"/api/control/devices/speaker/settings/clock-display", `{"format":"12"}`))

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if fixture.clockPosts.Load() != 0 {
			t.Fatalf("unsupported time format sent %d POSTs", fixture.clockPosts.Load())
		}
	})

	t.Run("timezone omitted from readback", func(t *testing.T) {
		fixture := newSettingsSpeakerFixture(t, true)
		fixture.clockHasZone = false
		app := settingsTestApp(fixture)
		recorder := httptest.NewRecorder()

		app.HandleSetClockDisplay(recorder, settingsRequest(http.MethodPatch,
			"/api/control/devices/speaker/settings/clock-display", `{"timeZone":"Europe/Berlin"}`))

		if recorder.Code != http.StatusConflict {
			t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		if fixture.clockPosts.Load() != 0 {
			t.Fatalf("unsupported timezone sent %d POSTs", fixture.clockPosts.Load())
		}
	})
}

func TestHandleSetClockTimeRequiresCurrentTimeReadback(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.clockHasTime = false
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleSetClockTime(recorder, settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/clock-time", ""))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.clockTimePosts.Load() != 0 {
		t.Fatalf("unsupported current-time control sent %d POSTs", fixture.clockTimePosts.Load())
	}
}

func TestSettingsMutationsConfirmReadback(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		invoke func(*WebApp, http.ResponseWriter, *http.Request)
		verify func(*testing.T, *settingsSpeakerFixture)
	}{
		{
			name:   "clock time",
			method: http.MethodPost,
			path:   "/api/control/devices/speaker/settings/clock-time",
			invoke: (*WebApp).HandleSetClockTime,
		},
		{
			name:   "automatic standby",
			method: http.MethodPatch,
			path:   "/api/control/devices/speaker/settings/system-timeout",
			body:   `{"enabled":false}`,
			invoke: (*WebApp).HandleSetSystemTimeout,
			verify: func(t *testing.T, fixture *settingsSpeakerFixture) {
				t.Helper()
				if fixture.timeoutEnabled {
					t.Fatal("automatic standby readback remained enabled")
				}
			},
		},
		{
			name:   "language",
			method: http.MethodPatch,
			path:   "/api/control/devices/speaker/settings/language",
			body:   `{"code":3}`,
			invoke: (*WebApp).HandleSetSystemLanguage,
			verify: func(t *testing.T, fixture *settingsSpeakerFixture) {
				t.Helper()
				if fixture.language != 3 {
					t.Fatalf("language readback = %d, want 3", fixture.language)
				}
			},
		},
		{
			name:   "sync priority",
			method: http.MethodPatch,
			path:   "/api/control/devices/speaker/settings/sync",
			body:   `{"mode":"SYNC_TO_ROOM"}`,
			invoke: (*WebApp).HandleSetRebroadcastLatencyMode,
			verify: func(t *testing.T, fixture *settingsSpeakerFixture) {
				t.Helper()
				if fixture.syncMode != "SYNC_TO_ROOM" {
					t.Fatalf("sync readback = %q, want SYNC_TO_ROOM", fixture.syncMode)
				}
			},
		},
		{
			name:   "Bluetooth pairing",
			method: http.MethodPost,
			path:   "/api/control/devices/speaker/settings/bluetooth/pair",
			invoke: (*WebApp).HandleEnterBluetoothPairing,
			verify: func(t *testing.T, fixture *settingsSpeakerFixture) {
				t.Helper()
				if !fixture.discoverable {
					t.Fatal("Bluetooth pairing did not reach discoverable state")
				}
			},
		},
		{
			name:   "source name",
			method: http.MethodPatch,
			path:   "/api/control/devices/speaker/settings/source-name",
			body:   `{"source":"AUX","sourceAccount":"AUX1","name":"  Turntable  "}`,
			invoke: (*WebApp).HandleSetSourceName,
			verify: func(t *testing.T, fixture *settingsSpeakerFixture) {
				t.Helper()
				if fixture.sourceName != "Turntable" {
					t.Fatalf("source-name readback = %q, want Turntable", fixture.sourceName)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSettingsSpeakerFixture(t, true)
			app := settingsTestApp(fixture)
			recorder := httptest.NewRecorder()

			test.invoke(app, recorder, settingsRequest(test.method, test.path, test.body))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			response := decodeSettingsResponse(t, recorder)
			if !response.Success {
				t.Fatalf("settings response failed: %s", response.Error)
			}
			if test.verify != nil {
				test.verify(t, fixture)
			}
		})
	}
}

func TestHandleSetSystemLanguageRejectsUnknownCodeBeforeWrite(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleSetSystemLanguage(recorder, settingsRequest(http.MethodPatch,
		"/api/control/devices/speaker/settings/language", `{"code":14}`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleSetSourceNameRejectsWhitespaceBeforeWrite(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleSetSourceName(recorder, settingsRequest(http.MethodPatch,
		"/api/control/devices/speaker/settings/source-name",
		`{"source":"AUX","sourceAccount":"AUX1","name":"   "}`))

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.sourceName != "Line in" {
		t.Fatalf("source name changed to %q", fixture.sourceName)
	}
}

func TestHandleClearBluetoothPairingsReportsUnverifiedReadback(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleClearBluetoothPairings(recorder, settingsRequest(http.MethodDelete,
		"/api/control/devices/speaker/settings/bluetooth/pairings", ""))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" || response.Error == "" {
		t.Fatalf("clear response claimed success: %+v", response)
	}
	if response.Data.Errors["bluetoothClear"] == "" {
		t.Fatalf("clear response claimed verified success: %+v", response.Data.Errors)
	}
	if fixture.clearGets.Load() != 1 {
		t.Fatalf("clear GET count = %d, want 1", fixture.clearGets.Load())
	}
}
