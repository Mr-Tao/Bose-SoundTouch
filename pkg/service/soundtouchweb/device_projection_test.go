package soundtouchweb

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

func projectionDevice(host, deviceID, name string, connected bool, group *models.Group) DeviceEntry {
	conn := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID:  deviceID,
		Name:      name,
		IPAddress: host,
	})
	conn.SetStatus(&webtypes.DeviceStatus{IsConnected: connected, Group: group})

	return DeviceEntry{ID: host, Device: conn}
}

func testStereoGroup() *models.Group {
	return &models.Group{
		ID:             "pair-1",
		Name:           "Living Room + Living Room",
		MasterDeviceID: "left-id",
		Status:         "GROUP_OK",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "left-id", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "right-id", Role: "RIGHT", IPAddress: "192.0.2.11"},
		}},
	}
}

func TestProjectDeviceEntriesCollapsesStereoPairUnderMaster(t *testing.T) {
	group := testStereoGroup()
	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, group),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	})

	if len(got) != 1 {
		t.Fatalf("projected devices = %d, want one logical stereo target: %+v", len(got), got)
	}

	master, ok := got["192.0.2.10"]
	if !ok {
		t.Fatalf("master control target missing: %+v", got)
	}

	if master.StereoPair == nil {
		t.Fatal("master is missing stereo-pair metadata")
	}

	if master.StereoPair.MemberCount != 2 || master.StereoPair.AvailableMemberCount != 2 || master.StereoPair.Degraded {
		t.Errorf("unexpected pair availability: %+v", master.StereoPair)
	}

	if master.Info.Name != "Living Room" || master.StereoPair.Name != "Living Room" {
		t.Errorf("logical pair name was not projected consistently: %+v", master)
	}

	if _, ok := got["192.0.2.11"]; ok {
		t.Error("physical right member must not be a second control target")
	}
}

func TestProjectDeviceEntriesShowsDegradedPairWhenMemberIsMissing(t *testing.T) {
	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, testStereoGroup()),
	})

	pair := got["192.0.2.10"].StereoPair
	if pair == nil {
		t.Fatal("connected master should remain a logical pair when its member is unavailable")
	}

	if pair.AvailableMemberCount != 1 || !pair.Degraded {
		t.Errorf("missing member not reflected as degraded: %+v", pair)
	}
}

func TestProjectDeviceEntriesKeepsStablePairWhenMasterIsDisconnected(t *testing.T) {
	group := testStereoGroup()
	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", false, group),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	})

	if len(got) != 1 {
		t.Fatalf("projected devices = %d, want a stable logical pair while its master is registered", len(got))
	}

	pair := got["192.0.2.10"].StereoPair
	if pair == nil || !pair.Degraded || pair.AvailableMemberCount != 1 {
		t.Errorf("disconnected master should produce a degraded logical pair: %+v", got)
	}
}

func TestProjectDeviceEntriesLeavesMemberPhysicalWhenMasterIsAbsent(t *testing.T) {
	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, testStereoGroup()),
	})

	if len(got) != 1 || got["192.0.2.11"].StereoPair != nil {
		t.Fatalf("member without a registered master must remain a physical target: %+v", got)
	}
}

func TestProjectDeviceEntriesRequiresMasterReportedGroup(t *testing.T) {
	group := testStereoGroup()
	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, nil),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	})

	if len(got) != 2 {
		t.Fatalf("slave-only group data must not collapse the registry: %+v", got)
	}
}

func TestProjectDeviceEntriesRejectsMalformedGroup(t *testing.T) {
	group := testStereoGroup()
	group.Roles.Roles[1].DeviceID = group.Roles.Roles[0].DeviceID

	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, group),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	})

	if len(got) != 2 {
		t.Fatalf("malformed pair must not hide a physical device: %+v", got)
	}
}

func TestProjectDeviceEntriesRejectsConflictingMemberClaim(t *testing.T) {
	masterGroup := testStereoGroup()
	memberGroup := testStereoGroup()
	memberGroup.ID = "different-pair"

	got := projectDeviceEntries([]DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, masterGroup),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, memberGroup),
	})

	if len(got) != 2 {
		t.Fatalf("conflicting pair claims must fail open: %+v", got)
	}
}

func TestProjectCapturedDeviceEntriesUsesOneCoherentStatusPerDevice(t *testing.T) {
	group := testStereoGroup()
	entries := []DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, group),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	}
	captured := captureDeviceProjectionEntries(entries)

	entries[0].Device.ApplyGroupEvent(&models.Group{}, time.Now())
	entries[1].Device.ApplyGroupEvent(&models.Group{}, time.Now())

	got := projectCapturedDeviceEntries(captured)
	master := got["192.0.2.10"]
	if master.StereoPair == nil || master.Status == nil || master.Status.Group == nil || master.Status.Group.ID != "pair-1" {
		t.Fatalf("captured projection mixed newer connection state into its response: %+v", got)
	}

	if fresh := projectDeviceEntries(entries); len(fresh) != 2 {
		t.Fatalf("fresh projection did not observe the cleared group: %+v", fresh)
	}
}

func TestHandleAPIDevicesUsesLogicalStereoProjection(t *testing.T) {
	app := NewWebApp()
	group := testStereoGroup()
	for _, entry := range []DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, group),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	} {
		app.AddDevice(entry.ID, entry.Device)
	}

	response := httptest.NewRecorder()
	app.HandleAPIDevices(response, httptest.NewRequest("GET", "/api/control/devices", nil))

	var payload struct {
		Success bool                  `json:"success"`
		Data    map[string]deviceView `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatalf("decode devices response: %v", err)
	}

	if response.Code != http.StatusOK || !payload.Success || len(payload.Data) != 1 {
		t.Fatalf("unexpected devices response: status=%d payload=%+v", response.Code, payload)
	}

	if pair := payload.Data["192.0.2.10"].StereoPair; pair == nil || pair.ID != "pair-1" || pair.MemberCount != 2 {
		t.Fatalf("logical stereo metadata missing from devices API: %+v", payload.Data)
	}
}
