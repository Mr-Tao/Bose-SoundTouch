package soundtouchweb

import (
	"encoding/json"
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

func registerBroadcastWebSocket(t *testing.T, app *WebApp) *websocket.Conn {
	t.Helper()

	serverConn := make(chan *websocket.Conn, 1)
	release := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade broadcast test WebSocket: %v", err)

			return
		}

		serverConn <- conn
		<-release
		_ = conn.Close()
	}))
	clientConn, response, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(server.URL, "http"), nil,
	)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		server.Close()
		t.Fatalf("dial broadcast test WebSocket: %v", err)
	}
	remoteConn := <-serverConn

	app.WSMutex.Lock()
	app.WSClients[remoteConn] = true
	app.WSMutex.Unlock()

	t.Cleanup(func() {
		app.WSMutex.Lock()
		delete(app.WSClients, remoteConn)
		app.WSMutex.Unlock()
		_ = clientConn.Close()
		close(release)
		server.Close()
	})

	return clientConn
}

func readBroadcastDevices(t *testing.T, conn *websocket.Conn) map[string]deviceView {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("set broadcast read deadline: %v", err)
	}

	var message struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatalf("read immediate device broadcast: %v", err)
	}
	if message.Type != "devices" {
		t.Fatalf("broadcast type = %q, want devices", message.Type)
	}

	var projected map[string]deviceView
	if err := json.Unmarshal(message.Data, &projected); err != nil {
		t.Fatalf("decode broadcast device projection: %v", err)
	}

	return projected
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

func TestSpeakerConnectionEventMatchesRegisteredHardwareID(t *testing.T) {
	conn := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{DeviceID: "AA11BB22CC33"})

	for _, eventDeviceID := range []string{"", "AA11BB22CC33", "aa11bb22cc33"} {
		if !speakerConnectionEventMatches(conn, eventDeviceID) {
			t.Fatalf("event device ID %q should match", eventDeviceID)
		}
	}
	if speakerConnectionEventMatches(conn, "DEADBEEF0000") {
		t.Fatal("mismatched speaker connection event was accepted")
	}
	if speakerConnectionEventMatches(webtypes.NewDeviceConnection(nil, nil), "AA11BB22CC33") {
		t.Fatal("identified event matched a connection without device identity")
	}
}

func TestUpdateDeviceStatusRefreshesDeviceName(t *testing.T) {
	server := newStatusTestServer(t, http.StatusOK, `<group/>`)
	defer server.Close()

	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{Name: "Old Name", DeviceID: "device-1"},
	)
	NewWebApp().UpdateDeviceStatus("device-1", conn)

	if info := conn.Info(); info == nil || info.Name != "Living Room Left" {
		t.Fatalf("refreshed device info = %+v, want Living Room Left", info)
	}
}

func TestUpdateDeviceStatusFetchesAndCachesBassCapabilities(t *testing.T) {
	var capabilityRequests atomic.Int32
	responses := map[string]string{
		"/now_playing": `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		"/name":        `<name>Living Room</name>`,
		"/volume":      `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
		"/presets":     `<presets/>`,
		"/sources":     `<sources/>`,
		"/bass":        `<bass><targetbass>-3</targetbass><actualbass>-3</actualbass></bass>`,
		"/bassCapabilities": `<bassCapabilities deviceID="device-1">
			<bassAvailable>true</bassAvailable><bassMin>-9</bassMin>
			<bassMax>0</bassMax><bassDefault>0</bassDefault>
		</bassCapabilities>`,
		"/getZone": `<zone master="device-1"><member>device-1</member></zone>`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := responses[r.URL.Path]
		if !ok {
			t.Errorf("unexpected status endpoint %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/bassCapabilities" {
			capabilityRequests.Add(1)
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{DeviceID: "device-1", Type: "SoundTouch 20"},
	)
	app := NewWebApp()
	app.UpdateDeviceStatus("device-1", conn)
	app.UpdateDeviceStatus("device-1", conn)

	capabilities := conn.Status().BassCapabilities
	if capabilities == nil || !capabilities.BassAvailable ||
		capabilities.BassMin != -9 || capabilities.BassMax != 0 || capabilities.BassDefault != 0 {
		t.Fatalf("bass capabilities = %+v, want reported -9..0 default 0", capabilities)
	}
	if got := capabilityRequests.Load(); got != 1 {
		t.Fatalf("/bassCapabilities requests = %d, want 1", got)
	}
}

func TestUpdateDeviceStatusCacheHitDoesNotMaskTotalHTTPFailure(t *testing.T) {
	var capabilityRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bassCapabilities" {
			capabilityRequests.Add(1)
		}
		http.Error(w, "speaker unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	capabilities := &models.BassCapabilities{
		BassAvailable: true, BassMin: -9, BassMax: 0, BassDefault: 0,
	}
	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{DeviceID: "device-1", Type: "SoundTouch 20"},
	)
	conn.UpdateStatus(func(status *webtypes.DeviceStatus) {
		status.BassCapabilities = capabilities
	})
	conn.MarkHTTPSuccess(time.Now().Add(-61 * time.Second))

	app := NewWebApp()
	app.UpdateDeviceStatus("device-1", conn)
	if status := conn.Status(); status.Connectivity != webtypes.ConnectivityStale || status.HTTPReachable {
		t.Fatalf("first failed poll = connectivity %q, reachable %v; want stale false", status.Connectivity, status.HTTPReachable)
	}

	app.UpdateDeviceStatus("device-1", conn)
	status := conn.Status()
	if status.Connectivity != webtypes.ConnectivityOffline || status.IsConnected || status.HTTPReachable {
		t.Fatalf("second failed poll = connectivity %q, connected %v, reachable %v; want offline false false",
			status.Connectivity, status.IsConnected, status.HTTPReachable)
	}
	if status.BassCapabilities != capabilities {
		t.Fatal("cached capabilities were discarded by failed status polls")
	}
	if got := capabilityRequests.Load(); got != 0 {
		t.Fatalf("cached /bassCapabilities requests = %d, want 0", got)
	}
}

func TestUpdateDeviceStatusRetriesInvalidBassCapabilities(t *testing.T) {
	valid := `<bassCapabilities deviceID="device-1">
		<bassAvailable>true</bassAvailable><bassMin>-9</bassMin>
		<bassMax>0</bassMax><bassDefault>0</bassDefault>
	</bassCapabilities>`

	for _, test := range []struct {
		name  string
		first string
	}{
		{
			name: "omitted bassMax",
			first: `<bassCapabilities><bassAvailable>true</bassAvailable>
				<bassMin>-9</bassMin><bassDefault>0</bassDefault></bassCapabilities>`,
		},
		{
			name: "semantic range error",
			first: `<bassCapabilities><bassAvailable>true</bassAvailable>
				<bassMin>1</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`,
		},
		{
			name: "malformed scalar",
			first: `<bassCapabilities><bassAvailable>true</bassAvailable>
				<bassMin>-9</bassMin><bassMax>invalid</bassMax><bassDefault>0</bassDefault></bassCapabilities>`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var capabilityRequests atomic.Int32
			responses := map[string]string{
				"/now_playing": `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
				"/name":        `<name>Living Room</name>`,
				"/volume":      `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
				"/presets":     `<presets/>`,
				"/sources":     `<sources/>`,
				"/bass":        `<bass><targetbass>-3</targetbass><actualbass>-3</actualbass></bass>`,
				"/getZone":     `<zone master="device-1"><member>device-1</member></zone>`,
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				if r.URL.Path == "/bassCapabilities" {
					if capabilityRequests.Add(1) == 1 {
						_, _ = w.Write([]byte(test.first))
					} else {
						_, _ = w.Write([]byte(valid))
					}
					return
				}

				body, ok := responses[r.URL.Path]
				if !ok {
					t.Errorf("unexpected status endpoint %q", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			conn := webtypes.NewDeviceConnection(
				client.NewClientFromHost(server.URL),
				&models.DeviceInfo{DeviceID: "device-1", Type: "SoundTouch 20"},
			)
			app := NewWebApp()
			app.UpdateDeviceStatus("device-1", conn)
			if conn.Status().BassCapabilities != nil {
				t.Fatal("invalid capability response was cached")
			}

			app.UpdateDeviceStatus("device-1", conn)
			capabilities := conn.Status().BassCapabilities
			if capabilities == nil || capabilities.BassMin != -9 || capabilities.BassMax != 0 {
				t.Fatalf("retried capabilities = %+v, want valid -9..0", capabilities)
			}
			if got := capabilityRequests.Load(); got != 2 {
				t.Fatalf("capability requests = %d, want invalid attempt plus retry", got)
			}
		})
	}
}

func TestUpdateDeviceStatusDoesNotOverwriteNewerNameEvent(t *testing.T) {
	nameRequestStarted := make(chan struct{})
	releaseNameResponse := make(chan struct{})
	responses := map[string]string{
		"/now_playing":      `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		"/volume":           `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
		"/presets":          `<presets/>`,
		"/sources":          `<sources/>`,
		"/bass":             `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/bassCapabilities": `<bassCapabilities><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`,
		"/getGroup":         `<group/>`,
		"/getZone":          `<zone master="device-1"><member>device-1</member></zone>`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.URL.Path == "/name" {
			close(nameRequestStarted)
			<-releaseNameResponse
			_, _ = w.Write([]byte(`<name>Old Poll Result</name>`))

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
	defer server.Close()

	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{Name: "Initial Name", DeviceID: "device-1"},
	)
	refreshDone := make(chan struct{})
	go func() {
		NewWebApp().UpdateDeviceStatus("device-1", conn)
		close(refreshDone)
	}()

	select {
	case <-nameRequestStarted:
	case <-time.After(time.Second):
		close(releaseNameResponse)
		t.Fatal("name poll did not start")
	}

	conn.ApplyNameEvent("New Event Name")
	close(releaseNameResponse)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("status refresh did not finish")
	}

	if info := conn.Info(); info == nil || info.Name != "New Event Name" {
		t.Fatalf("device info after stale poll = %+v, want newer event name", info)
	}
}

func TestUpdateDeviceStatusCannotOverwriteNewerVolumeReadback(t *testing.T) {
	presetsRequestStarted := make(chan struct{})
	releasePresetsResponse := make(chan struct{})
	responses := map[string]string{
		"/now_playing":      `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		"/name":             `<name>Living Room</name>`,
		"/volume":           `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
		"/presets":          `<presets/>`,
		"/sources":          `<sources/>`,
		"/bass":             `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/bassCapabilities": `<bassCapabilities><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`,
		"/getZone":          `<zone master="device-1"><member>device-1</member></zone>`,
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

	zoneReadback := conn.BeginVolumeRefresh()
	if !conn.ApplyPolledVolume(zoneReadback, &models.Volume{ActualVolume: 40, TargetVolume: 40}) {
		t.Fatal("newer zone readback was rejected")
	}
	close(releasePresetsResponse)

	select {
	case <-refreshDone:
	case <-time.After(time.Second):
		t.Fatal("status refresh did not finish")
	}

	if got := conn.Status().Volume; got == nil || got.ActualVolume != 40 {
		t.Fatalf("volume = %+v, want newer zone readback 40", got)
	}
}

func TestRefreshZonesAfterEventClearsCachedOldMaster(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}
	master := newVolumeSpeaker(t, 20, `<zone master="MASTER"><member ipaddress="192.0.2.10">MASTER</member></zone>`)
	member := newVolumeSpeaker(t, 20, `<zone master="MEMBER"><member ipaddress="192.0.2.20">MEMBER</member></zone>`)

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Master", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Member", member, 20, nil)

	app.refreshZonesAfterEvent("MEMBER", "")

	masterConn, _ := app.GetDevice("192.0.2.10")
	if masterConn.Status().Zone != nil {
		t.Fatalf("old master topology survived confirmed dissolve: %+v", masterConn.Status().Zone)
	}
}

func TestRefreshZonesAfterEventPreservesCacheOnMasterError(t *testing.T) {
	zone := &models.ZoneInfo{
		Master: "MASTER",
		Members: []models.Member{
			{DeviceID: "MASTER", IP: "192.0.2.10"},
			{DeviceID: "MEMBER", IP: "192.0.2.20"},
		},
	}
	master := newVolumeSpeaker(t, 20, "not XML")
	member := newVolumeSpeaker(t, 20, "")

	app := NewWebApp()
	addVolumeDevice(app, "192.0.2.10", "MASTER", "Master", master, 20, zone)
	addVolumeDevice(app, "192.0.2.20", "MEMBER", "Member", member, 20, nil)

	app.refreshZonesAfterEvent("MEMBER", "")

	masterConn, _ := app.GetDevice("192.0.2.10")
	if masterConn.Status().Zone == nil || masterConn.Status().Zone.Master != "MASTER" {
		t.Fatalf("failed refresh discarded cached topology: %+v", masterConn.Status().Zone)
	}
}

func TestUpdateDeviceStatusSkipsGroupForNonStereoModel(t *testing.T) {
	var groupRequested atomic.Bool
	var balanceRequested atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/getGroup" {
			groupRequested.Store(true)
			http.Error(w, "unsupported endpoint", http.StatusInternalServerError)

			return
		}
		if r.URL.Path == "/balance" {
			balanceRequested.Store(true)
			http.Error(w, "unsupported endpoint", http.StatusInternalServerError)

			return
		}

		responses := map[string]string{
			"/now_playing":      `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
			"/name":             `<name>Living Room</name>`,
			"/volume":           `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
			"/presets":          `<presets/>`,
			"/sources":          `<sources/>`,
			"/bass":             `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
			"/bassCapabilities": `<bassCapabilities><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`,
			"/getZone":          `<zone master="device-1"><member>device-1</member></zone>`,
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
	if balanceRequested.Load() {
		t.Fatal("UpdateDeviceStatus requested /balance for a non-stereo model")
	}
}

func TestUpdateDeviceStatusPollsBalanceOnlyForConfirmedStereoMaster(t *testing.T) {
	for _, masterRole := range []string{"LEFT", "RIGHT"} {
		t.Run(masterRole+" role master", func(t *testing.T) {
			speaker := newBalanceTestSpeaker(t, 4)
			app := NewWebApp()
			master, member := addStereoBalancePair(app, speaker, masterRole)
			member.Client = client.NewClientFromHost(speaker.server.URL)

			app.UpdateDeviceStatus("192.0.2.20", member)
			if gets := speaker.getCount(); gets != 0 {
				t.Fatalf("non-master balance GETs = %d, want 0", gets)
			}

			app.UpdateDeviceStatus("192.0.2.10", master)
			if gets := speaker.getCount(); gets != 1 {
				t.Fatalf("master balance GETs = %d, want 1", gets)
			}
			if got := master.Status().Balance; got == nil || got.TargetBalance != 4 || got.ActualBalance != 4 {
				t.Fatalf("polled balance = %+v, want 4", got)
			}
		})
	}
}

func TestUpdateDeviceStatusRequiresFreshMasterGroupBeforeBalance(t *testing.T) {
	t.Run("failed group refresh", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 3)
		speaker.groupStatus = http.StatusInternalServerError
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")

		app.UpdateDeviceStatus("192.0.2.10", left)

		if gets := speaker.getCount(); gets != 0 {
			t.Fatalf("balance GETs = %d, want 0 after failed group refresh", gets)
		}
		if got := left.Status().Balance; got == nil || got.ActualBalance != 3 || !got.CapabilityKnown {
			t.Fatalf("failed refresh discarded last verified balance: %+v", got)
		}
		if _, ok := left.BeginBalanceRefresh(); ok {
			t.Fatal("failed refresh allowed a balance operation against unconfirmed topology")
		}
	})

	t.Run("in-flight unchanged group refresh keeps the verified projection", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 3)
		speaker.groupStarted = make(chan struct{}, 1)
		speaker.releaseGroup = make(chan struct{})
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")

		done := make(chan struct{})
		go func() {
			app.UpdateDeviceStatus("192.0.2.10", left)
			close(done)
		}()
		<-speaker.groupStarted

		if got := left.Status().Balance; got == nil || got.ActualBalance != 3 || !got.CapabilityKnown {
			t.Fatalf("in-flight group poll hid verified balance: %+v", got)
		}
		if _, ok := left.BeginBalanceRefresh(); ok {
			t.Fatal("balance write became eligible before group refresh completed")
		}

		close(speaker.releaseGroup)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("status poll did not finish")
		}
	})

	t.Run("fresh unpaired group", func(t *testing.T) {
		speaker := newBalanceTestSpeaker(t, 3)
		app := NewWebApp()
		left, _ := addStereoBalancePair(app, speaker, "LEFT")
		speaker.mu.Lock()
		speaker.group = &models.Group{}
		speaker.mu.Unlock()

		app.UpdateDeviceStatus("192.0.2.10", left)

		if gets := speaker.getCount(); gets != 0 {
			t.Fatalf("unpaired ST10 balance GETs = %d, want 0", gets)
		}
		if left.Status().Group != nil || left.Status().Balance != nil {
			t.Fatalf("unpaired refresh retained stereo state: %+v", left.Status())
		}
	})
}

func TestUpdateDeviceStatusRejectsBalanceAfterGroupEvent(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 3)
	speaker.getStarted = make(chan struct{}, 1)
	speaker.releaseGet = make(chan struct{})
	app := NewWebApp()
	left, _ := addStereoBalancePair(app, speaker, "LEFT")

	done := make(chan struct{})
	go func() {
		app.UpdateDeviceStatus("192.0.2.10", left)
		close(done)
	}()
	<-speaker.getStarted

	eventGroup := stereoBalanceGroup("LEFT")
	eventGroup.ID = "pair-event"
	left.ApplyGroupEvent(eventGroup, time.Now())
	close(speaker.releaseGet)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("status poll did not finish")
	}
	if got := left.Status().Group; got == nil || got.ID != "pair-event" {
		t.Fatalf("group event was lost: %+v", got)
	}
	if got := left.Status().Balance; got != nil {
		t.Fatalf("stale poll balance crossed group event: %+v", got)
	}
}

func TestBalancePollSerializesWithPOSTAndPOSTWins(t *testing.T) {
	speaker := newBalanceTestSpeaker(t, 0)
	speaker.getStarted = make(chan struct{}, 2)
	speaker.releaseGet = make(chan struct{})
	speaker.postStarted = make(chan int, 1)
	app := NewWebApp()
	left, _ := addStereoBalancePair(app, speaker, "LEFT")

	pollDone := make(chan struct{})
	go func() {
		app.UpdateDeviceStatus("192.0.2.10", left)
		close(pollDone)
	}()
	<-speaker.getStarted

	response := httptest.NewRecorder()
	postDone := make(chan struct{})
	go func() {
		app.HandleStereoBalance(response, stereoBalanceRequest("5"))
		close(postDone)
	}()
	select {
	case post := <-speaker.postStarted:
		t.Fatalf("POST %d started while the poll readback was in flight", post)
	case <-time.After(50 * time.Millisecond):
	}

	close(speaker.releaseGet)
	select {
	case post := <-speaker.postStarted:
		if post != 1 {
			t.Fatalf("started POST = %d, want 1", post)
		}
	case <-time.After(time.Second):
		t.Fatal("POST did not start after the poll readback completed")
	}

	for name, done := range map[string]<-chan struct{}{"poll": pollDone, "POST": postDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("%s did not finish", name)
		}
	}
	if response.Code != http.StatusOK {
		t.Fatalf("POST status = %d: %s", response.Code, response.Body.String())
	}
	if got := left.Status().Balance; got == nil || got.ActualBalance != 5 {
		t.Fatalf("final balance = %+v, want POST readback 5", got)
	}
	if gets := speaker.getCount(); gets != 2 {
		t.Fatalf("balance GETs = %d, want poll plus POST", gets)
	}
}

func TestBalanceUpdatedEventReadsAndBroadcastsAuthoritativeBalance(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "captured empty event",
			raw:  `<updates deviceID='LEFT'><balanceUpdated></balanceUpdated></updates>`,
		},
		{
			name: "stale populated event",
			raw: `<updates deviceID='LEFT'><balanceUpdated><balance deviceID='LEFT'>` +
				`<balanceAvailable>true</balanceAvailable><balanceMin>-7</balanceMin>` +
				`<balanceMax>7</balanceMax><balanceDefault>0</balanceDefault>` +
				`<targetBalance>-4</targetBalance><actualBalance>-4</actualBalance>` +
				`</balance></balanceUpdated></updates>`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			speaker := newBalanceTestSpeaker(t, 0)
			app := NewWebApp()
			master, _ := addStereoBalancePair(app, speaker, "LEFT")
			speaker.mu.Lock()
			speaker.balance = 5
			speaker.mu.Unlock()
			browser := registerBroadcastWebSocket(t, app)

			parsed, err := models.ParseWebSocketEvent([]byte(test.raw))
			if err != nil || parsed.BalanceUpdated == nil {
				t.Fatalf("parse balance event: event=%+v err=%v", parsed, err)
			}

			app.applyBalanceUpdatedEvent("192.0.2.10", master, parsed.BalanceUpdated)

			status := master.Status()
			if status.Balance == nil || status.Balance.ActualBalance != 5 ||
				status.Balance.TargetBalance != 5 || status.BalanceRevision != 31 {
				t.Fatalf("authoritative balance was not projected: balance=%+v revision=%d", status.Balance, status.BalanceRevision)
			}
			if gets := speaker.getCount(); gets != 1 {
				t.Fatalf("balance GETs = %d, want 1", gets)
			}

			projected := readBroadcastDevices(t, browser)
			view := projected["192.0.2.10"]
			if view.Status == nil || view.Status.Balance == nil ||
				view.Status.Balance.ActualBalance != 5 || view.Status.BalanceRevision != 31 {
				t.Fatalf("immediate balance broadcast = %+v", view.Status)
			}
		})
	}
}

func TestEmptyBassUpdatedEventRefreshesAuthoritativeBass(t *testing.T) {
	var gets atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bass" {
			http.NotFound(w, r)

			return
		}

		gets.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<bass deviceID="MASTER"><targetbass>-3</targetbass><actualbass>-3</actualbass></bass>`))
	}))
	defer server.Close()

	group := &models.Group{
		ID:             "pair-1",
		Name:           "Living pair",
		MasterDeviceID: "MASTER",
		Roles: models.GroupRoles{Roles: []models.GroupRole{
			{DeviceID: "MEMBER", Role: "LEFT", IPAddress: "192.0.2.10"},
			{DeviceID: "MASTER", Role: "RIGHT", IPAddress: "192.0.2.20"},
		}},
		Status: "GROUP_OK",
	}
	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{DeviceID: "MASTER", Name: "Right", Type: "SoundTouch 10", IPAddress: "192.0.2.20"},
	)
	conn.SetStatus(&webtypes.DeviceStatus{
		Bass:         &models.Bass{DeviceID: "MASTER", TargetBass: -2, ActualBass: -2},
		BassRevision: 10,
		Group:        group,
		IsConnected:  true,
		Connectivity: webtypes.ConnectivityOnline,
	})
	member := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "MEMBER", Name: "Left", Type: "SoundTouch 10", IPAddress: "192.0.2.10",
	})
	member.SetStatus(&webtypes.DeviceStatus{
		Group: group, IsConnected: true, Connectivity: webtypes.ConnectivityOnline,
	})
	zone := &models.ZoneInfo{
		Master: "ZONE",
		Members: []models.Member{
			{DeviceID: "ZONE", IP: "192.0.2.30"},
			{DeviceID: "MEMBER", IP: "192.0.2.10"},
			{DeviceID: "MASTER", IP: "192.0.2.20"},
		},
	}
	zoneMaster := webtypes.NewDeviceConnection(nil, &models.DeviceInfo{
		DeviceID: "ZONE", Name: "Zone master", Type: "SoundTouch 20", IPAddress: "192.0.2.30",
	})
	zoneMaster.SetStatus(&webtypes.DeviceStatus{
		Zone: zone, IsConnected: true, Connectivity: webtypes.ConnectivityOnline,
	})

	app := NewWebApp()
	app.AddDevice("192.0.2.10", member)
	app.AddDevice("192.0.2.20", conn)
	app.AddDevice("192.0.2.30", zoneMaster)
	browser := registerBroadcastWebSocket(t, app)

	parsed, err := models.ParseWebSocketEvent([]byte(
		`<updates deviceID='MASTER'><bassUpdated></bassUpdated></updates>`,
	))
	if err != nil || parsed.BassUpdated == nil {
		t.Fatalf("parse captured empty bass event: event=%+v err=%v", parsed, err)
	}

	app.applyBassUpdatedEvent(conn, parsed.BassUpdated)

	status := conn.Status()
	if status.Bass == nil || status.Bass.TargetBass != -3 || status.Bass.ActualBass != -3 {
		t.Fatalf("empty bass event did not refresh authoritative state: %+v", status.Bass)
	}
	if status.BassRevision != 11 {
		t.Fatalf("bass revision = %d, want 11", status.BassRevision)
	}
	if got := gets.Load(); got != 1 {
		t.Fatalf("bass GETs = %d, want 1", got)
	}

	assertProjectedBass := func(source string, projected map[string]deviceView) {
		t.Helper()
		view := projected["192.0.2.30"].Zone
		if view == nil || len(view.Members) != 2 {
			t.Fatalf("%s nested zone projection = %+v", source, view)
		}
		target := view.Members[1].DeviceSettingsTarget
		if target == nil || target.ControlID != "192.0.2.20" || target.Bass == nil ||
			target.Bass.ActualBass != -3 || target.BassRevision != 11 {
			t.Fatalf("%s projected bass target = %+v, want authoritative -3 revision 11", source, target)
		}
	}
	assertProjectedBass("fresh snapshot", app.deviceViewSnapshot())
	assertProjectedBass("immediate broadcast", readBroadcastDevices(t, browser))

	messages := app.periodicPlayerMessages()
	projected, ok := messages[0].Data.(map[string]deviceView)
	if !ok {
		t.Fatalf("periodic devices payload type = %T", messages[0].Data)
	}
	assertProjectedBass("periodic websocket", projected)
}

func TestEmptyBassUpdatedEventRetainsVerifiedBassWhenReadbackFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporarily unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()

	conn := webtypes.NewDeviceConnection(
		client.NewClientFromHost(server.URL),
		&models.DeviceInfo{DeviceID: "MASTER"},
	)
	verified := &models.Bass{DeviceID: "MASTER", TargetBass: -2, ActualBass: -2}
	conn.SetStatus(&webtypes.DeviceStatus{Bass: verified, BassRevision: 10})

	NewWebApp().applyBassUpdatedEvent(conn, &models.BassUpdatedEvent{DeviceID: "MASTER"})

	status := conn.Status()
	if status.Bass != verified || status.BassRevision != 10 {
		t.Fatalf("failed empty-event readback replaced verified bass: %+v revision=%d", status.Bass, status.BassRevision)
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
	if len(messages) != 4 {
		t.Fatalf("periodic messages = %d, want one devices frame and three status updates: %+v", len(messages), messages)
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
	if !statusUpdates["192.0.2.10"] || !statusUpdates["192.0.2.11"] || !statusUpdates["192.0.2.12"] {
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

func TestDiscoveryStatusRejectsOlderGeneration(t *testing.T) {
	app := NewWebApp()
	older := app.BeginDiscovery()
	newer := app.BeginDiscovery()

	if !app.BroadcastDiscoveryStatusFor(newer, "completed", 4) {
		t.Fatal("current discovery status was rejected")
	}
	if app.BroadcastDiscoveryStatusFor(older, "starting", 1) {
		t.Fatal("older discovery status was accepted")
	}

	stored, ok := app.discoveryStatus.Load().(*webtypes.DiscoveryStatus)
	if !ok {
		t.Fatal("discovery status was not stored")
	}
	if stored.Status != "completed" || stored.IsDiscovering || stored.DeviceCount != 4 {
		t.Fatalf("stale discovery overwrote current state: %+v", stored)
	}
}

func TestBeginDiscoverySerializesWithStatusPublication(t *testing.T) {
	app := NewWebApp()
	_ = app.BeginDiscovery()

	writerEntered := make(chan struct{})
	releaseWriter := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		_ = app.withGlobalWebSocketWrite(func(webSocketWriteBatch) error {
			close(writerEntered)
			<-releaseWriter

			return nil
		})
	}()

	<-writerEntered
	reserved := make(chan uint64, 1)
	go func() {
		reserved <- app.BeginDiscovery()
	}()

	select {
	case generation := <-reserved:
		close(releaseWriter)
		<-writerDone
		t.Fatalf("generation %d was reserved during an active status publication", generation)
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseWriter)
	<-writerDone

	select {
	case generation := <-reserved:
		if generation != 2 {
			t.Fatalf("generation = %d, want 2", generation)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery reservation did not proceed after publication")
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
		"/now_playing":      `<nowPlaying source="STANDBY"><playStatus>STOP_STATE</playStatus></nowPlaying>`,
		"/name":             `<name>Living Room Left</name>`,
		"/volume":           `<volume><targetvolume>10</targetvolume><actualvolume>10</actualvolume><muteenabled>false</muteenabled></volume>`,
		"/presets":          `<presets/>`,
		"/sources":          `<sources/>`,
		"/bass":             `<bass><targetbass>0</targetbass><actualbass>0</actualbass></bass>`,
		"/bassCapabilities": `<bassCapabilities><bassAvailable>true</bassAvailable><bassMin>-9</bassMin><bassMax>0</bassMax><bassDefault>0</bassDefault></bassCapabilities>`,
		"/getZone":          `<zone master="device-1"><member>device-1</member></zone>`,
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
