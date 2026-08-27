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

func projectionDeviceWithZone(
	host, deviceID, name string,
	connected bool,
	volume int,
	group *models.Group,
	zone *models.ZoneInfo,
) DeviceEntry {
	entry := projectionDevice(host, deviceID, name, connected, group)
	entry.Device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: volume, TargetVolume: volume}
		status.Zone = zone
	})

	return entry
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

func TestProjectDeviceEntriesCollapsesMasterConfirmedZone(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.10"},
			{DeviceID: "member-id", IP: "192.0.2.20"},
		},
	}

	got := projectDeviceEntries([]DeviceEntry{
		projectionDeviceWithZone("192.0.2.10", "master-id", "Kitchen", true, 20, nil, zone),
		projectionDeviceWithZone("192.0.2.20", "member-id", "Dining", true, 35, nil, nil),
		projectionDeviceWithZone("192.0.2.30", "other-id", "Bedroom", true, 10, nil, nil),
	})

	if len(got) != 2 {
		t.Fatalf("projected devices = %d, want zone plus standalone: %+v", len(got), got)
	}

	master, ok := got["192.0.2.10"]
	if !ok || master.Zone == nil {
		t.Fatalf("logical zone master missing: %+v", got)
	}
	if master.Zone.MemberCount != 2 || master.Zone.AvailableMemberCount != 2 || master.Zone.Degraded {
		t.Fatalf("unexpected zone availability: %+v", master.Zone)
	}
	if master.Zone.Volume == nil || *master.Zone.Volume != 35 {
		t.Fatalf("zone volume = %v, want highest member volume 35", master.Zone.Volume)
	}
	if _, exists := got["192.0.2.20"]; exists {
		t.Fatal("zone member remained a separate control target")
	}
}

func TestProjectDeviceEntriesPreservesZoneWhenMemberIsMissing(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.10"},
			{DeviceID: "missing-id", IP: "192.0.2.99"},
		},
	}

	got := projectDeviceEntries([]DeviceEntry{
		projectionDeviceWithZone("192.0.2.10", "master-id", "Kitchen", true, 20, nil, zone),
	})

	view := got["192.0.2.10"].Zone
	if view == nil || !view.Degraded || view.MemberCount != 2 || view.AvailableMemberCount != 1 {
		t.Fatalf("missing member did not produce a stable degraded zone: %+v", got)
	}
}

func TestProjectDeviceEntriesFoldsStereoBeforeZone(t *testing.T) {
	group := testStereoGroup()
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.5"},
			{DeviceID: "left-id", IP: "192.0.2.10"},
			{DeviceID: "right-id", IP: "192.0.2.11"},
		},
	}

	got := projectDeviceEntries([]DeviceEntry{
		projectionDeviceWithZone("192.0.2.5", "master-id", "Kitchen", true, 25, nil, zone),
		projectionDeviceWithZone("192.0.2.10", "left-id", "Living Room", true, 12, group, nil),
		projectionDeviceWithZone("192.0.2.11", "right-id", "Living Room", true, 12, group, nil),
	})

	if len(got) != 1 {
		t.Fatalf("zone with stereo member produced %d cards, want one: %+v", len(got), got)
	}
	view := got["192.0.2.5"].Zone
	if view == nil || view.MemberCount != 2 || len(view.Members) != 2 {
		t.Fatalf("stereo members were not folded before zone projection: %+v", view)
	}
	if len(view.Members[1].DeviceIDs) != 2 {
		t.Fatalf("stereo logical member does not retain both physical IDs: %+v", view.Members[1])
	}
}

func TestProjectDeviceEntriesRequiresZoneMasterConfirmation(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.10"},
			{DeviceID: "member-id", IP: "192.0.2.20"},
		},
	}

	got := projectDeviceEntries([]DeviceEntry{
		projectionDeviceWithZone("192.0.2.10", "master-id", "Kitchen", true, 20, nil, nil),
		projectionDeviceWithZone("192.0.2.20", "member-id", "Dining", true, 35, nil, zone),
	})

	if len(got) != 2 || got["192.0.2.10"].Zone != nil || got["192.0.2.20"].Zone != nil {
		t.Fatalf("member-only zone data collapsed physical targets: %+v", got)
	}
}

func TestProjectDeviceEntriesFailsOpenForConflictingZoneClaims(t *testing.T) {
	zoneA := &models.ZoneInfo{
		Master: "a-id",
		Members: []models.Member{
			{DeviceID: "a-id", IP: "192.0.2.10"},
			{DeviceID: "b-id", IP: "192.0.2.20"},
		},
	}
	zoneB := &models.ZoneInfo{
		Master: "b-id",
		Members: []models.Member{
			{DeviceID: "b-id", IP: "192.0.2.20"},
			{DeviceID: "c-id", IP: "192.0.2.30"},
		},
	}

	got := projectDeviceEntries([]DeviceEntry{
		projectionDeviceWithZone("192.0.2.10", "a-id", "A", true, 10, nil, zoneA),
		projectionDeviceWithZone("192.0.2.20", "b-id", "B", true, 20, nil, zoneB),
		projectionDeviceWithZone("192.0.2.30", "c-id", "C", true, 30, nil, nil),
	})

	if len(got) != 3 {
		t.Fatalf("conflicting cached zones hid a device: %+v", got)
	}
	for id, view := range got {
		if view.Zone != nil {
			t.Fatalf("conflicting zone was projected for %s: %+v", id, view.Zone)
		}
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
