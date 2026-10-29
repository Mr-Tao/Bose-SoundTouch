package soundtouchweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
)

const movedPresetBankXML = `<presets><preset id="3"><ContentItem source="TUNEIN" location="station-one" isPresetable="true"><itemName>One</itemName></ContentItem></preset></presets>`

func TestDevicePresetsFreshBankWithoutPush(t *testing.T) {
	var reads atomic.Int32
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/presets" {
			t.Errorf("unexpected speaker request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reads.Add(1)
		_, _ = w.Write([]byte(movedPresetBankXML))
	}))
	defer speaker.Close()
	app := newLibraryTestApp(speaker.URL)
	conn, _ := app.GetDevice("lib-device")
	conn.ApplyPresetEvent(&models.Presets{Preset: []models.Preset{{ID: 1, ContentItem: &models.ContentItem{Source: "TUNEIN", Location: "station-one"}}}})
	router := chi.NewRouter()
	app.Mount(router, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/control/devices/lib-device/presets", nil))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("fresh bank: status=%d cache=%q body=%s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Status webtypes.DeviceStatus `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	bank := response.Data.Status.Presets
	if !response.Success || bank == nil || len(bank.Preset) != 1 || bank.Preset[0].ID != 3 || reads.Load() != 1 {
		t.Fatalf("fresh read did not render both sides of move: %+v reads=%d", bank, reads.Load())
	}
}

func TestDevicePresetsReadFailureDoesNotConfirmCachedBank(t *testing.T) {
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer speaker.Close()
	app := newLibraryTestApp(speaker.URL)
	conn, _ := app.GetDevice("lib-device")
	cached := &models.Presets{Preset: []models.Preset{{ID: 1}}}
	conn.ApplyPresetEvent(cached)
	w := httptest.NewRecorder()
	app.HandleDevicePresets(w, withChiParams(httptest.NewRequest(http.MethodGet, "/presets", nil), map[string]string{"id": "lib-device"}))
	if w.Code != http.StatusBadGateway || conn.Status().Presets != cached {
		t.Fatalf("failed read incorrectly confirmed/replaced cache: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPresetReadbackAndBareRefreshRejectOlderBank(t *testing.T) {
	for _, apiRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "bare-event", true: "fresh-api"}[apiRead], func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				<-release
				_, _ = w.Write([]byte(`<presets><preset id="1"/></presets>`))
			}))
			defer speaker.Close()
			app := newLibraryTestApp(speaker.URL)
			conn, _ := app.GetDevice("lib-device")
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				if apiRead {
					app.HandleDevicePresets(w, withChiParams(httptest.NewRequest(http.MethodGet, "/presets", nil), map[string]string{"id": "lib-device"}))
				} else {
					app.refreshPresets("lib-device", conn)
				}
			}()
			<-started
			newer := &models.Presets{Preset: []models.Preset{{ID: 3, ContentItem: &models.ContentItem{Source: "TUNEIN", Location: "new"}}}}
			app.applyPresetEvent(conn, newer)
			close(release)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("preset read did not complete")
			}
			if conn.Status().Presets != newer {
				t.Fatal("older HTTP bank overwrote a newer push")
			}
			if apiRead && (w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ID":3`)) {
				t.Fatalf("fresh endpoint returned obsolete raw answer: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestCheckedPresetWritesRefuseChangedPhysicalSpeaker(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			var writes atomic.Int32
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					writes.Add(1)
				}
				switch r.URL.Path {
				case "/info":
					_, _ = w.Write([]byte(`<info deviceID="DIFFERENT"><name>Replacement</name></info>`))
				case "/sources":
					_, _ = w.Write([]byte(`<sources><sourceItem source="TUNEIN" status="READY"/></sources>`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer speaker.Close()
			app := newLibraryTestApp(speaker.URL)
			req := httptest.NewRequest(method, "/preset/1", strings.NewReader(`{"source":"TUNEIN","location":"station-one","itemName":"One"}`))
			req.Header.Set(presetTargetHeader, "AABBCCDDEEFF")
			req = withChiParams(req, map[string]string{"id": "lib-device", "slot": "1"})
			w := httptest.NewRecorder()
			if method == http.MethodPost {
				app.HandleStorePresetContent(w, req)
			} else {
				app.HandleRemovePreset(w, req)
			}
			if w.Code != http.StatusConflict || writes.Load() != 0 {
				t.Fatalf("wrong physical target: status=%d writes=%d body=%s", w.Code, writes.Load(), w.Body.String())
			}
		})
	}
}
