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
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

type zoneMemberVolumeSpeaker struct {
	server       *httptest.Server
	mu           sync.Mutex
	zone         string
	volume       int
	posts        []int
	volumeGets   int
	failReadback bool
	ignoreWrites bool
	propagate    func(int)
	afterPost    func()
	onVolumeGet  func(int)
}

func newZoneMemberVolumeSpeaker(t *testing.T, volume int, zone string) *zoneMemberVolumeSpeaker {
	t.Helper()

	speaker := &zoneMemberVolumeSpeaker{volume: volume, zone: zone}
	speaker.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/getZone":
			_, _ = io.WriteString(w, speaker.zone)
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
			propagate := speaker.propagate
			afterPost := speaker.afterPost
			speaker.mu.Unlock()
			if propagate != nil {
				propagate(request.Value)
			}
			if afterPost != nil {
				afterPost()
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/volume":
			speaker.mu.Lock()
			speaker.volumeGets++
			volumeGets := speaker.volumeGets
			volume := speaker.volume
			fail := speaker.failReadback
			onVolumeGet := speaker.onVolumeGet
			speaker.mu.Unlock()
			if onVolumeGet != nil {
				onVolumeGet(volumeGets)
			}
			if fail {
				http.Error(w, "readback failed", http.StatusBadGateway)
				return
			}
			_, _ = fmt.Fprintf(w, `<volume deviceID="speaker"><targetvolume>%d</targetvolume><actualvolume>%d</actualvolume><muteenabled>false</muteenabled></volume>`, volume, volume)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(speaker.server.Close)

	return speaker
}

func (speaker *zoneMemberVolumeSpeaker) setVolume(volume int) {
	speaker.mu.Lock()
	speaker.volume = volume
	speaker.mu.Unlock()
}

func (speaker *zoneMemberVolumeSpeaker) values() (int, []int, int) {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()

	return speaker.volume, append([]int(nil), speaker.posts...), speaker.volumeGets
}

func addZoneMemberVolumeDevice(
	app *WebApp,
	controlID, deviceID, name, deviceType string,
	speaker *zoneMemberVolumeSpeaker,
	volume int,
	group *models.Group,
	zone *models.ZoneInfo,
) *webtypes.DeviceConnection {
	conn := webtypes.NewDeviceConnection(
		client.NewClient(&client.Config{Host: speaker.server.URL}),
		&models.DeviceInfo{
			DeviceID:  deviceID,
			Name:      name,
			Type:      deviceType,
			IPAddress: controlID,
		},
	)
	conn.SetStatus(&webtypes.DeviceStatus{
		Connectivity:  webtypes.ConnectivityOnline,
		HTTPReachable: true,
		IsConnected:   true,
		Volume:        &models.Volume{ActualVolume: volume, TargetVolume: volume},
		Group:         group,
		Zone:          zone,
	})
	app.AddDevice(controlID, conn)

	return conn
}

func zoneMemberVolumeRequest(masterID, memberID string, volume int) *http.Request {
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf(
		"/api/control/devices/%s/zone/member/%s/volume/%d",
		masterID,
		memberID,
		volume,
	), nil)

	return withChiParams(req, map[string]string{
		"id":       masterID,
		"memberId": memberID,
		"volume":   fmt.Sprint(volume),
	})
}

func TestHandleZoneMemberVolumeOrdinarySuccessAndReadback(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	member := newZoneMemberVolumeSpeaker(t, 31, "")

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	memberConn := addZoneMemberVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", "SoundTouch 20", member, 31, nil, nil)

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.10", "192.0.2.20", 44))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Success bool                   `json:"success"`
		Data    zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Success || payload.Data.Requested != 44 || payload.Data.ControlID != "192.0.2.20" ||
		payload.Data.Partial || len(payload.Data.Members) != 1 ||
		payload.Data.Members[0].Actual == nil || *payload.Data.Members[0].Actual != 44 {
		t.Fatalf("response = %+v", payload)
	}
	if volume, posts, gets := member.values(); volume != 44 || fmt.Sprint(posts) != "[44]" || gets != 1 {
		t.Fatalf("member operations = volume %d, posts %v, gets %d", volume, posts, gets)
	}
	if _, posts, gets := master.values(); len(posts) != 0 || gets != 0 {
		t.Fatalf("master unexpectedly received volume operations: posts %v, gets %d", posts, gets)
	}
	if actual := memberConn.Status().Volume.ActualVolume; actual != 44 {
		t.Fatalf("cached member volume = %d, want 44", actual)
	}
}

func TestHandleZoneMemberVolumeStereoUsesOneAuthoritativeTarget(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.5">MASTER</member><member ipaddress="192.0.2.10">LEFT</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.5"},
		{DeviceID: "LEFT", IP: "192.0.2.10"},
	}}
	group := &models.Group{
		ID: "pair", Name: "Living Room", MasterDeviceID: "LEFT", Status: "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	left := newZoneMemberVolumeSpeaker(t, 30, "")
	right := newZoneMemberVolumeSpeaker(t, 30, "")
	left.propagate = right.setVolume

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.5", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	leftConn := addZoneMemberVolumeDevice(app, "192.0.2.10", "LEFT", "Living Room", "SoundTouch 10", left, 30, group, nil)
	rightConn := addZoneMemberVolumeDevice(app, "192.0.2.11", "RIGHT", "Living Room", "SoundTouch 10", right, 30, group, nil)
	leftConn.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Balance = &models.Balance{TargetBalance: 0, ActualBalance: 0}
	})
	projected := app.deviceViewSnapshot()["192.0.2.5"].Zone
	if projected == nil || len(projected.Members) != 2 || projected.Members[1].Kind != "stereoPair" ||
		len(projected.Members[1].PhysicalMembers) != 2 {
		t.Fatalf("stereo fixture did not project a logical pair: %+v", projected)
	}

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.5", "192.0.2.10", 46))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.Partial || payload.Data.ControlID != "192.0.2.10" || len(payload.Data.Members) != 1 {
		t.Fatalf("response = %+v; body=%s", payload.Data, response.Body.String())
	}
	memberResult := payload.Data.Members[0]
	if memberResult.DeviceID != "LEFT" || memberResult.Actual == nil || *memberResult.Actual != 46 || memberResult.Error != "" {
		t.Fatalf("logical readback = %+v", memberResult)
	}
	if _, posts, gets := left.values(); fmt.Sprint(posts) != "[46]" || gets != 1 {
		t.Fatalf("pair master operations = posts %v, gets %d", posts, gets)
	}
	if _, posts, gets := right.values(); len(posts) != 0 || gets != 0 {
		t.Fatalf("pair member operations = posts %v, gets %d", posts, gets)
	}
	if leftConn.Status().Volume.ActualVolume != 46 || rightConn.Status().Volume.ActualVolume != 30 {
		t.Fatalf("pair cache was not refreshed: left=%d right=%d",
			leftConn.Status().Volume.ActualVolume,
			rightConn.Status().Volume.ActualVolume,
		)
	}
}

func TestHandleZoneMemberVolumeStereoUsesRightRoleMaster(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.5">MASTER</member><member ipaddress="192.0.2.11">RIGHT</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.5"},
		{DeviceID: "RIGHT", IP: "192.0.2.11"},
	}}
	group := &models.Group{
		ID: "pair", Name: "Living Room", MasterDeviceID: "RIGHT", Status: "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "LEFT", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "RIGHT", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	left := newZoneMemberVolumeSpeaker(t, 30, "")
	right := newZoneMemberVolumeSpeaker(t, 30, "")
	right.propagate = left.setVolume

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.5", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	leftConn := addZoneMemberVolumeDevice(app, "192.0.2.10", "LEFT", "Living Room", "SoundTouch 10", left, 30, group, nil)
	rightConn := addZoneMemberVolumeDevice(app, "192.0.2.11", "RIGHT", "Living Room", "SoundTouch 10", right, 30, group, nil)
	rightConn.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Balance = &models.Balance{TargetBalance: 0, ActualBalance: 0}
	})

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.5", "192.0.2.11", 46))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.Partial || payload.Data.ControlID != "192.0.2.11" || len(payload.Data.Members) != 1 {
		t.Fatalf("response = %+v; body=%s", payload.Data, response.Body.String())
	}
	memberResult := payload.Data.Members[0]
	if memberResult.DeviceID != "RIGHT" || memberResult.Actual == nil || *memberResult.Actual != 46 || memberResult.Error != "" {
		t.Fatalf("logical readback = %+v", memberResult)
	}
	if _, posts, gets := right.values(); fmt.Sprint(posts) != "[46]" || gets != 1 {
		t.Fatalf("RIGHT pair master operations = posts %v, gets %d", posts, gets)
	}
	if _, posts, gets := left.values(); len(posts) != 0 || gets != 0 {
		t.Fatalf("LEFT pair member operations = posts %v, gets %d", posts, gets)
	}
	if rightConn.Status().Volume.ActualVolume != 46 || leftConn.Status().Volume.ActualVolume != 30 {
		t.Fatalf("pair cache was not refreshed: RIGHT=%d LEFT=%d",
			rightConn.Status().Volume.ActualVolume,
			leftConn.Status().Volume.ActualVolume,
		)
	}
}

func TestHandleZoneMemberVolumeReportsReadbackMismatch(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	member := newZoneMemberVolumeSpeaker(t, 31, "")
	member.ignoreWrites = true

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	addZoneMemberVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", "SoundTouch 20", member, 31, nil, nil)

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.10", "MEMBER", 44))

	var payload struct {
		Data zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial || len(payload.Data.Members) != 1 ||
		payload.Data.Members[0].Actual == nil || *payload.Data.Members[0].Actual != 31 ||
		!strings.Contains(payload.Data.Members[0].Error, "does not both match requested") {
		t.Fatalf("mismatch response: status=%d data=%+v", response.Code, payload.Data)
	}
}

func TestHandleZoneMemberVolumeRejectsInvalidNonmemberAndTopologyChange(t *testing.T) {
	initialZone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}

	tests := []struct {
		name       string
		memberID   string
		volume     int
		zoneXML    string
		wantStatus int
	}{
		{
			name:       "invalid volume",
			memberID:   "192.0.2.20",
			volume:     101,
			zoneXML:    `<zone master="MASTER"><member>MASTER</member><member>MEMBER</member></zone>`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "nonmember",
			memberID:   "192.0.2.99",
			volume:     40,
			zoneXML:    `<zone master="MASTER"><member>MASTER</member><member>MEMBER</member></zone>`,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "topology changed",
			memberID:   "192.0.2.20",
			volume:     40,
			zoneXML:    `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.30">OTHER</member></zone>`,
			wantStatus: http.StatusConflict,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			master := newZoneMemberVolumeSpeaker(t, 20, test.zoneXML)
			member := newZoneMemberVolumeSpeaker(t, 30, "")
			app := NewWebApp()
			addZoneMemberVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, initialZone)
			addZoneMemberVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", "SoundTouch 20", member, 30, nil, nil)

			response := httptest.NewRecorder()
			app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.10", test.memberID, test.volume))

			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.wantStatus, response.Body.String())
			}
			if _, posts, gets := member.values(); len(posts) != 0 || gets != 0 {
				t.Fatalf("rejected request mutated member: posts %v, gets %d", posts, gets)
			}
			if strings.Contains(response.Body.String(), `"success":true`) {
				t.Fatalf("rejection reported success: %s", response.Body.String())
			}
		})
	}
}

func TestHandleZoneMemberVolumeReportsPartialReadback(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	member := newZoneMemberVolumeSpeaker(t, 31, "")
	member.failReadback = true

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	memberConn := addZoneMemberVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", "SoundTouch 20", member, 31, nil, nil)

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.10", "MEMBER", 44))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Success bool                   `json:"success"`
		Data    zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Success || !payload.Data.Partial || len(payload.Data.Members) != 1 ||
		payload.Data.Members[0].Actual != nil || !strings.Contains(payload.Data.Members[0].Error, "readback volume") {
		t.Fatalf("partial response = %+v", payload)
	}
	if volume, posts, gets := member.values(); volume != 44 || fmt.Sprint(posts) != "[44]" || gets != 1 {
		t.Fatalf("member operations = volume %d, posts %v, gets %d", volume, posts, gets)
	}
	if actual := memberConn.Status().Volume.ActualVolume; actual != 31 {
		t.Fatalf("failed readback overwrote cache with %d", actual)
	}
}

func TestHandleZoneMemberVolumeRefusesRegisteredStereoSlaveWithoutMaster(t *testing.T) {
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
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	slave := newZoneMemberVolumeSpeaker(t, 30, "")

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	addZoneMemberVolumeDevice(app, "192.0.2.20", "SLAVE", "Living right", "SoundTouch 10", slave, 30, group, nil)

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.10", "SLAVE", 44))

	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "Stereo pair master is unavailable") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if _, posts, gets := slave.values(); len(posts) != 0 || gets != 0 {
		t.Fatalf("registered stereo slave was touched: posts=%v gets=%d", posts, gets)
	}
}

func TestHandleZoneMemberVolumeRejectsStereoMasterChangeDuringWriteReadback(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.5">MASTER</member><member ipaddress="192.0.2.10">LEFT</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.5"},
		{DeviceID: "LEFT", IP: "192.0.2.10"},
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
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	left := newZoneMemberVolumeSpeaker(t, 30, "")
	right := newZoneMemberVolumeSpeaker(t, 30, "")

	app := NewWebApp()
	addZoneMemberVolumeDevice(app, "192.0.2.5", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	leftConn := addZoneMemberVolumeDevice(app, "192.0.2.10", "LEFT", "Living Room", "SoundTouch 10", left, 30, group, nil)
	addZoneMemberVolumeDevice(app, "192.0.2.11", "RIGHT", "Living Room", "SoundTouch 10", right, 30, group, nil)

	postFinished := make(chan struct{})
	groupChanged := make(chan struct{})
	left.afterPost = func() { close(postFinished) }
	left.onVolumeGet = func(request int) {
		if request == 1 {
			<-groupChanged
		}
	}
	go func() {
		<-postFinished
		leftConn.ApplyGroupEvent(&replacement, time.Now())
		close(groupChanged)
	}()

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.5", "192.0.2.10", 46))

	var payload struct {
		Data zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial || len(payload.Data.Members) != 1 ||
		payload.Data.Members[0].Actual != nil ||
		!strings.Contains(payload.Data.Members[0].Error, "topology") {
		t.Fatalf("master change was confirmed: status=%d data=%+v", response.Code, payload.Data)
	}
	if volume, posts, gets := left.values(); volume != 46 || fmt.Sprint(posts) != "[46]" || gets != 1 {
		t.Fatalf("speaker operations = volume %d, posts %v, gets %d", volume, posts, gets)
	}
	if cached := leftConn.Status().Volume.ActualVolume; cached != 30 {
		t.Fatalf("topology-invalid readback overwrote cache with %d", cached)
	}
}

func TestHandleZoneMemberVolumeRejectsZoneChangeDuringReadback(t *testing.T) {
	zoneXML := `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member><member ipaddress="192.0.2.20">MEMBER</member></zone>`
	zone := &models.ZoneInfo{Master: "MASTER", Members: []models.Member{
		{DeviceID: "MASTER", IP: "192.0.2.10"},
		{DeviceID: "MEMBER", IP: "192.0.2.20"},
	}}
	master := newZoneMemberVolumeSpeaker(t, 20, zoneXML)
	member := newZoneMemberVolumeSpeaker(t, 30, "")

	app := NewWebApp()
	masterConn := addZoneMemberVolumeDevice(app, "192.0.2.10", "MASTER", "Kitchen", "SoundTouch 30", master, 20, nil, zone)
	memberConn := addZoneMemberVolumeDevice(app, "192.0.2.20", "MEMBER", "Dining", "SoundTouch 20", member, 30, nil, nil)
	member.onVolumeGet = func(request int) {
		if request != 1 {
			return
		}
		generation := masterConn.BeginZoneRefresh()
		if !masterConn.ApplyPolledZone(generation, "MASTER", &models.ZoneInfo{}) {
			t.Error("failed to apply concurrent zone dissolution")
		}
	}

	response := httptest.NewRecorder()
	app.HandleZoneMemberVolume(response, zoneMemberVolumeRequest("192.0.2.10", "MEMBER", 46))

	var payload struct {
		Data zoneMemberVolumeResult `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Code != http.StatusOK || !payload.Data.Partial || len(payload.Data.Members) != 1 ||
		payload.Data.Members[0].Actual != nil ||
		!strings.Contains(payload.Data.Members[0].Error, "topology") {
		t.Fatalf("zone dissolution was confirmed: status=%d data=%+v", response.Code, payload.Data)
	}
	if cached := memberConn.Status().Volume.ActualVolume; cached != 30 {
		t.Fatalf("zone-invalid readback overwrote cache with %d", cached)
	}
}
