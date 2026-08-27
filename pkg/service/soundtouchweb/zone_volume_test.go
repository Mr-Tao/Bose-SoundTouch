package soundtouchweb

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

type volumeSpeaker struct {
	server       *httptest.Server
	mu           sync.Mutex
	volume       int
	zone         string
	posts        []int
	ignoreWrites bool
	volumeGets   int
	onVolumeGet  func(int)
}

func newVolumeSpeaker(t *testing.T, initial int, zone string) *volumeSpeaker {
	t.Helper()

	speaker := &volumeSpeaker{volume: initial, zone: zone}
	speaker.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/getZone":
			_, _ = io.WriteString(w, speaker.zone)
		case r.Method == http.MethodGet && r.URL.Path == "/volume":
			speaker.mu.Lock()
			volume := speaker.volume
			speaker.volumeGets++
			volumeGets := speaker.volumeGets
			onVolumeGet := speaker.onVolumeGet
			speaker.mu.Unlock()
			if onVolumeGet != nil {
				onVolumeGet(volumeGets)
			}
			_, _ = fmt.Fprintf(w, `<volume deviceID="speaker"><targetvolume>%d</targetvolume><actualvolume>%d</actualvolume><muteenabled>false</muteenabled></volume>`, volume, volume)
		case r.Method == http.MethodPost && r.URL.Path == "/volume":
			var request models.VolumeRequest
			if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			speaker.mu.Lock()
			if !speaker.ignoreWrites {
				speaker.volume = request.Value
			}
			speaker.posts = append(speaker.posts, request.Value)
			speaker.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(speaker.server.Close)

	return speaker
}

func (speaker *volumeSpeaker) setIgnoreWrites(ignore bool) {
	speaker.mu.Lock()
	speaker.ignoreWrites = ignore
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) setVolumeGetHook(hook func(int)) {
	speaker.mu.Lock()
	speaker.onVolumeGet = hook
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) values() (int, []int) {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return speaker.volume, append([]int(nil), speaker.posts...)
}

func addVolumeDevice(
	app *WebApp,
	controlID, deviceID, name string,
	speaker *volumeSpeaker,
	volume int,
	zone *models.ZoneInfo,
) {
	conn := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.server.URL}),
		&models.DeviceInfo{DeviceID: deviceID, Name: name, IPAddress: controlID},
	)
	conn.SetStatus(&webtypes.DeviceStatus{
		Connectivity:  webtypes.ConnectivityOnline,
		HTTPReachable: true,
		IsConnected:   true,
		Volume:        &models.Volume{ActualVolume: volume, TargetVolume: volume},
		Zone:          zone,
	})
	app.AddDevice(controlID, conn)
}

func TestHandleZoneVolumeAppliesSharedDeltaAndReadback(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 35, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}

	var payload struct {
		Success bool             `json:"success"`
		Data    zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Success || payload.Data.Partial || payload.Data.Baseline != 35 || payload.Data.Delta != 5 {
		t.Fatalf("unexpected response: %+v", payload)
	}

	masterVolume, masterPosts := master.values()
	memberVolume, memberPosts := member.values()
	if masterVolume != 25 || memberVolume != 40 || fmt.Sprint(masterPosts) != "[25]" || fmt.Sprint(memberPosts) != "[40]" {
		t.Fatalf("unexpected writes: master=%d/%v member=%d/%v", masterVolume, masterPosts, memberVolume, memberPosts)
	}
	for _, result := range payload.Data.Members {
		if result.Actual == nil || result.Target == nil || *result.Actual != *result.Target {
			t.Fatalf("missing final readback: %+v", result)
		}
	}

	masterConn, _ := app.GetDevice("192.0.2.10")
	memberConn, _ := app.GetDevice("192.0.2.20")
	if masterConn.Status().Volume.ActualVolume != 25 || memberConn.Status().Volume.ActualVolume != 40 {
		t.Fatalf("readback cache not refreshed: master=%d member=%d",
			masterConn.Status().Volume.ActualVolume, memberConn.Status().Volume.ActualVolume)
	}
}

func TestHandleZoneVolumeReportsReadbackMismatch(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)
	member.setIgnoreWrites(true)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Living room", member, 35, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Data.Partial {
		t.Fatalf("readback mismatch reported as success: %+v", payload.Data)
	}

	var memberResult *zoneVolumeMemberResult
	for index := range payload.Data.Members {
		if payload.Data.Members[index].DeviceID == "MEMBER" {
			memberResult = &payload.Data.Members[index]
			break
		}
	}
	if memberResult == nil || memberResult.Target == nil || *memberResult.Target != 40 ||
		memberResult.Actual == nil || *memberResult.Actual != 35 ||
		!strings.Contains(memberResult.Error, "does not match target") {
		t.Fatalf("mismatch detail absent: %+v", memberResult)
	}

	masterConn, _ := app.GetDevice("192.0.2.10")
	memberConn, _ := app.GetDevice("192.0.2.20")
	if masterConn.Status().Volume.ActualVolume != 25 || memberConn.Status().Volume.ActualVolume != 35 {
		t.Fatalf("mismatch readback cache incorrect: master=%d member=%d",
			masterConn.Status().Volume.ActualVolume, memberConn.Status().Volume.ActualVolume)
	}
}

func TestHandleZoneVolumeCachesReadbackAcrossUnrelatedSpeakerEvent(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Living room", member, 35, nil)
	memberConn, _ := app.GetDevice("192.0.2.20")
	member.setVolumeGetHook(func(request int) {
		if request == 2 {
			memberConn.ApplySpeakerEvent(func(status *webtypes.DeviceStatus) {
				status.NowPlaying = &models.NowPlaying{Source: "RADIO"}
			})
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	got := memberConn.Status()
	if got.Volume == nil || got.Volume.ActualVolume != 40 ||
		got.NowPlaying == nil || got.NowPlaying.Source != "RADIO" {
		t.Fatalf("status = %+v, want readback volume and unrelated event", got)
	}
}

func TestHandleZoneVolumeReportsMissingMemberAndClamps(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member><member ipaddress="192.0.2.99">MISSING</member></zone>`
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
			{DeviceID: "MISSING", IP: "192.0.2.99"},
		},
	}
	master := newVolumeSpeaker(t, 95, zoneXML)
	member := newVolumeSpeaker(t, 20, zoneXML)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 95, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 20, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/100", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "100"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Data.Partial || payload.Data.Delta != 5 {
		t.Fatalf("partial result not reported: %+v", payload.Data)
	}

	masterVolume, _ := master.values()
	memberVolume, _ := member.values()
	if masterVolume != 100 || memberVolume != 25 {
		t.Fatalf("clamped delta mismatch: master=%d member=%d", masterVolume, memberVolume)
	}

	missingErrors := 0
	for _, result := range payload.Data.Members {
		if result.DeviceID == "MISSING" && strings.Contains(result.Error, "unavailable") {
			missingErrors++
		}
	}
	if missingErrors != 1 {
		t.Fatalf("missing member error absent: %+v", payload.Data.Members)
	}
}

func TestHandleZoneVolumeClearsCacheWhenMasterReportsStandalone(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}
	master := newVolumeSpeaker(t, 20, `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member></zone>`)
	member := newVolumeSpeaker(t, 20, "")

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 20, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/30", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": strconv.Itoa(30)})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	conn, _ := app.GetDevice("192.0.2.10")
	if conn.Status().Zone != nil {
		t.Fatalf("master-confirmed standalone response did not clear cache: %+v", conn.Status().Zone)
	}
}
