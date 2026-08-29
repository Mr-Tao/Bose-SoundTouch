// Package webtypes tests for the atomic Status API on DeviceConnection
// (Status, SetStatus, UpdateStatus, NewDeviceConnection).
package webtypes

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestNewDeviceConnection_InitialStatus(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})

	status := conn.Status()
	if status == nil {
		t.Fatal("Status() returned nil from a NewDeviceConnection")
	}

	if status.IsConnected {
		t.Error("IsConnected should default to false")
	}

	if status.LastActivity.IsZero() {
		t.Error("LastActivity should be initialised, got zero time")
	}
}

func TestSetStatus_ReplacesEntireStatus(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{
		Volume:      &models.Volume{ActualVolume: 42},
		IsConnected: true,
	})

	got := conn.Status()
	if got.Volume == nil || got.Volume.ActualVolume != 42 {
		t.Errorf("Volume not stored: got %+v", got.Volume)
	}

	// Setting a sparser status should wipe previously-set fields.
	conn.SetStatus(&DeviceStatus{IsConnected: false})

	got = conn.Status()
	if got.Volume != nil {
		t.Error("SetStatus did not wipe previously-set Volume")
	}

	if got.IsConnected {
		t.Error("SetStatus did not wipe IsConnected")
	}
}

func TestUpdateStatus_AppliesMutator(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})

	conn.UpdateStatus(func(s *DeviceStatus) {
		s.IsConnected = true
		s.Volume = &models.Volume{ActualVolume: 30}
	})

	got := conn.Status()
	if !got.IsConnected {
		t.Error("UpdateStatus did not set IsConnected")
	}

	if got.Volume == nil || got.Volume.ActualVolume != 30 {
		t.Errorf("UpdateStatus did not set Volume: %+v", got.Volume)
	}
}

func TestUpdateStatus_PreservesUnchangedFields(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{
		Volume:      &models.Volume{ActualVolume: 10},
		Bass:        &models.Bass{ActualBass: 3},
		Group:       &models.Group{ID: "pair-1", Name: "Living Room"},
		Zone:        &models.ZoneInfo{Master: "zone-master", Members: []models.Member{{DeviceID: "zone-member"}}},
		IsConnected: true,
	})

	// Only touch Volume; Bass and IsConnected must survive.
	conn.UpdateStatus(func(s *DeviceStatus) {
		s.Volume = &models.Volume{ActualVolume: 99}
	})

	got := conn.Status()
	if got.Volume.ActualVolume != 99 {
		t.Errorf("Volume = %d, want 99", got.Volume.ActualVolume)
	}

	if got.Bass == nil || got.Bass.ActualBass != 3 {
		t.Errorf("Bass not preserved: %+v", got.Bass)
	}

	if got.Group == nil || got.Group.ID != "pair-1" {
		t.Errorf("Group not preserved: %+v", got.Group)
	}

	if got.Zone == nil || got.Zone.Master != "zone-master" {
		t.Errorf("Zone not preserved: %+v", got.Zone)
	}

	if !got.IsConnected {
		t.Error("IsConnected not preserved")
	}
}

func TestDeviceStatusGroupJSON(t *testing.T) {
	status := DeviceStatus{
		Group: &models.Group{
			ID:             "pair-1",
			Name:           "Living Room",
			MasterDeviceID: "master-1",
		},
	}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal DeviceStatus: %v", err)
	}

	var decoded struct {
		Group *models.Group `json:"group"`
	}

	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal DeviceStatus: %v", err)
	}

	if decoded.Group == nil || decoded.Group.ID != "pair-1" || decoded.Group.MasterDeviceID != "master-1" {
		t.Fatalf("group did not round-trip in status JSON: %+v", decoded.Group)
	}

	emptyPayload, err := json.Marshal(DeviceStatus{})
	if err != nil {
		t.Fatalf("Marshal empty DeviceStatus: %v", err)
	}

	var emptyDecoded map[string]json.RawMessage
	if err := json.Unmarshal(emptyPayload, &emptyDecoded); err != nil {
		t.Fatalf("Unmarshal empty DeviceStatus: %v", err)
	}

	if _, ok := emptyDecoded["group"]; ok {
		t.Errorf("nil group should be omitted, JSON = %s", emptyPayload)
	}
}

func TestGroupEventSupersedesInFlightPoll(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	generation := conn.BeginGroupRefresh()

	eventGroup := &models.Group{ID: "new-pair", MasterDeviceID: "master"}
	if !conn.ApplyGroupEvent(eventGroup, time.Now()) {
		t.Fatal("new group event should change group state")
	}

	if conn.ApplyPolledGroup(generation, &models.Group{ID: "stale-pair"}) {
		t.Fatal("stale poll must not replace a newer group event")
	}

	if got := conn.Status().Group; got == nil || got.ID != "new-pair" {
		t.Fatalf("Group = %+v, want newer event state", got)
	}
}

func TestEmptyGroupClearsCurrentClaim(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{Group: &models.Group{ID: "pair-1"}})

	if !conn.ApplyGroupEvent(&models.Group{}, time.Now()) {
		t.Fatal("empty teardown event should change group state")
	}

	if got := conn.Status().Group; got != nil {
		t.Fatalf("Group = %+v, want nil after teardown", got)
	}
}

func TestPolledVolumePreservesUnrelatedSpeakerEvent(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	generation := conn.BeginVolumeRefresh()
	conn.ApplySpeakerEvent(func(status *DeviceStatus) {
		status.NowPlaying = &models.NowPlaying{Source: "SPOTIFY"}
	})

	if !conn.ApplyPolledVolume(generation, &models.Volume{TargetVolume: 42, ActualVolume: 42}) {
		t.Fatal("unrelated speaker event invalidated volume readback")
	}
	status := conn.Status()
	if status.Volume == nil || status.Volume.ActualVolume != 42 ||
		status.NowPlaying == nil || status.NowPlaying.Source != "SPOTIFY" {
		t.Fatalf("merged status = %+v", status)
	}
}

func TestZoneCacheRequiresAuthoritativeMaster(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "master"})
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}

	refresh := conn.BeginZoneRefresh()
	if !conn.ApplyPolledZone(refresh, "MASTER", zone) {
		t.Fatal("master-confirmed zone was not stored")
	}
	topology, current := conn.SnapshotZoneTopology()
	if !current || topology.Zone == nil || !conn.ZoneTopologyCurrent(topology) {
		t.Fatalf("confirmed zone topology = %+v, current=%v", topology, current)
	}

	memberRefresh := conn.BeginZoneRefresh()
	if _, current := conn.SnapshotZoneTopology(); current {
		t.Fatal("zone remained writable during refresh")
	}
	if conn.ApplyPolledZone(memberRefresh, "MEMBER", zone) {
		t.Fatal("member response was accepted as authoritative")
	}
	if conn.ZoneTopologyCurrent(topology) {
		t.Fatal("older topology remained current after rejected generation")
	}
	if conn.Status().Zone == nil || conn.Status().Zone.Master != "MASTER" {
		t.Fatalf("member response cleared cached topology: %+v", conn.Status().Zone)
	}
}

func TestUnchangedTopologyRefreshConfirmsWithoutReportingChange(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{DeviceID: "MASTER"})
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}

	firstZone := conn.BeginZoneRefresh()
	if !conn.ApplyPolledZone(firstZone, "MASTER", zone) {
		t.Fatal("initial zone refresh did not report the topology change")
	}
	unchangedZone := conn.BeginZoneRefresh()
	if conn.ApplyPolledZone(unchangedZone, "MASTER", zone) {
		t.Fatal("unchanged zone refresh reported a dashboard-visible change")
	}
	if topology, current := conn.SnapshotZoneTopology(); !current || topology.Zone != zone {
		t.Fatalf("unchanged zone was not confirmed: topology=%+v current=%v", topology, current)
	}

	groupRefresh := conn.BeginGroupRefresh()
	if conn.ApplyPolledGroup(groupRefresh, nil) {
		t.Fatal("confirmed standalone group state reported a dashboard-visible change")
	}
	if topology, current := conn.SnapshotGroupTopology(); !current || topology.Group != nil {
		t.Fatalf("standalone group state was not confirmed: topology=%+v current=%v", topology, current)
	}
}

func TestZoneCacheRejectsStaleRefreshAndClearsOnMasterStandalone(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "master"})
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}

	initial := conn.BeginZoneRefresh()
	if !conn.ApplyPolledZone(initial, "MASTER", zone) {
		t.Fatal("initial zone was not stored")
	}

	stale := conn.BeginZoneRefresh()
	standalone := conn.BeginZoneRefresh()
	if conn.ApplyPolledZone(stale, "MASTER", &models.ZoneInfo{Master: "MASTER"}) {
		t.Fatal("stale standalone response was accepted")
	}
	if conn.Status().Zone == nil {
		t.Fatal("stale response cleared cached topology")
	}

	if !conn.ApplyPolledZone(standalone, "MASTER", &models.ZoneInfo{
		Master:  "MASTER",
		Members: []models.Member{{DeviceID: "MASTER", IP: "192.0.2.10"}},
	}) {
		t.Fatal("master-confirmed standalone response did not clear the zone")
	}
	if conn.Status().Zone != nil {
		t.Fatalf("standalone topology remained cached: %+v", conn.Status().Zone)
	}
}

func TestStatusPollCannotOverwriteNewerSpeakerEvent(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	poll := conn.BeginStatusPoll()

	conn.ApplySpeakerEvent(func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 99}
	})

	if conn.CompleteStatusPoll(poll, func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 42}
	}) {
		t.Fatal("poll that began before a speaker event was applied")
	}

	if got := conn.Status().Volume; got == nil || got.ActualVolume != 99 {
		t.Fatalf("speaker event was overwritten by older poll data: %+v", got)
	}
}

func TestNewerStatusPollSupersedesOlderPoll(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	older := conn.BeginStatusPoll()
	newer := conn.BeginStatusPoll()

	if !conn.CompleteStatusPoll(newer, func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 30}
	}) {
		t.Fatal("newer poll was not applied")
	}

	if conn.CompleteStatusPoll(older, func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 10}
	}) {
		t.Fatal("older poll completed after a newer poll was applied")
	}

	if got := conn.Status().Volume; got == nil || got.ActualVolume != 30 {
		t.Fatalf("newer poll state was overwritten: %+v", got)
	}
}

func TestDuplicateSpeakerEventStillInvalidatesOlderPoll(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{Volume: &models.Volume{ActualVolume: 25}})
	poll := conn.BeginStatusPoll()

	conn.ApplySpeakerEvent(func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 25}
		status.LastActivity = time.Now()
	})

	if conn.CompleteStatusPoll(poll, func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 10}
	}) {
		t.Fatal("poll that preceded duplicate speaker evidence was applied")
	}
}

func TestStatusSnapshotIsolation(t *testing.T) {
	// A snapshot returned by Status() must NOT change when a later
	// UpdateStatus replaces a pointer field. This proves the atomic
	// store gives readers a stable view (so long as the writer
	// follows the docstring contract of replacing nested pointers).
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{Volume: &models.Volume{ActualVolume: 1}})

	first := conn.Status()

	conn.UpdateStatus(func(s *DeviceStatus) {
		s.Volume = &models.Volume{ActualVolume: 2}
	})

	if first.Volume.ActualVolume != 1 {
		t.Errorf("Snapshot mutated after later UpdateStatus: got %d, want 1",
			first.Volume.ActualVolume)
	}

	if conn.Status().Volume.ActualVolume != 2 {
		t.Errorf("Current status not updated: got %d, want 2",
			conn.Status().Volume.ActualVolume)
	}
}

// TestStatusConcurrent runs many UpdateStatus writers alongside many
// Status() readers. Before atomic.Pointer[DeviceStatus] this pattern
// would be flagged by the race detector (writers mutate
// conn.Status.X while readers copy conn.Status). With the atomic
// pointer it must run clean under `go test -race`.
func TestStatusConcurrent(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "concurrent"})

	const writers = 16

	const readersPerKind = 16

	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(writers + 2*readersPerKind)

	// Writers: each goroutine replaces NowPlaying with a fresh struct
	// carrying its worker id. Replacement (not in-place mutation)
	// is what the UpdateStatus contract requires for nested
	// pointers.
	for w := 0; w < writers; w++ {
		go func(worker int) {
			defer wg.Done()

			for i := 0; i < opsPerGoroutine; i++ {
				conn.UpdateStatus(func(s *DeviceStatus) {
					s.NowPlaying = &models.NowPlaying{
						Track: fmt.Sprintf("w%d-%d", worker, i),
					}
					s.IsConnected = true
				})
			}
		}(w)
	}

	// Readers via Status() — full snapshot.
	for r := 0; r < readersPerKind; r++ {
		go func() {
			defer wg.Done()

			for i := 0; i < opsPerGoroutine; i++ {
				_ = conn.Status()
			}
		}()
	}

	// Readers that deref a single field. Tests the common
	// "device.Status().IsConnected" pattern.
	for r := 0; r < readersPerKind; r++ {
		go func() {
			defer wg.Done()

			for i := 0; i < opsPerGoroutine; i++ {
				_ = conn.Status().IsConnected
			}
		}()
	}

	wg.Wait()

	// After all writers finish, IsConnected should be true (every
	// writer sets it). The exact NowPlaying value is whichever
	// writer landed last, but it must be a valid non-nil pointer.
	final := conn.Status()
	if !final.IsConnected {
		t.Error("IsConnected should be true after writers ran")
	}

	if final.NowPlaying == nil {
		t.Error("NowPlaying should be non-nil after writers ran")
	}
}
