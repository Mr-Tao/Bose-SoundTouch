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

	masterEntry := projectionDeviceWithZone("192.0.2.10", "master-id", "Kitchen", true, 20, nil, zone)
	memberEntry := projectionDeviceWithZone("192.0.2.20", "member-id", "Dining", true, 35, nil, nil)
	memberEntry.Device.DeviceInfo.Type = "SoundTouch 20"
	got := projectDeviceEntries([]DeviceEntry{
		masterEntry,
		memberEntry,
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
	if master.Zone.PhysicalMemberCount != 2 {
		t.Fatalf("physical member count = %d, want 2", master.Zone.PhysicalMemberCount)
	}
	ordinary := master.Zone.Members[1]
	if ordinary.Kind != "speaker" || ordinary.Type != "SoundTouch 20" || ordinary.Model != "SoundTouch 20" ||
		ordinary.ControlID != "192.0.2.20" ||
		len(ordinary.PhysicalMembers) != 1 || ordinary.PhysicalMembers[0].DeviceID != "member-id" ||
		ordinary.PhysicalMembers[0].IP != "192.0.2.20" || ordinary.PhysicalMembers[0].Name != "Dining" ||
		ordinary.PhysicalMembers[0].Type != "SoundTouch 20" || !ordinary.PhysicalMembers[0].Available ||
		ordinary.PhysicalMembers[0].Connectivity != webtypes.ConnectivityOnline {
		t.Fatalf("ordinary logical member metadata = %+v", ordinary)
	}
	if master.Zone.Volume == nil || *master.Zone.Volume != 35 {
		t.Fatalf("zone volume = %v, want highest member volume 35", master.Zone.Volume)
	}
	if _, exists := got["192.0.2.20"]; exists {
		t.Fatal("zone member remained a separate control target")
	}
}

func TestProjectDeviceEntriesSerializesCollapsedMemberStatus(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.10"},
			{DeviceID: "stale-id", IP: "192.0.2.20"},
			{DeviceID: "offline-id", IP: "192.0.2.30"},
		},
	}
	master := projectionDeviceWithZone("192.0.2.10", "master-id", "Kitchen", true, 20, nil, zone)
	stale := projectionDeviceWithZone("192.0.2.20", "stale-id", "Dining", true, 35, nil, nil)
	offline := projectionDeviceWithZone("192.0.2.30", "offline-id", "Patio", false, 0, nil, nil)
	master.Device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Connectivity = webtypes.ConnectivityOnline
	})
	stale.Device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Connectivity = webtypes.ConnectivityStale
	})
	offline.Device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Connectivity = webtypes.ConnectivityOffline
	})

	got := projectDeviceEntries([]DeviceEntry{master, stale, offline})
	if len(got) != 1 {
		t.Fatalf("projected devices = %d, want only the zone master: %+v", len(got), got)
	}

	encoded, err := json.Marshal(got["192.0.2.10"])
	if err != nil {
		t.Fatalf("marshal projected zone: %v", err)
	}
	var payload struct {
		Zone struct {
			AvailableMemberCount int  `json:"availableMemberCount"`
			Degraded             bool `json:"degraded"`
			Members              []struct {
				ControlID    string                `json:"controlId"`
				Connectivity webtypes.Connectivity `json:"connectivity"`
				ActualVolume *int                  `json:"actualVolume"`
			} `json:"members"`
		} `json:"zone"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal projected zone: %v", err)
	}
	if payload.Zone.AvailableMemberCount != 2 || !payload.Zone.Degraded || len(payload.Zone.Members) != 3 {
		t.Fatalf("unexpected zone status projection: %+v", payload.Zone)
	}
	if member := payload.Zone.Members[1]; member.ControlID != "192.0.2.20" ||
		member.Connectivity != webtypes.ConnectivityStale || member.ActualVolume == nil || *member.ActualVolume != 35 {
		t.Fatalf("stale member JSON = %+v", member)
	}
	if member := payload.Zone.Members[2]; member.ControlID != "192.0.2.30" ||
		member.Connectivity != webtypes.ConnectivityOffline || member.ActualVolume == nil || *member.ActualVolume != 0 {
		t.Fatalf("offline member JSON = %+v", member)
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
	if len(view.Members) != 2 || view.Members[1].ControlID != "192.0.2.99" ||
		view.Members[1].IP != "192.0.2.99" || view.Members[1].HardwareID != "missing-id" {
		t.Fatalf("missing member lost its topology identity: %+v", view.Members)
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

	master := projectionDeviceWithZone("192.0.2.5", "master-id", "Kitchen", true, 25, nil, zone)
	left := projectionDeviceWithZone("192.0.2.10", "left-id", "Living Room", true, 12, group, nil)
	right := projectionDeviceWithZone("192.0.2.11", "right-id", "Living Room", true, 12, group, nil)
	left.Device.DeviceInfo.Type = "SoundTouch 10"
	right.Device.DeviceInfo.Type = "SoundTouch 10"
	left.Device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.Balance = &models.Balance{TargetBalance: 2, ActualBalance: 1}
	})

	got := projectDeviceEntries([]DeviceEntry{master, left, right})

	if len(got) != 1 {
		t.Fatalf("zone with stereo member produced %d cards, want one: %+v", len(got), got)
	}
	view := got["192.0.2.5"].Zone
	if view == nil || view.MemberCount != 2 || view.PhysicalMemberCount != 3 || len(view.Members) != 2 {
		t.Fatalf("stereo members were not folded before zone projection: %+v", view)
	}
	pair := view.Members[1]
	if pair.Kind != "stereoPair" || pair.Type != "SoundTouch 10" || pair.Model != "SoundTouch 10" ||
		len(pair.DeviceIDs) != 2 || len(pair.PhysicalMembers) != 2 || pair.StereoPair == nil ||
		pair.Balance == nil || pair.Balance.ActualBalance != 1 || pair.ActualVolume == nil || *pair.ActualVolume != 12 {
		t.Fatalf("stereo logical member metadata = %+v", pair)
	}
	if physical := pair.PhysicalMembers[0]; physical.DeviceID != "left-id" || physical.Role != "LEFT" ||
		physical.IP != "192.0.2.10" || physical.Name != "Living Room" || physical.Type != "SoundTouch 10" ||
		!physical.Available || physical.Connectivity != webtypes.ConnectivityOnline {
		t.Fatalf("stereo physical member metadata = %+v", physical)
	}
}

func TestProjectZoneInfoExpandsStereoPairRepresentedByMasterOnly(t *testing.T) {
	group := testStereoGroup()
	group.Name = "Living Room"
	zone := &models.ZoneInfo{
		Master: "master-id",
		Members: []models.Member{
			{DeviceID: "master-id", IP: "192.0.2.5"},
			{DeviceID: "left-id", IP: "192.0.2.10"},
		},
	}

	entries := []DeviceEntry{
		projectionDeviceWithZone("192.0.2.5", "master-id", "Kitchen", true, 25, nil, zone),
		projectionDeviceWithZone("192.0.2.10", "left-id", "Living Room Left", true, 12, group, nil),
		projectionDeviceWithZone("192.0.2.11", "right-id", "Living Room Right", true, 18, group, nil),
	}
	view, ok := projectZoneInfo(zone, captureDeviceProjectionEntries(entries))
	if !ok || len(view.Members) != 2 {
		t.Fatalf("projected zone = %+v, ok=%v; want master plus one stereo member", view, ok)
	}

	pair := view.Members[1]
	if pair.Name != "Living Room" || pair.ControlID != "192.0.2.10" || pair.IP != "192.0.2.10" ||
		pair.HardwareID != "left-id" || len(pair.DeviceIDs) != 2 ||
		pair.DeviceIDs[0] != "left-id" || pair.DeviceIDs[1] != "right-id" ||
		pair.ActualVolume == nil || *pair.ActualVolume != 12 {
		t.Fatalf("stereo zone member = %+v", pair)
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

func TestLogicalStereoProjectionPreservesMasterBassCapabilities(t *testing.T) {
	group := testStereoGroup()
	master := projectionDevice("192.0.2.10", "left-id", "Living Room", true, group)
	master.Device.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.BassCapabilities = &models.BassCapabilities{
			BassAvailable: true,
			BassMin:       -9,
			BassMax:       0,
			BassDefault:   0,
		}
	})

	got := projectDeviceEntries([]DeviceEntry{
		master,
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
	})

	view := got["192.0.2.10"]
	if view.StereoPair == nil || view.Status == nil || view.Status.BassCapabilities == nil ||
		view.Status.BassCapabilities.BassMax != 0 {
		t.Fatalf("logical stereo projection lost master bass capabilities: %+v", view)
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
