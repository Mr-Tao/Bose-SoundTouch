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

	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), &models.DeviceInfo{Type: "SoundTouch 10"})
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

// dialTestWebSocket opens a real client/server WebSocket pair against app's
// Upgrader. remote is the server-side *websocket.Conn (the one production
// code operates on -- e.g. as a WSClients key); client is the dialer side,
// for tests that need to read the frames production code writes to remote.
// It does not register remote into app.WSClients; callers do that
// themselves so each test controls its own per-connection lock.
func dialTestWebSocket(t *testing.T, app *WebApp) (remote *websocket.Conn, client *websocket.Conn, cleanup func()) {
	t.Helper()

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

	remote = <-serverConnection

	return remote, client, func() {
		close(releaseServer)
		_ = client.Close()
		server.Close()
	}
}

func TestConnWriteSerializesWritesToSameConnection(t *testing.T) {
	app := NewWebApp()
	remote, _, cleanup := dialTestWebSocket(t, app)
	defer cleanup()

	connMu := &sync.Mutex{}
	app.WSMutex.Lock()
	app.WSClients[remote] = connMu
	app.WSMutex.Unlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	go func() {
		_ = app.withConnWrite(remote, func(webSocketWriteBatch) error {
			close(entered)
			<-release

			return nil
		})
		close(done)
	}()

	<-entered
	if connMu.TryLock() {
		connMu.Unlock()
		t.Fatal("connection writer lock was not held across the write seam")
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection writer lock was not released")
	}
}

// TestConnWriteDoesNotBlockUnrelatedConnection is the money test for the
// removed global write mutex: a write to one connection must never block a
// write to a different connection.
func TestConnWriteDoesNotBlockUnrelatedConnection(t *testing.T) {
	app := NewWebApp()
	remoteA, _, cleanupA := dialTestWebSocket(t, app)
	defer cleanupA()
	remoteB, _, cleanupB := dialTestWebSocket(t, app)
	defer cleanupB()

	app.WSMutex.Lock()
	app.WSClients[remoteA] = &sync.Mutex{}
	app.WSClients[remoteB] = &sync.Mutex{}
	app.WSMutex.Unlock()

	blockedA := make(chan struct{})
	releaseA := make(chan struct{})

	go func() {
		_ = app.withConnWrite(remoteA, func(webSocketWriteBatch) error {
			close(blockedA)
			<-releaseA

			return nil
		})
	}()
	<-blockedA

	done := make(chan error, 1)
	go func() {
		done <- app.withConnWrite(remoteB, func(webSocketWriteBatch) error {
			return nil
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write to unrelated connection failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("write to an unrelated connection blocked on a stalled connection's lock")
	}

	close(releaseA)
}

// TestBroadcastDeviceListSkipsStalledClientWithoutBlockingHealthyClient
// encodes the HandleDeleteDevice failure scenario from the code review:
// a stalled/backgrounded client must not delay a healthy client's OWN
// periodic update (which runs on a separate goroutine, contending only on
// that client's own connection lock) merely because BroadcastDeviceList --
// possibly called synchronously from an HTTP handler -- is concurrently
// stuck writing to the stalled client.
func TestBroadcastDeviceListSkipsStalledClientWithoutBlockingHealthyClient(t *testing.T) {
	app := NewWebApp()
	remoteA, _, cleanupA := dialTestWebSocket(t, app)
	defer cleanupA()
	remoteB, _, cleanupB := dialTestWebSocket(t, app)
	defer cleanupB()

	app.WSMutex.Lock()
	app.WSClients[remoteA] = &sync.Mutex{}
	app.WSClients[remoteB] = &sync.Mutex{}
	app.WSMutex.Unlock()

	// Simulate client A's own in-flight write (e.g. its periodic ticker
	// mid-write) by holding its connection lock directly.
	app.WSMutex.RLock()
	connMuA := app.WSClients[remoteA]
	app.WSMutex.RUnlock()
	connMuA.Lock()

	broadcastDone := make(chan struct{})
	go func() {
		app.BroadcastDeviceList()
		close(broadcastDone)
	}()

	// Client B's own periodic update path (a separate goroutine, exactly as
	// HandleWebSocket's ticker loop does) must succeed immediately, whether
	// or not BroadcastDeviceList's internal loop currently happens to be
	// stuck waiting on A.
	bDone := make(chan error, 1)
	go func() {
		bDone <- app.withConnWrite(remoteB, func(webSocketWriteBatch) error { return nil })
	}()

	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("healthy client's own update failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("healthy client's own update was blocked by an unrelated stalled client")
	}

	connMuA.Unlock()
	select {
	case <-broadcastDone:
	case <-time.After(time.Second):
		t.Fatal("BroadcastDeviceList did not complete after the stalled client's lock was released")
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

// TestRegistrationDuringDiscoveryPublicationIsNotBlockedAndSeesLatestState
// replaces a prior version of this test that asserted registration BLOCKS
// until an in-flight, unrelated discovery publication finishes. That
// coupling was removed deliberately (see discoveryPublishMu's doc comment):
// registration no longer shares any lock with publication. This proves the
// weaker, now-true property instead: registration is never blocked by an
// in-flight publication, yet still never observes a state older than what
// was stored before that publication began -- because
// withDiscoveryStatusWrite always calls discoveryStatus.Store() before
// taking its client snapshot, so by the time the publication's write
// callback is reached (clientsSelected below), the new status is already
// the one any concurrent registration will read.
func TestRegistrationDuringDiscoveryPublicationIsNotBlockedAndSeesLatestState(t *testing.T) {
	app := NewWebApp()
	oldStatus := &webtypes.DiscoveryStatus{Status: "starting", IsDiscovering: true}
	newStatus := &webtypes.DiscoveryStatus{Status: "completed", DeviceCount: 3}
	app.discoveryStatus.Store(oldStatus)

	remote, client, cleanup := dialTestWebSocket(t, app)
	defer cleanup()
	t.Cleanup(func() { app.removeGlobalWebSocketClient(remote) })

	clientsSelected := make(chan struct{})
	releasePublication := make(chan struct{})
	published := make(chan error, 1)
	go func() {
		published <- app.withDiscoveryStatusWrite(newStatus, func(
			_ webSocketWriteBatch,
			_ []*websocket.Conn,
		) error {
			close(clientsSelected)
			<-releasePublication

			return nil
		})
	}()

	select {
	case <-clientsSelected:
	case <-time.After(time.Second):
		t.Fatal("discovery publication did not reach its write callback")
	}

	registered := make(chan error, 1)
	go func() {
		registered <- app.registerGlobalWebSocket(remote)
	}()

	// Registration must complete WITHOUT waiting for the in-flight,
	// unrelated publication to release releasePublication.
	select {
	case err := <-registered:
		if err != nil {
			t.Fatalf("register global WebSocket: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("registration blocked on an in-flight, unrelated discovery publication")
	}

	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set test WebSocket deadline: %v", err)
	}
	var discoveryMessage webtypes.WebSocketMessage
	if err := client.ReadJSON(&discoveryMessage); err != nil {
		t.Fatalf("read initial discovery frame: %v", err)
	}
	if discoveryMessage.Type != "discovery_status" {
		t.Fatalf("initial frame type = %q, want discovery_status", discoveryMessage.Type)
	}
	data, ok := discoveryMessage.Data.(map[string]interface{})
	if !ok || data["status"] != "completed" || data["deviceCount"] != float64(3) {
		t.Fatalf("initial discovery frame = %#v, want new completed state (already stored before this publication reached its write callback)", discoveryMessage.Data)
	}

	close(releasePublication)
	if err := <-published; err != nil {
		t.Fatalf("publish discovery status: %v", err)
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
