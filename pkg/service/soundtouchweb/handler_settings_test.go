package soundtouchweb

import (
	"context"
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

	mu                  sync.Mutex
	clockEnabled        bool
	clockFormat         string
	clockTimeZone       string
	clockHasFormat      bool
	clockHasZone        bool
	clockHasTime        bool
	timeoutEnabled      bool
	language            int
	syncMode            string
	sourceName          string
	discoverable        bool
	pairingConfirmAfter int32
	nowPlayingSource    string
	nowPlayingStatus    int
	legacyBluetoothURLs bool
	pairingMutation     string
	clearMutation       string

	clockPosts          atomic.Int32
	clockTimePosts      atomic.Int32
	timeoutPosts        atomic.Int32
	clearGets           atomic.Int32
	capabilityGets      atomic.Int32
	pairingGets         atomic.Int32
	pairingReads        atomic.Int32
	capabilityFailAfter int32
}

func newSettingsSpeakerFixture(t *testing.T, advertiseSettings bool) *settingsSpeakerFixture {
	t.Helper()

	fixture := &settingsSpeakerFixture{
		clockEnabled:     false,
		clockFormat:      "TIME_FORMAT_24HOUR_ID",
		clockTimeZone:    "Europe/Prague",
		clockHasFormat:   true,
		clockHasZone:     true,
		clockHasTime:     true,
		timeoutEnabled:   true,
		language:         15,
		syncMode:         "SYNC_TO_ZONE",
		sourceName:       "Line in",
		nowPlayingSource: "BLUETOOTH",
		nowPlayingStatus: http.StatusOK,
	}

	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()

		w.Header().Set("Content-Type", "application/xml")

		switch r.URL.Path {
		case "/capabilities":
			capabilityGet := fixture.capabilityGets.Add(1)
			if fixture.capabilityFailAfter > 0 && capabilityGet >= fixture.capabilityFailAfter {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `<error>capabilities unavailable</error>`)

				return
			}
			if advertiseSettings {
				_, _ = io.WriteString(w, `<capabilities><clockDisplay>true</clockDisplay>`+
					`<networkConfig><hostedWifiConfigWebPage hostedBy="BCO" generation="1" port="80">true</hostedWifiConfigWebPage></networkConfig>`+
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
					"/rebroadcastlatencymode", "/bluetoothInfo", "/nameSource",
				)
				if fixture.legacyBluetoothURLs {
					locations = append(locations, "/enterPairingMode", "/clearPairedList")
				} else {
					locations = append(locations, "/enterBluetoothPairing", "/clearBluetoothPaired")
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
				fixture.timeoutPosts.Add(1)
				body, _ := io.ReadAll(r.Body)
				var request models.SystemTimeout
				if err := xml.Unmarshal(body, &request); err != nil {
					http.Error(w, "invalid systemtimeout body", http.StatusBadRequest)

					return
				}
				fixture.timeoutEnabled = request.PowerSavingEnabled
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
		case "/enterBluetoothPairing":
			fixture.pairingGets.Add(1)
			switch fixture.pairingMutation {
			case "unknown-confirmed":
				fixture.discoverable = true
				http.Error(w, "response lost", http.StatusInternalServerError)
			case "unknown-unconfirmed":
				http.Error(w, "response lost", http.StatusInternalServerError)
			case "typed-error":
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `<errors deviceID="AABBCCDDEEFF"><error value="1029" name="UNKNOWN_ACTION_ERROR">rejected</error></errors>`)
			default:
				fixture.discoverable = true
				_, _ = io.WriteString(w, `<status>/enterBluetoothPairing</status>`)
			}
		case "/clearBluetoothPaired":
			fixture.clearGets.Add(1)
			switch fixture.clearMutation {
			case "unknown":
				http.Error(w, "response lost", http.StatusInternalServerError)
			case "typed-error":
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, `<errors deviceID="AABBCCDDEEFF"><error value="1029" name="UNKNOWN_ACTION_ERROR">rejected</error></errors>`)
			default:
				_, _ = io.WriteString(w, `<status>/clearBluetoothPaired</status>`)
			}
		case "/now_playing":
			if fixture.nowPlayingStatus != http.StatusOK {
				w.WriteHeader(fixture.nowPlayingStatus)
				_, _ = io.WriteString(w, `<error>now playing unavailable</error>`)

				return
			}

			status := "DISCONNECTED"
			pairingRead := int32(0)
			if fixture.pairingGets.Load() > 0 {
				pairingRead = fixture.pairingReads.Add(1)
			}
			if fixture.discoverable && (fixture.pairingConfirmAfter == 0 || pairingRead >= fixture.pairingConfirmAfter) {
				status = "DISCOVERABLE"
			}
			_, _ = fmt.Fprintf(w, `<nowPlaying source="%s"><connectionStatusInfo status="%s" deviceName="Test phone"/></nowPlaying>`, fixture.nowPlayingSource, status)
		case "/nameSource":
			body, _ := io.ReadAll(r.Body)
			var request models.SourceRenameRequest
			if err := xml.Unmarshal(body, &request); err != nil {
				http.Error(w, "invalid nameSource body", http.StatusBadRequest)

				return
			}
			fixture.sourceName = request.ItemName
			_, _ = io.WriteString(w, `<status>/nameSource</status>`)
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

func withBluetoothPairingPoll(request *http.Request, attempts int) *http.Request {
	strategy := bluetoothPairingPollStrategy{
		Attempts: attempts,
		Wait:     func() error { return nil },
	}

	return request.WithContext(context.WithValue(request.Context(), bluetoothPairingPollStrategyKey{}, strategy))
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

	responseBody := recorder.Body.String()
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
	if response.Data.SystemTimeout == nil || !response.Data.SystemTimeout.Enabled {
		t.Fatalf("unexpected automatic-standby projection: %+v", response.Data.SystemTimeout)
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
	if !support.WiFiOnboarding || response.Data.OnboardingURL != "/setup/" {
		t.Fatalf("unexpected Wi-Fi onboarding projection: support=%+v url=%q",
			support, response.Data.OnboardingURL)
	}
	if strings.Contains(responseBody, `"network"`) {
		t.Fatalf("excluded network diagnostics were projected: %s", responseBody)
	}
}

func TestHandleGetDeviceSettingsOmitsWiFiOnboardingWithoutMountedWorkflow(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	app.OnboardingURL = ""
	recorder := httptest.NewRecorder()

	app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet,
		"/api/control/devices/speaker/settings", ""))

	response := decodeSettingsResponse(t, recorder)
	if recorder.Code != http.StatusOK || !response.Success {
		t.Fatalf("response = status %d %+v", recorder.Code, response)
	}
	if response.Data.OnboardingURL != "" {
		t.Fatalf("onboarding URL = %q, want omitted", response.Data.OnboardingURL)
	}
}

func TestHandleGetDeviceSettingsIgnoresGeneralPairingEndpoints(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.legacyBluetoothURLs = true
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet, "/api/control/devices/speaker/settings", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if !response.Data.Support.Bluetooth {
		t.Fatal("Bluetooth information should remain supported")
	}
	if response.Data.Support.BluetoothPair || response.Data.Support.BluetoothClear {
		t.Fatalf("general pairing endpoints enabled Bluetooth controls: %+v", response.Data.Support)
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

func TestHandleSetSystemTimeoutRejectsUnsupportedDeviceBeforeWrite(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, false)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleSetSystemTimeout(recorder, settingsRequest(http.MethodPatch,
		"/api/control/devices/speaker/settings/system-timeout", `{"enabled":false}`))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.timeoutPosts.Load() != 0 {
		t.Fatalf("unsupported automatic standby sent %d POSTs", fixture.timeoutPosts.Load())
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
				if fixture.timeoutPosts.Load() != 1 {
					t.Fatalf("automatic standby POST count = %d, want 1", fixture.timeoutPosts.Load())
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
				if fixture.pairingGets.Load() != 1 {
					t.Fatalf("Bluetooth pairing GET count = %d, want 1", fixture.pairingGets.Load())
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

func TestHandleEnterBluetoothPairingPollsWithoutMutationReplay(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.pairingConfirmAfter = 3
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 4)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.pairingGets.Load() != 1 {
		t.Fatalf("Bluetooth pairing GET count = %d, want 1", fixture.pairingGets.Load())
	}
	if fixture.pairingReads.Load() < 3 {
		t.Fatalf("post-mutation now-playing reads = %d, want at least 3", fixture.pairingReads.Load())
	}
}

func TestHandleEnterBluetoothPairingDoesNotWriteUnversionedSharedStatus(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	device, ok := app.GetDevice("speaker")
	if !ok {
		t.Fatal("settings test device is missing")
	}

	device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.NowPlaying = &models.NowPlaying{Source: "SPOTIFY"}
	})
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 1)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if got := device.Status().NowPlaying; got == nil || got.Source != "SPOTIFY" {
		t.Fatalf("shared now-playing cache = %+v, want existing revision-owned state", got)
	}
}

func TestHandleEnterBluetoothPairingConfirmsUnknownMutationOutcome(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.pairingMutation = "unknown-confirmed"
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 3)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.pairingGets.Load() != 1 || fixture.pairingReads.Load() != 2 {
		t.Fatalf("pairing mutation/read counts = %d/%d, want 1/2 including settings refresh",
			fixture.pairingGets.Load(), fixture.pairingReads.Load())
	}
}

func TestHandleEnterBluetoothPairingReportsUnknownUnconfirmedOutcome(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.pairingMutation = "unknown-unconfirmed"
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 3)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" ||
		!strings.Contains(response.Error, "indeterminate") ||
		!strings.Contains(response.Error, "discoverability remains unconfirmed") {
		t.Fatalf("unknown pairing outcome = %+v, want explicit unverified", response)
	}
	if fixture.pairingGets.Load() != 1 || fixture.pairingReads.Load() != 3 {
		t.Fatalf("pairing mutation/read counts = %d/%d, want 1/3",
			fixture.pairingGets.Load(), fixture.pairingReads.Load())
	}
}

func TestHandleEnterBluetoothPairingFailsTypedMutationError(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.pairingMutation = "typed-error"
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 3)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.pairingGets.Load() != 1 || fixture.pairingReads.Load() != 0 {
		t.Fatalf("pairing mutation/read counts = %d/%d, want 1/0",
			fixture.pairingGets.Load(), fixture.pairingReads.Load())
	}
}

func TestHandleEnterBluetoothPairingRejectsDiscoverableWrongSource(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.nowPlayingSource = "AUX"
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 3)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" || response.Error == "" {
		t.Fatalf("wrong-source response = %+v, want explicit unverified outcome", response)
	}
	if fixture.pairingGets.Load() != 1 {
		t.Fatalf("Bluetooth pairing GET count = %d, want 1", fixture.pairingGets.Load())
	}
	if fixture.pairingReads.Load() != 3 {
		t.Fatalf("post-mutation now-playing reads = %d, want 3", fixture.pairingReads.Load())
	}
}

func TestHandleEnterBluetoothPairingReportsAcceptedReadFailureAsUnverified(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.nowPlayingStatus = http.StatusServiceUnavailable
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 3)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" {
		t.Fatalf("accepted pairing read failure = %+v, want unverified", response)
	}
	if !strings.Contains(response.Error, "state readback failed") {
		t.Fatalf("read failure diagnostic was not preserved: %+v", response)
	}
	if fixture.pairingGets.Load() != 1 {
		t.Fatalf("Bluetooth pairing GET count = %d, want 1", fixture.pairingGets.Load())
	}
}

func TestHandleEnterBluetoothPairingReportsConfirmedRefreshFailureAsUnverified(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.capabilityFailAfter = 2
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()
	request := withBluetoothPairingPoll(settingsRequest(http.MethodPost,
		"/api/control/devices/speaker/settings/bluetooth/pair", ""), 3)

	app.HandleEnterBluetoothPairing(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" {
		t.Fatalf("confirmed pairing refresh failure = %+v, want unverified", response)
	}
	if !strings.Contains(response.Error, "confirmed Bluetooth pairing mode") ||
		!strings.Contains(response.Error, "settings refresh failed") {
		t.Fatalf("refresh failure diagnostic was not preserved: %+v", response)
	}
	if fixture.pairingGets.Load() != 1 || fixture.pairingReads.Load() != 1 {
		t.Fatalf("pairing mutation/read counts = %d/%d, want 1/1",
			fixture.pairingGets.Load(), fixture.pairingReads.Load())
	}
	if fixture.capabilityGets.Load() != 2 {
		t.Fatalf("capability reads = %d, want preflight and failed refresh", fixture.capabilityGets.Load())
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
		"/api/control/devices/speaker/settings/bluetooth/pairings?confirmed=true", ""))

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

func TestHandleClearBluetoothPairingsReportsAcceptedRefreshFailureAsUnverified(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.capabilityFailAfter = 2
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleClearBluetoothPairings(recorder, settingsRequest(http.MethodDelete,
		"/api/control/devices/speaker/settings/bluetooth/pairings?confirmed=true", ""))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" {
		t.Fatalf("accepted clear refresh failure = %+v, want unverified", response)
	}
	if !strings.Contains(response.Error, "settings refresh failed") ||
		!strings.Contains(response.Error, "read device capabilities") {
		t.Fatalf("refresh failure diagnostic was not preserved: %+v", response)
	}
	if fixture.clearGets.Load() != 1 {
		t.Fatalf("clear GET count = %d, want 1", fixture.clearGets.Load())
	}
	if fixture.capabilityGets.Load() != 2 {
		t.Fatalf("capability reads = %d, want preflight and failed refresh", fixture.capabilityGets.Load())
	}
}

func TestHandleClearBluetoothPairingsRequiresConfirmation(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleClearBluetoothPairings(recorder, settingsRequest(http.MethodDelete,
		"/api/control/devices/speaker/settings/bluetooth/pairings", ""))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.clearGets.Load() != 0 {
		t.Fatalf("unconfirmed request sent %d clear mutations", fixture.clearGets.Load())
	}
}

func TestHandleClearBluetoothPairingsReportsUnknownOutcomeUnverified(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.clearMutation = "unknown"
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleClearBluetoothPairings(recorder, settingsRequest(http.MethodDelete,
		"/api/control/devices/speaker/settings/bluetooth/pairings?confirmed=true", ""))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	response := decodeSettingsResponse(t, recorder)
	if response.Success || response.Outcome != "unverified" ||
		!strings.Contains(response.Error, "outcome is unknown") {
		t.Fatalf("unknown clear outcome = %+v, want explicit unverified", response)
	}
	if fixture.clearGets.Load() != 1 {
		t.Fatalf("clear GET count = %d, want 1", fixture.clearGets.Load())
	}
}

func TestHandleClearBluetoothPairingsFailsTypedMutationError(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.clearMutation = "typed-error"
	app := settingsTestApp(fixture)
	recorder := httptest.NewRecorder()

	app.HandleClearBluetoothPairings(recorder, settingsRequest(http.MethodDelete,
		"/api/control/devices/speaker/settings/bluetooth/pairings?confirmed=true", ""))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fixture.clearGets.Load() != 1 {
		t.Fatalf("clear GET count = %d, want 1", fixture.clearGets.Load())
	}
}
