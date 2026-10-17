package soundtouchweb

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

func TestHandleGetSoundSettingsReadsOnlyAcousticEndpoints(t *testing.T) {
	for _, mode := range []string{"supported", "unadvertised", "unavailable", "capability error", "bass error"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSettingsSpeakerFixture(t, true)
			fixture.bassAdvertised = mode != "unadvertised"
			fixture.bassUnavailable = mode == "unavailable"
			fixture.bassCapabilityFail = mode == "capability error"
			fixture.bassReadFail = mode == "bass error"
			var mu sync.Mutex
			requests := make(map[string]int)
			original := fixture.server.Config.Handler
			fixture.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests[r.URL.Path]++
				mu.Unlock()
				original.ServeHTTP(w, r)
			})
			app := settingsTestApp(fixture)
			device, _ := app.GetDevice("speaker")
			recorder := httptest.NewRecorder()
			app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet, "/api/control/devices/speaker/settings/?scope=sound", ""))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			response := decodeSettingsResponse(t, recorder)
			if !response.Success || response.Data.TargetIdentity != "AABBCCDDEEFF" || response.Data.StatusEpoch != device.Status().Epoch {
				t.Fatalf("missing target or epoch: %+v", response)
			}
			if response.Data.StatusRevision == 0 || response.Data.StatusRevision > device.Status().Revision {
				t.Fatalf("invalid status revision: %+v", response.Data)
			}
			if response.Data.Support.ClockDisplay || response.Data.Network != nil || response.Data.Sources != nil {
				t.Fatalf("sound settings included unrelated settings: %+v", response.Data)
			}
			if mode == "capability error" || mode == "bass error" {
				if response.Data.Errors["bass"] == "" {
					t.Fatal("missing acoustic read error")
				}
			}
			if mode == "supported" && (response.Data.Bass == nil || response.Data.Bass.ActualBass != -2 || response.Data.BassCapabilities == nil) {
				t.Fatalf("missing acoustic readback: %+v", response.Data)
			}
			mu.Lock()
			defer mu.Unlock()
			identityReads := 2
			if mode == "supported" {
				identityReads++ // The bass merge also verifies identity before publishing.
			}
			if requests["/supportedURLs"] != 1 || requests["/info"] != identityReads {
				t.Fatalf("endpoint discovery or live identity fences missing: %v", requests)
			}
			for path := range requests {
				switch path {
				case "/info", "/supportedURLs", "/bassCapabilities", "/bass":
				default:
					t.Errorf("unrelated endpoint requested: %s", path)
				}
			}
			if (mode == "unadvertised" || mode == "unavailable" || mode == "capability error") && requests["/bass"] != 0 {
				t.Fatalf("unusable bass endpoint requested: %v", requests)
			}
		})
	}
}

func TestHandleGetSoundSettingsPreservesNewerBassEvent(t *testing.T) {
	fixture := newSettingsSpeakerFixture(t, true)
	fixture.bassAdvertised = true
	fixture.bassStarted, fixture.bassRelease = make(chan struct{}, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(fixture.bassRelease) }) }
	defer release()
	app := settingsTestApp(fixture)
	device, _ := app.GetDevice("speaker")
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet, "/api/control/devices/speaker/settings/?scope=sound", ""))
		close(done)
	}()
	select {
	case <-fixture.bassStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("bass read did not begin")
	}
	device.ApplyFieldEvent(webtypes.FieldBass, func(status *webtypes.DeviceStatus) {
		status.Bass = &models.Bass{DeviceID: "AABBCCDDEEFF", ActualBass: -5}
	})
	release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("sound settings read did not finish")
	}
	response := decodeSettingsResponse(t, recorder)
	status := device.Status()
	if recorder.Code != http.StatusOK || response.Data.Bass == nil || response.Data.Bass.ActualBass != -5 || status.Bass.ActualBass != -5 || response.Data.StatusRevision != status.Revision {
		t.Fatalf("delayed GET overwrote newer bass: status=%+v response=%+v", status, response)
	}
}

func TestHandleGetSoundSettingsRejectsIdentityChanges(t *testing.T) {
	for _, mode := range []string{"supported URLs", "bass capabilities", "final live identity"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newSettingsSpeakerFixture(t, false)
			fixture.bassAdvertised = mode != "final live identity"
			original := fixture.server.Config.Handler
			fixture.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "supported URLs" && r.URL.Path == "/supportedURLs" {
					_, _ = fmt.Fprint(w, `<supportedURLs deviceID="OTHER"><URL location="/bass"/><URL location="/bassCapabilities"/></supportedURLs>`)
					return
				}
				if mode == "bass capabilities" && r.URL.Path == "/bassCapabilities" {
					_, _ = fmt.Fprint(w, `<bassCapabilities deviceID="OTHER"><bassAvailable>true</bassAvailable></bassCapabilities>`)
					return
				}
				original.ServeHTTP(w, r)
			})
			if mode == "final live identity" {
				fixture.liveDeviceIDs = []string{"AABBCCDDEEFF", "OTHER"}
			}
			app := settingsTestApp(fixture)
			recorder := httptest.NewRecorder()
			app.HandleGetDeviceSettings(recorder, settingsRequest(http.MethodGet, "/api/control/devices/speaker/settings/?scope=sound", ""))
			if recorder.Code != http.StatusConflict {
				t.Fatalf("identity change returned %d: %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestUpdateDeviceStatusDoesNotWaitForBassCapabilities(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bassCapabilities":
			if reads.Add(1) == 1 {
				close(started)
			}
			<-release
			_, _ = fmt.Fprint(w, `<bassCapabilities deviceID="SPEAKER"><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>0</bassMax></bassCapabilities>`)
		case "/volume":
			_, _ = fmt.Fprint(w, `<volume><actualvolume>42</actualvolume></volume>`)
		default:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	defer close(release)
	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{DeviceID: "SPEAKER", Type: "SoundTouch 20"})
	app := NewWebApp()
	done := make(chan struct{})
	go func() {
		app.UpdateDeviceStatus("speaker", conn)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background capability request did not begin")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("slow bass capabilities delayed status refresh")
	}
	if conn.Status().Volume == nil || conn.Status().Volume.ActualVolume != 42 {
		t.Fatal("slow bass capabilities delayed the current volume merge")
	}
	conn.Close()
	app.UpdateDeviceStatus("speaker", conn)
	if reads.Load() != 1 {
		t.Fatalf("closed connection started another capability read: %d", reads.Load())
	}
}
