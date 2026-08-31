package soundtouchweb

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
	"github.com/gorilla/websocket"
)

func TestUpdateDeviceStatusRefreshesGroup(t *testing.T) {
	server := newStatusTestServer(t, http.StatusOK, `<group id="pair-1">
		<name>Living Room</name>
		<masterDeviceId>master-1</masterDeviceId>
		<roles>
			<groupRole><deviceId>master-1</deviceId><role>LEFT</role></groupRole>
			<groupRole><deviceId>member-1</deviceId><role>RIGHT</role></groupRole>
		</roles>
		<status>GROUP_OK</status>
	</group>`)
	defer server.Close()

	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{
		DeviceID: "master-1",
		Type:     "SoundTouch 10",
	})
	NewWebApp().UpdateDeviceStatus("device-1", conn)

	status := conn.Status()
	if status.Group == nil {
		t.Fatal("Group was not populated by UpdateDeviceStatus")
	}

	if status.Group.ID != "pair-1" || status.Group.MasterDeviceID != "master-1" {
		t.Errorf("Group = %+v, want refreshed stereo pair", status.Group)
	}

	if len(status.Group.Roles.Roles) != 2 {
		t.Errorf("group roles = %d, want 2", len(status.Group.Roles.Roles))
	}

	if !status.IsConnected {
		t.Error("successful status refresh should mark the device connected")
	}
	if status.Zone == nil || status.Zone.Master != "master-1" || len(status.Zone.Members) != 1 {
		t.Fatalf("Zone = %+v, want refreshed multiroom topology", status.Zone)
	}
}

func TestUpdateDeviceStatusPreservesGroupOnError(t *testing.T) {
	server := newStatusTestServer(t, http.StatusInternalServerError, "group unavailable")
	defer server.Close()

	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{Type: "SoundTouch 10"})
	existing := &models.Group{ID: "pair-old", Name: "Existing Pair"}
	conn.SetStatus(&webtypes.DeviceStatus{Group: existing})

	NewWebApp().UpdateDeviceStatus("device-1", conn)

	status := conn.Status()
	if status.Group != existing {
		t.Errorf("Group = %+v, want previous group preserved on refresh error", status.Group)
	}

	if !status.IsConnected {
		t.Error("other successful status fetches should keep the device connected")
	}
}

func TestUpdateDeviceStatusSkipsGroupForNonStereoModel(t *testing.T) {
	var groupRequested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getGroup" {
			groupRequested.Store(true)
			http.Error(w, "unsupported endpoint", http.StatusInternalServerError)

			return
		}

		responses := map[string]string{
			"/now_playing": `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
			"/volume":      `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
			"/presets":     `<presets/>`,
			"/sources":     `<sources/>`,
			"/bass":        `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
			"/getZone":     `<zone master="device-1"/>`,
		}
		body, ok := responses[r.URL.Path]
		if !ok {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	for _, model := range []string{"SoundTouch 20", "SoundTouch 30"} {
		t.Run(model, func(t *testing.T) {
			conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{Type: model})
			NewWebApp().UpdateDeviceStatus("device-1", conn)

			if !conn.Status().IsConnected {
				t.Fatal("successful ordinary status requests should mark a non-stereo model connected")
			}
		})
	}

	if groupRequested.Load() {
		t.Fatal("UpdateDeviceStatus requested /getGroup for a non-stereo model")
	}
}

// TestUpdateDeviceStatusNotConnectedWhenOnlyGroupSucceeds covers a stereo-
// capable device where every substantive status fetch fails but /getGroup
// alone succeeds (a near-guaranteed reply -- even an empty <group/> is a
// success, see Client.GetGroup's doc comment). IsConnected must not be set
// from GetGroup's success alone, or a device with genuinely stale
// NowPlaying/Volume/Presets/Sources/Bass data would be reported connected.
func TestUpdateDeviceStatusNotConnectedWhenOnlyGroupSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getGroup" {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<group/>`))

			return
		}

		http.Error(w, "device struggling", http.StatusInternalServerError)
	}))
	defer server.Close()

	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{Type: "SoundTouch 10"})
	NewWebApp().UpdateDeviceStatus("device-1", conn)

	if conn.Status().IsConnected {
		t.Fatal("IsConnected must stay false when every substantive status fetch failed, even though GetGroup alone succeeded")
	}
}

func TestApplyGroupUpdatedEventReplacesGroup(t *testing.T) {
	conn := webtypes.NewDeviceConnection(nil, nil)
	previousActivity := time.Unix(1, 0)
	conn.SetStatus(&webtypes.DeviceStatus{
		Group:        &models.Group{ID: "pair-old"},
		Volume:       &models.Volume{ActualVolume: 25},
		IsConnected:  true,
		LastActivity: previousActivity,
	})

	event := &models.GroupUpdatedEvent{
		Group: models.Group{ID: "pair-new", Name: "Renamed Pair"},
	}
	applyGroupUpdatedEvent(conn, event)

	status := conn.Status()
	if status.Group != &event.Group || status.Group.ID != "pair-new" {
		t.Errorf("Group = %+v, want event group", status.Group)
	}

	if status.Volume == nil || status.Volume.ActualVolume != 25 || !status.IsConnected {
		t.Errorf("unrelated status fields were not preserved: %+v", status)
	}

	if !status.LastActivity.After(previousActivity) {
		t.Errorf("LastActivity = %s, want after %s", status.LastActivity, previousActivity)
	}

	teardown := &models.GroupUpdatedEvent{Group: models.Group{}}
	applyGroupUpdatedEvent(conn, teardown)

	if conn.Status().Group != nil {
		t.Errorf("teardown event did not clear the group: %+v", conn.Status().Group)
	}
}

func TestRefreshZonesAfterEventClearsCachedMasterClaim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/getZone" {
			http.NotFound(w, r)
			return
		}

		_, _ = w.Write([]byte(`<zone master="master-id"/>`))
	}))
	defer server.Close()

	app := NewWebApp()
	master := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{
		DeviceID: "master-id",
		Name:     "Kitchen",
	})
	master.SetStatus(&webtypes.DeviceStatus{Zone: &models.ZoneInfo{
		Master:  "master-id",
		Members: []models.Member{{DeviceID: "member-id", IP: "192.0.2.20"}},
	}})
	app.AddDevice("192.0.2.10", master)
	member := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "member-id",
		Name:     "Dining",
	})
	app.AddDevice("192.0.2.20", member)

	// Some firmware events omit deviceID. The callback must fall back to the
	// connection's hardware ID, not its IP-keyed registry entry.
	app.refreshZonesAfterEvent(zoneEventDeviceID("", member), "")

	if master.Status().Zone != nil {
		t.Fatalf("dissolved zone remained cached: %+v", master.Status().Zone)
	}
}

func TestUpdateDeviceStatusCannotOverwriteNewerVolumeReadback(t *testing.T) {
	presetsRequestStarted := make(chan struct{})
	releasePresetsResponse := make(chan struct{})
	responses := map[string]string{
		"/now_playing": `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		"/volume":      `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
		"/presets":     `<presets/>`,
		"/sources":     `<sources/>`,
		"/bass":        `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/getZone":     `<zone master="device-1"><member>device-1</member></zone>`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path == "/presets" {
			close(presetsRequestStarted)
			<-releasePresetsResponse
		}

		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected status endpoint %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{Name: "Living Room", DeviceID: "device-1", Type: "SoundTouch 20"},
	)
	refreshDone := make(chan struct{})
	go func() {
		NewWebApp().UpdateDeviceStatus("device-1", conn)
		close(refreshDone)
	}()

	select {
	case <-presetsRequestStarted:
	case <-time.After(time.Second):
		close(releasePresetsResponse)
		t.Fatal("status poll did not read volume before blocking")
	}

	newerReadback := conn.BeginVolumeRefresh()
	if !conn.ApplyPolledVolume(newerReadback, &models.Volume{ActualVolume: 40, TargetVolume: 40}) {
		t.Fatal("newer volume readback was rejected")
	}
	close(releasePresetsResponse)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("status refresh did not finish")
	}

	if got := conn.Status().Volume; got == nil || got.ActualVolume != 40 {
		t.Fatalf("volume = %+v, want newer readback 40", got)
	}
}

func TestReserveZoneRefreshesAfterEventFencesOlderPollBeforeIO(t *testing.T) {
	app := NewWebApp()
	master := webtypes.NewDeviceConnection(client.NewClientFromHost("http://127.0.0.1"), &models.DeviceInfo{
		DeviceID: "master-id",
		Name:     "Kitchen",
	})
	initialZone := &models.ZoneInfo{
		Master:  "master-id",
		Members: []models.Member{{DeviceID: "member-id", IP: "192.0.2.20"}},
	}
	master.SetStatus(&webtypes.DeviceStatus{Zone: initialZone})
	app.AddDevice("192.0.2.10", master)

	staleGeneration := master.BeginZoneRefresh()
	refreshes := app.reserveZoneRefreshesAfterEvent("member-id", "")
	if len(refreshes) != 1 || refreshes[0].connection != master {
		t.Fatalf("reserved refreshes = %+v, want the cached master", refreshes)
	}

	staleZone := &models.ZoneInfo{
		Master:  "master-id",
		Members: []models.Member{{DeviceID: "old-member", IP: "192.0.2.30"}},
	}
	if master.ApplyPolledZone(staleGeneration, "master-id", staleZone) {
		t.Fatal("pre-event /getZone response crossed the event reservation barrier")
	}
	if master.Status().Zone != initialZone {
		t.Fatalf("stale poll changed the cached topology: %+v", master.Status().Zone)
	}
}

func TestPeriodicPlayerMessagesPreserveStatusUpdateStream(t *testing.T) {
	app := NewWebApp()
	group := testStereoGroup()
	for _, entry := range []DeviceEntry{
		projectionDevice("192.0.2.10", "left-id", "Living Room", true, group),
		projectionDevice("192.0.2.11", "right-id", "Living Room", true, group),
		projectionDevice("192.0.2.12", "standalone-id", "Kitchen", false, nil),
	} {
		app.AddDevice(entry.ID, entry.Device)
	}

	messages := app.periodicPlayerMessages()
	if len(messages) != 3 {
		t.Fatalf("periodic messages = %d, want one devices frame and two connected status updates: %+v", len(messages), messages)
	}

	if messages[0].Type != "devices" {
		t.Fatalf("first periodic message type = %q, want devices", messages[0].Type)
	}
	devices, ok := messages[0].Data.(map[string]deviceView)
	if !ok || len(devices) != 2 || devices["192.0.2.10"].StereoPair == nil {
		t.Fatalf("periodic devices frame is not the logical projection: %#v", messages[0].Data)
	}

	statusUpdates := make(map[string]bool)
	for _, message := range messages[1:] {
		if message.Type != "status_update" {
			t.Fatalf("periodic message type = %q, want status_update", message.Type)
		}
		statusUpdates[message.DeviceID] = true
	}
	if !statusUpdates["192.0.2.10"] || !statusUpdates["192.0.2.11"] || statusUpdates["192.0.2.12"] {
		t.Fatalf("unexpected status_update device IDs: %+v", statusUpdates)
	}
}

func newStatusTestServer(t *testing.T, groupStatus int, groupBody string) *httptest.Server {
	t.Helper()

	responses := map[string]string{
		"/now_playing": `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		"/volume":      `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
		"/presets":     `<presets/>`,
		"/sources":     `<sources/>`,
		"/bass":        `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/getZone":     `<zone master="master-1"><member ipaddress="192.0.2.20">member-2</member></zone>`,
	}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method for %s = %s, want GET", r.URL.Path, r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/xml")

		if r.URL.Path == "/getGroup" {
			w.WriteHeader(groupStatus)
			_, _ = w.Write([]byte(groupBody))

			return
		}

		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected status endpoint %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)

			return
		}

		_, _ = w.Write([]byte(body))
	}))
}

type recordingWebSocketWriter struct {
	mu        sync.Mutex
	deadlines []time.Time
	messages  []interface{}
}

func (writer *recordingWebSocketWriter) SetWriteDeadline(deadline time.Time) error {
	writer.mu.Lock()
	writer.deadlines = append(writer.deadlines, deadline)
	writer.mu.Unlock()

	return nil
}

func (writer *recordingWebSocketWriter) WriteJSON(value interface{}) error {
	writer.mu.Lock()
	writer.messages = append(writer.messages, value)
	writer.mu.Unlock()

	return nil
}

func (*recordingWebSocketWriter) WriteMessage(int, []byte) error { return nil }

func (writer *recordingWebSocketWriter) lastDeadline() (time.Time, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.deadlines) == 0 {
		return time.Time{}, false
	}

	return writer.deadlines[len(writer.deadlines)-1], true
}

func (writer *recordingWebSocketWriter) messageSnapshot() []interface{} {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	return append([]interface{}(nil), writer.messages...)
}

type deadlineBlockingWebSocketWriter struct {
	mu       sync.Mutex
	deadline time.Time
	started  chan struct{}
	once     sync.Once
}

func (writer *deadlineBlockingWebSocketWriter) SetWriteDeadline(deadline time.Time) error {
	writer.mu.Lock()
	writer.deadline = deadline
	writer.mu.Unlock()

	return nil
}

func (writer *deadlineBlockingWebSocketWriter) WriteJSON(interface{}) error {
	writer.once.Do(func() { close(writer.started) })
	writer.mu.Lock()
	deadline := writer.deadline
	writer.mu.Unlock()

	if delay := time.Until(deadline); delay > 0 {
		time.Sleep(delay)
	}

	return errors.New("write deadline exceeded")
}

func (writer *deadlineBlockingWebSocketWriter) WriteMessage(int, []byte) error {
	return writer.WriteJSON(nil)
}

func TestGlobalWebSocketWriteSeamSerializesWriters(t *testing.T) {
	app := NewWebApp()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		_ = app.withGlobalWebSocketWrite(func(webSocketWriteBatch) error {
			close(entered)
			<-release

			return nil
		})
		close(done)
	}()

	<-entered
	if app.webSocketWriteMu.TryLock() {
		app.webSocketWriteMu.Unlock()
		t.Fatal("global WebSocket writer lock was not held across the write seam")
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("global WebSocket writer lock was not released")
	}
}

func TestDiscoveryStatusStateAndFramesShareWriteOrder(t *testing.T) {
	app := NewWebApp()
	writer := &recordingWebSocketWriter{}
	starting := &webtypes.DiscoveryStatus{Status: "starting", IsDiscovering: true}
	completed := &webtypes.DiscoveryStatus{Status: "completed", DeviceCount: 3}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)

	go func() {
		firstDone <- app.withDiscoveryStatusWrite(starting, func(
			batch webSocketWriteBatch,
			_ []*websocket.Conn,
		) error {
			close(firstEntered)
			<-releaseFirst

			return batch.writeJSON(writer, webtypes.WebSocketMessage{
				Type: "discovery_status",
				Data: starting,
			})
		})
	}()

	<-firstEntered
	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- app.withDiscoveryStatusWrite(completed, func(
			batch webSocketWriteBatch,
			_ []*websocket.Conn,
		) error {
			return batch.writeJSON(writer, webtypes.WebSocketMessage{
				Type: "discovery_status",
				Data: completed,
			})
		})
	}()
	<-secondStarted

	if stored := app.discoveryStatus.Load(); stored != starting {
		t.Fatalf("discovery state changed ahead of its serialized frame: %#v", stored)
	}
	select {
	case err := <-secondDone:
		t.Fatalf("second discovery publication bypassed the writer lock: %v", err)
	default:
	}

	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("publish starting discovery status: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("publish completed discovery status: %v", err)
	}

	if stored := app.discoveryStatus.Load(); stored != completed {
		t.Fatalf("final discovery state = %#v, want completed", stored)
	}
	messages := writer.messageSnapshot()
	if len(messages) != 2 {
		t.Fatalf("discovery frames = %d, want 2", len(messages))
	}
	last, ok := messages[1].(webtypes.WebSocketMessage)
	if !ok || last.Data != completed {
		t.Fatalf("last discovery frame = %#v, want completed", messages[1])
	}
}

func TestDiscoveryPublicationBeforeRegistrationUsesNewInitialState(t *testing.T) {
	app := NewWebApp()
	oldStatus := &webtypes.DiscoveryStatus{Status: "starting", IsDiscovering: true}
	newStatus := &webtypes.DiscoveryStatus{Status: "completed", DeviceCount: 3}
	app.discoveryStatus.Store(oldStatus)

	serverConnection := make(chan *websocket.Conn, 1)
	releaseServer := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := app.Upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade test WebSocket: %v", err)

			return
		}
		serverConnection <- conn
		<-releaseServer
		_ = conn.Close()
	}))

	client, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil,
	)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		close(releaseServer)
		server.Close()
		t.Fatalf("dial test WebSocket: %v", err)
	}
	remote := <-serverConnection
	t.Cleanup(func() {
		app.removeGlobalWebSocketClient(remote)
		_ = client.Close()
		close(releaseServer)
		server.Close()
	})

	clientsSelected := make(chan struct{})
	releasePublication := make(chan struct{})
	published := make(chan error, 1)
	go func() {
		published <- app.withDiscoveryStatusWrite(newStatus, func(
			_ webSocketWriteBatch,
			clients []*websocket.Conn,
		) error {
			if len(clients) != 0 {
				return errors.New("new client was registered before forced publication snapshot")
			}
			close(clientsSelected)
			<-releasePublication

			return nil
		})
	}()

	select {
	case <-clientsSelected:
	case <-time.After(time.Second):
		t.Fatal("discovery publication did not select clients")
	}

	registered := make(chan error, 1)
	go func() {
		registered <- app.registerGlobalWebSocket(remote)
	}()

	select {
	case err := <-registered:
		t.Fatalf("registration bypassed in-flight discovery publication: %v", err)
	default:
	}
	close(releasePublication)
	if err := <-published; err != nil {
		t.Fatalf("publish discovery status: %v", err)
	}

	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set test WebSocket deadline: %v", err)
	}
	var discoveryMessage webtypes.WebSocketMessage
	if err := client.ReadJSON(&discoveryMessage); err != nil {
		t.Fatalf("read initial discovery frame: %v", err)
	}
	if err := <-registered; err != nil {
		t.Fatalf("register global WebSocket: %v", err)
	}
	if discoveryMessage.Type != "discovery_status" {
		t.Fatalf("initial frame type = %q, want discovery_status", discoveryMessage.Type)
	}
	data, ok := discoveryMessage.Data.(map[string]interface{})
	if !ok || data["status"] != "completed" || data["deviceCount"] != float64(3) {
		t.Fatalf("initial discovery frame = %#v, want new completed state", discoveryMessage.Data)
	}
}

func TestWebSocketWriteBatchRefreshesDeadlineForHealthyWriter(t *testing.T) {
	timeout := 30 * time.Millisecond
	batch := webSocketWriteBatch{timeout: timeout}
	blocked := &deadlineBlockingWebSocketWriter{started: make(chan struct{})}

	if err := batch.writeJSON(blocked, struct{}{}); err == nil {
		t.Fatal("blocked writer unexpectedly succeeded")
	}

	healthy := &recordingWebSocketWriter{}
	started := time.Now()
	if err := batch.writeJSON(healthy, webtypes.WebSocketMessage{Type: "devices"}); err != nil {
		t.Fatalf("healthy writer inherited the failed client's deadline: %v", err)
	}

	deadline, ok := healthy.lastDeadline()
	if !ok || deadline.Before(started.Add(timeout/2)) {
		t.Fatalf("healthy writer deadline = %v, want a fresh deadline after %v", deadline, started)
	}
}
