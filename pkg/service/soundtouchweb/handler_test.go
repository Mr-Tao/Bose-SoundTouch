// Package handlers contains tests for HTTP handlers.
package soundtouchweb

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	bmxpkg "github.com/gesellix/bose-soundtouch/pkg/service/bmx"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/go-chi/chi/v5"
)

func createTestApp() *WebApp {
	app := NewWebApp()

	// Add test device with minimal data
	deviceInfo := &models.DeviceInfo{
		Name: "Test Speaker",
		Type: "SoundTouch 30",
		NetworkInfo: []models.NetworkInfo{
			{MacAddress: "TEST123", IPAddress: "192.0.2.100"},
		},
	}

	device := webtypes.NewDeviceConnection(nil, deviceInfo)
	device.SetStatus(&webtypes.DeviceStatus{
		Volume:       &models.Volume{ActualVolume: 50, MuteEnabled: false},
		Bass:         &models.Bass{ActualBass: 0},
		IsConnected:  true,
		LastActivity: time.Now(),
	})

	app.AddDevice("test-device", device)
	return app
}

func withChiParams(r *http.Request, params map[string]string) *http.Request {
	rctx := chi.NewRouteContext()
	for k, v := range params {
		rctx.URLParams.Add(k, v)
	}
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestNewWebApp(t *testing.T) {
	app := NewWebApp()

	// Use require-style checks that satisfy static analyzer
	if app == nil {
		t.Fatal("NewWebApp returned nil")
	}

	if count := app.DeviceCount(); count != 0 {
		t.Errorf("Expected empty device registry, got %d devices", count)
	}
}

func TestStereoPairPersistenceClientHasBoundedLifecycleTimeout(t *testing.T) {
	app := NewWebApp()
	transport := &http.Transport{}
	app.ServiceClient = &http.Client{Transport: transport, Timeout: 10 * time.Second}

	configured := app.stereoPairPersistenceClient()
	if configured.Timeout != 45*time.Second {
		t.Fatalf("timeout = %s, want 45s", configured.Timeout)
	}
	if configured.Transport != transport {
		t.Fatal("custom service transport was not preserved")
	}
	if app.ServiceClient.Timeout != 10*time.Second {
		t.Fatalf("source client timeout was mutated to %s", app.ServiceClient.Timeout)
	}
}

func TestHandleAPIDevices(t *testing.T) {
	app := createTestApp()

	req := httptest.NewRequest("GET", "/api/control/devices", nil)
	w := httptest.NewRecorder()

	app.HandleAPIDevices(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	contentType := w.Header().Get("Content-Type")
	if !strings.Contains(contentType, "application/json") {
		t.Errorf("Expected JSON content type, got %s", contentType)
	}

	var response webtypes.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !response.Success {
		t.Errorf("Expected success=true, got false")
	}

	// Check that devices data is present
	data, ok := response.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data to be map[string]interface{}")
	}

	if _, exists := data["test-device"]; !exists {
		t.Errorf("Expected 'test-device' in response data")
	}
}

func TestHandleAPIDevice(t *testing.T) {
	app := createTestApp()

	tests := []struct {
		name           string
		path           string
		chiID          string
		expectedStatus int
		expectSuccess  bool
	}{
		{
			name:           "valid device",
			path:           "/api/control/devices/test-device",
			chiID:          "test-device",
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
		{
			name:           "missing device ID",
			path:           "/api/control/devices/",
			chiID:          "",
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
		{
			name:           "unknown device",
			path:           "/api/control/devices/unknown",
			chiID:          "unknown",
			expectedStatus: http.StatusNotFound,
			expectSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			if tt.chiID != "" {
				req = withChiParams(req, map[string]string{"id": tt.chiID})
			}
			w := httptest.NewRecorder()

			app.HandleAPIDevice(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			contentType := w.Header().Get("Content-Type")
			if !strings.Contains(contentType, "application/json") {
				t.Errorf("Expected JSON content type, got %s", contentType)
			}

			var response webtypes.APIResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response.Success != tt.expectSuccess {
				t.Errorf("Expected success=%v, got %v", tt.expectSuccess, response.Success)
			}
		})
	}
}

func TestHandleAPIControl_InvalidDevice(t *testing.T) {
	app := createTestApp()

	req := httptest.NewRequest("GET", "/api/control/devices/unknown-device/action/play", nil)
	req = withChiParams(req, map[string]string{"id": "unknown-device", "action": "play"})
	w := httptest.NewRecorder()

	app.HandleAPIControl(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", w.Code)
	}

	var response webtypes.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Success {
		t.Errorf("Expected success=false, got true")
	}

	if response.Error != "Device not found" {
		t.Errorf("Expected 'Device not found' error, got '%s'", response.Error)
	}
}

func TestHandleAPIControl_InvalidPath(t *testing.T) {
	app := createTestApp()

	tests := []struct {
		name string
		path string
	}{
		{"missing action", "/api/control/devices/test-device/action/"},
		{"missing device and action", "/api/control/devices//action/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.path, nil)
			w := httptest.NewRecorder()

			app.HandleAPIControl(w, req)

			if w.Code != http.StatusBadRequest {
				t.Errorf("Expected status 400, got %d", w.Code)
			}

			var response webtypes.APIResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response.Success {
				t.Errorf("Expected success=false, got true")
			}
		})
	}
}

func TestHandleAPIControl_VolumeValidation(t *testing.T) {
	app := createTestApp()

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		expectSuccess  bool
	}{
		{
			name:           "invalid method",
			method:         "GET",
			body:           "",
			expectedStatus: http.StatusMethodNotAllowed,
			expectSuccess:  false,
		},
		{
			name:           "invalid JSON",
			method:         "POST",
			body:           `invalid json`,
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
		{
			name:           "volume too low",
			method:         "POST",
			body:           `{"level": -1}`,
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
		{
			name:           "volume too high",
			method:         "POST",
			body:           `{"level": 101}`,
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req *http.Request
			if tt.body != "" {
				req = httptest.NewRequest(tt.method, "/api/control/devices/test-device/volume/50", strings.NewReader(tt.body))
			} else {
				req = httptest.NewRequest(tt.method, "/api/control/devices/test-device/volume/50", nil)
			}
			req = withChiParams(req, map[string]string{"id": "test-device", "action": "volume"})
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			app.HandleAPIControl(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var response webtypes.APIResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response.Success != tt.expectSuccess {
				t.Errorf("Expected success=%v, got %v", tt.expectSuccess, response.Success)
			}
		})
	}
}

func TestHandleDirectVolumeControlRequiresMatchingAuthoritativeReadback(t *testing.T) {
	t.Run("matching readback updates confirmed cache", func(t *testing.T) {
		speaker := newVolumeSpeaker(t, 35, "")
		app := NewWebApp()
		addVolumeDevice(app, "192.0.2.10", "STANDALONE", "Kitchen", speaker, 35, nil)

		request := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/volume/40", nil)
		request = withChiParams(request, map[string]string{"id": "192.0.2.10", "volume": "40"})
		response := httptest.NewRecorder()
		app.HandleDirectVolumeControl(response, request)

		if response.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", response.Code, response.Body.String())
		}
		var payload webtypes.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if !payload.Success {
			t.Fatalf("matching readback failed: %+v", payload)
		}
		conn, _ := app.GetDevice("192.0.2.10")
		if got := conn.Status().Volume; got == nil || got.ActualVolume != 40 || got.TargetVolume != 40 {
			t.Fatalf("confirmed cache = %+v, want 40", got)
		}
		if volume, posts := speaker.values(); volume != 40 || fmt.Sprint(posts) != "[40]" || speaker.getCount() != 1 {
			t.Fatalf("speaker operations = volume %d, posts %v, gets %d", volume, posts, speaker.getCount())
		}
	})

	t.Run("mismatched readback cannot succeed", func(t *testing.T) {
		speaker := newVolumeSpeaker(t, 35, "")
		speaker.setIgnoreWrites(true)
		app := NewWebApp()
		addVolumeDevice(app, "192.0.2.10", "STANDALONE", "Kitchen", speaker, 35, nil)

		request := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/volume/40", nil)
		request = withChiParams(request, map[string]string{"id": "192.0.2.10", "volume": "40"})
		response := httptest.NewRecorder()
		app.HandleDirectVolumeControl(response, request)

		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
		}
		var payload webtypes.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if payload.Success || !strings.Contains(payload.Error, "does not both match requested") {
			t.Fatalf("mismatch response = %+v", payload)
		}
		conn, _ := app.GetDevice("192.0.2.10")
		if got := conn.Status().Volume; got == nil || got.ActualVolume != 35 {
			t.Fatalf("mismatch cache = %+v, want authoritative 35", got)
		}
		if _, posts := speaker.values(); fmt.Sprint(posts) != "[40]" ||
			speaker.getCount() != zoneVolumeReadbackAttempts {
			t.Fatalf("bounded mismatch operations: posts=%v gets=%d", posts, speaker.getCount())
		}
	})

	t.Run("matching actual with different target cannot succeed", func(t *testing.T) {
		speaker := newVolumeSpeaker(t, 40, "")
		speaker.setIgnoreWrites(true)
		speaker.setReportedTarget(50)
		app := NewWebApp()
		addVolumeDevice(app, "192.0.2.10", "STANDALONE", "Kitchen", speaker, 40, nil)

		request := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/volume/40", nil)
		request = withChiParams(request, map[string]string{"id": "192.0.2.10", "volume": "40"})
		response := httptest.NewRecorder()
		app.HandleDirectVolumeControl(response, request)

		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
		}
		var payload webtypes.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if payload.Success || !strings.Contains(payload.Error, "target 50 actual 40") {
			t.Fatalf("different speaker target was confirmed: %+v", payload)
		}
		conn, _ := app.GetDevice("192.0.2.10")
		if got := conn.Status().Volume; got == nil || got.TargetVolume != 50 || got.ActualVolume != 40 {
			t.Fatalf("authoritative mismatched readback not retained: %+v", got)
		}
	})

	t.Run("missing readback cannot succeed", func(t *testing.T) {
		speaker := newVolumeSpeaker(t, 35, "")
		speaker.setVolumeError(true)
		app := NewWebApp()
		addVolumeDevice(app, "192.0.2.10", "STANDALONE", "Kitchen", speaker, 35, nil)

		request := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/volume/40", nil)
		request = withChiParams(request, map[string]string{"id": "192.0.2.10", "volume": "40"})
		response := httptest.NewRecorder()
		app.HandleDirectVolumeControl(response, request)

		if response.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502: %s", response.Code, response.Body.String())
		}
		var payload webtypes.APIResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if payload.Success || !strings.Contains(payload.Error, "readback volume") {
			t.Fatalf("missing readback response = %+v", payload)
		}
		conn, _ := app.GetDevice("192.0.2.10")
		if got := conn.Status().Volume; got == nil || got.ActualVolume != 35 {
			t.Fatalf("failed readback changed cache: %+v", got)
		}
	})
}

func TestHandleAPIControl_BassValidation(t *testing.T) {
	app := createTestApp()

	tests := []struct {
		name           string
		method         string
		body           string
		expectedStatus int
		expectSuccess  bool
	}{
		{
			name:           "bass too low",
			method:         "POST",
			body:           `{"level": -10}`,
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
		{
			name:           "bass too high",
			method:         "POST",
			body:           `{"level": 10}`,
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
		{
			name:           "fallback rejects positive bass",
			method:         "POST",
			body:           `{"level": 1}`,
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/control/devices/test-device/action/bass", strings.NewReader(tt.body))
			req = withChiParams(req, map[string]string{"id": "test-device", "action": "bass"})
			req.Header.Set("Content-Type", "application/json")

			w := httptest.NewRecorder()
			app.HandleAPIControl(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var response webtypes.APIResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response.Success != tt.expectSuccess {
				t.Errorf("Expected success=%v, got %v", tt.expectSuccess, response.Success)
			}
		})
	}
}

func TestHandleAPIControl_BassCapabilities(t *testing.T) {
	tests := []struct {
		name         string
		capabilities *models.BassCapabilities
		level        int
		wantStatus   int
		wantPost     bool
	}{
		{
			name: "reported SoundTouch range accepts zero",
			capabilities: &models.BassCapabilities{
				BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
			},
			level: 0, wantStatus: http.StatusOK, wantPost: true,
		},
		{
			name: "reported SoundTouch range rejects positive",
			capabilities: &models.BassCapabilities{
				BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
			},
			level: 1, wantStatus: http.StatusBadRequest,
		},
		{
			name: "different reported range accepts its maximum",
			capabilities: &models.BassCapabilities{
				BassAvailable: true, BassMin: -4, BassMax: 12, BassDefault: 1,
			},
			level: 12, wantStatus: http.StatusOK, wantPost: true,
		},
		{
			name: "different reported range rejects above maximum",
			capabilities: &models.BassCapabilities{
				BassAvailable: true, BassMin: -4, BassMax: 12, BassDefault: 1,
			},
			level: 13, wantStatus: http.StatusBadRequest,
		},
		{
			name: "reported unavailable capability rejects write",
			capabilities: &models.BassCapabilities{
				BassAvailable: false, BassMin: 0, BassMax: 0, BassDefault: 0,
			},
			level: 0, wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unknown capability uses conservative fallback",
			level:      -9,
			wantStatus: http.StatusOK,
			wantPost:   true,
		},
		{
			name:       "unknown capability fallback rejects positive",
			level:      1,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var posts atomic.Int32
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/bass" {
					t.Errorf("speaker path = %s, want /bass", r.URL.Path)
					http.NotFound(w, r)
					return
				}

				switch r.Method {
				case http.MethodPost:
					posts.Add(1)
					var request models.BassRequest
					if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Errorf("decode bass request: %v", err)
						http.Error(w, "invalid bass request", http.StatusBadRequest)
						return
					}
					if request.Level != tt.level {
						t.Errorf("posted bass = %d, want %d", request.Level, tt.level)
					}
				case http.MethodGet:
					_, _ = fmt.Fprintf(w,
						`<bass><targetbass>%d</targetbass><actualbass>%d</actualbass></bass>`,
						tt.level, tt.level)
				default:
					t.Errorf("speaker method = %s, want GET or POST", r.Method)
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			}))
			defer speaker.Close()

			app := NewWebApp()
			device := webtypes.NewDeviceConnection(
				client.NewClientFromHost(speaker.URL),
				&models.DeviceInfo{Name: "Test Speaker"},
			)
			device.SetStatus(&webtypes.DeviceStatus{
				Bass:             &models.Bass{TargetBass: 0, ActualBass: 0},
				BassCapabilities: tt.capabilities,
				IsConnected:      true,
			})
			app.AddDevice("test-device", device)

			request := httptest.NewRequest(
				http.MethodPost,
				"/api/control/devices/test-device/action/bass",
				strings.NewReader(fmt.Sprintf(`{"level":%d}`, tt.level)),
			)
			request = withChiParams(request, map[string]string{"id": "test-device", "action": "bass"})
			response := httptest.NewRecorder()

			app.HandleAPIControl(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, tt.wantStatus, response.Body.String())
			}
			if got := posts.Load() > 0; got != tt.wantPost {
				t.Fatalf("speaker POST = %v, want %v", got, tt.wantPost)
			}
		})
	}
}

func TestHandleAPIControl_BassReadback(t *testing.T) {
	tests := []struct {
		name          string
		writeStatus   int
		readStatus    int
		target        int
		actual        int
		wantStatus    int
		wantSuccess   bool
		wantAtTarget  bool
		wantCached    int
		wantCacheKept bool
		wantRevision  uint64
		wantAccepted  bool
	}{
		{
			name: "matching readback confirms write", writeStatus: http.StatusOK,
			readStatus: http.StatusOK, target: -3, actual: -3,
			wantStatus: http.StatusOK, wantSuccess: true, wantAtTarget: true, wantCached: -3,
			wantRevision: 11, wantAccepted: true,
		},
		{
			name: "matching readback confirms transport error", writeStatus: http.StatusInternalServerError,
			readStatus: http.StatusOK, target: -3, actual: -3,
			wantStatus: http.StatusOK, wantSuccess: true, wantAtTarget: true, wantCached: -3,
			wantRevision: 11, wantAccepted: true,
		},
		{
			name: "mismatch is authoritative", writeStatus: http.StatusOK,
			readStatus: http.StatusOK, target: -3, actual: -2,
			wantStatus: http.StatusOK, wantSuccess: true, wantAtTarget: false, wantCached: -2,
			wantRevision: 11, wantAccepted: true,
		},
		{
			name: "failed readback keeps previous value", writeStatus: http.StatusOK,
			readStatus: http.StatusInternalServerError,
			wantStatus: http.StatusBadGateway, wantSuccess: false, wantCached: 0, wantCacheKept: true,
			wantRevision: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodPost:
					w.WriteHeader(tt.writeStatus)
				case http.MethodGet:
					if tt.readStatus != http.StatusOK {
						http.Error(w, "readback failed", tt.readStatus)
						return
					}
					_, _ = fmt.Fprintf(w,
						`<bass><targetbass>%d</targetbass><actualbass>%d</actualbass></bass>`,
						tt.target, tt.actual)
				default:
					http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				}
			}))
			defer speaker.Close()

			app := NewWebApp()
			device := webtypes.NewDeviceConnection(
				client.NewClientFromHost(speaker.URL),
				&models.DeviceInfo{Name: "Test Speaker"},
			)
			device.SetStatus(&webtypes.DeviceStatus{
				Bass:         &models.Bass{TargetBass: 0, ActualBass: 0},
				BassRevision: 10,
				BassCapabilities: &models.BassCapabilities{
					BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
				},
				IsConnected: true,
			})
			app.AddDevice("test-device", device)

			request := httptest.NewRequest(http.MethodPost,
				"/api/control/devices/test-device/action/bass", strings.NewReader(`{"level":-3}`))
			request = withChiParams(request, map[string]string{"id": "test-device", "action": "bass"})
			response := httptest.NewRecorder()
			app.HandleAPIControl(response, request)

			var payload struct {
				Success bool   `json:"success"`
				Error   string `json:"error"`
				Data    struct {
					Requested int     `json:"requested"`
					Target    *int    `json:"target"`
					Actual    *int    `json:"actual"`
					AtTarget  bool    `json:"atTarget"`
					Revision  *uint64 `json:"revision"`
				} `json:"data"`
			}
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Code != tt.wantStatus || payload.Success != tt.wantSuccess ||
				payload.Data.AtTarget != tt.wantAtTarget {
				t.Fatalf("response status=%d success=%v atTarget=%v error=%q",
					response.Code, payload.Success, payload.Data.AtTarget, payload.Error)
			}
			if got := device.Status().Bass; got == nil || got.ActualBass != tt.wantCached {
				t.Fatalf("cached bass = %+v, want actual %d", got, tt.wantCached)
			}
			if got := device.Status().BassRevision; got != tt.wantRevision {
				t.Fatalf("cached bass revision = %d, want %d", got, tt.wantRevision)
			}
			if tt.wantAccepted {
				if payload.Data.Revision == nil || *payload.Data.Revision != tt.wantRevision {
					t.Fatalf("response revision = %v, want %d", payload.Data.Revision, tt.wantRevision)
				}
			} else if payload.Data.Revision != nil {
				t.Fatalf("failed readback invented response revision %d", *payload.Data.Revision)
			}
			if tt.wantCacheKept && payload.Data.Target != nil {
				t.Fatalf("unverified readback returned confirmed target: %+v", payload.Data)
			}
		})
	}
}

func TestHandleAPIControl_PresetValidation(t *testing.T) {
	app := createTestApp()

	tests := []struct {
		name           string
		query          string
		expectedStatus int
		expectSuccess  bool
	}{
		{
			name:           "missing preset ID",
			query:          "",
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
		{
			name:           "invalid preset ID",
			query:          "?id=abc",
			expectedStatus: http.StatusBadRequest,
			expectSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/control/devices/test-device/action/preset"+tt.query, nil)
			req = withChiParams(req, map[string]string{"id": "test-device", "action": "preset"})
			w := httptest.NewRecorder()

			app.HandleAPIControl(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var response webtypes.APIResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response.Success != tt.expectSuccess {
				t.Errorf("Expected success=%v, got %v", tt.expectSuccess, response.Success)
			}
		})
	}
}

func TestHandleAPIControl_SourceValidation(t *testing.T) {
	app := createTestApp()

	req := httptest.NewRequest("GET", "/api/control/devices/test-device/action/source", nil)
	req = withChiParams(req, map[string]string{"id": "test-device", "action": "source"})
	w := httptest.NewRecorder()

	app.HandleAPIControl(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var response webtypes.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Success {
		t.Errorf("Expected success=false, got true")
	}

	if response.Error != "Source name required" {
		t.Errorf("Expected 'Source name required' error, got '%s'", response.Error)
	}
}

func TestHandleAPIDiscover(t *testing.T) {
	app := createTestApp()

	tests := []struct {
		name           string
		method         string
		expectedStatus int
		expectSuccess  bool
	}{
		{
			name:           "valid POST request",
			method:         "POST",
			expectedStatus: http.StatusOK,
			expectSuccess:  true,
		},
		{
			name:           "invalid GET request",
			method:         "GET",
			expectedStatus: http.StatusMethodNotAllowed,
			expectSuccess:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "/api/control/discover", nil)
			w := httptest.NewRecorder()

			app.HandleAPIDiscover(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("Expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var response webtypes.APIResponse
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			if response.Success != tt.expectSuccess {
				t.Errorf("Expected success=%v, got %v", tt.expectSuccess, response.Success)
			}
		})
	}
}

func TestSendError(t *testing.T) {
	app := createTestApp()

	w := httptest.NewRecorder()
	app.sendError(w, "Test error", http.StatusBadRequest)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var response webtypes.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Success {
		t.Errorf("Expected success=false, got true")
	}

	if response.Error != "Test error" {
		t.Errorf("Expected 'Test error', got '%s'", response.Error)
	}

	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("Expected Content-Type 'application/json', got '%s'", contentType)
	}
}

func TestHandleWebSocket_InvalidUpgrade(t *testing.T) {
	app := createTestApp()

	// Test without proper WebSocket headers (should fail gracefully)
	req := httptest.NewRequest("GET", "/api/control/ws", nil)
	w := httptest.NewRecorder()

	// This will fail because it's not a real WebSocket upgrade, but should not panic
	app.HandleWebSocket(w, req)

	// We're just checking that the handler doesn't panic
	// The actual upgrade will fail in test environment without proper headers
}

func TestHandleAPIControl_UnsupportedAction(t *testing.T) {
	app := createTestApp()

	req := httptest.NewRequest("GET", "/api/control/devices/test-device/action/unsupported", nil)
	req = withChiParams(req, map[string]string{"id": "test-device", "action": "unsupported"})
	w := httptest.NewRecorder()

	app.HandleAPIControl(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected status 400, got %d", w.Code)
	}

	var response webtypes.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if response.Success {
		t.Errorf("Expected success=false, got true")
	}

	if response.Error != "Unknown action" {
		t.Errorf("Expected 'Unknown action' error, got '%s'", response.Error)
	}
}

func TestHandleAPIVersion(t *testing.T) {
	app := createTestApp()
	app.Version = "1.2.3"
	app.Commit = "abcdef123"
	app.Date = "2023-01-01"
	app.RepoURL = "https://github.com/example/repo"

	req := httptest.NewRequest("GET", "/api/control/version", nil)
	w := httptest.NewRecorder()

	app.HandleAPIVersion(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	var resp webtypes.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if !resp.Success {
		t.Errorf("Expected success=true, got false")
	}

	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data to be map[string]interface{}, got %T", resp.Data)
	}

	expected := map[string]string{
		"version":     "1.2.3",
		"commit":      "abcdef123",
		"date":        "2023-01-01",
		"repo_url":    "https://github.com/example/repo",
		"release_url": "https://github.com/example/repo/releases/tag/1.2.3",
		"commit_url":  "https://github.com/example/repo/commit/abcdef123",
	}

	for k, v := range expected {
		if data[k] != v {
			t.Errorf("Expected %s=%s, got %v", k, v, data[k])
		}
	}
}

// Benchmark tests
func BenchmarkHandleAPIDevices(b *testing.B) {
	app := createTestApp()

	// Add more devices for realistic benchmarking
	for i := 0; i < 10; i++ {
		deviceID := "device-" + string(rune('0'+i))
		conn := webtypes.NewDeviceConnection(&client.Client{}, &models.DeviceInfo{Name: "Test Device " + deviceID})
		conn.SetStatus(&webtypes.DeviceStatus{IsConnected: true})
		app.AddDevice(deviceID, conn)
	}

	req := httptest.NewRequest("GET", "/api/control/devices", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		app.HandleAPIDevices(w, req)
	}
}

func BenchmarkHandleAPIDevice(b *testing.B) {
	app := createTestApp()
	req := httptest.NewRequest("GET", "/api/control/devices/test-device", nil)
	req = withChiParams(req, map[string]string{"id": "test-device"})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		app.HandleAPIDevice(w, req)
	}
}

func BenchmarkSendError(b *testing.B) {
	app := createTestApp()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		app.sendError(w, "Test error", http.StatusBadRequest)
	}
}

// TestHandleDevicePlay_SourceAccountFiltering verifies that a SourceAccount
// equal to Source (the placeholder speakers echo back, e.g. "TUNEIN") is
// stripped before the ContentItem XML is sent to the speaker, while a real
// credential (SourceAccount != Source) is preserved.
func TestHandleDevicePlay_SourceAccountFiltering(t *testing.T) {
	tests := []struct {
		name              string
		source            string
		sourceAccount     string
		wantSourceAccount string // empty means the XML attr must be absent
	}{
		{
			name:              "placeholder echoed back — stripped",
			source:            "TUNEIN",
			sourceAccount:     "TUNEIN",
			wantSourceAccount: "",
		},
		{
			name:              "real credential — preserved",
			source:            "TUNEIN",
			sourceAccount:     "real-account-id",
			wantSourceAccount: "real-account-id",
		},
		{
			name:              "empty account — stays empty",
			source:            "TUNEIN",
			sourceAccount:     "",
			wantSourceAccount: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody string

			// Fake speaker that captures the /select POST body.
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select" {
					b, _ := io.ReadAll(r.Body)
					capturedBody = string(b)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer speaker.Close()

			speakerClient := client.NewClient(&client.Config{Host: speaker.URL})

			app := NewWebApp()
			deviceInfo := &models.DeviceInfo{Name: "Test Speaker"}
			conn := webtypes.NewDeviceConnection(speakerClient, deviceInfo)
			conn.SetStatus(&webtypes.DeviceStatus{IsConnected: true, LastActivity: time.Now()})
			app.AddDevice("play-device", conn)

			body := strings.NewReader(`{
				"source":"` + tt.source + `",
				"type":"stationurl",
				"location":"/v1/playback/station/s6634",
				"sourceAccount":"` + tt.sourceAccount + `",
				"itemName":"Venice Classic Radio"
			}`)
			req := httptest.NewRequest("POST", "/api/control/devices/play-device/play", body)
			req.Header.Set("Content-Type", "application/json")
			req = withChiParams(req, map[string]string{"id": "play-device"})
			w := httptest.NewRecorder()

			app.HandleDevicePlay(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}

			// SourceAccount XML attribute is always emitted (no omitempty on the struct
			// tag), so check its value rather than its presence/absence.
			want := `sourceAccount="` + tt.wantSourceAccount + `"`
			if !strings.Contains(capturedBody, want) {
				t.Errorf("XML should contain %q, got: %s", want, capturedBody)
			}
		})
	}
}

func TestHandlePlayURLReturnsSelectedContentIdentity(t *testing.T) {
	var selected models.ContentItem
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/select" {
			http.NotFound(w, r)
			return
		}
		if err := xml.NewDecoder(r.Body).Decode(&selected); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer speaker.Close()

	app := NewWebApp()
	app.ServiceURL = "http://aftertouch.test"
	conn := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{DeviceID: "DEVICE1", Name: "Speaker"},
	)
	app.AddDevice("speaker", conn)

	body := strings.NewReader(`{"url":"http://stream.test/audio","name":"Fixture stream","imageUrl":"http://stream.test/art.png"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/speaker/providers/url/play", body)
	req = withChiParams(req, map[string]string{"id": "speaker"})
	w := httptest.NewRecorder()
	app.HandlePlayURL(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("play URL status = %d: %s", w.Code, w.Body.String())
	}
	expectedLocation := bmxpkg.BuildOrionLocation(
		app.ServiceURL,
		"Fixture stream",
		"http://stream.test/art.png",
		"http://stream.test/audio",
	)
	var response struct {
		Success bool              `json:"success"`
		Data    map[string]string `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode play URL response: %v", err)
	}
	if !response.Success || response.Data["source"] != "LOCAL_INTERNET_RADIO" ||
		response.Data["location"] != expectedLocation || response.Data["itemName"] != "Fixture stream" {
		t.Fatalf("play URL identity = %+v, want exact selected content", response)
	}
	if selected.Source != response.Data["source"] || selected.Location != response.Data["location"] ||
		selected.ItemName != response.Data["itemName"] {
		t.Fatalf("speaker selection = %+v, response = %+v", selected, response.Data)
	}
}

// TestHandleSourceControl_LegacyGETForwardsAccount verifies that the temporary
// GET compatibility route still forwards sourceAccount and marks the response
// deprecated.
func TestHandleSourceControl_LegacyGETForwardsAccount(t *testing.T) {
	tests := []struct {
		name              string
		query             string
		wantSourceAccount string
	}{
		{
			name:              "AUX with explicit account — forwarded",
			query:             "name=AUX&account=AUX1",
			wantSourceAccount: "AUX1",
		},
		{
			name:              "AUX without account — defaults to AUX",
			query:             "name=AUX",
			wantSourceAccount: "AUX",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody string

			// Fake speaker that captures the /select POST body.
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/select" {
					b, _ := io.ReadAll(r.Body)
					capturedBody = string(b)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer speaker.Close()

			speakerClient := client.NewClient(&client.Config{Host: speaker.URL})

			app := NewWebApp()
			deviceInfo := &models.DeviceInfo{Name: "Test Speaker"}
			conn := webtypes.NewDeviceConnection(speakerClient, deviceInfo)
			conn.SetStatus(&webtypes.DeviceStatus{IsConnected: true, LastActivity: time.Now()})
			app.AddDevice("source-device", conn)

			req := httptest.NewRequest("GET", "/api/control/devices/source-device/action/source?"+tt.query, nil)
			req = withChiParams(req, map[string]string{"id": "source-device", "action": "source"})
			w := httptest.NewRecorder()

			app.HandleAPIControl(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
			}
			if got := w.Header().Get("Deprecation"); got != "true" {
				t.Errorf("Deprecation header = %q, want true", got)
			}
			if got := w.Header().Get("Warning"); !strings.Contains(got, "use POST") {
				t.Errorf("Warning header = %q, want POST migration guidance", got)
			}

			if want := `source="AUX"`; !strings.Contains(capturedBody, want) {
				t.Errorf("XML should contain %q, got: %s", want, capturedBody)
			}
			if want := `sourceAccount="` + tt.wantSourceAccount + `"`; !strings.Contains(capturedBody, want) {
				t.Errorf("XML should contain %q, got: %s", want, capturedBody)
			}
		})
	}
}

func TestHandleSourceControl_CanonicalPOSTForwardsExactBody(t *testing.T) {
	var capturedBody string
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/select" {
			body, _ := io.ReadAll(r.Body)
			capturedBody = string(body)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer speaker.Close()

	app := NewWebApp()
	conn := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{Name: "Test Speaker"},
	)
	app.AddDevice("source-device", conn)

	body := strings.NewReader(`{"source":"AUX","account":"AUX1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/source-device/action/source", body)
	req = withChiParams(req, map[string]string{"id": "source-device", "action": "source"})
	w := httptest.NewRecorder()
	app.HandleAPIControl(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Deprecation"); got != "" {
		t.Errorf("canonical POST unexpectedly marked deprecated: %q", got)
	}
	for _, want := range []string{`source="AUX"`, `sourceAccount="AUX1"`} {
		if !strings.Contains(capturedBody, want) {
			t.Errorf("XML should contain %q, got: %s", want, capturedBody)
		}
	}
}

func TestHandleSourceControl_CanonicalPOSTRejectsUnknownFields(t *testing.T) {
	app := NewWebApp()
	app.AddDevice("source-device", webtypes.NewDeviceConnection(nil, &models.DeviceInfo{Name: "Test Speaker"}))

	body := strings.NewReader(`{"source":"AUX","account":"AUX1","name":"legacy"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/source-device/action/source", body)
	req = withChiParams(req, map[string]string{"id": "source-device", "action": "source"})
	w := httptest.NewRecorder()
	app.HandleAPIControl(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleZoneAddRejectsSelf(t *testing.T) {
	app := NewWebApp()
	req := httptest.NewRequest("POST", "/api/control/devices/192.0.2.10/zone/add/192.0.2.10", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.10"})
	w := httptest.NewRecorder()

	app.HandleZoneAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cannot be added to its own zone") {
		t.Fatalf("unexpected response: %s", w.Body.String())
	}
}

func TestHandleGetZoneDoesNotProjectMasterAsMember(t *testing.T) {
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<zone master="MASTERHW01"><member ipaddress="192.0.2.10">MASTERHW01</member><member ipaddress="192.0.2.20">SLAVEHW02</member></zone>`))
	}))
	defer speaker.Close()

	app := NewWebApp()
	app.AddDevice("192.0.2.10", webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{Name: "Master", DeviceID: "MASTERHW01"},
	))
	app.AddDevice("192.0.2.20", webtypes.NewDeviceConnection(nil,
		&models.DeviceInfo{Name: "Slave", DeviceID: "SLAVEHW02"}))

	req := httptest.NewRequest("GET", "/api/control/devices/192.0.2.10/zone", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10"})
	w := httptest.NewRecorder()
	app.HandleGetZone(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			IsMaster bool `json:"isMaster"`
			IsSlave  bool `json:"isSlave"`
			Members  []struct {
				HwID string `json:"hwId"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.Success || !response.Data.IsMaster || response.Data.IsSlave {
		t.Fatalf("unexpected role projection: %+v", response.Data)
	}
	if len(response.Data.Members) != 1 || response.Data.Members[0].HwID != "SLAVEHW02" {
		t.Fatalf("members = %+v, want only SLAVEHW02", response.Data.Members)
	}
}

func TestHandleGetZoneProjectsStereoMasterAsLogicalMember(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "zone-master",
		Members: []models.Member{
			{DeviceID: "zone-master", IP: "192.0.2.5"},
			{DeviceID: "left-id", IP: "192.0.2.10"},
		},
	}
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`<zone master="zone-master"><member ipaddress="192.0.2.5">zone-master</member><member ipaddress="192.0.2.10">left-id</member></zone>`))
	}))
	defer speaker.Close()

	group := testStereoGroup()
	group.Name = "Living Room"
	group.Roles.Roles[0].IPAddress = "192.0.2.10"
	group.Roles.Roles[1].IPAddress = "192.0.2.11"

	app := NewWebApp()
	master := webtypes.NewDeviceConnection(client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{Name: "Kitchen", DeviceID: "zone-master", IPAddress: "192.0.2.5"})
	master.SetStatus(&webtypes.DeviceStatus{
		Zone: zone, Volume: &models.Volume{ActualVolume: 25},
		Connectivity: webtypes.ConnectivityOnline, IsConnected: true,
	})
	left := webtypes.NewDeviceConnection(nil,
		&models.DeviceInfo{Name: "Living Room Left", DeviceID: "left-id", IPAddress: "192.0.2.10"})
	left.SetStatus(&webtypes.DeviceStatus{
		Group: group, Volume: &models.Volume{ActualVolume: 12},
		Connectivity: webtypes.ConnectivityStale, IsConnected: true,
	})
	right := webtypes.NewDeviceConnection(nil,
		&models.DeviceInfo{Name: "Living Room Right", DeviceID: "right-id", IPAddress: "192.0.2.11"})
	right.SetStatus(&webtypes.DeviceStatus{
		Group: group, Volume: &models.Volume{ActualVolume: 18},
		Connectivity: webtypes.ConnectivityOnline, IsConnected: true,
	})
	app.AddDevice("192.0.2.5", master)
	app.AddDevice("192.0.2.10", left)
	app.AddDevice("192.0.2.11", right)

	req := httptest.NewRequest("GET", "/api/control/devices/192.0.2.5/zone", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.5"})
	w := httptest.NewRecorder()
	app.HandleGetZone(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Members []struct {
				ControlID    string                `json:"controlId"`
				IP           string                `json:"ip"`
				HwID         string                `json:"hwId"`
				Name         string                `json:"name"`
				DeviceIDs    []string              `json:"deviceIds"`
				Connectivity webtypes.Connectivity `json:"connectivity"`
				ActualVolume *int                  `json:"actualVolume"`
			} `json:"members"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data.Members) != 1 {
		t.Fatalf("zone members = %+v, want one logical stereo member", response.Data.Members)
	}
	member := response.Data.Members[0]
	if member.Name != "Living Room" || member.ControlID != "192.0.2.10" ||
		member.IP != "192.0.2.10" || member.HwID != "left-id" ||
		len(member.DeviceIDs) != 2 || member.DeviceIDs[0] != "left-id" || member.DeviceIDs[1] != "right-id" ||
		member.Connectivity != webtypes.ConnectivityStale || member.ActualVolume == nil || *member.ActualVolume != 12 {
		t.Fatalf("logical stereo member = %+v", member)
	}

	projected := app.deviceViewSnapshot()
	if len(projected) != 1 || projected["192.0.2.5"].Zone == nil ||
		len(projected["192.0.2.5"].Zone.Members) != 2 {
		t.Fatalf("top-level projection diverged from zone detail: %+v", projected)
	}
}

func TestHandleZoneAddRejectsSameHardwareUnderDifferentKeys(t *testing.T) {
	app := NewWebApp()
	app.AddDevice("speaker.local", webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: "http://speaker.local"}),
		&models.DeviceInfo{Name: "Speaker", DeviceID: "SAMEHW01"},
	))
	app.AddDevice("192.0.2.10", webtypes.NewDeviceConnection(nil,
		&models.DeviceInfo{Name: "Speaker alias", DeviceID: "SAMEHW01"}))

	req := httptest.NewRequest("POST", "/api/control/devices/speaker.local/zone/add/192.0.2.10", nil)
	req = withChiParams(req, map[string]string{"id": "speaker.local", "slaveId": "192.0.2.10"})
	w := httptest.NewRecorder()
	app.HandleZoneAdd(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCurrentSourceAllowsMultiroom(t *testing.T) {
	sources := &models.Sources{SourceItem: []models.SourceItem{
		{Source: "SPOTIFY", SourceAccount: "first", MultiroomAllowed: true},
		{Source: "BLUETOOTH", MultiroomAllowed: false},
	}}

	for _, test := range []struct {
		name       string
		nowPlaying *models.NowPlaying
		allowed    bool
	}{
		{name: "matching account", nowPlaying: &models.NowPlaying{Source: "SPOTIFY", SourceAccount: "first"}, allowed: true},
		{name: "different account", nowPlaying: &models.NowPlaying{Source: "SPOTIFY", SourceAccount: "second"}},
		{name: "source disallows multiroom", nowPlaying: &models.NowPlaying{Source: "BLUETOOTH"}},
		{name: "standby", nowPlaying: &models.NowPlaying{Source: "STANDBY"}},
		{name: "missing state"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := currentSourceAllowsMultiroom(test.nowPlaying, sources); got != test.allowed {
				t.Fatalf("currentSourceAllowsMultiroom() = %t, want %t", got, test.allowed)
			}
		})
	}
}

func TestHandleZoneAddUsesSetZoneWithoutStartingPlayback(t *testing.T) {
	var paths []string
	var zoneBody string
	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/now_playing":
			_, _ = w.Write([]byte(`<nowPlaying deviceID="MASTERHW01" source="LOCAL_INTERNET_RADIO"><playStatus>PLAY_STATE</playStatus></nowPlaying>`))
		case "/sources":
			_, _ = w.Write([]byte(`<sources deviceID="MASTERHW01"><sourceItem source="LOCAL_INTERNET_RADIO" status="READY" isLocal="false" multiroomallowed="true" /></sources>`))
		case "/getZone":
			_, _ = w.Write([]byte(`<zone master="MASTERHW01"/>`))
		case "/setZone":
			body, _ := io.ReadAll(r.Body)
			zoneBody = string(body)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer masterSpeaker.Close()

	app := NewWebApp()
	master := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: masterSpeaker.URL}),
		&models.DeviceInfo{Name: "Master", DeviceID: "MASTERHW01"},
	)
	master.SetStatus(&webtypes.DeviceStatus{IsConnected: true, LastActivity: time.Now()})
	app.AddDevice("192.0.2.10", master)
	app.AddDevice("192.0.2.20", webtypes.NewDeviceConnection(nil,
		&models.DeviceInfo{Name: "Slave", DeviceID: "SLAVEHW02"}))

	req := httptest.NewRequest("POST", "/api/control/devices/192.0.2.10/zone/add/192.0.2.20", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"})
	w := httptest.NewRecorder()
	app.HandleZoneAdd(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	wantPaths := []string{"GET /now_playing", "GET /sources", "GET /getZone", "POST /setZone"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("requests = %v, want %v", paths, wantPaths)
	}
	if !strings.Contains(zoneBody, "SLAVEHW02") {
		t.Fatalf("setZone body does not contain slave: %s", zoneBody)
	}
}

func TestHandleZoneAddRejectsStandbyMaster(t *testing.T) {
	var paths []string
	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/now_playing":
			_, _ = w.Write([]byte(`<nowPlaying deviceID="MASTERHW01" source="STANDBY"/>`))
		case "/sources":
			_, _ = w.Write([]byte(`<sources deviceID="MASTERHW01"><sourceItem source="LOCAL_INTERNET_RADIO" status="READY" multiroomallowed="true" /></sources>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer masterSpeaker.Close()

	app := NewWebApp()
	app.AddDevice("192.0.2.10", webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: masterSpeaker.URL}),
		&models.DeviceInfo{Name: "Master", DeviceID: "MASTERHW01"},
	))
	app.AddDevice("192.0.2.20", webtypes.NewDeviceConnection(nil,
		&models.DeviceInfo{Name: "Slave", DeviceID: "SLAVEHW02"}))

	req := httptest.NewRequest("POST", "/api/control/devices/192.0.2.10/zone/add/192.0.2.20", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"})
	w := httptest.NewRecorder()
	app.HandleZoneAdd(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if want := []string{"GET /now_playing", "GET /sources"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("requests = %v, want %v", paths, want)
	}
}

// TestHandleZoneRemove_UsesRemoveZoneSlave is the #511 regression: removing one
// member from a multi-member zone must target that member via /removeZoneSlave.
// The previous implementation rebuilt the zone with /setZone and the remaining
// members, which the speaker only honoured when the resulting member set was
// empty — so removing one of several members appeared to do nothing, while
// removing the last member (empty set == dissolve) worked.
func TestHandleZoneRemove_UsesRemoveZoneSlave(t *testing.T) {
	var gotPath, gotBody string

	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer speaker.Close()

	app := NewWebApp()

	// Registry is IP-keyed; use RFC-5737 documentation addresses.
	master := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{Name: "Master", DeviceID: "MASTERHW01"},
	)
	master.SetStatus(&webtypes.DeviceStatus{IsConnected: true, LastActivity: time.Now()})
	app.AddDevice("192.0.2.10", master)

	slave := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{Name: "Slave", DeviceID: "SLAVEHW02"})
	app.AddDevice("192.0.2.20", slave)

	req := httptest.NewRequest("POST", "/api/control/devices/192.0.2.10/zone/remove/192.0.2.20", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"})
	w := httptest.NewRecorder()

	app.HandleZoneRemove(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if gotPath != "/removeZoneSlave" {
		t.Errorf("expected POST to /removeZoneSlave, got %q (a /setZone rebuild does not drop a member from a multi-member zone)", gotPath)
	}

	if !strings.Contains(gotBody, "SLAVEHW02") {
		t.Errorf("removeZoneSlave body should target the slave device ID, got: %s", gotBody)
	}

	if !strings.Contains(gotBody, `master="MASTERHW01"`) {
		t.Errorf("removeZoneSlave body should name the master, got: %s", gotBody)
	}
}

func TestHandleZoneRemoveUsesMasterTopologyForMissingMember(t *testing.T) {
	var requests []string
	var removeBody string
	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/getZone":
			_, _ = w.Write([]byte(`<zone master="MASTERHW01"><member ipaddress="192.0.2.10">MASTERHW01</member><member ipaddress="192.0.2.99">MISSINGHW02</member></zone>`))
		case "/removeZoneSlave":
			body, _ := io.ReadAll(r.Body)
			removeBody = string(body)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer speaker.Close()

	app := NewWebApp()
	app.AddDevice("192.0.2.10", webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.URL}),
		&models.DeviceInfo{Name: "Master", DeviceID: "MASTERHW01"},
	))

	req := httptest.NewRequest("POST", "/api/control/devices/192.0.2.10/zone/remove/192.0.2.99", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.99"})
	w := httptest.NewRecorder()
	app.HandleZoneRemove(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if want := []string{"GET /getZone", "POST /removeZoneSlave"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests = %v, want %v", requests, want)
	}
	if !strings.Contains(removeBody, "MISSINGHW02") || !strings.Contains(removeBody, "192.0.2.99") {
		t.Fatalf("removeZoneSlave body lost topology-only member identity: %s", removeBody)
	}
}

// TestHandleZoneLeave_UsesRemoveZoneSlave is the #511 regression for the slave's
// "Leave zone" path: it must drop the slave via /removeZoneSlave on the master,
// not rebuild the master's zone with /setZone (which leaves a 3+ device zone
// unchanged). The leaving slave's "id" is its IP; it carries the master's hwID
// in its /getZone, which we resolve to the master's registry entry.
func TestHandleZoneLeave_UsesRemoveZoneSlave(t *testing.T) {
	var masterPath, masterBody string

	// Master speaker captures the /removeZoneSlave call.
	masterSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		masterPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		masterBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer masterSpeaker.Close()

	// Slave speaker answers /getZone naming the master by hwID.
	slaveSpeaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getZone" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8" ?>
<zone master="MASTERHW01">
	<member ipaddress="192.0.2.20">SLAVEHW02</member>
	<member ipaddress="192.0.2.30">SLAVEHW03</member>
</zone>`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer slaveSpeaker.Close()

	app := NewWebApp()

	master := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: masterSpeaker.URL}),
		&models.DeviceInfo{Name: "Master", DeviceID: "MASTERHW01"},
	)
	master.SetStatus(&webtypes.DeviceStatus{IsConnected: true, LastActivity: time.Now()})
	app.AddDevice("192.0.2.10", master)

	slave := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: slaveSpeaker.URL}),
		&models.DeviceInfo{Name: "Slave", DeviceID: "SLAVEHW02"},
	)
	slave.SetStatus(&webtypes.DeviceStatus{IsConnected: true, LastActivity: time.Now()})
	app.AddDevice("192.0.2.20", slave)

	req := httptest.NewRequest("POST", "/api/control/devices/192.0.2.20/zone/leave", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.20"})
	w := httptest.NewRecorder()

	app.HandleZoneLeave(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	if masterPath != "/removeZoneSlave" {
		t.Errorf("expected POST to master's /removeZoneSlave, got %q", masterPath)
	}

	if !strings.Contains(masterBody, "SLAVEHW02") {
		t.Errorf("removeZoneSlave body should target the leaving slave, got: %s", masterBody)
	}

	if !strings.Contains(masterBody, `master="MASTERHW01"`) {
		t.Errorf("removeZoneSlave body should name the master, got: %s", masterBody)
	}
}

func TestHandleGetZoneCandidatesIncludesHiddenPairMemberAndSelf(t *testing.T) {
	app := NewWebApp()
	group := testStereoGroup()

	for _, device := range []struct {
		id     string
		hwID   string
		name   string
		status *webtypes.DeviceStatus
	}{
		{"192.0.2.10", "left-id", "Living Room", &webtypes.DeviceStatus{IsConnected: true, Group: group}},
		{"192.0.2.11", "right-id", "Living Room", &webtypes.DeviceStatus{IsConnected: true, Group: group}},
		{"192.0.2.12", "kitchen-id", "Kitchen", &webtypes.DeviceStatus{IsConnected: true}},
	} {
		conn := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
			DeviceID:  device.hwID,
			Name:      device.name,
			IPAddress: device.id,
		})
		conn.SetStatus(device.status)
		app.AddDevice(device.id, conn)
	}

	if _, visible := app.deviceViewSnapshot()["192.0.2.11"]; visible {
		t.Fatal("test setup: stereo member should be hidden from logical projection")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/control/devices/192.0.2.10/zone/candidates", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10"})
	w := httptest.NewRecorder()
	app.HandleGetZoneCandidates(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, id := range []string{"192.0.2.10", "192.0.2.11", "192.0.2.12"} {
		if _, ok := response.Data[id]; !ok {
			t.Errorf("candidate %s missing from %+v", id, response.Data)
		}
	}
}

func TestHandleGetZoneCandidatesUnknownDeviceNotFound(t *testing.T) {
	app := NewWebApp()
	req := httptest.NewRequest(http.MethodGet, "/api/control/devices/missing/zone/candidates", nil)
	req = withChiParams(req, map[string]string{"id": "missing"})
	w := httptest.NewRecorder()

	app.HandleGetZoneCandidates(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
