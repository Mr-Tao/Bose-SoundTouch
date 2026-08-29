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
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

type volumeSpeaker struct {
	server          *httptest.Server
	mu              sync.Mutex
	volume          int
	zone            string
	posts           []int
	ignoreWrites    bool
	postError       bool
	volumeError     bool
	reportedTarget  *int
	volumeResponses []int
	zoneResponses   []string
	volumeGets      int
	zoneGets        int
	onVolumeGet     func(int)
	onZoneGet       func(int)
}

func newVolumeSpeaker(t *testing.T, initial int, zone string) *volumeSpeaker {
	t.Helper()

	speaker := &volumeSpeaker{volume: initial, zone: zone}
	speaker.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/getZone":
			speaker.mu.Lock()
			speaker.zoneGets++
			zoneGets := speaker.zoneGets
			zone := speaker.zone
			if len(speaker.zoneResponses) != 0 {
				zone = speaker.zoneResponses[0]
				speaker.zoneResponses = speaker.zoneResponses[1:]
			}
			onZoneGet := speaker.onZoneGet
			speaker.mu.Unlock()
			if onZoneGet != nil {
				onZoneGet(zoneGets)
			}
			_, _ = io.WriteString(w, zone)
		case r.Method == http.MethodGet && r.URL.Path == "/volume":
			speaker.mu.Lock()
			volume := speaker.volume
			if len(speaker.volumeResponses) != 0 {
				volume = speaker.volumeResponses[0]
				speaker.volumeResponses = speaker.volumeResponses[1:]
			}
			target := volume
			if speaker.reportedTarget != nil {
				target = *speaker.reportedTarget
			}
			volumeError := speaker.volumeError
			speaker.volumeGets++
			volumeGets := speaker.volumeGets
			onVolumeGet := speaker.onVolumeGet
			speaker.mu.Unlock()
			if onVolumeGet != nil {
				onVolumeGet(volumeGets)
			}
			if volumeError {
				http.Error(w, "readback failed", http.StatusBadGateway)
				return
			}
			_, _ = fmt.Fprintf(w, `<volume deviceID="speaker"><targetvolume>%d</targetvolume><actualvolume>%d</actualvolume><muteenabled>false</muteenabled></volume>`, target, volume)
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
			postError := speaker.postError
			speaker.mu.Unlock()
			if postError {
				http.Error(w, "write response lost", http.StatusBadGateway)
				return
			}
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

func (speaker *volumeSpeaker) setPostError(postError bool) {
	speaker.mu.Lock()
	speaker.postError = postError
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) setVolumeError(volumeError bool) {
	speaker.mu.Lock()
	speaker.volumeError = volumeError
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) setReportedTarget(target int) {
	speaker.mu.Lock()
	speaker.reportedTarget = intPointer(target)
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) queueVolumeResponses(values ...int) {
	speaker.mu.Lock()
	speaker.volumeResponses = append(speaker.volumeResponses, values...)
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) queueZoneResponses(values ...string) {
	speaker.mu.Lock()
	speaker.zoneResponses = append(speaker.zoneResponses, values...)
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) setVolumeGetHook(hook func(int)) {
	speaker.mu.Lock()
	speaker.onVolumeGet = hook
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) setZoneGetHook(hook func(int)) {
	speaker.mu.Lock()
	speaker.onZoneGet = hook
	speaker.mu.Unlock()
}

func (speaker *volumeSpeaker) values() (int, []int) {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return speaker.volume, append([]int(nil), speaker.posts...)
}

func (speaker *volumeSpeaker) getCount() int {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return speaker.volumeGets
}

func (speaker *volumeSpeaker) zoneGetCount() int {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return speaker.zoneGets
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
		!strings.Contains(memberResult.Error, "does not both match requested") {
		t.Fatalf("mismatch detail absent: %+v", memberResult)
	}
	if _, posts := member.values(); fmt.Sprint(posts) != "[40]" ||
		member.getCount() != 1+zoneVolumeReadbackAttempts {
		t.Fatalf("bounded mismatch operations: posts=%v gets=%d", posts, member.getCount())
	}

	masterConn, _ := app.GetDevice("192.0.2.10")
	memberConn, _ := app.GetDevice("192.0.2.20")
	if masterConn.Status().Volume.ActualVolume != 25 || memberConn.Status().Volume.ActualVolume != 35 {
		t.Fatalf("mismatch readback cache incorrect: master=%d member=%d",
			masterConn.Status().Volume.ActualVolume, memberConn.Status().Volume.ActualVolume)
	}
}

func TestHandleZoneVolumeWaitsForLaggingReadback(t *testing.T) {
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
	member.queueVolumeResponses(35, 35, 35, 40)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Living room", member, 35, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || payload.Data.Partial {
		t.Fatalf("lagging readback remained partial: status=%d data=%+v", response.Code, payload.Data)
	}
	if got := member.getCount(); got != 4 {
		t.Fatalf("member volume reads = %d, want initial plus three readback attempts", got)
	}
	if _, posts := member.values(); fmt.Sprint(posts) != "[40]" {
		t.Fatalf("member volume writes = %v, want one write despite the readback retry", posts)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID == "MEMBER" &&
			(result.Actual == nil || *result.Actual != 40 || result.Error != "") {
			t.Fatalf("lagging member result = %+v", result)
		}
	}
}

func TestHandleZoneVolumeRejectsTopologyChangeDuringRetryWait(t *testing.T) {
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
	masterConn, _ := app.GetDevice("192.0.2.10")
	app.volumeReadbackRetryWait = func(time.Duration) {
		generation := masterConn.BeginZoneRefresh()
		if !masterConn.ApplyPolledZone(generation, "MASTER", &models.ZoneInfo{}) {
			t.Error("failed to apply zone dissolution during retry wait")
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial {
		t.Fatalf("topology change was not rejected: status=%d data=%+v", response.Code, payload.Data)
	}
	if got := member.getCount(); got != 2 {
		t.Fatalf("member volume reads = %d, want initial plus one readback", got)
	}
	if _, posts := member.values(); fmt.Sprint(posts) != "[40]" {
		t.Fatalf("member volume writes = %v, want exactly one", posts)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID != "MEMBER" {
			continue
		}
		if result.Actual != nil || !strings.Contains(result.Error, "state changed during readback") {
			t.Fatalf("topology-change result = %+v", result)
		}

		return
	}
	t.Fatal("member result missing")
}

func TestHandleZoneVolumeDoesNotConfirmRejectedFreshMismatchFromOlderCache(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)
	member.setIgnoreWrites(true)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 40, nil)
	memberConn, _ := app.GetDevice("192.0.2.20")
	member.setVolumeGetHook(func(request int) {
		if request == 2 {
			memberConn.ApplyVolumeEvent(
				&models.Volume{TargetVolume: 40, ActualVolume: 40},
				time.Now(),
			)
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial {
		t.Fatalf("rejected fresh mismatch was confirmed: status=%d data=%+v", response.Code, payload.Data)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID != "MEMBER" {
			continue
		}
		if result.Actual == nil || *result.Actual != 35 ||
			!strings.Contains(result.Error, "does not both match requested") {
			t.Fatalf("fresh retry did not expose the authoritative mismatch: %+v", result)
		}
		if cached := memberConn.Status().Volume.ActualVolume; cached != 35 {
			t.Fatalf("fresh retry cache = %d, want 35", cached)
		}

		return
	}
	t.Fatal("member result missing")
}

func TestHandleZoneVolumeRetriesReadbackSupersededByMatchingEvent(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 35, nil)
	memberConn, _ := app.GetDevice("192.0.2.20")
	member.setVolumeGetHook(func(request int) {
		if request == 2 {
			memberConn.ApplyVolumeEvent(
				&models.Volume{TargetVolume: 40, ActualVolume: 40},
				time.Now(),
			)
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || payload.Data.Partial {
		t.Fatalf("matching event race remained partial: status=%d data=%+v", response.Code, payload.Data)
	}
	if got := member.getCount(); got != 3 {
		t.Fatalf("member volume reads = %d, want initial plus two readback attempts", got)
	}
	if _, posts := member.values(); fmt.Sprint(posts) != "[40]" {
		t.Fatalf("member volume writes = %v, want one write despite the readback retry", posts)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID == "MEMBER" &&
			(result.Actual == nil || *result.Actual != 40 || result.Error != "") {
			t.Fatalf("retried member result = %+v", result)
		}
	}
}

func TestHandleZoneVolumeBoundsRepeatedReadbackInvalidation(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 35, nil)
	memberConn, _ := app.GetDevice("192.0.2.20")
	member.setVolumeGetHook(func(request int) {
		if request >= 2 {
			memberConn.ApplyVolumeEvent(
				&models.Volume{TargetVolume: 40, ActualVolume: 40},
				time.Now(),
			)
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial {
		t.Fatalf("repeated event race was not bounded as partial: status=%d data=%+v", response.Code, payload.Data)
	}
	if got := member.getCount(); got != 1+zoneVolumeReadbackAttempts {
		t.Fatalf("member volume reads = %d, want initial plus %d attempts", got, zoneVolumeReadbackAttempts)
	}
	if _, posts := member.values(); fmt.Sprint(posts) != "[40]" {
		t.Fatalf("member volume writes = %v, want one write despite bounded retries", posts)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID == "MEMBER" &&
			(result.Actual != nil || !strings.Contains(result.Error, "state changed during readback")) {
			t.Fatalf("repeatedly invalidated result = %+v", result)
		}
	}
}

func TestHandleZoneVolumeRejectsZoneChangeDuringReadback(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, zoneXML)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 35, nil)
	masterConn, _ := app.GetDevice("192.0.2.10")
	memberConn, _ := app.GetDevice("192.0.2.20")
	member.setVolumeGetHook(func(request int) {
		if request != 2 {
			return
		}
		generation := masterConn.BeginZoneRefresh()
		if !masterConn.ApplyPolledZone(generation, "MASTER", &models.ZoneInfo{}) {
			t.Error("failed to apply concurrent zone dissolution")
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial {
		t.Fatalf("zone dissolution was confirmed: status=%d data=%+v", response.Code, payload.Data)
	}
	if got := member.getCount(); got != 2 {
		t.Fatalf("topology-invalid member reads = %d, want no retry after initial readback", got)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID != "MEMBER" {
			continue
		}
		if result.Actual != nil ||
			!strings.Contains(result.Error, "topology") {
			t.Fatalf("zone-invalid readback result = %+v", result)
		}
		if cached := memberConn.Status().Volume.ActualVolume; cached != 35 {
			t.Fatalf("zone-invalid readback overwrote cache with %d", cached)
		}
		return
	}
	t.Fatal("member result missing")
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

func TestHandleZoneVolumeCollapsesStereoPairToOneControlTarget(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.5">MASTER</member><member ipaddress="192.0.2.10">LEFT</member><member ipaddress="192.0.2.11">RIGHT</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.5"},
		{DeviceID: "LEFT", IP: "192.0.2.10"},
		{DeviceID: "RIGHT", IP: "192.0.2.11"},
	}}
	group := &models.Group{
		ID: "pair", Name: "Living Room", MasterDeviceID: "LEFT", Status: "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
	master := newVolumeSpeaker(t, 20, zoneXML)
	left := newVolumeSpeaker(t, 30, "")
	right := newVolumeSpeaker(t, 80, "")

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.5", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.10", "LEFT", "Living Room", left, 30, nil)
	addVolumeDevice(app, "192.0.2.11", "RIGHT", "Living Room", right, 80, nil)
	for _, controlID := range []string{"192.0.2.10", "192.0.2.11"} {
		conn, _ := app.GetDevice(controlID)
		conn.UpdateStatus(func(status *webtypes.DeviceStatus) { status.Group = group })
	}

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.5/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.5", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || payload.Data.Partial || payload.Data.Baseline != 30 ||
		payload.Data.Delta != 10 || len(payload.Data.Members) != 2 {
		t.Fatalf("logical group result: status=%d data=%+v", response.Code, payload.Data)
	}
	if volume, posts := left.values(); volume != 40 || fmt.Sprint(posts) != "[40]" {
		t.Fatalf("pair master operations: volume=%d posts=%v", volume, posts)
	}
	if volume, posts := right.values(); volume != 80 || len(posts) != 0 {
		t.Fatalf("pair member was controlled separately: volume=%d posts=%v", volume, posts)
	}
	if left.getCount() != 2 || right.getCount() != 0 {
		t.Fatalf("pair reads: control=%d physical member=%d", left.getCount(), right.getCount())
	}
}

func TestHandleZoneVolumeUsesRightRoleStereoMaster(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.5">MASTER</member><member ipaddress="192.0.2.10">LEFT</member><member ipaddress="192.0.2.11">RIGHT</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.5"},
		{DeviceID: "LEFT", IP: "192.0.2.10"},
		{DeviceID: "RIGHT", IP: "192.0.2.11"},
	}}
	group := &models.Group{
		ID: "pair", Name: "Living Room", MasterDeviceID: "RIGHT", Status: "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
	master := newVolumeSpeaker(t, 20, zoneXML)
	left := newVolumeSpeaker(t, 80, "")
	right := newVolumeSpeaker(t, 30, "")

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.5", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.10", "LEFT", "Living Room left", left, 80, nil)
	addVolumeDevice(app, "192.0.2.11", "RIGHT", "Living Room right", right, 30, nil)
	for _, controlID := range []string{"192.0.2.10", "192.0.2.11"} {
		conn, _ := app.GetDevice(controlID)
		conn.UpdateStatus(func(status *webtypes.DeviceStatus) { status.Group = group })
	}

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.5/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.5", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || payload.Data.Partial || payload.Data.Baseline != 30 ||
		payload.Data.Delta != 10 || len(payload.Data.Members) != 2 {
		t.Fatalf("logical group result: status=%d data=%+v", response.Code, payload.Data)
	}
	if volume, posts := right.values(); volume != 40 || fmt.Sprint(posts) != "[40]" {
		t.Fatalf("RIGHT pair master operations: volume=%d posts=%v", volume, posts)
	}
	if volume, posts := left.values(); volume != 80 || len(posts) != 0 {
		t.Fatalf("LEFT pair member was controlled separately: volume=%d posts=%v", volume, posts)
	}
	if right.getCount() != 2 || left.getCount() != 0 {
		t.Fatalf("pair reads: RIGHT control=%d LEFT member=%d", right.getCount(), left.getCount())
	}
	for _, result := range payload.Data.Members {
		if result.ControlID == "192.0.2.11" && result.Name != "Living Room" {
			t.Fatalf("logical pair result name = %q, want %q", result.Name, "Living Room")
		}
	}
}

func TestHandleZoneVolumeAcceptsMatchingReadbackAfterPostError(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newVolumeSpeaker(t, 20, zoneXML)
	member := newVolumeSpeaker(t, 35, "")
	member.setPostError(true)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", member, 35, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || payload.Data.Partial {
		t.Fatalf("matching readback was not authoritative: status=%d data=%+v", response.Code, payload.Data)
	}
	for _, result := range payload.Data.Members {
		if result.Actual == nil || result.Target == nil || *result.Actual != *result.Target || result.Error != "" {
			t.Fatalf("confirmed member result = %+v", result)
		}
	}
}

func TestHandleZoneVolumeRefusesRegisteredStereoSlaveWithoutMaster(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">SLAVE</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "SLAVE", IP: "192.0.2.20"},
	}}
	group := &models.Group{
		ID: "pair", Name: "Living Room", MasterDeviceID: "PAIRMASTER", Status: "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "PAIRMASTER", Role: "LEFT", IPAddress: "192.0.2.99"},
			{DeviceID: "SLAVE", Role: "RIGHT", IPAddress: "192.0.2.20"},
		}},
	}
	master := newVolumeSpeaker(t, 20, zoneXML)
	slave := newVolumeSpeaker(t, 30, "")

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "SLAVE", "Living right", slave, 30, nil)
	slaveConn, ok := app.GetDevice("192.0.2.20")
	if !ok {
		t.Fatal("registered stereo slave missing from fixture")
	}
	slaveConn.UpdateStatus(func(status *webtypes.DeviceStatus) { status.Group = group })

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.10/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.10", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial || len(payload.Data.Members) != 2 {
		t.Fatalf("unsafe slave response: status=%d data=%+v", response.Code, payload.Data)
	}
	if _, posts := slave.values(); len(posts) != 0 || slave.getCount() != 0 {
		t.Fatalf("registered stereo slave was touched: posts=%v gets=%d", posts, slave.getCount())
	}
	found := false
	for _, member := range payload.Data.Members {
		if member.DeviceID == "SLAVE" {
			found = strings.Contains(member.Error, "stereo pair master unavailable")
		}
	}
	if !found {
		t.Fatalf("missing explicit slave failure: %+v", payload.Data.Members)
	}
}

func TestHandleZoneVolumeRejectsStereoMasterChangeBeforeWrite(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.5">MASTER</member><member ipaddress="192.0.2.10">LEFT</member><member ipaddress="192.0.2.11">RIGHT</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.5"},
		{DeviceID: "LEFT", IP: "192.0.2.10"},
		{DeviceID: "RIGHT", IP: "192.0.2.11"},
	}}
	group := &models.Group{
		ID: "pair", Name: "Living Room", MasterDeviceID: "LEFT", Status: "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
	replacement := *group
	replacement.MasterDeviceID = "RIGHT"
	master := newVolumeSpeaker(t, 20, zoneXML)
	left := newVolumeSpeaker(t, 30, "")
	right := newVolumeSpeaker(t, 30, "")

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.5", "MASTER", "Kitchen", master, 20, zone)
	addVolumeDevice(app, "192.0.2.10", "LEFT", "Living Room", left, 30, nil)
	addVolumeDevice(app, "192.0.2.11", "RIGHT", "Living Room", right, 30, nil)
	leftConn, _ := app.GetDevice("192.0.2.10")
	rightConn, _ := app.GetDevice("192.0.2.11")
	leftConn.UpdateStatus(func(status *webtypes.DeviceStatus) { status.Group = group })
	rightConn.UpdateStatus(func(status *webtypes.DeviceStatus) { status.Group = group })
	left.setVolumeGetHook(func(request int) {
		if request == 1 {
			leftConn.ApplyGroupEvent(&replacement, time.Now())
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/api/control/devices/192.0.2.5/zone/volume/40", nil)
	req = withChiParams(req, map[string]string{"id": "192.0.2.5", "volume": "40"})
	response := httptest.NewRecorder()
	app.HandleZoneVolume(response, req)

	var payload struct {
		Data zoneVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial {
		t.Fatalf("master change was not degraded: status=%d data=%+v", response.Code, payload.Data)
	}
	if _, posts := left.values(); len(posts) != 0 {
		t.Fatalf("former stereo master received write: %v", posts)
	}
	for _, result := range payload.Data.Members {
		if result.DeviceID == "LEFT" && strings.Contains(result.Error, "topology changed") {
			return
		}
	}
	t.Fatalf("topology failure missing: %+v", payload.Data.Members)
}
