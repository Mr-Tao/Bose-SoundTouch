// Package webtypes tests for the atomic Status API on DeviceConnection
// (Status, SetStatus, UpdateStatus, NewDeviceConnection).
package webtypes

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	soundtouchclient "github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
)

func TestDeviceConnectionWebSocketCompatibilityField(t *testing.T) {
	apiClient := soundtouchclient.NewClientFromHost("192.0.2.10")
	webSocket := apiClient.NewWebSocketClient(nil)
	conn := &DeviceConnection{WebSocket: webSocket}

	if got := conn.CurrentWebSocket(); got != webSocket {
		t.Fatalf("CurrentWebSocket() = %p, want compatibility field value %p", got, webSocket)
	}

	conn.SetWebSocket(nil)
	if conn.WebSocket != nil || conn.CurrentWebSocket() != nil {
		t.Fatal("SetWebSocket(nil) did not clear the compatibility field")
	}
}

func TestNewDeviceConnection_InitialStatus(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})

	status := conn.Status()
	if status == nil {
		t.Fatal("Status() returned nil from a NewDeviceConnection")
	}

	if status.IsConnected {
		t.Error("IsConnected should default to false")
	}

	if status.Connectivity != ConnectivityOffline {
		t.Errorf("Connectivity = %q, want %q", status.Connectivity, ConnectivityOffline)
	}

	if status.LastActivity.IsZero() {
		t.Error("LastActivity should be initialised, got zero time")
	}
}

func TestHTTPPollConnectivityTransitions(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	started := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	conn.MarkHTTPSuccess(started)

	status := conn.Status()
	if status.Connectivity != ConnectivityOnline || !status.IsConnected {
		t.Fatalf("after success: connectivity=%q isConnected=%v", status.Connectivity, status.IsConnected)
	}

	firstFailure := conn.BeginHTTPPoll()
	conn.CompleteHTTPPoll(firstFailure, false, started.Add(30*time.Second), nil)

	status = conn.Status()
	if status.Connectivity != ConnectivityStale || !status.IsConnected {
		t.Fatalf("after first failure: connectivity=%q isConnected=%v", status.Connectivity, status.IsConnected)
	}

	if !status.LastActivity.Equal(started) {
		t.Fatalf("failed poll changed LastActivity: got %v, want %v", status.LastActivity, started)
	}

	secondFailure := conn.BeginHTTPPoll()
	conn.CompleteHTTPPoll(secondFailure, false, started.Add(60*time.Second), nil)

	status = conn.Status()
	if status.Connectivity != ConnectivityOffline || status.IsConnected {
		t.Fatalf("after sustained failure: connectivity=%q isConnected=%v", status.Connectivity, status.IsConnected)
	}
}

func TestHTTPPollStaysStaleBeforeGracePeriod(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	started := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	conn.MarkHTTPSuccess(started)

	firstFailure := conn.BeginHTTPPoll()
	conn.CompleteHTTPPoll(firstFailure, false, started.Add(30*time.Second), nil)
	secondFailure := conn.BeginHTTPPoll()
	conn.CompleteHTTPPoll(secondFailure, false, started.Add(60*time.Second-time.Nanosecond), nil)

	status := conn.Status()
	if status.Connectivity != ConnectivityStale || !status.IsConnected {
		t.Fatalf("connectivity=%q isConnected=%v before grace period", status.Connectivity, status.IsConnected)
	}

	recovery := conn.BeginHTTPPoll()
	conn.CompleteHTTPPoll(recovery, true, started.Add(61*time.Second), nil)
	status = conn.Status()
	if status.Connectivity != ConnectivityOnline || !status.HTTPReachable || !status.IsConnected {
		t.Fatalf("after recovery: connectivity=%q httpReachable=%v isConnected=%v",
			status.Connectivity, status.HTTPReachable, status.IsConnected)
	}
}

func TestHTTPPollOlderFailureCannotOverwriteNewerSuccess(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	started := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	conn.MarkHTTPSuccess(started)

	older := conn.BeginHTTPPoll()
	newer := conn.BeginHTTPPoll()
	accepted := conn.CompleteHTTPPoll(newer, true, started.Add(time.Second), func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 42}
	})
	if !accepted {
		t.Fatal("newer successful poll was unexpectedly discarded")
	}

	accepted = conn.CompleteHTTPPoll(older, false, started.Add(2*time.Second), nil)
	if accepted {
		t.Fatal("older failed poll was unexpectedly accepted")
	}

	status := conn.Status()
	if status.Connectivity != ConnectivityOnline || !status.IsConnected {
		t.Fatalf("connectivity=%q isConnected=%v, want online", status.Connectivity, status.IsConnected)
	}

	if status.Volume == nil || status.Volume.ActualVolume != 42 {
		t.Fatalf("newer status payload was lost: %+v", status.Volume)
	}

	if conn.CompleteHTTPPoll(newer, false, started.Add(3*time.Second), nil) {
		t.Fatal("duplicate poll completion was unexpectedly accepted")
	}
}

func TestHTTPPollCannotOverwriteNewerSpeakerEvent(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	started := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	conn.MarkHTTPSuccess(started)

	poll := conn.BeginHTTPPoll()
	conn.ApplySpeakerEvent(func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 99}
	})
	conn.CompleteHTTPPoll(poll, true, started.Add(time.Second), func(status *DeviceStatus) {
		status.Volume = &models.Volume{ActualVolume: 42}
	})

	status := conn.Status()
	if status.Volume == nil || status.Volume.ActualVolume != 99 {
		t.Fatalf("speaker event was overwritten by older poll data: %+v", status.Volume)
	}

	if status.Connectivity != ConnectivityOnline || !status.IsConnected {
		t.Fatalf("poll health was not applied: connectivity=%q isConnected=%v",
			status.Connectivity, status.IsConnected)
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

	memberRefresh := conn.BeginZoneRefresh()
	if conn.ApplyPolledZone(memberRefresh, "MEMBER", zone) {
		t.Fatal("member response was accepted as authoritative")
	}
	if conn.Status().Zone == nil || conn.Status().Zone.Master != "MASTER" {
		t.Fatalf("member response cleared cached topology: %+v", conn.Status().Zone)
	}
}

func TestZoneCacheRejectsStaleRefreshAndClearsOnEmptyStandalone(t *testing.T) {
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
	if conn.ApplyPolledZone(stale, "MASTER", &models.ZoneInfo{Master: " \t "}) {
		t.Fatal("stale standalone response was accepted")
	}
	if conn.Status().Zone == nil {
		t.Fatal("stale response cleared cached topology")
	}

	if !conn.ApplyPolledZone(standalone, "MASTER", &models.ZoneInfo{}) {
		t.Fatal("empty standalone response did not clear the zone")
	}
	if conn.Status().Zone != nil {
		t.Fatalf("standalone topology remained cached: %+v", conn.Status().Zone)
	}
}

func TestZoneCacheRejectsMalformedMasterlessResponse(t *testing.T) {
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

	malformed := conn.BeginZoneRefresh()
	if conn.ApplyPolledZone(malformed, "MASTER", &models.ZoneInfo{
		Members: []models.Member{{DeviceID: "MEMBER", IP: "192.0.2.20"}},
	}) {
		t.Fatal("masterless response with members was accepted")
	}
	if conn.Status().Zone == nil || conn.Status().Zone.Master != "MASTER" {
		t.Fatalf("malformed response cleared cached topology: %+v", conn.Status().Zone)
	}
}

func TestConnectivityJSONCompatibility(t *testing.T) {
	for _, test := range []struct {
		connectivity Connectivity
		connected    bool
	}{
		{connectivity: ConnectivityOnline, connected: true},
		{connectivity: ConnectivityStale, connected: true},
		{connectivity: ConnectivityOffline, connected: false},
	} {
		payload, err := json.Marshal(DeviceStatus{
			Connectivity: test.connectivity,
			IsConnected:  test.connected,
		})
		if err != nil {
			t.Fatalf("marshal %q: %v", test.connectivity, err)
		}

		var decoded map[string]interface{}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatalf("unmarshal %q: %v", test.connectivity, err)
		}

		if decoded["connectivity"] != string(test.connectivity) {
			t.Errorf("connectivity = %v, want %q", decoded["connectivity"], test.connectivity)
		}

		if decoded["isConnected"] != test.connected {
			t.Errorf("isConnected = %v, want %v", decoded["isConnected"], test.connected)
		}
	}
}

func TestWebSocketLoopHasSingleOwner(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})

	const contenders = 64

	var owners int

	var mu sync.Mutex

	var wg sync.WaitGroup
	wg.Add(contenders)

	for range contenders {
		go func() {
			defer wg.Done()
			if !conn.TryStartWebSocketLoop() {
				return
			}

			mu.Lock()
			owners++
			mu.Unlock()
		}()
	}

	wg.Wait()

	if owners != 1 {
		t.Fatalf("WebSocket loop owners = %d, want 1", owners)
	}

	conn.FinishWebSocketLoop()
	if !conn.TryStartWebSocketLoop() {
		t.Fatal("loop ownership was not released")
	}
}

func TestDeviceConnectionInfoReflectsUpdatedName(t *testing.T) {
	discovered := &models.DeviceInfo{Name: "Living Room", DeviceID: "DEVICE01"}
	conn := NewDeviceConnection(nil, discovered)

	conn.ApplyNameEvent("Living Room Left")
	info := conn.Info()

	if info == nil || info.Name != "Living Room Left" || info.DeviceID != "DEVICE01" {
		t.Fatalf("Info() = %+v, want updated name with original metadata", info)
	}

	if discovered.Name != "Living Room" {
		t.Fatalf("discovery snapshot was mutated: %+v", discovered)
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
		Volume: &models.Volume{ActualVolume: 10},
		Bass:   &models.Bass{ActualBass: 3},
		BassCapabilities: &models.BassCapabilities{
			BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
		},
		Group:       &models.Group{ID: "pair-1", Name: "Living Room"},
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

	if got.BassCapabilities == nil || got.BassCapabilities.BassMax != 0 {
		t.Errorf("BassCapabilities not preserved: %+v", got.BassCapabilities)
	}

	if got.Group == nil || got.Group.ID != "pair-1" {
		t.Errorf("Group not preserved: %+v", got.Group)
	}

	if !got.IsConnected {
		t.Error("IsConnected not preserved")
	}
}

func TestEnsureBassCapabilitiesSharesSuccessfulFetch(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	capabilities := &models.BassCapabilities{
		BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
	}

	var fetches atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	fetch := func() (*models.BassCapabilities, error) {
		if fetches.Add(1) == 1 {
			close(started)
		}
		<-release
		return capabilities, nil
	}

	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			outcome, err := conn.EnsureBassCapabilities(fetch)
			if err != nil {
				t.Errorf("EnsureBassCapabilities: %v", err)
			}
			if outcome != BassCapabilitiesFetched && outcome != BassCapabilitiesCacheHit {
				t.Errorf("EnsureBassCapabilities outcome = %v", outcome)
			}
		}()
	}

	<-started
	close(release)
	wg.Wait()

	if got := fetches.Load(); got != 1 {
		t.Fatalf("capability fetches = %d, want 1", got)
	}
	if got := conn.Status().BassCapabilities; got != capabilities {
		t.Fatalf("stored capabilities = %p, want %p", got, capabilities)
	}
}

func TestEnsureBassCapabilitiesFailedFlightSurvivesNextRetryOvertake(t *testing.T) {
	previousProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(previousProcs)

	type result struct {
		outcome BassCapabilitiesFetchOutcome
		err     error
	}

	const (
		rounds  = 50
		waiters = 8
	)
	for round := range rounds {
		conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
		fetchErr := fmt.Errorf("first flight failed: %d", round)
		waiterResults := make(chan result, waiters)
		var waiterFetches atomic.Int32
		ownerOutcome, ownerErr := conn.EnsureBassCapabilities(func() (*models.BassCapabilities, error) {
			for range waiters {
				waiterStarted := make(chan struct{})
				go func() {
					close(waiterStarted)
					outcome, err := conn.EnsureBassCapabilities(func() (*models.BassCapabilities, error) {
						waiterFetches.Add(1)
						return nil, errors.New("waiter incorrectly started a fetch")
					})
					waiterResults <- result{outcome: outcome, err: err}
				}()
				<-waiterStarted
				for range 4 {
					runtime.Gosched()
				}
			}

			return nil, fetchErr
		})
		if ownerOutcome != BassCapabilitiesFetchFailed || !errors.Is(ownerErr, fetchErr) {
			t.Fatalf("round %d owner result = (%v, %v), want first-flight failure", round, ownerOutcome, ownerErr)
		}
		if conn.Status().BassCapabilities != nil {
			t.Fatalf("round %d failed flight cached capabilities", round)
		}

		capabilities := &models.BassCapabilities{
			BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
		}
		retryOutcome, retryErr := conn.EnsureBassCapabilities(func() (*models.BassCapabilities, error) {
			return capabilities, nil
		})
		if retryErr != nil || retryOutcome != BassCapabilitiesFetched {
			t.Fatalf("round %d retry = (%v, %v), want fetched success", round, retryOutcome, retryErr)
		}

		for range waiters {
			waiter := <-waiterResults
			if waiter.outcome != BassCapabilitiesFetchFailed || !errors.Is(waiter.err, fetchErr) {
				t.Fatalf("round %d waiter result = (%v, %v), want immutable first-flight failure",
					round, waiter.outcome, waiter.err)
			}
		}
		if got := waiterFetches.Load(); got != 0 {
			t.Fatalf("round %d waiter fetches = %d, want 0", round, got)
		}
		if got := conn.Status().BassCapabilities; got != capabilities {
			t.Fatalf("round %d cached capabilities = %p, want retry result %p", round, got, capabilities)
		}
	}
}

func TestApplyPolledVolumeSurvivesUnrelatedSpeakerEvent(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{
		Volume:     &models.Volume{ActualVolume: 10},
		NowPlaying: &models.NowPlaying{Source: "INITIAL"},
	})

	generation := conn.BeginVolumeRefresh()
	conn.ApplySpeakerEvent(func(status *DeviceStatus) {
		status.NowPlaying = &models.NowPlaying{Source: "RADIO"}
	})

	if !conn.ApplyPolledVolume(generation, &models.Volume{ActualVolume: 40}) {
		t.Fatal("unrelated speaker event invalidated volume readback")
	}
	if got := conn.Status(); got.Volume == nil || got.Volume.ActualVolume != 40 ||
		got.NowPlaying == nil || got.NowPlaying.Source != "RADIO" {
		t.Fatalf("status = %+v, want volume readback and speaker event", got)
	}
}

func TestApplyVolumeEventSupersedesPolledVolume(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	generation := conn.BeginVolumeRefresh()
	conn.ApplyVolumeEvent(&models.Volume{ActualVolume: 55}, time.Now())

	if conn.ApplyPolledVolume(generation, &models.Volume{ActualVolume: 40}) {
		t.Fatal("older volume readback superseded volume event")
	}
	if got := conn.Status().Volume; got == nil || got.ActualVolume != 55 {
		t.Fatalf("volume = %+v, want event value 55", got)
	}
}

func TestNewerBalanceReadbackSupersedesOlderPoll(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{Group: &models.Group{ID: "pair-1"}})
	staleRefresh, ok := conn.BeginBalanceRefresh()
	if !ok {
		t.Fatal("initial balance refresh was rejected")
	}
	newRefresh, ok := conn.BeginBalanceRefresh()
	if !ok {
		t.Fatal("new balance refresh was rejected")
	}

	if !conn.ApplyBalanceReadback(newRefresh, &models.Balance{TargetBalance: 6, ActualBalance: 6}) {
		t.Fatal("newest balance readback was rejected")
	}
	if conn.ApplyBalanceReadback(staleRefresh, &models.Balance{TargetBalance: -6, ActualBalance: -6}) {
		t.Fatal("stale balance readback replaced a newer result")
	}
	if got := conn.Status().Balance; got == nil || got.ActualBalance != 6 {
		t.Fatalf("balance = %+v, want newest readback 6", got)
	}
}

func TestBalanceReadbackRejectedAcrossTeardownAndRepair(t *testing.T) {
	conn := NewDeviceConnection(nil, &models.DeviceInfo{Name: "test"})
	conn.SetStatus(&DeviceStatus{
		Group:   &models.Group{ID: "pair-1", MasterDeviceID: "left"},
		Balance: &models.Balance{TargetBalance: 2, ActualBalance: 2},
	})

	staleRefresh, ok := conn.BeginBalanceRefresh()
	if !ok {
		t.Fatal("initial balance refresh was rejected")
	}
	conn.ApplyGroupEvent(&models.Group{}, time.Now())
	conn.ApplyGroupEvent(&models.Group{ID: "pair-2", MasterDeviceID: "left"}, time.Now())

	if conn.ApplyBalanceReadback(staleRefresh, &models.Balance{TargetBalance: 5, ActualBalance: 5}) {
		t.Fatal("pre-teardown readback was applied to a replacement pair")
	}
	if got := conn.Status().Balance; got != nil {
		t.Fatalf("balance = %+v, want unknown after re-pair", got)
	}

	freshRefresh, ok := conn.BeginBalanceRefresh()
	if !ok {
		t.Fatal("replacement pair balance refresh was rejected")
	}
	if !conn.ApplyBalanceReadback(freshRefresh, &models.Balance{TargetBalance: -3, ActualBalance: -3}) {
		t.Fatal("replacement pair readback was rejected")
	}
}

func TestDeviceStatusBalanceJSON(t *testing.T) {
	status := DeviceStatus{Balance: &models.Balance{
		BalanceAvailable: true,
		BalanceMin:       -12,
		BalanceMax:       9,
		BalanceDefault:   1,
		TargetBalance:    8,
		ActualBalance:    7,
		CapabilityKnown:  true,
	}}

	payload, err := json.Marshal(status)
	if err != nil {
		t.Fatalf("Marshal DeviceStatus: %v", err)
	}

	var decoded struct {
		Balance *models.Balance `json:"balance"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("Unmarshal DeviceStatus: %v", err)
	}
	if decoded.Balance == nil || !decoded.Balance.CapabilityKnown || !decoded.Balance.BalanceAvailable ||
		decoded.Balance.BalanceMin != -12 || decoded.Balance.BalanceMax != 9 ||
		decoded.Balance.BalanceDefault != 1 || decoded.Balance.TargetBalance != 8 ||
		decoded.Balance.ActualBalance != 7 {
		t.Fatalf("balance did not round-trip in status JSON: %+v", decoded.Balance)
	}

	emptyPayload, err := json.Marshal(DeviceStatus{})
	if err != nil {
		t.Fatalf("Marshal empty DeviceStatus: %v", err)
	}
	var emptyDecoded map[string]json.RawMessage
	if err := json.Unmarshal(emptyPayload, &emptyDecoded); err != nil {
		t.Fatalf("Unmarshal empty DeviceStatus: %v", err)
	}
	if _, ok := emptyDecoded["balance"]; ok {
		t.Errorf("nil balance should be omitted, JSON = %s", emptyPayload)
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
