package soundtouchweb

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gesellix/bose-soundtouch/pkg/client"
	"github.com/gesellix/bose-soundtouch/pkg/models"
	"github.com/gesellix/bose-soundtouch/pkg/service/soundtouchweb/webtypes"
)

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

func (writer *recordingWebSocketWriter) WriteMessage(int, []byte) error { return nil }

func (writer *recordingWebSocketWriter) firstMessage() (webtypes.WebSocketMessage, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.messages) == 0 {
		return webtypes.WebSocketMessage{}, false
	}

	message, ok := writer.messages[0].(webtypes.WebSocketMessage)

	return message, ok
}

func (writer *recordingWebSocketWriter) lastDeadline() (time.Time, bool) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.deadlines) == 0 {
		return time.Time{}, false
	}

	return writer.deadlines[len(writer.deadlines)-1], true
}

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

	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), nil)
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

	conn := webtypes.NewDeviceConnection(client.NewClientFromHost(server.URL), nil)
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

func TestDeviceWebSocketUpdateCapturesStatusAfterLifecycleOrderingBarrier(t *testing.T) {
	app := NewWebApp()
	app.webSocketWriteTimeout = 40 * time.Millisecond
	device := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{DeviceID: "left-id"})
	device.SetStatus(&webtypes.DeviceStatus{Group: &models.Group{ID: "pair-old"}})

	blockedWriter := &deadlineBlockingWebSocketWriter{started: make(chan struct{})}
	priorDone := make(chan struct{})
	go func() {
		_ = app.withGlobalWebSocketWrite(func(batch webSocketWriteBatch) error {
			return batch.writeJSON(blockedWriter, struct{}{})
		})
		close(priorDone)
	}()
	<-blockedWriter.started

	recorder := &recordingWebSocketWriter{}
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- app.writeDeviceWebSocketUpdate(recorder, "left-id", device)
	}()

	device.ApplyGroupEvent(&models.Group{ID: "pair-new"}, time.Now())

	select {
	case <-priorDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("stalled prior WebSocket writer ignored its deadline")
	}
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatalf("writeDeviceWebSocketUpdate: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("device WebSocket update remained blocked")
	}

	message, ok := recorder.firstMessage()
	if !ok || message.Type != "device_status" {
		t.Fatalf("first device message = %#v", message)
	}
	data, ok := message.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("device message data = %#v", message.Data)
	}
	status, ok := data["status"].(*webtypes.DeviceStatus)
	if !ok || status.Group == nil || status.Group.ID != "pair-new" {
		t.Fatalf("device frame captured stale group: %#v", data["status"])
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
