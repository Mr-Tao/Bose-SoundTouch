package soundtouchweb

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

func TestZoneControlsRejectSlaveAsMaster(t *testing.T) {
	for _, operation := range []string{"add", "remove", "dissolve"} {
		t.Run(operation, func(t *testing.T) {
			var writes atomic.Int32
			speaker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					writes.Add(1)
				}
				switch r.URL.Path {
				case "/getZone":
					_, _ = w.Write([]byte(`<zone master="OTHER"><member>SLAVE</member></zone>`))
				case "/now_playing":
					_, _ = w.Write([]byte(`<nowPlaying source="LOCAL_INTERNET_RADIO"><playStatus>PLAY_STATE</playStatus></nowPlaying>`))
				case "/sources":
					_, _ = w.Write([]byte(`<sources><sourceItem source="LOCAL_INTERNET_RADIO" multiroomallowed="true"/></sources>`))
				}
			}))
			defer speaker.Close()
			app := NewWebApp()
			app.AddDevice("192.0.2.10", webtypes.NewDeviceConnection(client.NewClient(&client.Config{Host: speaker.URL}), &models.DeviceInfo{DeviceID: "SLAVE"}))
			app.AddDevice("192.0.2.20", webtypes.NewDeviceConnection(nil, &models.DeviceInfo{DeviceID: "CANDIDATE"}))
			request := withChiParams(httptest.NewRequest(http.MethodPost, "/", nil), map[string]string{"id": "192.0.2.10", "slaveId": "192.0.2.20"})
			response := httptest.NewRecorder()
			switch operation {
			case "add":
				app.HandleZoneAdd(response, request)
			case "remove":
				app.HandleZoneRemove(response, request)
			case "dissolve":
				app.HandleZoneDissolve(response, request)
			}
			if response.Code != http.StatusConflict || writes.Load() != 0 {
				t.Fatalf("status=%d writes=%d body=%s", response.Code, writes.Load(), response.Body.String())
			}
		})
	}
}

func TestZoneControlMasterAcceptsStandalone(t *testing.T) {
	for _, zone := range []*models.ZoneInfo{{}, {Master: "SPEAKER"}} {
		if !zoneControlMaster(zone, "SPEAKER") {
			t.Fatalf("standalone rejected: %+v", zone)
		}
	}
	if zoneControlMaster(nil, "SPEAKER") || zoneControlMaster(&models.ZoneInfo{}, "") {
		t.Fatal("missing topology or identity accepted")
	}
}

func TestZoneLeaveChecksMastersMembership(t *testing.T) {
	var writes atomic.Int32
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			writes.Add(1)
		}
		_, _ = w.Write([]byte(`<zone master="MASTER"><member>OTHER</member></zone>`))
	}))
	defer master.Close()
	slave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<zone master="MASTER"><member>SLAVE</member></zone>`))
	}))
	defer slave.Close()
	app := NewWebApp()
	app.AddDevice("192.0.2.10", webtypes.NewDeviceConnection(client.NewClient(&client.Config{Host: master.URL}), &models.DeviceInfo{DeviceID: "MASTER"}))
	app.AddDevice("192.0.2.20", webtypes.NewDeviceConnection(client.NewClient(&client.Config{Host: slave.URL}), &models.DeviceInfo{DeviceID: "SLAVE"}))
	response := httptest.NewRecorder()
	app.HandleZoneLeave(response, withChiParams(httptest.NewRequest(http.MethodPost, "/", nil), map[string]string{"id": "192.0.2.20"}))
	if response.Code != http.StatusConflict || writes.Load() != 0 || !strings.Contains(response.Body.String(), "current master zone") {
		t.Fatalf("status=%d writes=%d body=%s", response.Code, writes.Load(), response.Body.String())
	}
}
