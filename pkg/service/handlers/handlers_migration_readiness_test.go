package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/datastore"
	"github.com/gesellix/bose-soundtouch/pkg/service/setup"
	"github.com/go-chi/chi/v5"
)

func TestHandleMigrateDeviceMapsMigrationDataNotReadyToConflict(t *testing.T) {
	const (
		accountID = "1234567"
		deviceID  = "DEVICE01"
	)

	speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			http.NotFound(w, r)
			return
		}

		_, _ = fmt.Fprintf(w, `<info deviceID="%s"><name>Test Speaker</name><margeAccountUUID>%s</margeAccountUUID></info>`, deviceID, accountID)
	}))
	defer speaker.Close()

	ds := datastore.NewDataStore(t.TempDir())
	deviceIP := strings.TrimPrefix(speaker.URL, "http://")
	if err := ds.SaveDeviceInfo(accountID, deviceID, &models.ServiceDeviceInfo{
		DeviceID:  deviceID,
		AccountID: accountID,
		IPAddress: deviceIP,
		Name:      "Test Speaker",
	}); err != nil {
		t.Fatalf("SaveDeviceInfo: %v", err)
	}

	manager := setup.NewManager("http://aftertouch.example:8000", ds, nil)
	server := NewServer(ds, manager, manager.ServerURL, false, false, false)
	router := chi.NewRouter()
	router.Post("/migrate/{deviceId}", server.HandleMigrateDevice)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/migrate/"+deviceID+"?method=telnet", nil)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}

	var response struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OK {
		t.Fatal("response ok = true, want false")
	}
	if !strings.Contains(response.Message, "Data Sync") {
		t.Fatalf("message = %q, want actionable Data Sync guidance", response.Message)
	}
}
